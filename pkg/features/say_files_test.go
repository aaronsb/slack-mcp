package features_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// say files= (#92): attachments from the exchange directory, as one message.

// uploadFake records the three upload steps against a slacktest server.
type uploadFake struct {
	srv *slacktest.Server

	mu         sync.Mutex
	urlForms   []url.Values
	uploads    map[string][]byte // file ID -> bytes received
	completion url.Values

	// uploadURL builds the upload_url returned for a file ID.
	uploadURL func(id string) string
	// share, when set, is the ts files.info reports for the share.
	share string
}

func newUploadFake(t *testing.T) (*uploadFake, *provider.ApiProvider) {
	t.Helper()
	t.Setenv(exchange.EnvOverride, "")
	srv := slacktest.New(t)
	f := &uploadFake{srv: srv, uploads: map[string][]byte{}}
	f.uploadURL = func(id string) string { return srv.URL + "/api/upload/" + id }

	var next int32
	srv.Handle("files.getUploadURLExternal", func(r *http.Request) any {
		_ = r.ParseForm()
		id := fmt.Sprintf("F%d", atomic.AddInt32(&next, 1))
		f.mu.Lock()
		f.urlForms = append(f.urlForms, r.PostForm)
		f.mu.Unlock()
		// Register the upload path for this ID.
		srv.Handle("upload/"+id, func(r *http.Request) any {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				return map[string]any{"ok": false, "error": err.Error()}
			}
			fh := r.MultipartForm.File["file"]
			if len(fh) != 1 {
				return map[string]any{"ok": false, "error": "no file part"}
			}
			rd, _ := fh[0].Open()
			b, _ := io.ReadAll(rd)
			rd.Close()
			f.mu.Lock()
			f.uploads[id] = b
			f.mu.Unlock()
			return map[string]any{"ok": true}
		})
		return map[string]any{"ok": true, "upload_url": f.uploadURL(id), "file_id": id}
	})
	srv.Handle("files.completeUploadExternal", func(r *http.Request) any {
		_ = r.ParseForm()
		f.mu.Lock()
		f.completion = r.PostForm
		f.mu.Unlock()
		var files []map[string]string
		_ = json.Unmarshal([]byte(r.PostForm.Get("files")), &files)
		return map[string]any{"ok": true, "files": files}
	})
	srv.Handle("files.info", func(r *http.Request) any {
		_ = r.ParseForm()
		file := map[string]any{"id": r.Form.Get("file"), "name": "x"}
		f.mu.Lock()
		share := f.share
		f.mu.Unlock()
		if share != "" {
			file["shares"] = map[string]any{"public": map[string]any{
				"C1": []any{map[string]any{"ts": share, "channel_name": "eng"}},
			}}
		}
		return map[string]any{"ok": true, "file": file}
	})

	t.Cleanup(features.SetUploadLimitsForTest(provider.MaxUploadBytes, []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}))
	ap := bootedProvider(t, srv)
	srv.ResetCalls()
	return f, ap
}

// putExchange writes a file into the exchange directory, creating it.
func putExchange(t *testing.T, name string, data []byte) {
	t.Helper()
	dir, err := exchange.Open()
	if err != nil {
		t.Fatalf("exchange.Open: %v", err)
	}
	dir.Close()
	if err := os.WriteFile(filepath.Join(exchangeDir(), name), data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestSayFilesOneFileWithTextSharesOneMessage(t *testing.T) {
	f, ap := newUploadFake(t)
	f.share = "1786752114.508819"
	putExchange(t, "report.pdf", []byte("%PDF-1.7 the bytes"))

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "text": "Revised **PDF** attached", "files": []any{"report.pdf"},
	})

	if n := f.srv.Calls("files.getUploadURLExternal"); n != 1 {
		t.Fatalf("upload URL requests = %d, want 1\n%s", n, out)
	}
	if got := f.urlForms[0]; got.Get("filename") != "report.pdf" || got.Get("length") != "18" {
		t.Fatalf("getUploadURLExternal form = %v", got)
	}
	if string(f.uploads["F1"]) != "%PDF-1.7 the bytes" {
		t.Fatalf("upload body = %q", f.uploads["F1"])
	}
	if n := f.srv.Calls("files.completeUploadExternal"); n != 1 {
		t.Fatalf("completions = %d, want 1", n)
	}
	c := f.completion
	if c.Get("channel_id") != "C1" {
		t.Fatalf("completion channel_id = %q", c.Get("channel_id"))
	}
	if c.Get("initial_comment") != "Revised *PDF* attached" {
		t.Fatalf("initial_comment not normalized: %q", c.Get("initial_comment"))
	}
	if !strings.Contains(c.Get("files"), `"id":"F1"`) {
		t.Fatalf("completion files = %q", c.Get("files"))
	}
	if c.Get("thread_ts") != "" {
		t.Fatalf("top-level share sent thread_ts %q", c.Get("thread_ts"))
	}
	if f.srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("text was posted separately from the files")
	}

	for _, want := range []string{
		"`say to='#eng' files=[report.pdf]`",
		"Shared 1 file to #eng with your comment:",
		"- report.pdf (18 B, application/pdf) — fileId F1",
		"Message ts 1786752114.508819.",
		"**Next:**",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "C1") {
		t.Fatalf("channel ID leaked:\n%s", out)
	}
	if f.srv.Calls("conversations.mark") != 0 {
		t.Fatalf("upload fired a read receipt")
	}
}

func TestSayFilesTwoFilesCompleteOnce(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.png", []byte("png-bytes"))
	putExchange(t, "data.csv", []byte("x,y\n1,2\n"))

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "files": []any{"a.png", "data.csv"},
	})
	if n := f.srv.Calls("files.getUploadURLExternal"); n != 2 {
		t.Fatalf("upload URL requests = %d, want 2\n%s", n, out)
	}
	if f.srv.Calls("upload/F1") != 1 || f.srv.Calls("upload/F2") != 1 {
		t.Fatalf("uploads: F1=%d F2=%d", f.srv.Calls("upload/F1"), f.srv.Calls("upload/F2"))
	}
	if string(f.uploads["F1"]) != "png-bytes" || string(f.uploads["F2"]) != "x,y\n1,2\n" {
		t.Fatalf("upload bodies = %q", f.uploads)
	}
	if n := f.srv.Calls("files.completeUploadExternal"); n != 1 {
		t.Fatalf("completions = %d, want 1", n)
	}
	var files []map[string]string
	if err := json.Unmarshal([]byte(f.completion.Get("files")), &files); err != nil || len(files) != 2 ||
		files[0]["id"] != "F1" || files[1]["id"] != "F2" {
		t.Fatalf("completion files = %q (%v)", f.completion.Get("files"), err)
	}
	if _, ok := f.completion["initial_comment"]; ok {
		t.Fatalf("files alone sent an initial_comment: %v", f.completion)
	}
	if !strings.Contains(out, "Shared 2 files to #eng:") || !strings.Contains(out, "fileId F2") {
		t.Fatalf("output:\n%s", out)
	}
	if f.srv.Calls("conversations.mark") != 0 {
		t.Fatalf("upload fired a read receipt")
	}
}

func TestSayFilesIntoThreadSendsThreadTs(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "chart.png", []byte("chart"))

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "thread": "1782246118.543969", "files": []any{"chart.png"},
	})
	if f.completion.Get("thread_ts") != "1782246118.543969" {
		t.Fatalf("thread_ts = %q\n%s", f.completion.Get("thread_ts"), out)
	}
	if !strings.Contains(out, "to a thread in #eng") || !strings.Contains(out, "thread=1782246118.543969") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestSayFilesTsNotReportedIsSaidNotGuessed(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.txt", []byte("hello"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	if n := f.srv.Calls("files.info"); n != 3 {
		t.Fatalf("files.info polls = %d, want 3", n)
	}
	if !strings.Contains(out, "Slack has not yet reported the message timestamp") {
		t.Fatalf("missing ts not stated:\n%s", out)
	}
	if strings.Contains(out, "Message ts") || strings.Contains(out, "1786752114") {
		t.Fatalf("a ts was invented:\n%s", out)
	}
}

func TestSayFilesForeignUploadHostRefused(t *testing.T) {
	f, ap := newUploadFake(t)
	var foreign int32
	evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreign, 1)
	}))
	t.Cleanup(evil.Close)
	f.uploadURL = func(id string) string { return evil.URL + "/upload/" + id }
	putExchange(t, "a.txt", []byte("secret-ish"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}, "text": "hi"})
	if atomic.LoadInt32(&foreign) != 0 {
		t.Fatalf("bytes were sent to the foreign host")
	}
	if n := f.srv.Calls("files.completeUploadExternal"); n != 0 {
		t.Fatalf("completions = %d after a refused host", n)
	}
	if f.srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("text was posted after a refused host")
	}
	if !strings.Contains(out, "not a Slack host") || !strings.Contains(out, "Nothing was shared") {
		t.Fatalf("refusal unclear:\n%s", out)
	}
}

func TestCheckUploadURLAcceptsOnlySlackHosts(t *testing.T) {
	ap := provider.NewWithTokens("xoxc-test", "xoxd-test")
	for _, ok := range []string{
		"https://files.slack.com/upload/v1/abc",
		"https://slack.com/upload/abc",
		"https://FILES.Slack.com/upload",
	} {
		if err := ap.CheckUploadURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://files.slack.com/upload",
		"https://files.slack.com.evil.example/upload",
		"https://evilslack.com/upload",
		"https://example.com/upload",
		"https://127.0.0.1/upload",
	} {
		if err := ap.CheckUploadURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// Every refusal is local: zero upload URL requests, nothing posted.
func TestSayFilesRefusalsMakeZeroSlackCalls(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "report-v2.pdf", []byte("pdf"))
	putExchange(t, "ok.txt", []byte("ok"))
	putExchange(t, "big.bin", []byte("0123456789"))
	putExchange(t, "empty.txt", nil)
	putExchange(t, "orig.txt", []byte("linked"))
	linked := os.Link(filepath.Join(exchangeDir(), "orig.txt"), filepath.Join(exchangeDir(), "link.txt")) == nil

	eleven := make([]any, 11)
	for i := range eleven {
		eleven[i] = "ok.txt"
	}

	cases := []struct {
		name   string
		params map[string]any
		want   []string
	}{
		{"bad name", map[string]any{"files": []any{"../etc/passwd"}}, []string{"is not a bare file name"}},
		{"missing with hint", map[string]any{"files": []any{"report.pdf"}}, []string{"No file named report.pdf", "report-v2.pdf"}},
		{"one of two missing", map[string]any{"files": []any{"ok.txt", "nope.txt"}}, []string{"1 of 2 files", "No file named nope.txt"}},
		{"over cap", map[string]any{"files": []any{"big.bin"}}, []string{"over the 4 byte limit"}},
		{"empty file", map[string]any{"files": []any{"empty.txt"}}, []string{"empty.txt is empty"}},
		{"eleven files", map[string]any{"files": eleven}, []string{"at most 10"}},
		{"empty list", map[string]any{"files": []any{}}, []string{"files is empty"}},
		{"not strings", map[string]any{"files": []any{3.0}}, []string{"array of bare file names"}},
		{"with broadcast", map[string]any{"files": []any{"ok.txt"}, "thread": "1782246118.543969", "broadcast": true}, []string{"broadcast can't be combined with files"}},
		{"with emoji", map[string]any{"files": []any{"ok.txt"}, "emoji": "thumbsup", "messageTs": "1782246118.543969"}, []string{"can't be combined with emoji"}},
	}
	if linked {
		cases = append(cases, struct {
			name   string
			params map[string]any
			want   []string
		}{"hard link", map[string]any{"files": []any{"link.txt"}}, []string{"hard link"}})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit := int64(provider.MaxUploadBytes)
			if tc.name == "over cap" {
				limit = 4
			}
			defer features.SetUploadLimitsForTest(limit, []time.Duration{time.Millisecond})()
			p := map[string]any{"to": "#eng", "text": "should not post"}
			for k, v := range tc.params {
				p[k] = v
			}
			out := runTool(t, features.Say, ap, p)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			if !strings.Contains(out, "Nothing was sent") {
				t.Errorf("refusal does not say nothing was sent:\n%s", out)
			}
		})
	}
	for _, m := range []string{"files.getUploadURLExternal", "files.completeUploadExternal", "chat.postMessage", "reactions.add", "conversations.open"} {
		if n := f.srv.Calls(m); n != 0 {
			t.Fatalf("refusals made %d %s calls", n, m)
		}
	}
}

func TestSayFilesPreSendBoundarySeesBytesAndCanRefuse(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.txt", []byte("the exact bytes"))

	var gotTo, gotComment string
	var gotData [][]byte
	t.Cleanup(features.SetPreSendUploadForTest(func(to, comment string, names []string, data [][]byte) string {
		gotTo, gotComment, gotData = to, comment, data
		return "blocked by test"
	}))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "**hi**", "files": []any{"a.txt"}})
	if gotTo != "#eng" || gotComment != "*hi*" || len(gotData) != 1 || string(gotData[0]) != "the exact bytes" {
		t.Fatalf("boundary saw to=%q comment=%q data=%q", gotTo, gotComment, gotData)
	}
	if !strings.Contains(out, "blocked by test") {
		t.Fatalf("refusal not returned:\n%s", out)
	}
	if n := f.srv.Calls("files.getUploadURLExternal"); n != 0 {
		t.Fatalf("refused call still requested %d upload URLs", n)
	}
}

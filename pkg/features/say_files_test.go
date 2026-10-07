package features_test

import (
	"encoding/json"
	"encoding/pem"
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
	"github.com/slack-go/slack"
)

// say files= (#92): attachments from the exchange directory, as one message.

// uploadFake records the three upload steps against a slacktest server,
// beside a foreign TLS host the provider's client trusts, so a request that
// reached it would succeed and be counted.
type uploadFake struct {
	srv *slacktest.Server
	// evil is a non-Slack host; evilHits counts requests that reached it.
	evil     *httptest.Server
	evilHits int32

	mu         sync.Mutex
	urlForms   []url.Values
	uploads    map[string][]byte // file ID -> bytes received
	completion url.Values        // the last completion form
	// completions is every completion form, in order.
	completions []url.Values

	// uploadURL builds the upload_url returned for a file ID.
	uploadURL func(id string) string
	// uploadResp, when set for a file ID, replaces the upload response.
	uploadResp map[string]slacktest.Response
	// completeResp, when set, replaces the completion response;
	// completeQueue, while non-empty, answers first, one response per call.
	completeResp  any
	completeQueue []any
	// shareConv/sharePrivate/shareTS describe the share files.info reports;
	// an empty shareTS reports none.
	shareConv    string
	sharePrivate bool
	shareTS      string
}

func newUploadFake(t *testing.T, seed ...slack.Channel) (*uploadFake, *provider.ApiProvider) {
	t.Helper()
	t.Setenv(exchange.EnvOverride, "")
	srv := slacktest.New(t)
	if len(seed) > 0 {
		srv.SeedChannels(seed...)
	}
	f := &uploadFake{srv: srv, uploads: map[string][]byte{}, uploadResp: map[string]slacktest.Response{}, shareConv: "C1"}
	f.uploadURL = func(id string) string { return srv.URL + "/api/upload/" + id }

	f.evil = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.evilHits, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(f.evil.Close)
	caFile := filepath.Join(t.TempDir(), "evil-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.evil.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLACK_MCP_SERVER_CA", caFile)
	t.Setenv("SLACK_MCP_SERVER_CA_INSECURE", "")

	var next int32
	srv.Handle("files.getUploadURLExternal", func(r *http.Request) any {
		_ = r.ParseForm()
		id := fmt.Sprintf("F%d", atomic.AddInt32(&next, 1))
		f.mu.Lock()
		f.urlForms = append(f.urlForms, r.PostForm)
		f.mu.Unlock()
		srv.Handle("upload/"+id, func(r *http.Request) any {
			f.mu.Lock()
			resp, override := f.uploadResp[id]
			f.mu.Unlock()
			if override {
				return resp
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				return slacktest.Response{Status: 400, Body: err.Error()}
			}
			fh := r.MultipartForm.File["file"]
			if len(fh) != 1 {
				return slacktest.Response{Status: 400, Body: "no file part"}
			}
			rd, _ := fh[0].Open()
			b, _ := io.ReadAll(rd)
			rd.Close()
			f.mu.Lock()
			f.uploads[id] = b
			f.mu.Unlock()
			return slacktest.Response{Status: 200, Body: "OK - " + fmt.Sprint(len(b))}
		})
		return map[string]any{"ok": true, "upload_url": f.uploadURL(id), "file_id": id}
	})
	srv.Handle("files.completeUploadExternal", func(r *http.Request) any {
		_ = r.ParseForm()
		f.mu.Lock()
		f.completion = r.PostForm
		f.completions = append(f.completions, r.PostForm)
		resp := f.completeResp
		if len(f.completeQueue) > 0 {
			resp, f.completeQueue = f.completeQueue[0], f.completeQueue[1:]
		}
		f.mu.Unlock()
		if resp != nil {
			return resp
		}
		var files []map[string]string
		_ = json.Unmarshal([]byte(r.PostForm.Get("files")), &files)
		return map[string]any{"ok": true, "files": files}
	})
	srv.Handle("files.info", func(r *http.Request) any {
		_ = r.ParseForm()
		file := map[string]any{"id": r.Form.Get("file"), "name": "x"}
		f.mu.Lock()
		conv, private, ts := f.shareConv, f.sharePrivate, f.shareTS
		f.mu.Unlock()
		if ts != "" {
			kind := "public"
			if private {
				kind = "private"
			}
			file["shares"] = map[string]any{kind: map[string]any{
				conv: []any{map[string]any{"ts": ts}},
			}}
		}
		return map[string]any{"ok": true, "file": file}
	})
	srv.Handle("conversations.open", func(*http.Request) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D1", "is_im": true, "user": "U2"}}
	})

	t.Cleanup(features.SetUploadLimitsForTest(features.UploadLimits{
		PerFile: provider.MaxUploadBytes, Total: 1 << 30,
		Delays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}))
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

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestSayFilesOneFileWithTextSharesOneMessage(t *testing.T) {
	f, ap := newUploadFake(t)
	f.shareTS = "1786752114.508819"
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
	if _, ok := c["initial_comment"]; ok {
		t.Fatalf("a comment that went as blocks also sent initial_comment %q, which makes Slack ignore the blocks", c.Get("initial_comment"))
	}
	if got := completionBlocks(t, c); !strings.Contains(got, `"text":"PDF","style":{"bold":true}`) {
		t.Fatalf("comment block lacks the bold run:\n%s", got)
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
	mustContain(t, out,
		"`say to='#eng' files=[report.pdf]`",
		"Shared 1 file to #eng with your comment:",
		"- report.pdf (18 B, application/pdf) — fileId F1",
		"Message ts 1786752114.508819.",
		"**Next:**",
	)
	if strings.Contains(out, "C1") {
		t.Fatalf("channel ID leaked:\n%s", out)
	}
	if f.srv.Calls("conversations.mark") != 0 {
		t.Fatalf("upload fired a read receipt")
	}
}

// completionBlocks returns the blocks form value of a completion, failing
// unless it is one rich_text block.
func completionBlocks(t *testing.T, c url.Values) string {
	t.Helper()
	raw := c.Get("blocks")
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil || len(blocks) != 1 || blocks[0]["type"] != "rich_text" {
		t.Fatalf("completion blocks = %q (%v), want one rich_text block", raw, err)
	}
	return raw
}

// A list in the comment arrives as a rich_text_list, as the composer sends
// it, not as dash lines in mrkdwn, which has no list syntax.
func TestSayFilesCommentListGoesAsRichTextList(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "card.png", []byte("png-bytes"))

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "files": []any{"card.png"},
		"text": "**What changed**\n- one\n- two",
	})
	if n := f.srv.Calls("files.completeUploadExternal"); n != 1 {
		t.Fatalf("completions = %d, want 1\n%s", n, out)
	}
	got := completionBlocks(t, f.completion)
	if !strings.Contains(got, `"type":"rich_text_list"`) || !strings.Contains(got, `"style":"bullet"`) {
		t.Fatalf("comment list did not become a bullet rich_text_list:\n%s", got)
	}
	mustContain(t, out, "Shared 1 file to #eng with your comment:")
	if strings.Contains(out, "mrkdwn text") {
		t.Fatalf("reported a fallback that did not happen:\n%s", out)
	}
}

// A comment block Slack refuses at validation shared nothing, so the
// completion is retried exactly once with the mrkdwn comment, and the
// result says the lists may be plain lines.
func TestSayFilesRejectedCommentBlockRetriesOnceAsMrkdwn(t *testing.T) {
	f, ap := newUploadFake(t)
	f.completeQueue = []any{map[string]any{"ok": false, "error": "invalid_blocks"}}
	putExchange(t, "a.txt", []byte("a"))

	out := runTool(t, features.Say, ap, map[string]any{
		"to": "#eng", "files": []any{"a.txt"}, "text": "Revised **PDF** attached",
	})
	if n := f.srv.Calls("files.completeUploadExternal"); n != 2 {
		t.Fatalf("completions = %d, want 2 (blocks, then mrkdwn)\n%s", n, out)
	}
	first, second := f.completions[0], f.completions[1]
	completionBlocks(t, first)
	if second.Get("blocks") != "" {
		t.Fatalf("retry sent blocks again: %q", second.Get("blocks"))
	}
	if second.Get("initial_comment") != "Revised *PDF* attached" {
		t.Fatalf("retry initial_comment not normalized mrkdwn: %q", second.Get("initial_comment"))
	}
	mustContain(t, out, "Shared 1 file to #eng with your comment:", "Comment posted as mrkdwn text")
}

// Any refusal other than the blocks themselves is not retried.
func TestSayFilesCommentRefusedOtherwiseIsNotRetried(t *testing.T) {
	f, ap := newUploadFake(t)
	f.completeResp = map[string]any{"ok": false, "error": "not_in_channel"}
	putExchange(t, "a.txt", []byte("a"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}, "text": "hi"})
	if n := f.srv.Calls("files.completeUploadExternal"); n != 1 {
		t.Fatalf("completions = %d, want 1\n%s", n, out)
	}
	mustContain(t, out, "Slack refused to share them", "not_in_channel")
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
	if _, ok := f.completion["blocks"]; ok {
		t.Fatalf("files alone sent blocks: %v", f.completion)
	}
	mustContain(t, out, "Shared 2 files to #eng:", "fileId F2")
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
	mustContain(t, out, "to a thread in #eng", "thread=1782246118.543969")
}

func TestSayFilesTsNotReportedIsSaidNotGuessed(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.txt", []byte("hello"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	if n := f.srv.Calls("files.info"); n != 3 {
		t.Fatalf("files.info polls = %d, want 3", n)
	}
	mustContain(t, out, "Slack has not yet reported the message timestamp")
	if strings.Contains(out, "Message ts") || strings.Contains(out, "1786752114") {
		t.Fatalf("a ts was invented:\n%s", out)
	}
}

// A person with no DM: the pre-send boundary sees the person before any
// conversations.open; the DM is opened only after it passes, and the ts
// comes from shares.private for the DM.
func TestSayFilesToPersonOpensDMAfterPreSendAndReadsPrivateShare(t *testing.T) {
	f, ap := newUploadFake(t)
	f.shareConv, f.sharePrivate, f.shareTS = "D1", true, "1786752200.000100"
	putExchange(t, "a.txt", []byte("hello"))

	var seen features.PreSendSeen
	opensAtHook := -1
	t.Cleanup(features.SetPreSendForTest(func(s features.PreSendSeen) string {
		seen, opensAtHook = s, f.srv.Calls("conversations.open")
		return ""
	}))

	out := runTool(t, features.Say, ap, map[string]any{"to": "@schen", "files": []any{"a.txt"}})
	if opensAtHook != 0 || seen.ConvID != "" || seen.UserID != "U2" || seen.Name != "@schen" {
		t.Fatalf("preSend saw conv=%q user=%q name=%q with %d opens before it", seen.ConvID, seen.UserID, seen.Name, opensAtHook)
	}
	if f.srv.Calls("conversations.open") != 1 || f.completion.Get("channel_id") != "D1" {
		t.Fatalf("DM not opened before the share: opens=%d channel=%q", f.srv.Calls("conversations.open"), f.completion.Get("channel_id"))
	}
	mustContain(t, out, "Shared 1 file to @schen", "Message ts 1786752200.000100.")
	for _, id := range []string{"D1", "U2"} {
		if strings.Contains(out, id) {
			t.Fatalf("ID %s leaked:\n%s", id, out)
		}
	}
}

func TestSayFilesNamesAnIMByItsPersonNotItsID(t *testing.T) {
	var im slack.Channel
	im.ID, im.IsIM, im.User = "D7", true, "U2"
	var eng slack.Channel
	eng.ID, eng.Name, eng.IsChannel, eng.IsMember = "C1", "eng", true, true
	_, ap := newUploadFake(t, eng, im)
	putExchange(t, "a.txt", []byte("hello"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "D7", "files": []any{"a.txt"}})
	mustContain(t, out, "Shared 1 file to @schen")
}

func TestSayFilesForeignUploadHostRefused(t *testing.T) {
	f, ap := newUploadFake(t)
	f.uploadURL = func(id string) string { return f.evil.URL + "/upload/" + id }
	putExchange(t, "a.txt", []byte("secret-ish"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}, "text": "hi"})
	if n := atomic.LoadInt32(&f.evilHits); n != 0 {
		t.Fatalf("%d requests reached the foreign host", n)
	}
	if n := f.srv.Calls("files.completeUploadExternal"); n != 0 {
		t.Fatalf("completions = %d after a refused host", n)
	}
	if f.srv.Calls("chat.postMessage") != 0 {
		t.Fatalf("text was posted after a refused host")
	}
	mustContain(t, out, "not a Slack host", "Nothing was shared")
}

func TestSayFilesLookalikeUploadHostRefused(t *testing.T) {
	f, ap := newUploadFake(t)
	f.uploadURL = func(id string) string { return "https://files.slac\u212A.com/upload/" + id }
	putExchange(t, "a.txt", []byte("x"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	if n := f.srv.Calls("files.completeUploadExternal"); n != 0 {
		t.Fatalf("completions = %d after a look-alike host", n)
	}
	mustContain(t, out, "not a Slack host")
}

func TestSayFilesUploadRedirectToForeignHostRefused(t *testing.T) {
	f, ap := newUploadFake(t)
	f.uploadResp["F1"] = slacktest.Response{Status: http.StatusFound, Header: map[string]string{"Location": "https://" + f.evil.Listener.Addr().String() + "/steal"}}
	putExchange(t, "a.txt", []byte("x"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	if n := atomic.LoadInt32(&f.evilHits); n != 0 {
		t.Fatalf("redirect reached the foreign host %d times", n)
	}
	if n := f.srv.Calls("files.completeUploadExternal"); n != 0 {
		t.Fatalf("completions = %d after a refused redirect", n)
	}
	mustContain(t, out, "Uploading a.txt failed", "redirect")
}

func TestSayTextRedirectToForeignHostRefused(t *testing.T) {
	f, ap := newUploadFake(t)
	f.srv.Handle("chat.postMessage", func(*http.Request) any {
		return slacktest.Response{Status: http.StatusFound, Header: map[string]string{"Location": f.evil.URL + "/api/chat.postMessage"}}
	})
	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "text": "hi"})
	if n := atomic.LoadInt32(&f.evilHits); n != 0 {
		t.Fatalf("redirect reached the foreign host %d times:\n%s", n, out)
	}
	mustContain(t, out, "Failed to send message")
}

func TestSayFilesSecondUploadFailsNothingShared(t *testing.T) {
	f, ap := newUploadFake(t)
	f.uploadResp["F2"] = slacktest.Response{Status: http.StatusInternalServerError, Body: "boom"}
	putExchange(t, "a.txt", []byte("a"))
	putExchange(t, "b.txt", []byte("b"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt", "b.txt"}})
	if n := f.srv.Calls("files.completeUploadExternal"); n != 0 {
		t.Fatalf("completions = %d after a failed upload", n)
	}
	mustContain(t, out, "Uploading b.txt failed", "the 1 file(s) uploaded before the failure were not shared")
}

func TestSayFilesCompletionRefusedSaysNothingPosted(t *testing.T) {
	f, ap := newUploadFake(t)
	f.completeResp = map[string]any{"ok": false, "error": "not_in_channel"}
	putExchange(t, "a.txt", []byte("a"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	mustContain(t, out, "Slack refused to share them", "not_in_channel", "Nothing was posted", "were not shared")
	if f.srv.Calls("files.info") != 0 {
		t.Fatalf("polled for a ts after a refused share")
	}
}

func TestSayFilesCompletionTransportErrorSaysOutcomeUnknown(t *testing.T) {
	f, ap := newUploadFake(t)
	f.completeResp = slacktest.Response{Status: http.StatusOK, Body: "not json"}
	putExchange(t, "a.txt", []byte("a"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	mustContain(t, out, "unknown", "messages target='#eng' since='5m'")
	if strings.Contains(out, "Nothing was posted") {
		t.Fatalf("claimed nothing was posted on an unknown outcome:\n%s", out)
	}
}

// Every refusal is local: to a person with no DM, zero Slack calls of any
// kind, conversations.open included, and nothing posted.
func TestSayFilesRefusalsMakeZeroSlackCalls(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "report-v2.pdf", []byte("pdf"))
	putExchange(t, "ok.txt", []byte("ok"))
	putExchange(t, "big.bin", []byte("0123456789"))
	putExchange(t, "mid1.bin", []byte("012345"))
	putExchange(t, "mid2.bin", []byte("012345"))
	putExchange(t, "empty.txt", nil)
	putExchange(t, "orig.txt", []byte("linked"))
	linked := os.Link(filepath.Join(exchangeDir(), "orig.txt"), filepath.Join(exchangeDir(), "link.txt")) == nil

	eleven := make([]any, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("f%d.txt", i)
	}

	type refusal struct {
		name   string
		params map[string]any
		want   []string
	}
	cases := []refusal{
		{"bad name", map[string]any{"files": []any{"../etc/passwd"}}, []string{"is not a bare file name"}},
		{"missing with hint", map[string]any{"files": []any{"report.pdf"}}, []string{"No file named report.pdf", "report-v2.pdf", "put name="}},
		{"one of two missing", map[string]any{"files": []any{"ok.txt", "nope.txt"}}, []string{"1 of 2 files", "No file named nope.txt"}},
		{"over per-file cap", map[string]any{"files": []any{"big.bin"}}, []string{"over the 8 byte limit"}},
		{"over total cap", map[string]any{"files": []any{"mid1.bin", "mid2.bin"}}, []string{"over the 10 B limit", "mid1.bin: 6 B (6 bytes)", "mid2.bin: 6 B (6 bytes)"}},
		{"empty file", map[string]any{"files": []any{"empty.txt"}}, []string{"empty.txt is empty"}},
		{"eleven files", map[string]any{"files": eleven}, []string{"at most 10"}},
		{"duplicate names", map[string]any{"files": []any{"ok.txt", "ok.txt"}}, []string{"ok.txt more than once"}},
		{"empty list", map[string]any{"files": []any{}}, []string{"files is empty"}},
		{"not strings", map[string]any{"files": []any{3.0}}, []string{"array of bare file names"}},
		{"with broadcast", map[string]any{"files": []any{"ok.txt"}, "thread": "1782246118.543969", "broadcast": true}, []string{"broadcast can't be combined with files"}},
		{"with emoji", map[string]any{"files": []any{"ok.txt"}, "emoji": "thumbsup", "messageTs": "1782246118.543969"}, []string{"can't be combined with emoji"}},
	}
	if linked {
		cases = append(cases, refusal{"hard link", map[string]any{"files": []any{"link.txt"}}, []string{"hard link"}})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer features.SetUploadLimitsForTest(features.UploadLimits{PerFile: 8, Total: 10, Delays: []time.Duration{time.Millisecond}})()
			p := map[string]any{"to": "@schen", "text": "should not post"}
			for k, v := range tc.params {
				p[k] = v
			}
			out := runTool(t, features.Say, ap, p)
			mustContain(t, out, tc.want...)
			mustContain(t, out, "Nothing was sent")
		})
	}
	if n := f.srv.TotalCalls(); n != 0 {
		t.Fatalf("refusals made %d Slack calls (conversations.open: %d)", n, f.srv.Calls("conversations.open"))
	}
}

func TestSayFilesPreSendRefusalMakesZeroSlackCalls(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.txt", []byte("the exact bytes"))

	var seen features.PreSendSeen
	t.Cleanup(features.SetPreSendForTest(func(s features.PreSendSeen) string {
		seen = s
		return "blocked by test"
	}))

	out := runTool(t, features.Say, ap, map[string]any{"to": "@schen", "text": "**hi**", "files": []any{"a.txt"}})
	if seen.Typed != "@schen" || seen.Fallback != "*hi*" || len(seen.Data) != 1 || string(seen.Data[0]) != "the exact bytes" {
		t.Fatalf("boundary saw %+v", seen)
	}
	mustContain(t, out, "blocked by test")
	if n := f.srv.TotalCalls(); n != 0 {
		t.Fatalf("refused call made %d Slack calls (conversations.open: %d)", n, f.srv.Calls("conversations.open"))
	}
}

// The bytes preSend sees are the bytes sent, even if the file changes on
// disk after they were read.
func TestSayFilesUploadsTheBufferPreSendSaw(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.txt", []byte("checked bytes"))

	var saw []byte
	t.Cleanup(features.SetPreSendForTest(func(s features.PreSendSeen) string {
		saw = append([]byte(nil), s.Data[0]...)
		if err := os.WriteFile(filepath.Join(exchangeDir(), "a.txt"), []byte("SWAPPED AFTER THE CHECK"), 0o600); err != nil {
			t.Errorf("overwrite: %v", err)
		}
		return ""
	}))

	runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}})
	if string(saw) != "checked bytes" || string(f.uploads["F1"]) != string(saw) {
		t.Fatalf("hook saw %q, uploaded %q", saw, f.uploads["F1"])
	}
}

// say text to a person with no DM: preSend sees the person and the text as
// sent before any conversations.open, and a refusal sends nothing.
func TestSayTextPreSendRunsBeforeOpeningADM(t *testing.T) {
	f, ap := newUploadFake(t)
	var seen features.PreSendSeen
	t.Cleanup(features.SetPreSendForTest(func(s features.PreSendSeen) string {
		seen = s
		return "blocked by test"
	}))

	out := runTool(t, features.Say, ap, map[string]any{"to": "@schen", "text": "- one\n- two"})
	if seen.ConvID != "" || seen.UserID != "U2" || seen.Fallback == "" || !seen.HasRichText || len(seen.Names) != 0 {
		t.Fatalf("boundary saw %+v", seen)
	}
	mustContain(t, out, "blocked by test")
	if n := f.srv.TotalCalls(); n != 0 {
		t.Fatalf("refused text say made %d Slack calls", n)
	}
}

func TestPreWriteLocalRefusesEveryWriteFirst(t *testing.T) {
	f, ap := newUploadFake(t)
	putExchange(t, "a.txt", []byte("a"))
	calls := 0
	t.Cleanup(features.SetPreWriteLocalForTest(func() string {
		calls++
		return "locked by test"
	}))

	for _, p := range []map[string]any{
		{"to": "@schen", "text": "hi"},
		{"to": "@schen", "files": []any{"a.txt"}},
		{"to": "#eng", "emoji": "thumbsup", "messageTs": "1782246118.543969"},
	} {
		mustContain(t, runTool(t, features.Say, ap, p), "locked by test")
	}
	mustContain(t, runTool(t, features.MarkAsRead, ap, map[string]any{"channel": "#eng"}), "locked by test")
	if calls != 4 {
		t.Fatalf("preWriteLocal ran %d times, want 4", calls)
	}
	if n := f.srv.TotalCalls(); n != 0 {
		t.Fatalf("locked writes made %d Slack calls", n)
	}
}

func TestCheckUploadURLAcceptsOnlySlackHosts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ap := provider.NewWithTokens("xoxc-test", "xoxd-test")
	for _, ok := range []string{
		"https://files.slack.com/upload/v1/abc",
		"https://slack.com/upload/abc",
		"https://FILES.Slack.com/upload",
		"https://files.slack.com:443/upload",
	} {
		if err := ap.CheckUploadURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://files.slack.com/upload",
		"https://files.slac\u212A.com/upload",
		"https://files.slack.com.evil.example/upload",
		"https://evilslack.com/upload",
		"https://user:pw@files.slack.com/upload",
		"https://files.slack.com:8443/upload",
		"https://example.com/upload",
		"https://127.0.0.1/upload",
	} {
		if err := ap.CheckUploadURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// When the mrkdwn retry is refused too, the result names both refusals and
// still says nothing was posted.
func TestSayFilesRetryRefusedNamesBothRefusals(t *testing.T) {
	f, ap := newUploadFake(t)
	f.completeQueue = []any{map[string]any{"ok": false, "error": "invalid_blocks"}}
	f.completeResp = map[string]any{"ok": false, "error": "not_in_channel"}
	putExchange(t, "a.txt", []byte("a"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}, "text": "hi"})
	if n := f.srv.Calls("files.completeUploadExternal"); n != 2 {
		t.Fatalf("completions = %d, want 2\n%s", n, out)
	}
	mustContain(t, out, "rejected the comment's rich-text block, and the mrkdwn retry failed too", "not_in_channel", "Nothing was posted")
}

// A transport error on the retry leaves the outcome unknown, and says so.
func TestSayFilesRetryTransportErrorSaysOutcomeUnknown(t *testing.T) {
	f, ap := newUploadFake(t)
	f.completeQueue = []any{map[string]any{"ok": false, "error": "invalid_blocks"}}
	f.completeResp = slacktest.Response{Status: http.StatusOK, Body: "not json"}
	putExchange(t, "a.txt", []byte("a"))

	out := runTool(t, features.Say, ap, map[string]any{"to": "#eng", "files": []any{"a.txt"}, "text": "hi"})
	mustContain(t, out, "rejected the comment's rich-text block, and the mrkdwn retry failed too", "unknown", "messages target='#eng' since='5m'")
	if strings.Contains(out, "Nothing was posted") {
		t.Fatalf("claimed nothing was posted on an unknown outcome:\n%s", out)
	}
}

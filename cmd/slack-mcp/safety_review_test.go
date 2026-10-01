package main

import (
	"bytes"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
)

const (
	esc   = "\x1b"
	csi   = esc + "[2J" + esc + "[31m"              // clear screen, red
	osc52 = esc + "]52;c;Y3VybCBldmlsLnNo" + "\x07" // write the clipboard
	c1CSI = "\u009b2J"                              // 8-bit CSI
)

func TestTermSafe(t *testing.T) {
	in := "a" + csi + "b" + osc52 + "c" + c1CSI + "d\n\te\x7f\xff"
	got := termSafe(in)
	for _, raw := range []string{esc, "\x07", "\u009b", "\x7f", "\xff"} {
		if strings.Contains(got, raw) {
			t.Fatalf("raw %q survived: %q", raw, got)
		}
	}
	if !strings.Contains(got, `\x1b[2J`) || !strings.Contains(got, `\x1b]52;c;`) || !strings.Contains(got, `\u009b`) {
		t.Fatalf("escapes not visible: %q", got)
	}
	if !strings.Contains(got, "d\n\te") {
		t.Fatal("newline and tab must pass")
	}
	if termSafe("héllo #général") != "héllo #général" {
		t.Fatal("printable UTF-8 altered")
	}
}

func TestSafetyCLIApproveEscapesAgentText(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	r, _, err := w.Pending.Create(safety.Request{
		Cases: []safety.Case{safety.CaseExternal}, Tool: "say",
		Destination: safety.Destination{Kind: safety.DestChannel, ConversationID: "C1", Name: "#p" + csi},
		ContentHash: "h", Text: "hi" + osc52 + c1CSI, FileNames: []string{"f" + csi + ".txt"}, FileCount: 1,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.run("nope\n", "approve", r.ID)
	h.tty = false
	listOut := h.out.String()
	h.run("", "approve")
	all := listOut + h.out.String()
	for _, raw := range []string{esc, "\x07", "\u009b"} {
		if strings.Contains(all, raw) {
			t.Fatalf("raw control %q printed:\n%q", raw, all)
		}
	}
	if !strings.Contains(listOut, `\x1b]52`) {
		t.Fatalf("escaped text not shown: %q", listOut)
	}
}

func TestMatchKeyHonorsSigils(t *testing.T) {
	ks := []safety.Key{safety.Person("U1", "@general"), safety.Conversation("C1", "#general")}
	if k, ok := matchKey("#general", ks); !ok || k.ID != "C1" {
		t.Fatalf("#general: %+v", k)
	}
	if k, ok := matchKey("@general", ks); !ok || k.ID != "U1" {
		t.Fatalf("@general: %+v", k)
	}
	if _, ok := matchKey("#U1", ks); ok {
		t.Fatal("# matched a person by ID")
	}
	if _, ok := matchKey("general", ks); !ok {
		t.Fatal("bare word matched nothing")
	}
}

func TestSafetyCLIClearNoteSkipsIgnoredTrust(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	soft := h.workspace(safety.Soft)
	soft.Trust.Add(safety.TrustAdd{Key: safety.Conversation("C2", "#general"), Cases: []safety.Case{safety.CaseExternal}, Source: safety.SourceElicitation})
	blockOn(t, w, safety.Destination{Kind: safety.DestChannel, ConversationID: "C2", Name: "#general"})
	if code := h.run("#general\n", "quarantine", "clear", "#general"); code != 0 {
		t.Fatalf("clear: %s", h.err.String())
	}
	if strings.Contains(h.out.String(), "trusted destination") {
		t.Fatalf("strict announced trust it ignores: %s", h.out.String())
	}
}

func TestSafetyCLIListsShowDataDir(t *testing.T) {
	h := newHarness(t)
	h.workspace(safety.Strict)
	for _, args := range [][]string{{"quarantine", "list"}, {"trust", "list"}, {"approve"}} {
		h.run("", args...)
		if !strings.Contains(h.out.String(), h.dir) {
			t.Fatalf("%v does not name the data directory:\n%s", args, h.out.String())
		}
	}
}

func TestSafetyCLIApproveExpiresHeldContent(t *testing.T) {
	h := newHarness(t)
	w := h.workspace(safety.Strict)
	w.Pending.Create(safety.Request{
		Cases: []safety.Case{safety.CaseExternal}, Tool: "say",
		Destination: safety.Destination{Kind: safety.DestChannel, ConversationID: "C1", Name: "#partner"},
		ContentHash: "h", Text: "long expired text",
	}, time.Now().Add(-safety.PendingTTL-time.Hour))
	h.tty = false
	if code := h.run("", "approve"); code != 0 {
		t.Fatalf("list: %s", h.err.String())
	}
	raw, _ := os.ReadFile(filepath.Join(h.dir, safety.PendingFile))
	if bytes.Contains(raw, []byte("long expired text")) {
		t.Fatal("expired case 1 text still in pending.jsonl")
	}
}

func TestCLITransport(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	noFile := func(string) ([]byte, error) { return nil, errors.New("no file") }

	tr, err := cliTransport(env(map[string]string{"SLACK_MCP_SERVER_CA_INSECURE": "1"}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("verification turned off")
	}
	if tr.Proxy != nil {
		t.Fatal("proxy set without SLACK_MCP_PROXY")
	}

	tr, err = cliTransport(env(map[string]string{"SLACK_MCP_PROXY": "http://proxy.local:3128"}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "https://slack.com/api/auth.test", nil)
	if u, _ := tr.Proxy(req); u == nil || u.Host != "proxy.local:3128" {
		t.Fatalf("proxy not honored: %v", u)
	}

	if _, err := cliTransport(env(map[string]string{"SLACK_MCP_PROXY": "http://user:pw@%zz"}), noFile); err == nil || strings.Contains(err.Error(), "pw") {
		t.Fatalf("bad proxy: %v", err)
	}
	if _, err := cliTransport(env(map[string]string{"SLACK_MCP_SERVER_CA": "/ca.pem"}), noFile); err == nil {
		t.Fatal("unreadable CA accepted")
	}
	notPEM := func(string) ([]byte, error) { return []byte("not a cert"), nil }
	if _, err := cliTransport(env(map[string]string{"SLACK_MCP_SERVER_CA": "/ca.pem"}), notPEM); err == nil {
		t.Fatal("CA file without a certificate accepted")
	}
	tr, _ = cliTransport(env(nil), noFile)
	if sys, _ := x509.SystemCertPool(); sys != nil && tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("no roots")
	}
}

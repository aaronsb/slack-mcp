package provider_test

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/slacktest"
)

// syncBuffer collects log output from the boot goroutines safely.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Slack's auth.test response sets a session cookie, and slack-go keeps the
// response headers on the result. Booting must log the team and user by
// name and nothing credential-shaped. The log is captured raw, without the
// redacting writer, so the log sites themselves are under test.
func TestBootNeverLogsCredentials(t *testing.T) {
	srv := slacktest.New(t)
	srv.HandleHeader("auth.test", "Set-Cookie",
		"uc=xoxd-FAKEsessioncookie%2Fvalue%3D; expires=Fri, 01 Jan 2100 00:00:00 GMT; path=/; domain=.slack.com; secure; HttpOnly")
	srv.HandleHeader("auth.test", "Set-Cookie", "d=xoxd-FAKEsecondcookie; path=/")

	var out syncBuffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ap := srv.Provider(t)
	if _, err := ap.Provide(); err != nil {
		t.Fatalf("Provide(): %v", err)
	}
	srv.Quiesce(t)
	ap.Shutdown()

	got := out.String()
	if srv.Calls("auth.test") == 0 {
		t.Fatal("boot did not call auth.test; the test would prove nothing")
	}
	if !strings.Contains(got, "Authenticated: team=Praecipio user=bockeliea") {
		t.Errorf("boot did not log the authenticated identity by name; log:\n%s", got)
	}
	for _, leak := range []string{"xox", "FAKE", "Set-Cookie"} {
		if strings.Contains(got, leak) {
			t.Errorf("boot log contains %q; log:\n%s", leak, got)
		}
	}
}

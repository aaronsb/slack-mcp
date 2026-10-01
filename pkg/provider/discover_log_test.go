package provider

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"
)

// lockedBuffer collects log output safely.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The production boot path runs auth.test to discover the team endpoint.
// Slack answers with a Set-Cookie holding a live session cookie, and slack-go
// keeps the response headers on the result, so logging the result leaks it.
// This drives that path against a fake host and captures the log raw, without
// the redacting writer, so the log site itself is under test.
func TestTeamDiscoveryNeverLogsCredentials(t *testing.T) {
	var authCalls, teamCalls int
	var mu sync.Mutex
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/auth.test":
			authCalls++
			w.Header().Add("Set-Cookie", "uc=xoxd-FAKEsessioncookie%2Fvalue%3D; path=/; domain=.slack.com; secure; HttpOnly")
			w.Header().Add("Set-Cookie", "d=xoxd-FAKEsecondcookie; path=/")
			w.Header().Set("Content-Type", "application/json")
			// The team URL points back here under /team/, so the
			// returned client can be shown to use it.
			_, _ = w.Write([]byte(`{"ok":true,"url":"` + srv.URL + `/team/","team":"Praecipio","user":"bockeliea","team_id":"T1","user_id":"U1"}`))
		case "/team/api/auth.test":
			teamCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"team":"Praecipio","user":"bockeliea"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out lockedBuffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	api := discoverTeamClient("xoxc-FAKEtoken", "xoxd-FAKEcookie", "", slack.OptionAPIURL(srv.URL+"/api/"))

	mu.Lock()
	if authCalls != 1 {
		t.Fatalf("discovery made %d auth.test calls, want 1; the test would prove nothing", authCalls)
	}
	mu.Unlock()

	// The returned client is bound to the discovered team endpoint.
	if _, err := api.AuthTestContext(context.Background()); err != nil {
		t.Fatalf("auth.test on the team client: %v", err)
	}
	mu.Lock()
	if teamCalls != 1 {
		t.Errorf("team endpoint got %d calls, want 1", teamCalls)
	}
	mu.Unlock()

	got := out.String()
	for _, leak := range []string{"xox", "FAKE", "Set-Cookie"} {
		if strings.Contains(got, leak) {
			t.Errorf("discovery log contains %q; log:\n%s", leak, got)
		}
	}
}

// When auth.test fails, discovery logs the failure and still returns a usable
// client on the discovery endpoint — and the failure line carries nothing
// from the response, including the Set-Cookie Slack may send with it.
func TestTeamDiscoveryFailureNeverLogsCredentials(t *testing.T) {
	var mu sync.Mutex
	var authCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/api/auth.test" {
			http.NotFound(w, r)
			return
		}
		authCalls++
		w.Header().Add("Set-Cookie", "uc=xoxd-FAKEfailurecookie%2Fv%3D; path=/; secure")
		w.Header().Add("Set-Cookie", "d=xoxd-FAKEfailuresecond; path=/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer srv.Close()

	var out lockedBuffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	api := discoverTeamClient("xoxc-FAKEtoken", "xoxd-FAKEcookie", "", slack.OptionAPIURL(srv.URL+"/api/"))
	if api == nil {
		t.Fatal("discovery returned no client on failure")
	}

	// Usable: the client still reaches the discovery host.
	if _, err := api.AuthTestContext(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Fatalf("client did not reach the discovery host: %v", err)
	}
	mu.Lock()
	if authCalls != 2 {
		t.Errorf("auth.test calls = %d, want 2 (discovery, then the returned client)", authCalls)
	}
	mu.Unlock()

	got := out.String()
	if !strings.Contains(got, "Slack authentication failed: invalid_auth") {
		t.Errorf("failure was not logged; log:\n%s", got)
	}
	for _, leak := range []string{"xox", "FAKE", "Set-Cookie"} {
		if strings.Contains(got, leak) {
			t.Errorf("discovery failure log contains %q; log:\n%s", leak, got)
		}
	}
}

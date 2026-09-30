package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireBearer(t *testing.T) {
	const key = "s3cret"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := RequireBearer(key, ok)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong key", "Bearer nope", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + key, http.StatusUnauthorized},
		{"bare key", key, http.StatusUnauthorized},
		{"right key", "Bearer " + key, http.StatusOK},
		{"lowercase scheme", "bearer " + key, http.StatusOK},
		{"uppercase scheme", "BEARER " + key, http.StatusOK},
		{"extra space", "Bearer  " + key, http.StatusOK},
		{"trailing space", "Bearer " + key + " ", http.StatusOK},
		{"longer key", "Bearer " + key + key, http.StatusUnauthorized},
	}
	// Both the SSE stream endpoint (GET) and the message endpoint (POST).
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/sse"},
		{http.MethodPost, "/message?sessionId=x"},
	}
	for _, ep := range endpoints {
		for _, c := range cases {
			t.Run(ep.method+" "+c.name, func(t *testing.T) {
				req := httptest.NewRequest(ep.method, ep.path, strings.NewReader("{}"))
				if c.header != "" {
					req.Header.Set("Authorization", c.header)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != c.want {
					t.Fatalf("got %d, want %d", rec.Code, c.want)
				}
			})
		}
	}
}

func TestRequireBearerEmptyKeyPassesThrough(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	rec := httptest.NewRecorder()
	RequireBearer("", ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestValidateSSEConfig(t *testing.T) {
	cases := []struct {
		host, key string
		wantErr   bool
	}{
		{"127.0.0.1", "", false},
		{"localhost", "", false},
		{"::1", "", false},
		{"0.0.0.0", "", true},
		{"192.168.1.5", "", true},
		{"example.com", "", true},
		{"0.0.0.0", "k1234567890123456", false},
		{"0.0.0.0", "short", true},
		{"0.0.0.0", "   ", true},
		{"0.0.0.0", "  short       ", true},
		{"", "", true},
		{"::", "", true},
		{"[::1]", "", false},
		{"LOCALHOST", "", false},
		{"127.0.0.1", "   ", false},
	}
	for _, c := range cases {
		err := ValidateSSEConfig(c.host, c.key)
		if (err != nil) != c.wantErr {
			t.Errorf("host=%q key=%q: err=%v, wantErr=%v", c.host, c.key, err, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "SLACK_MCP_SSE_API_KEY") {
			t.Errorf("error should name SLACK_MCP_SSE_API_KEY: %v", err)
		}
	}
}

func TestNewSSEHTTPServer(t *testing.T) {
	s := NewSemanticMCPServer(nil)
	const key = "0123456789abcdef-key"

	if _, _, err := s.NewSSEHTTPServer("0.0.0.0", "1", ""); err == nil {
		t.Fatal("non-loopback without key should fail")
	}
	if _, _, err := s.NewSSEHTTPServer("0.0.0.0", "1", "short"); err == nil {
		t.Fatal("non-loopback with short key should fail")
	}

	for host, want := range map[string]string{
		"127.0.0.1": "127.0.0.1:13080",
		"::1":       "[::1]:13080",
		"[::1]":     "[::1]:13080",
	} {
		hs, _, err := s.NewSSEHTTPServer(host, "13080", "")
		if err != nil {
			t.Fatalf("host %q: %v", host, err)
		}
		if hs.Addr != want {
			t.Errorf("host %q: Addr=%q, want %q", host, hs.Addr, want)
		}
	}

	hs, _, err := s.NewSSEHTTPServer("127.0.0.1", "13080", key)
	if err != nil {
		t.Fatal(err)
	}
	if hs.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set")
	}
	if hs.ReadTimeout != 0 || hs.WriteTimeout != 0 {
		t.Error("ReadTimeout/WriteTimeout would kill the SSE stream")
	}

	ts := httptest.NewServer(hs.Handler)
	defer ts.Close()

	do := func(method, path, auth string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader("{}"))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Without the key: both endpoints are rejected before reaching mcp-go.
	for _, ep := range []struct{ m, p string }{{"GET", "/sse"}, {"POST", "/message?sessionId=x"}} {
		resp := do(ep.m, ep.p, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without key: %d, want 401", ep.m, ep.p, resp.StatusCode)
		}
	}

	// With the key: /message reaches mcp-go (400 for an unknown session,
	// anything but 401).
	resp := do("POST", "/message?sessionId=x", "Bearer "+key)
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("POST /message with key: got 401")
	}

	// With the key: /sse opens an event stream announcing the endpoint.
	resp = do("GET", "/sse", "Bearer "+key)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sse with key: %d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(resp.Body, buf, 6)
	if !strings.Contains(string(buf[:n]), "event:") {
		t.Errorf("expected SSE event, got %q", buf[:n])
	}
}

package server

import (
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
		{"0.0.0.0", "k", false},
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

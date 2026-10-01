package setup

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A page on the loopback listener answers only requests that name the
// loopback address and its port, so a DNS-rebinding name reaches nothing.
func TestLoopbackOnlyRefusesOtherHosts(t *testing.T) {
	h := LoopbackOnly(51837, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for host, want := range map[string]int{
		"127.0.0.1:51837":    http.StatusOK,
		"localhost:51837":    http.StatusOK,
		"LOCALHOST:51837":    http.StatusOK,
		"127.0.0.1:51838":    http.StatusForbidden,
		"evil.example:51837": http.StatusForbidden,
		"127.0.0.1":          http.StatusForbidden,
		"":                   http.StatusForbidden,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: %d, want %d", host, w.Code, want)
		}
	}
}

// A slow sender cannot hold a connection, and so a page's lock, open.
func TestLocalServerBoundsReadsAndWrites(t *testing.T) {
	l := NewLocalServer(nil, 51837, http.NotFoundHandler())
	if l.server.ReadTimeout == 0 || l.server.WriteTimeout == 0 || l.server.ReadHeaderTimeout == 0 {
		t.Fatalf("timeouts: read %v write %v header %v", l.server.ReadTimeout, l.server.WriteTimeout, l.server.ReadHeaderTimeout)
	}
}

func TestIsLoopbackOrigin(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://127.0.0.1:51837":  true,
		"http://localhost:51837":  true,
		"http://127.0.0.1:51838":  false,
		"https://127.0.0.1:51837": false,
		"http://evil.example":     false,
		"null":                    false,
		"":                        false,
	} {
		if got := IsLoopbackOrigin(origin, 51837); got != want {
			t.Errorf("%q: %v, want %v", origin, got, want)
		}
	}
}

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

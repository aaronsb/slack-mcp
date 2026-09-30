package server

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// SSEAPIKeyEnv is the environment variable holding the SSE transport API key.
const SSEAPIKeyEnv = "SLACK_MCP_SSE_API_KEY"

// isLoopbackHost reports whether host binds only to the loopback interface.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ValidateSSEConfig refuses to serve SSE on a non-loopback host without an API key.
func ValidateSSEConfig(host, apiKey string) error {
	if apiKey == "" && !isLoopbackHost(host) {
		return fmt.Errorf("refusing to serve SSE on non-loopback host %q without authentication: set %s", host, SSEAPIKeyEnv)
	}
	return nil
}

// RequireBearer wraps next so every request must carry "Authorization: Bearer <apiKey>".
// An empty apiKey disables the check (loopback-only deployments).
func RequireBearer(apiKey string, next http.Handler) http.Handler {
	if apiKey == "" {
		return next
	}
	want := []byte(apiKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

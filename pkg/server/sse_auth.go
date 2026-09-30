package server

import (
	"crypto/sha256"
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

// minRemoteKeyLen is the shortest API key accepted when SSE is reachable
// beyond loopback.
const minRemoteKeyLen = 16

// ValidateSSEConfig refuses to serve SSE on a non-loopback host without a
// usable API key. The key value is never included in errors.
func ValidateSSEConfig(host, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if isLoopbackHost(host) {
		return nil
	}
	if apiKey == "" {
		return fmt.Errorf("refusing to serve SSE on non-loopback host %q without authentication: set %s", host, SSEAPIKeyEnv)
	}
	if len(apiKey) < minRemoteKeyLen {
		return fmt.Errorf("refusing to serve SSE on non-loopback host %q: %s must be at least %d characters", host, SSEAPIKeyEnv, minRemoteKeyLen)
	}
	return nil
}

// RequireBearer wraps next so every request must carry "Authorization: Bearer <apiKey>".
// The scheme is case-insensitive and surrounding whitespace is ignored. An
// empty (or whitespace-only) apiKey disables the check (loopback-only deployments).
func RequireBearer(apiKey string, next http.Handler) http.Handler {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return next
	}
	want := sha256.Sum256([]byte(apiKey))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bearerMatches(r.Header.Get("Authorization"), want) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerMatches compares digests so the key's length is not leaked.
func bearerMatches(header string, want [sha256.Size]byte) bool {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

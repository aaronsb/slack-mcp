package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
)

// authKey is a custom context key for storing the auth token.
type authKey struct{}

// withAuthKey adds an auth key to the context.
func withAuthKey(ctx context.Context, auth string) context.Context {
	return context.WithValue(ctx, authKey{}, auth)
}

// authFromRequest extracts the auth token from the request headers.
func authFromRequest(ctx context.Context, r *http.Request) context.Context {
	return withAuthKey(ctx, r.Header.Get("Authorization"))
}

// authFromEnv extracts the auth token from the environment
func authFromEnv(ctx context.Context) context.Context {
	return withAuthKey(ctx, os.Getenv("SLACK_MCP_SSE_API_KEY"))
}

// tokenFromContext extracts the auth token from the context.
// This can be used by tools to extract the token regardless of the
// transport being used by the server.
func tokenFromContext(ctx context.Context) (string, error) {
	auth, ok := ctx.Value(authKey{}).(string)
	if !ok {
		return "", fmt.Errorf("missing auth")
	}
	return auth, nil
}

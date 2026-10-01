package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEnv is an in-memory environment for loadDotEnv.
type fakeEnv map[string]string

func (e fakeEnv) lookup(k string) (string, bool) { v, ok := e[k]; return v, ok }
func (e fakeEnv) setenv(k, v string) error       { e[k] = v; return nil }

func writeDotEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	env := fakeEnv{}
	if err := loadDotEnv(filepath.Join(t.TempDir(), ".env"), env.lookup, env.setenv); err != nil {
		t.Fatalf("missing file: got %v, want nil", err)
	}
	if len(env) != 0 {
		t.Fatalf("missing file set %v", env)
	}
}

func TestLoadDotEnvSetsAllowlistedKeyWhenAbsent(t *testing.T) {
	env := fakeEnv{}
	path := writeDotEnv(t, "SLACK_MCP_PERSONALITY=bot\nSLACK_MCP_NO_BROWSER=1\n")
	if err := loadDotEnv(path, env.lookup, env.setenv); err != nil {
		t.Fatal(err)
	}
	if env["SLACK_MCP_PERSONALITY"] != "bot" || env["SLACK_MCP_NO_BROWSER"] != "1" {
		t.Fatalf("got %v", env)
	}
}

func TestLoadDotEnvAllowlistedKeyDoesNotOverride(t *testing.T) {
	env := fakeEnv{"SLACK_MCP_PERSONALITY": "client"}
	path := writeDotEnv(t, "SLACK_MCP_PERSONALITY=dotenv\n")
	if err := loadDotEnv(path, env.lookup, env.setenv); err != nil {
		t.Fatal(err)
	}
	if got := env["SLACK_MCP_PERSONALITY"]; got != "client" {
		t.Fatalf("SLACK_MCP_PERSONALITY = %q, want client", got)
	}
}

func TestLoadDotEnvRefusesDisallowedKey(t *testing.T) {
	env := fakeEnv{}
	path := writeDotEnv(t, "SLACK_MCP_PERSONALITY=bot\nSLACK_MCP_PROXY=http://x\nXDG_DATA_HOME=/tmp/x\n")
	err := loadDotEnv(path, env.lookup, env.setenv)
	if err == nil {
		t.Fatal("want error for disallowed keys")
	}
	for _, want := range []string{"SLACK_MCP_PROXY", "XDG_DATA_HOME", "SLACK_MCP_PERSONALITY, SLACK_MCP_NO_BROWSER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(env) != 0 {
		t.Fatalf("refused load still set %v", env)
	}
}

func TestLoadDotEnvIgnoresDisallowedKeyAlreadySet(t *testing.T) {
	env := fakeEnv{"SLACK_MCP_XOXC_TOKEN": "client"}
	path := writeDotEnv(t, "SLACK_MCP_XOXC_TOKEN=dotenv\n")
	if err := loadDotEnv(path, env.lookup, env.setenv); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if got := env["SLACK_MCP_XOXC_TOKEN"]; got != "client" {
		t.Fatalf("SLACK_MCP_XOXC_TOKEN = %q, want client", got)
	}
}

func TestLoadDotEnvMalformedFile(t *testing.T) {
	env := fakeEnv{}
	path := writeDotEnv(t, "SLACK_MCP_PERSONALITY='unterminated\n")
	if err := loadDotEnv(path, env.lookup, env.setenv); err == nil {
		t.Fatalf("malformed file: got nil error, set %v", env)
	}
}

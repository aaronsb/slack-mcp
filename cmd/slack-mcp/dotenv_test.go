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

// The log path decides where credentials-adjacent output lands, so only the
// client environment may move it; .env may not.
func TestLoadDotEnvRefusesLogFile(t *testing.T) {
	env := fakeEnv{}
	path := writeDotEnv(t, "SLACK_MCP_LOG_FILE=/tmp/shared.log\n")
	err := loadDotEnv(path, env.lookup, env.setenv)
	if err == nil || !strings.Contains(err.Error(), "SLACK_MCP_LOG_FILE") {
		t.Fatalf("got %v, want an error naming SLACK_MCP_LOG_FILE", err)
	}
	if _, set := env["SLACK_MCP_LOG_FILE"]; set {
		t.Fatal("refused load still set SLACK_MCP_LOG_FILE")
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

func TestLoadDotEnvParseErrorOmitsFileText(t *testing.T) {
	const secret = "xoxc-planted-secret-value"
	for _, body := range []string{
		"SLACK_MCP_XOXC_TOKEN=\"" + secret + "\n",
		"SLACK_MCP_PERSONALITY=bot\nbad key " + secret + "\n",
		"SLACK_MCP_XOXC_TOKEN='" + secret + "\n",
	} {
		env := fakeEnv{}
		err := loadDotEnv(writeDotEnv(t, body), env.lookup, env.setenv)
		if err == nil {
			t.Fatalf("body %q: want parse error", body)
		}
		if !strings.Contains(err.Error(), "does not parse") {
			t.Errorf("body %q: got %v, want a parse error", body, err)
		}
		for _, part := range []string{secret, "planted", "xoxc-"} {
			if strings.Contains(err.Error(), part) {
				t.Errorf("error %q leaks %q", err, part)
			}
		}
	}
}

func TestLoadDotEnvRefusesOtherSyntaxForms(t *testing.T) {
	for _, body := range []string{
		"export SLACK_MCP_PROXY=http://x\n",
		"SLACK_MCP_PROXY: http://x\n",
	} {
		env := fakeEnv{}
		err := loadDotEnv(writeDotEnv(t, body), env.lookup, env.setenv)
		if err == nil || !strings.Contains(err.Error(), `"SLACK_MCP_PROXY"`) {
			t.Errorf("body %q: got %v, want refusal naming SLACK_MCP_PROXY", body, err)
		}
		if len(env) != 0 {
			t.Errorf("body %q: refused load set %v", body, env)
		}
	}
}

func TestLoadDotEnvEmptyClientValueCountsAsSet(t *testing.T) {
	env := fakeEnv{"SLACK_MCP_PROXY": ""}
	path := writeDotEnv(t, "SLACK_MCP_PROXY=http://x\n")
	if err := loadDotEnv(path, env.lookup, env.setenv); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if got := env["SLACK_MCP_PROXY"]; got != "" {
		t.Fatalf("SLACK_MCP_PROXY = %q, want empty", got)
	}
}

func TestLoadDotEnvDirectoryRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv{}
	if err := loadDotEnv(path, env.lookup, env.setenv); err == nil {
		t.Fatal("want error when .env is a directory")
	}
}

func TestLoadDotEnvMalformedFile(t *testing.T) {
	env := fakeEnv{}
	path := writeDotEnv(t, "SLACK_MCP_PERSONALITY='unterminated\n")
	if err := loadDotEnv(path, env.lookup, env.setenv); err == nil {
		t.Fatalf("malformed file: got nil error, set %v", env)
	}
}

// ADR-012: the exchange-directory override comes only from the client
// environment; a .env that sets it refuses startup.
func TestLoadDotEnvRefusesExchangeDirOverride(t *testing.T) {
	env := fakeEnv{}
	path := writeDotEnv(t, "SLACK_MCP_EXCHANGE_DIR=/tmp/x\n")
	err := loadDotEnv(path, env.lookup, env.setenv)
	if err == nil || !strings.Contains(err.Error(), "SLACK_MCP_EXCHANGE_DIR") {
		t.Fatalf("want refusal naming SLACK_MCP_EXCHANGE_DIR, got %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("refused load still set %v", env)
	}
}

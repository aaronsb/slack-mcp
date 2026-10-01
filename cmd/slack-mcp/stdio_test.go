package main

// Process-level checks for the stdio lifecycle (#82). The test binary
// re-executes itself as the server, in no-auth mode against an empty home,
// so no Slack call and no real ledger is involved.

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

const asServer = "SLACK_MCP_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(asServer) == "1" {
		os.Args = os.Args[:1]
		main()
		return
	}
	os.Exit(m.Run())
}

// startServer returns the re-executed server command and the write end of its
// stdin, which the caller holds open to simulate a client that never closes.
func startServer(t *testing.T, env ...string) (*exec.Cmd, *os.File) {
	t.Helper()
	return startServerOut(t, nil, env...)
}

// startServerOut is startServer with the server's stdout wired to out.
func startServerOut(t *testing.T, out *os.File, env ...string) (*exec.Cmd, *os.File) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		asServer+"=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+home+"/.config",
		"XDG_DATA_HOME="+home+"/.local/share",
		"XDG_STATE_HOME="+home+"/.local/state",
		"SLACK_MCP_XOXC_TOKEN=",
		"SLACK_MCP_XOXD_TOKEN=",
		"SLACK_MCP_IDLE_TIMEOUT=",
		"SLACK_MCP_DEPLOYMENT=local",
	)
	cmd.Env = append(cmd.Env, env...)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = r
	cmd.Stdout = out
	t.Cleanup(func() { w.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	return cmd, w
}

// exitWithin waits for cmd and fails unless it exits 0 within d.
func exitWithin(t *testing.T, cmd *exec.Cmd, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with %v, want status 0", err)
		}
	case <-time.After(d):
		cmd.Process.Kill()
		t.Fatalf("server still running after %s", d)
	}
}

func TestStdioExitsOnEOF(t *testing.T) {
	cmd, w := startServer(t)
	w.Close()
	exitWithin(t, cmd, 10*time.Second)
}

func TestStdioExitsWhenIdle(t *testing.T) {
	cmd, _ := startServer(t, "SLACK_MCP_DEPLOYMENT=remote", "SLACK_MCP_IDLE_TIMEOUT=300ms")
	exitWithin(t, cmd, 10*time.Second)
}

// A local stdio server with an idle timeout would leave its client
// disconnected, so the server refuses the combination before it boots.
func TestStdioRefusesLocalIdleTimeout(t *testing.T) {
	cmd, _ := startServer(t, "SLACK_MCP_IDLE_TIMEOUT=300ms")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("server exited with %v, want status 1", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("server started with a local idle timeout")
	}
}

func TestStdioExitsCleanlyOnSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on windows")
	}
	cmd, _ := startServer(t)
	time.Sleep(300 * time.Millisecond) // let it install the handler
	cmd.Process.Signal(syscall.SIGTERM)
	exitWithin(t, cmd, 10*time.Second)
}

// A client that closes its read end of stdout and then sends a request must
// get a clean exit: the write returns EPIPE instead of SIGPIPE killing the
// process (status 141) before Shutdown runs.
func TestStdioExitsCleanlyOnClosedStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGPIPE on windows")
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd, in := startServerOut(t, outW)
	outW.Close()
	outR.Close() // the client is gone before the response is written
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}` + "\n"
	if _, err := in.WriteString(initialize); err != nil {
		t.Fatal(err)
	}
	exitWithin(t, cmd, 15*time.Second)
}

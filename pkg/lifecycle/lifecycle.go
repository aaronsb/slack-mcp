// Package lifecycle decides when a stdio server should stop on its own.
//
// A stdio server lives exactly as long as its client, and the only reliable
// sign of a client is the pipe. Stdin EOF is the clean case. The orphan cases
// (#82) never reach EOF: a wrapper killed without forwarding the signal
// re-parents the server while something else still holds the pipe open, and a
// wedged SSH connection keeps the channel alive with nobody on the other end.
// The parent watch catches the first, the idle timer the second.
//
// Each watcher ends the server by cancelling a context with a cause, so the
// caller can log why it stopped and shut down in one place.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Causes a watcher cancels with. All three are clean exits: the client is
// gone, so the process has nothing left to serve.
var (
	ErrStdinEOF = errors.New("stdin closed")
	ErrIdle     = errors.New("idle timeout")
	ErrOrphaned = errors.New("parent process exited")
)

// ParentPollInterval is how often WatchParent checks the parent PID.
const ParentPollInterval = 5 * time.Second

// Activity records when the client last sent anything. Safe for concurrent use.
type Activity struct {
	last atomic.Int64 // unix nanoseconds
}

// NewActivity returns an Activity stamped now, so the idle clock starts at boot.
func NewActivity() *Activity {
	a := &Activity{}
	a.Touch()
	return a
}

// Touch stamps the current time.
func (a *Activity) Touch() { a.last.Store(time.Now().UnixNano()) }

// Last returns the most recent Touch.
func (a *Activity) Last() time.Time { return time.Unix(0, a.last.Load()) }

type activityReader struct {
	r      io.Reader
	touch  func()
	onEOF  func()
	eofOne sync.Once
}

// ActivityReader wraps r, calling touch on every read that returns bytes and
// onEOF, once, when r reports io.EOF.
func ActivityReader(r io.Reader, touch func(), onEOF func()) io.Reader {
	return &activityReader{r: r, touch: touch, onEOF: onEOF}
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.touch()
	}
	if errors.Is(err, io.EOF) {
		a.eofOne.Do(a.onEOF)
	}
	return n, err
}

// WatchIdle cancels with ErrIdle once timeout passes with no activity. It
// sleeps until the earliest moment the timeout could expire and re-arms when
// activity has moved, so it neither polls nor overshoots. Returns when done
// closes. A timeout of zero or less disables the watch.
func WatchIdle(ctx context.Context, timeout time.Duration, last func() time.Time, cancel context.CancelCauseFunc) {
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		remaining := timeout - time.Since(last())
		if remaining <= 0 {
			log.Printf("No client input for %s; exiting", timeout)
			cancel(ErrIdle)
			return
		}
		timer.Reset(remaining)
	}
}

// WatchParent cancels with ErrOrphaned when getppid stops returning start, the
// parent PID the caller read synchronously at startup (reading it here would
// race a parent that dies before this goroutine runs). The comparison is against the starting PID rather
// than 1, because an orphan re-parents to the nearest subreaper — observed as
// systemd --user, not init. Windows does not re-parent, so there the watch is
// a no-op. Returns when ctx ends.
func WatchParent(ctx context.Context, start int, getppid func() int, interval time.Duration, cancel context.CancelCauseFunc) {
	if runtime.GOOS == "windows" {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if now := getppid(); now != start {
			log.Printf("Parent process %d exited (re-parented to %d); exiting", start, now)
			cancel(ErrOrphaned)
			return
		}
	}
}

// Deployment is where a stdio server runs relative to its client. It decides
// whether an idle timeout is valid (#82).
type Deployment string

const (
	// Local: the client spawned the server on the same machine. Nothing can
	// wedge the pipe, and Claude Code does not respawn a stdio server that
	// exits, so an idle exit would only leave a healthy client disconnected.
	Local Deployment = "local"
	// Remote: the client reaches the server over SSH, declared explicitly with
	// SLACK_MCP_DEPLOYMENT=remote. A half-open connection
	// keeps the pipe open with nobody reading, which only the idle timer sees.
	Remote Deployment = "remote"
)

// DefaultRemoteIdle is the idle timeout a remote stdio server gets when
// SLACK_MCP_IDLE_TIMEOUT is unset.
const DefaultRemoteIdle = 2 * time.Hour

// DeploymentFromEnv reads SLACK_MCP_DEPLOYMENT ("local" or "remote",
// case-insensitive). Unset means Local. The environment is deliberately not
// probed: SSH_CONNECTION only says some ancestor logged in over SSH (running
// claude inside an SSH session, VS Code Remote-SSH), not that the MCP pipe
// crosses SSH. Only the MCP client config that launches the server knows.
func DeploymentFromEnv() (Deployment, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("SLACK_MCP_DEPLOYMENT"))); v {
	case "local":
		return Local, nil
	case "remote":
		return Remote, nil
	case "":
		return Local, nil
	default:
		return "", fmt.Errorf("SLACK_MCP_DEPLOYMENT=%q: want local or remote", v)
	}
}

// IdleTimeoutFromEnv resolves SLACK_MCP_IDLE_TIMEOUT for the given transport
// and the deployment from DeploymentFromEnv. See IdlePolicy.
func IdleTimeoutFromEnv(transport string) (time.Duration, error) {
	dep, err := DeploymentFromEnv()
	if err != nil {
		return 0, err
	}
	return IdlePolicy(transport, dep, os.Getenv("SLACK_MCP_IDLE_TIMEOUT"))
}

// IdlePolicy returns the idle timeout for a transport and deployment, given
// the raw SLACK_MCP_IDLE_TIMEOUT value, or an error naming the invalid
// combination so the server refuses to start rather than run with a timeout
// that cannot help. Only remote stdio may idle out, and it does by default;
// "0" or "off" disables it there and is accepted everywhere.
func IdlePolicy(transport string, dep Deployment, raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	var d time.Duration
	switch strings.ToLower(raw) {
	case "":
		if transport == "stdio" && dep == Remote {
			return DefaultRemoteIdle, nil
		}
		return 0, nil
	case "0", "off":
		return 0, nil
	default:
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("SLACK_MCP_IDLE_TIMEOUT=%q: want a duration like 90m, or off", raw)
		}
		d = parsed
	}
	switch {
	case transport != "stdio":
		if transport == "sse" {
			return 0, fmt.Errorf("SLACK_MCP_IDLE_TIMEOUT applies only to stdio; sse cleans up each session when its connection ends")
		}
		return 0, fmt.Errorf("SLACK_MCP_IDLE_TIMEOUT applies only to stdio; transport %q has no idle timeout", transport)
	case dep == Local:
		return 0, fmt.Errorf("SLACK_MCP_IDLE_TIMEOUT is not valid for a local stdio server: the client does not respawn a server that exits. Set SLACK_MCP_DEPLOYMENT=remote in the MCP client config if this server is reached over SSH")
	}
	return d, nil
}

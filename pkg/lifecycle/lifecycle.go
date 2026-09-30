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

// WatchParent cancels with ErrOrphaned when getppid stops returning the value
// it returned at start. The comparison is against the starting PID rather
// than 1, because an orphan re-parents to the nearest subreaper — observed as
// systemd --user, not init. Windows does not re-parent, so there the watch is
// a no-op. Returns when ctx ends.
func WatchParent(ctx context.Context, getppid func() int, interval time.Duration, cancel context.CancelCauseFunc) {
	if runtime.GOOS == "windows" {
		return
	}
	start := getppid()
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

// IdleTimeoutFromEnv reads SLACK_MCP_IDLE_TIMEOUT as a Go duration ("90m",
// "2h"). Unset, "0" and "off" all disable the timer. It is opt-in because a
// healthy client that merely goes quiet cannot tell an idle exit from a
// crash: Claude Code does not respawn a stdio server that exits, so the
// operator sees it disconnected until /mcp reconnects it. Where orphans come
// from wedged connections (remote hosts over SSH), set it. This is a knob
// where the estate has none: when to give up on a silent client is operator
// policy, not something the server can learn.
func IdleTimeoutFromEnv() time.Duration {
	return parseIdleTimeout(os.Getenv("SLACK_MCP_IDLE_TIMEOUT"))
}

func parseIdleTimeout(v string) time.Duration {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "", "0", "off":
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("Ignoring SLACK_MCP_IDLE_TIMEOUT=%q (want a duration like 90m, or off); idle exit stays off", v)
		return 0
	}
	return d
}

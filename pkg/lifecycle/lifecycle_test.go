package lifecycle

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActivityReaderTouchesAndSignalsEOFOnce(t *testing.T) {
	var touches, eofs int
	r := ActivityReader(strings.NewReader("abc"), func() { touches++ }, func() { eofs++ })

	buf := make([]byte, 1)
	for {
		if _, err := r.Read(buf); err == io.EOF {
			break
		}
	}
	// A reader past EOF keeps answering EOF; the callback must not re-fire.
	r.Read(buf)

	if touches != 3 {
		t.Errorf("touches = %d, want 3 (one per non-empty read)", touches)
	}
	if eofs != 1 {
		t.Errorf("onEOF calls = %d, want 1", eofs)
	}
}

// watch runs f with a cancel-cause context and returns the cause it ended
// with, or nil if it had not fired by the deadline.
func watch(t *testing.T, deadline time.Duration, f func(context.Context, context.CancelCauseFunc)) error {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go f(ctx, cancel)
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(deadline):
		return nil
	}
}

func TestWatchIdleFiresAfterSilence(t *testing.T) {
	a := NewActivity()
	start := time.Now()
	err := watch(t, time.Second, func(ctx context.Context, cancel context.CancelCauseFunc) {
		WatchIdle(ctx, 50*time.Millisecond, a.Last, cancel)
	})
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("cause = %v, want ErrIdle", err)
	}
	if el := time.Since(start); el < 50*time.Millisecond {
		t.Errorf("fired after %s, before the timeout", el)
	}
}

func TestWatchIdleHeldOffByActivity(t *testing.T) {
	a := NewActivity()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				a.Touch()
			}
		}
	}()
	err := watch(t, 200*time.Millisecond, func(ctx context.Context, cancel context.CancelCauseFunc) {
		WatchIdle(ctx, 50*time.Millisecond, a.Last, cancel)
	})
	if err != nil {
		t.Fatalf("fired with %v while activity continued", err)
	}
}

func TestWatchIdleDisabled(t *testing.T) {
	stale := func() time.Time { return time.Time{} }
	err := watch(t, 50*time.Millisecond, func(ctx context.Context, cancel context.CancelCauseFunc) {
		WatchIdle(ctx, 0, stale, cancel)
	})
	if err != nil {
		t.Fatalf("zero timeout fired with %v", err)
	}
}

func TestWatchParentFiresOnReparent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("parent watch is a no-op on windows")
	}
	var ppid atomic.Int64
	ppid.Store(100)
	getppid := func() int { return int(ppid.Load()) }

	// Stable parent: no fire.
	err := watch(t, 50*time.Millisecond, func(ctx context.Context, cancel context.CancelCauseFunc) {
		WatchParent(ctx, getppid, 5*time.Millisecond, cancel)
	})
	if err != nil {
		t.Fatalf("fired with %v while the parent stayed", err)
	}

	// Re-parented to a subreaper, not init: still an orphan.
	err = watch(t, time.Second, func(ctx context.Context, cancel context.CancelCauseFunc) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			ppid.Store(1457)
		}()
		WatchParent(ctx, getppid, 5*time.Millisecond, cancel)
	})
	if !errors.Is(err, ErrOrphaned) {
		t.Fatalf("cause = %v, want ErrOrphaned", err)
	}
}

func TestIdlePolicy(t *testing.T) {
	cases := []struct {
		transport string
		dep       Deployment
		raw       string
		want      time.Duration
		wantErr   bool
	}{
		{"stdio", Local, "", 0, false},
		{"stdio", Local, "off", 0, false},
		{"stdio", Local, "0", 0, false},
		{"stdio", Local, "90m", 0, true},
		{"stdio", Remote, "", DefaultRemoteIdle, false},
		{"stdio", Remote, "OFF", 0, false},
		{"stdio", Remote, " 90s ", 90 * time.Second, false},
		{"stdio", Remote, "garbage", 0, true},
		{"stdio", Remote, "-5m", 0, true},
		{"sse", Local, "", 0, false},
		{"sse", Remote, "", 0, false},
		{"sse", Remote, "off", 0, false},
		{"sse", Remote, "2h", 0, true},
		{"sse", Local, "garbage", 0, true},
	}
	for _, c := range cases {
		got, err := IdlePolicy(c.transport, c.dep, c.raw)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("IdlePolicy(%s, %s, %q) = %s, %v; want %s, err=%v", c.transport, c.dep, c.raw, got, err, c.want, c.wantErr)
		}
	}
}

func TestDeploymentFromEnv(t *testing.T) {
	cases := []struct {
		explicit, sshConn, sshClient string
		want                         Deployment
		wantErr                      bool
	}{
		{"", "", "", Local, false},
		{"", "10.0.0.1 5000 10.0.0.2 22", "", Remote, false},
		{"", "", "10.0.0.1 5000 22", Remote, false},
		{"local", "10.0.0.1 5000 10.0.0.2 22", "", Local, false},
		{"Remote", "", "", Remote, false},
		{"cloud", "", "", "", true},
	}
	for _, c := range cases {
		t.Setenv("SLACK_MCP_DEPLOYMENT", c.explicit)
		t.Setenv("SSH_CONNECTION", c.sshConn)
		t.Setenv("SSH_CLIENT", c.sshClient)
		got, err := DeploymentFromEnv()
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("DeploymentFromEnv(%q, ssh=%q/%q) = %q, %v; want %q, err=%v", c.explicit, c.sshConn, c.sshClient, got, err, c.want, c.wantErr)
		}
	}
}

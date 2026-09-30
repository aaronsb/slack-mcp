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

func TestParseIdleTimeout(t *testing.T) {
	cases := map[string]time.Duration{
		"":        0,
		"0":       0,
		"off":     0,
		"OFF":     0,
		"90s":     90 * time.Second,
		" 2h ":    2 * time.Hour,
		"garbage": 0,
		"-5m":     0,
	}
	for in, want := range cases {
		if got := parseIdleTimeout(in); got != want {
			t.Errorf("parseIdleTimeout(%q) = %s, want %s", in, got, want)
		}
	}
}

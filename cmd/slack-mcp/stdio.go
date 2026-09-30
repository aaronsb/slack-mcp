package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/lifecycle"
	"github.com/aaronsb/slack-mcp/pkg/server"
)

const (
	// eofDrain lets tool calls already queued when the client closes stdin
	// finish and answer. A call still running after it is cancelled, so a
	// Slack request hung on a dead connection cannot hold the process open.
	eofDrain = 5 * time.Second
	// shutdownTimeout bounds the provider's flush and ledger closes.
	shutdownTimeout = 5 * time.Second
	// forcedExit is the backstop from the moment the server starts stopping:
	// a handler that ignores its context must not recreate the orphan.
	forcedExit = 10 * time.Second
)

// runStdio serves MCP over stdio until the client is gone, then shuts the
// provider down and returns the exit status. The client is gone when stdin
// reaches EOF, a termination signal arrives, the parent process exits, or no
// input arrives for idle, when it is non-zero (#82). All of those are clean
// exits; only a transport error returns 1.
func runStdio(s *server.SemanticMCPServer, idle time.Duration) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	context.AfterFunc(ctx, func() {
		time.AfterFunc(forcedExit, func() {
			log.Printf("Shutdown did not finish within %s; forcing exit", forcedExit)
			os.Exit(1)
		})
	})

	// A write to a closed stdout must return EPIPE, not kill the process with
	// SIGPIPE before Shutdown runs.
	signal.Ignore(syscall.SIGPIPE)

	// SIGHUP too: its default action kills the process before the ledgers
	// close.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		select {
		case sig := <-sigs:
			// Restore default handling so a second Ctrl-C is not swallowed
			// while shutdown runs.
			signal.Stop(sigs)
			cancel(fmt.Errorf("received %v", sig))
		case <-ctx.Done():
		}
	}()

	// Read synchronously: if the parent dies before the watcher goroutine is
	// scheduled, a baseline read there would already be the subreaper.
	startPPID := os.Getppid()

	activity := lifecycle.NewActivity()
	if idle > 0 {
		log.Printf("Idle exit enabled: %s without client input", idle)
		go lifecycle.WatchIdle(ctx, idle, activity.Last, cancel)
	}
	go lifecycle.WatchParent(ctx, startPPID, os.Getppid, lifecycle.ParentPollInterval, cancel)

	in := lifecycle.ActivityReader(os.Stdin, activity.Touch, func() {
		time.AfterFunc(eofDrain, func() { cancel(lifecycle.ErrStdinEOF) })
	})
	err := s.ServeStdio(ctx, in)

	// Listen returns on its own only at EOF once the queue drains; every
	// other stop has already set its cause, and a second cancel keeps it.
	status := 0
	switch {
	case err != nil && (errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe)):
		// The client closed its read end: it is gone, which is a clean exit.
		cancel(fmt.Errorf("client closed stdout: %w", err))
	case err != nil && !errors.Is(err, context.Canceled):
		cancel(err)
		status = 1
	default:
		cancel(lifecycle.ErrStdinEOF)
	}
	log.Printf("Stopping: %v", context.Cause(ctx))
	s.Shutdown(shutdownTimeout)
	log.Println("Shutdown complete")
	return status
}

package provider

import (
	"log"
	"runtime/debug"
)

// Guard runs f, converting a panic into a logged error with its stack. An
// unrecovered panic in any goroutine kills the whole process, so every
// background goroutine the provider or server spawns runs under this; the
// request path has its own recovery in the MCP layer (server.WithRecovery).
//
// Guard does not restart f: a panicked loop task (backfill, sweep
// scheduler) ends and stays ended until the next boot. That is deliberate
// — a task that panics once will usually panic again on the same state,
// and the coverage surfaces the resulting staleness.
func Guard(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic recovered in background task %s: %v\n%s", name, r, debug.Stack())
		}
	}()
	f()
}

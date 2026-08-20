package provider

import "log"

// Guard runs f, converting a panic into a logged error. An unrecovered
// panic in any goroutine kills the whole process, so every background
// goroutine the provider or server spawns runs under this; the request
// path has its own recovery in the MCP layer (server.WithRecovery).
func Guard(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic recovered in background task %s: %v", name, r)
		}
	}()
	f()
}

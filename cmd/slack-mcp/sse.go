package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/aaronsb/slack-mcp/pkg/server"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// runSSE serves httpServer until it fails or a termination signal arrives,
// then closes SSE sessions, drains the HTTP server and shuts the provider
// down. A signal is a clean exit; only a listen error returns 1.
func runSSE(s *server.SemanticMCPServer, httpServer *http.Server, sseServer *mcpserver.SSEServer) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	errc := make(chan error, 1)
	go func() {
		log.Printf("SSE server listening on %s", httpServer.Addr)
		errc <- httpServer.ListenAndServe()
	}()

	status := 0
	select {
	case sig := <-sigs:
		signal.Stop(sigs) // a second Ctrl-C is not swallowed
		log.Printf("Stopping: received %v", sig)
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Server error: %v", err)
			status = 1
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Close sessions first: open SSE streams would otherwise hold Shutdown
	// until its deadline.
	sseServer.CloseSessions()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	s.Shutdown(shutdownTimeout)
	log.Println("Shutdown complete")
	return status
}

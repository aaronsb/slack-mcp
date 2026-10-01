package setup

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LocalServer serves one handler on a loopback listener for a page the
// operator opens in their browser: token setup, and ADR-013's clearing
// page. Every request must name the loopback address and this port in its
// Host header, so a DNS-rebinding page in the browser reaches nothing.
type LocalServer struct {
	server   *http.Server
	listener net.Listener
	port     int
	stopOnce sync.Once
}

// NewLocalServer wraps h for the listener at port. Call Start to serve.
func NewLocalServer(listener net.Listener, port int, h http.Handler) *LocalServer {
	return &LocalServer{
		server: &http.Server{
			Handler:           LoopbackOnly(port, h),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
		},
		listener: listener,
		port:     port,
	}
}

// Start begins serving in the background.
func (l *LocalServer) Start() {
	go func() {
		if err := l.server.Serve(l.listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Local server error: %v", err)
		}
	}()
}

// Stop shuts the server down, closing any connection a graceful shutdown
// leaves open; later calls do nothing.
func (l *LocalServer) Stop() {
	l.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := l.server.Shutdown(ctx); err != nil {
			_ = l.server.Close()
		}
	})
}

// Port returns the port the server listens on.
func (l *LocalServer) Port() int { return l.port }

// BaseURL is the origin the browser should open: the loopback address,
// not "localhost", which may resolve to an address nothing listens on.
func (l *LocalServer) BaseURL() string { return "http://127.0.0.1:" + strconv.Itoa(l.port) }

// LoopbackOnly refuses a request whose Host is not 127.0.0.1 or localhost
// at port.
func LoopbackOnly(port int, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsLoopbackHost(r.Host, port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// IsLoopbackHost reports whether host (a Host header) is 127.0.0.1 or
// localhost at port.
func IsLoopbackHost(host string, port int) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != strconv.Itoa(port) {
		return false
	}
	return h == "127.0.0.1" || strings.EqualFold(h, "localhost")
}

// IsLoopbackOrigin reports whether origin (an Origin header) is this
// server's own: http on 127.0.0.1 or localhost at port.
func IsLoopbackOrigin(origin string, port int) bool {
	for _, h := range []string{"127.0.0.1", "localhost"} {
		if strings.EqualFold(origin, fmt.Sprintf("http://%s:%d", h, port)) {
			return true
		}
	}
	return false
}

package setup

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed page.html
var pageHTML string

const (
	startPort  = 51837
	maxRetries = 10
)

// tokenPayload is what the browser snippet POSTs
type tokenPayload struct {
	Xoxc string `json:"xoxc"`
	Xoxd string `json:"xoxd"`
}

// TokenResult holds extracted and validated tokens
type TokenResult struct {
	Xoxc   string
	Xoxd   string
	Team   string
	User   string
	UserID string
	Err    error
}

// setupResult holds the outcome for the HTML status endpoint
type setupResult struct {
	Done  bool   `json:"done"`
	OK    bool   `json:"ok"`
	Team  string `json:"team,omitempty"`
	User  string `json:"user,omitempty"`
	Error string `json:"error,omitempty"`
}

// CallbackServer is a non-blocking localhost HTTP server that receives tokens
// from browser snippets, Firefox extensions, or the manual setup page.
type CallbackServer struct {
	server   *http.Server
	listener net.Listener
	port     int

	mu        sync.Mutex
	result    *TokenResult
	sResult   *setupResult // for HTML status polling
	done      chan struct{}
	closeOnce sync.Once
}

// NewCallbackServer creates a callback server on the given listener.
// Call Start() to begin serving.
func NewCallbackServer(listener net.Listener, port int) *CallbackServer {
	cs := &CallbackServer{
		listener: listener,
		port:     port,
		done:     make(chan struct{}),
	}

	mux := http.NewServeMux()

	// Serve the manual setup page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page := strings.ReplaceAll(pageHTML, "{{PORT}}", fmt.Sprintf("%d", port))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})

	// Receive tokens from browser snippet or extension
	mux.HandleFunc("/callback", cs.handleCallback)

	// Status endpoint for the page to poll
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cs.mu.Lock()
		defer cs.mu.Unlock()
		if cs.sResult != nil {
			json.NewEncoder(w).Encode(cs.sResult)
		} else {
			json.NewEncoder(w).Encode(setupResult{Done: false})
		}
	})

	cs.server = &http.Server{Handler: mux}
	return cs
}

func (cs *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Restrict CORS to localhost origins only — the callback server binds to
	// 127.0.0.1, so only localhost origins should be posting tokens here.
	origin := r.Header.Get("Origin")
	if origin == "" || strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1") {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "failed to read body"})
		return
	}

	var payload tokenPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}

	if payload.Xoxc == "" || payload.Xoxd == "" {
		msg := "missing tokens"
		if payload.Xoxc == "" {
			msg = "could not extract xoxc token from localStorage — make sure you're on a Slack tab"
		} else {
			msg = "could not extract xoxd cookie — make sure you're logged into Slack"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
		return
	}

	vTeam, vUser, vUserID, vErr := validateTokens(payload.Xoxc, payload.Xoxd)
	if vErr != nil {
		cs.mu.Lock()
		cs.sResult = &setupResult{Done: true, OK: false, Error: fmt.Sprintf("token validation failed: %v", vErr)}
		cs.result = &TokenResult{Err: vErr}
		cs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": cs.sResult.Error})
		return
	}

	cfg, cfgErr := LoadConfig()
	if cfgErr != nil {
		cfg = &Config{Workspaces: make(map[string]WorkspaceConfig)}
	}

	cfg.Workspaces[vTeam] = WorkspaceConfig{
		XoxcToken: payload.Xoxc,
		XoxdToken: payload.Xoxd,
		TeamName:  vTeam,
		UserName:  vUser,
		UserID:    vUserID,
	}

	if cfg.DefaultWorkspace == "" {
		cfg.DefaultWorkspace = vTeam
	}

	if saveErr := SaveConfig(cfg); saveErr != nil {
		cs.mu.Lock()
		cs.sResult = &setupResult{Done: true, OK: false, Error: fmt.Sprintf("failed to save config: %v", saveErr)}
		cs.result = &TokenResult{Err: saveErr}
		cs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": cs.sResult.Error})
		return
	}

	cs.mu.Lock()
	cs.sResult = &setupResult{Done: true, OK: true, Team: vTeam, User: vUser}
	cs.result = &TokenResult{
		Xoxc: payload.Xoxc, Xoxd: payload.Xoxd,
		Team: vTeam, User: vUser, UserID: vUserID,
	}
	cs.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "team": vTeam, "user": vUser})

	go func() {
		time.Sleep(500 * time.Millisecond)
		cs.closeOnce.Do(func() { close(cs.done) })
	}()
}

// Start begins serving in the background. Non-blocking.
func (cs *CallbackServer) Start() {
	go func() {
		if err := cs.server.Serve(cs.listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Callback server error: %v", err)
		}
	}()
}

// Port returns the port the server is listening on
func (cs *CallbackServer) Port() int {
	return cs.port
}

// Result returns the token result, or nil if tokens haven't been received yet
func (cs *CallbackServer) Result() *TokenResult {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.result
}

// Done returns a channel that's closed when tokens are received
func (cs *CallbackServer) Done() <-chan struct{} {
	return cs.done
}

// Stop shuts down the callback server
func (cs *CallbackServer) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cs.server.Shutdown(ctx)
}

// RunSetup starts the local HTTP server for token extraction (CLI entry point)
func RunSetup() error {
	port, listener, err := FindPort()
	if err != nil {
		return fmt.Errorf("could not find available port: %w", err)
	}

	fmt.Printf("\n  Slack MCP Setup\n")
	fmt.Printf("  ──────────────\n")
	fmt.Printf("  Starting setup server on localhost:%d...\n\n", port)

	// Open browser
	go func() {
		time.Sleep(300 * time.Millisecond)
		url := fmt.Sprintf("http://localhost:%d", port)
		fmt.Printf("  Opening browser to %s\n", url)
		fmt.Printf("  Follow the instructions there to connect your Slack workspace.\n\n")
		fmt.Printf("  Waiting for tokens... (Ctrl+C to cancel)\n\n")
		OpenBrowserURL(url)
	}()

	team, user, err := RunSetupServer(listener, port)
	if err != nil {
		fmt.Printf("  ✗ Setup failed: %s\n\n", err)
		return nil
	}

	fmt.Printf("  ✓ Got tokens for workspace %q (user: %s)\n", team, user)
	fmt.Printf("  ✓ Saved to %s\n\n", ConfigPath())
	return nil
}

// RunSetupServer runs the setup HTTP server on the given listener.
// It blocks until the user completes the browser flow.
// Returns the workspace team name and user on success.
func RunSetupServer(listener net.Listener, port int) (team, user string, err error) {
	cs := NewCallbackServer(listener, port)
	cs.Start()

	// Wait for completion
	<-cs.Done()
	cs.Stop()

	r := cs.Result()
	if r != nil && r.Err == nil {
		return r.Team, r.User, nil
	}
	if r != nil {
		return "", "", r.Err
	}
	return "", "", fmt.Errorf("setup completed without result")
}

// FindPort tries ports starting from startPort, incrementing on collision
func FindPort() (int, net.Listener, error) {
	for i := 0; i < maxRetries; i++ {
		port := startPort + i
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return port, listener, nil
		}
	}
	return 0, nil, fmt.Errorf("no available port found in range %d-%d", startPort, startPort+maxRetries-1)
}

// validateTokens checks tokens against Slack's auth.test API
func validateTokens(xoxc, xoxd string) (team, user, userID string, err error) {
	req, err := http.NewRequest("POST", "https://slack.com/api/auth.test", nil)
	if err != nil {
		return "", "", "", err
	}

	req.Header.Set("Authorization", "Bearer "+xoxc)
	req.Header.Set("Cookie", "d="+xoxd)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read response: %w", err)
	}

	var authResult struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Team   string `json:"team"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
	}

	if err := json.Unmarshal(body, &authResult); err != nil {
		return "", "", "", fmt.Errorf("invalid response: %w", err)
	}

	if !authResult.OK {
		return "", "", "", fmt.Errorf("Slack API error: %s", authResult.Error)
	}

	return authResult.Team, authResult.User, authResult.UserID, nil
}

// OpenBrowserURL opens the default browser to the given URL
func OpenBrowserURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Could not open browser: %v", err)
		fmt.Printf("  Please open this URL manually: %s\n", url)
	}
}

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

// setupResult holds the outcome of token validation
type setupResult struct {
	Done  bool   `json:"done"`
	OK    bool   `json:"ok"`
	Team  string `json:"team,omitempty"`
	User  string `json:"user,omitempty"`
	Error string `json:"error,omitempty"`
}

// RunSetup starts the local HTTP server for token extraction
func RunSetup() error {
	// Find an available port
	port, listener, err := findPort()
	if err != nil {
		return fmt.Errorf("could not find available port: %w", err)
	}

	fmt.Printf("\n  Slack MCP Setup\n")
	fmt.Printf("  ──────────────\n")
	fmt.Printf("  Starting setup server on localhost:%d...\n\n", port)

	var (
		mu     sync.Mutex
		result *setupResult
		done   = make(chan struct{})
	)

	mux := http.NewServeMux()

	// Serve the setup page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page := strings.ReplaceAll(pageHTML, "{{PORT}}", fmt.Sprintf("%d", port))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})

	// Receive tokens from the browser snippet
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// CORS for the Slack page POSTing to localhost
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
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

		// Validate tokens with Slack
		team, user, userID, err := validateTokens(payload.Xoxc, payload.Xoxd)
		if err != nil {
			mu.Lock()
			result = &setupResult{Done: true, OK: false, Error: fmt.Sprintf("token validation failed: %v", err)}
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": result.Error})
			return
		}

		// Save to config
		cfg, err := LoadConfig()
		if err != nil {
			cfg = &Config{Workspaces: make(map[string]WorkspaceConfig)}
		}

		cfg.Workspaces[team] = WorkspaceConfig{
			XoxcToken: payload.Xoxc,
			XoxdToken: payload.Xoxd,
			TeamName:  team,
			UserName:  user,
			UserID:    userID,
		}

		if cfg.DefaultWorkspace == "" {
			cfg.DefaultWorkspace = team
		}

		if err := SaveConfig(cfg); err != nil {
			mu.Lock()
			result = &setupResult{Done: true, OK: false, Error: fmt.Sprintf("failed to save config: %v", err)}
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": result.Error})
			return
		}

		mu.Lock()
		result = &setupResult{Done: true, OK: true, Team: team, User: user}
		mu.Unlock()

		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "team": team, "user": user})

		// Signal completion after a brief delay so the response gets sent
		go func() {
			time.Sleep(500 * time.Millisecond)
			close(done)
		}()
	})

	// CORS preflight for /callback
	mux.HandleFunc("/callback/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
		}
	})

	// Status endpoint for the page to poll
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if result != nil {
			json.NewEncoder(w).Encode(result)
		} else {
			json.NewEncoder(w).Encode(setupResult{Done: false})
		}
	})

	server := &http.Server{Handler: mux}

	// Open browser
	go func() {
		time.Sleep(300 * time.Millisecond)
		url := fmt.Sprintf("http://localhost:%d", port)
		fmt.Printf("  Opening browser to %s\n", url)
		fmt.Printf("  Follow the instructions there to connect your Slack workspace.\n\n")
		fmt.Printf("  Waiting for tokens... (Ctrl+C to cancel)\n\n")
		openBrowser(url)
	}()

	// Start server
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// Wait for completion or interrupt
	<-done

	// Print result
	mu.Lock()
	r := result
	mu.Unlock()

	if r != nil && r.OK {
		fmt.Printf("  ✓ Got tokens for workspace %q (user: %s)\n", r.Team, r.User)
		fmt.Printf("  ✓ Saved to %s\n\n", ConfigPath())
	} else if r != nil {
		fmt.Printf("  ✗ Setup failed: %s\n\n", r.Error)
	}

	// Shutdown server
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.Shutdown(ctx)

	return nil
}

// findPort tries ports starting from startPort, incrementing on collision
func findPort() (int, net.Listener, error) {
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

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Team  string `json:"team"`
		User  string `json:"user"`
		UserID string `json:"user_id"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", "", fmt.Errorf("invalid response: %w", err)
	}

	if !result.OK {
		return "", "", "", fmt.Errorf("Slack API error: %s", result.Error)
	}

	return result.Team, result.User, result.UserID, nil
}

// openBrowser opens the default browser to the given URL
func openBrowser(url string) {
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

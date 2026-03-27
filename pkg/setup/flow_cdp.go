package setup

// Network isolation: This file MUST NOT call browser.Get(), browser.MustGet(),
// or any rod download function. We launch Chrome directly via os/exec and
// connect rod to its debug port. No rod launcher is used.
// CI check: make check-network-isolation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const cdpExtractTimeout = 120 * time.Second

// CDPExtractor manages the async lifecycle of browser launch + token extraction.
type CDPExtractor struct {
	mu     sync.Mutex
	result *TokenResult
	cancel context.CancelFunc
	cmd    *exec.Cmd
	tmpDir string
	done   chan struct{}
}

// StartCDPExtraction launches Chrome in the background and begins extraction.
// Returns immediately. Poll Result() or wait on Done() for completion.
func StartCDPExtraction(browserPath, userDataDir, profileDir string) (*CDPExtractor, error) {
	tmpDir, err := prepareCDPUserDataDir(userDataDir, profileDir)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare user data dir: %w", err)
	}

	debugPort, err := findFreePort()
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cdpExtractTimeout)

	cmd := exec.CommandContext(ctx, browserPath,
		fmt.Sprintf("--user-data-dir=%s", tmpDir),
		fmt.Sprintf("--profile-directory=%s", profileDir),
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--no-first-run",
		"--no-default-browser-check",
		"about:blank",
	)

	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to start browser: %w", err)
	}

	ext := &CDPExtractor{
		cancel: cancel,
		cmd:    cmd,
		tmpDir: tmpDir,
		done:   make(chan struct{}),
	}

	// Run extraction in background
	go ext.run(ctx, debugPort)

	return ext, nil
}

func (e *CDPExtractor) run(ctx context.Context, debugPort int) {
	defer close(e.done)

	xoxc, xoxd, err := e.extract(ctx, debugPort)

	e.mu.Lock()
	if err != nil {
		e.result = &TokenResult{Err: err}
	} else {
		// Validate tokens
		team, user, userID, vErr := ValidateTokens(xoxc, xoxd)
		if vErr != nil {
			e.result = &TokenResult{Err: fmt.Errorf("token validation failed: %w", vErr)}
		} else {
			e.result = &TokenResult{
				Xoxc: xoxc, Xoxd: xoxd,
				Team: team, User: user, UserID: userID,
			}
		}
	}
	e.mu.Unlock()
}

func (e *CDPExtractor) extract(ctx context.Context, debugPort int) (string, string, error) {
	controlURL, err := waitForDebugEndpoint(ctx, debugPort)
	if err != nil {
		return "", "", fmt.Errorf("failed to get debug URL: %w", err)
	}

	browser := rod.New().ControlURL(controlURL).Context(ctx)
	if err := browser.Connect(); err != nil {
		return "", "", fmt.Errorf("failed to connect to browser: %w", err)
	}
	defer browser.Close()

	// Navigate to Slack
	page, err := browser.Page(proto.TargetCreateTarget{URL: "https://app.slack.com"})
	if err != nil {
		return "", "", fmt.Errorf("failed to open Slack page: %w", err)
	}

	// Poll for xoxc token in localStorage. Don't use WaitStable — Slack's
	// constant WebSocket activity means the page never "stabilizes."
	// Instead, poll until the token appears or we time out.
	xoxc, err := pollForXoxcToken(ctx, page)
	if err != nil {
		return "", "", err
	}

	// Extract d cookie via CDP (sees HttpOnly cookies)
	xoxd, err := extractDCookie(page)
	if err != nil {
		return "", "", err
	}

	return xoxc, xoxd, nil
}

// Result returns the extraction result, or nil if still in progress.
func (e *CDPExtractor) Result() *TokenResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

// Done returns a channel that closes when extraction completes (success or failure).
func (e *CDPExtractor) Done() <-chan struct{} {
	return e.done
}

// Cleanup kills the browser and removes the temp directory.
func (e *CDPExtractor) Cleanup() {
	e.cancel()
	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Process.Kill()
		e.cmd.Wait()
	}
	if e.tmpDir != "" {
		os.RemoveAll(e.tmpDir)
	}
}

// --- Extraction helpers ---

// pollForXoxcToken polls localStorage until an xoxc token appears or context expires.
func pollForXoxcToken(ctx context.Context, page *rod.Page) (string, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for xoxc token in localStorage — Slack may not be fully loaded")
		case <-ticker.C:
			xoxc, err := extractXoxcFromLocalStorage(page)
			if err == nil && xoxc != "" {
				return xoxc, nil
			}
			// Keep polling — Slack SPA may still be hydrating
		}
	}
}

// extractXoxcFromLocalStorage searches localStorage for an xoxc token.
func extractXoxcFromLocalStorage(page *rod.Page) (string, error) {
	result, err := page.Eval(`() => {
		for (const [k, v] of Object.entries(localStorage)) {
			try {
				const search = (o) => {
					if (typeof o === 'string' && o.startsWith('xoxc-')) return o;
					if (typeof o === 'object' && o) {
						for (const val of Object.values(o)) {
							const r = search(val);
							if (r) return r;
						}
					}
				};
				const parsed = typeof v === 'string' && v.startsWith('{') ? JSON.parse(v) : v;
				const r = search(parsed);
				if (r) return r;
			} catch(e) {}
			if (typeof v === 'string' && v.startsWith('xoxc-')) return v;
		}
		return null;
	}`)
	if err != nil {
		return "", fmt.Errorf("failed to evaluate localStorage script: %w", err)
	}

	val := result.Value.Str()
	if val == "" {
		return "", fmt.Errorf("no xoxc token found in localStorage — this profile may not have an active Slack session")
	}

	return val, nil
}

// extractDCookie reads the "d" cookie from Slack's domain via CDP.
// CDP-level cookie access sees HttpOnly cookies that document.cookie cannot.
func extractDCookie(page *rod.Page) (string, error) {
	cookies, err := page.Cookies([]string{"https://app.slack.com"})
	if err != nil {
		return "", fmt.Errorf("failed to read cookies: %w", err)
	}

	for _, c := range cookies {
		if c.Name == "d" && strings.Contains(c.Domain, "slack.com") {
			if c.Value == "" {
				return "", fmt.Errorf("found d cookie but value is empty")
			}
			return c.Value, nil
		}
	}

	return "", fmt.Errorf("no d cookie found for slack.com — this profile may not have an active Slack session")
}

// --- Chrome launch helpers ---

// prepareCDPUserDataDir creates a temporary user-data-dir that Chrome will
// accept for remote debugging. It copies "Local State" and symlinks the
// target profile directory so Chrome sees a "non-default" path while still
// using the real profile data.
func prepareCDPUserDataDir(realUserDataDir, profileDir string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "slack-mcp-cdp-*")
	if err != nil {
		return "", err
	}

	// Copy Local State (Chrome needs this to recognize profiles)
	localState := filepath.Join(realUserDataDir, "Local State")
	data, err := os.ReadFile(localState)
	if err == nil {
		os.WriteFile(filepath.Join(tmpDir, "Local State"), data, 0600)
	}

	// Link the real profile directory. Use symlink on Unix, fall back to
	// directory junction on Windows (junctions don't require Developer Mode
	// or admin privileges, unlike symlinks).
	realProfile := filepath.Join(realUserDataDir, profileDir)
	tmpProfile := filepath.Join(tmpDir, profileDir)
	if err := linkProfileDir(realProfile, tmpProfile); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("failed to link profile: %w", err)
	}

	return tmpDir, nil
}

// findFreePort asks the OS for an available port
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// waitForDebugEndpoint polls Chrome's HTTP debug endpoint until it responds
// with a valid WebSocket URL. More reliable than parsing stderr.
func waitForDebugEndpoint(ctx context.Context, port int) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: 2 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for Chrome debug endpoint")
		default:
		}

		resp, err := client.Get(url)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		var version struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.Unmarshal(body, &version); err != nil || version.WebSocketDebuggerURL == "" {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		return version.WebSocketDebuggerURL, nil
	}
}

package features

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/aaronsb/slack-mcp/pkg/setup"
)

// AuthSetup exposes the setup flow as an MCP tool for agent-mediated auth
var AuthSetup = &Feature{
	Name:        "auth-setup",
	Description: "Start or check Slack authentication setup. Opens a browser-based flow to connect your Slack workspace. Non-blocking and idempotent — call multiple times to check status. If the user needs help with the browser flow, read the slack-mcp://help/browser-setup resource for step-by-step instructions.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform: 'start' (default) begins or checks setup, 'status' checks current auth state, 'clear' removes stored credentials",
				"default":     "start",
			},
		},
	},
	Handler: authSetupHandler,
}

// setupState tracks the background setup server
var (
	setupMu      sync.Mutex
	setupRunning bool
	setupResult  *setupOutcome
)

type setupOutcome struct {
	Status    string `json:"status"`
	Workspace string `json:"workspace,omitempty"`
	User      string `json:"user,omitempty"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
	Message   string `json:"message,omitempty"`
}

func authSetupHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	action := "start"
	if a, ok := params["action"].(string); ok {
		action = a
	}

	switch action {
	case "clear":
		return handleClearCredentials()
	case "status":
		return handleCheckStatus()
	default:
		return handleStartSetup()
	}
}

func handleClearCredentials() (*FeatureResult, error) {
	cfg, err := setup.LoadConfig()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to load config: %v", err),
		}, nil
	}

	if len(cfg.Workspaces) == 0 {
		return &FeatureResult{
			Success: true,
			Message: "No credentials stored",
			Data: map[string]interface{}{
				"status": "no_credentials",
			},
		}, nil
	}

	// Clear all workspaces
	workspaceNames := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		workspaceNames = append(workspaceNames, name)
	}

	cfg.Workspaces = make(map[string]setup.WorkspaceConfig)
	cfg.DefaultWorkspace = ""

	if err := setup.SaveConfig(cfg); err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to clear credentials: %v", err),
		}, nil
	}

	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Cleared credentials for %d workspace(s)", len(workspaceNames)),
		Data: map[string]interface{}{
			"status":     "cleared",
			"workspaces": workspaceNames,
		},
		Guidance: "Run auth-setup again to connect a new workspace.",
	}, nil
}

func handleCheckStatus() (*FeatureResult, error) {
	// Check if setup is currently running
	setupMu.Lock()
	running := setupRunning
	result := setupResult
	setupMu.Unlock()

	if running {
		return &FeatureResult{
			Success: true,
			Message: "Setup is in progress — waiting for browser flow to complete",
			Data: map[string]interface{}{
				"status":  "waiting",
				"message": "Setup page is running. Waiting for user to complete the browser flow.",
			},
		}, nil
	}

	if result != nil && result.Status == "connected" {
		return &FeatureResult{
			Success: true,
			Message: fmt.Sprintf("Connected to %s as %s", result.Workspace, result.User),
			Data: map[string]interface{}{
				"status":    "connected",
				"workspace": result.Workspace,
				"user":      result.User,
			},
		}, nil
	}

	// Check stored config
	cfg, err := setup.LoadConfig()
	if err == nil && len(cfg.Workspaces) > 0 {
		wsName := cfg.DefaultWorkspace
		if wsName == "" {
			for name := range cfg.Workspaces {
				wsName = name
				break
			}
		}
		if ws, ok := cfg.Workspaces[wsName]; ok {
			return &FeatureResult{
				Success: true,
				Message: fmt.Sprintf("Configured workspace: %s (user: %s)", ws.TeamName, ws.UserName),
				Data: map[string]interface{}{
					"status":    "configured",
					"workspace": ws.TeamName,
					"user":      ws.UserName,
				},
			}, nil
		}
	}

	return &FeatureResult{
		Success: true,
		Message: "No Slack credentials configured",
		Data: map[string]interface{}{
			"status": "not_configured",
		},
		Guidance: "Use auth-setup to connect your Slack workspace.",
	}, nil
}

func handleStartSetup() (*FeatureResult, error) {
	setupMu.Lock()

	// If already running, return current status
	if setupRunning {
		setupMu.Unlock()
		return &FeatureResult{
			Success: true,
			Message: "Setup is already running — waiting for browser flow to complete",
			Data: map[string]interface{}{
				"status":  "waiting",
				"message": "Setup page is already running. Waiting for user to complete the browser flow.",
			},
			Guidance: "Ask the user to complete the setup in their browser. Call auth-setup again to check status.",
		}, nil
	}

	// If we already completed successfully this session, return that
	if setupResult != nil && setupResult.Status == "connected" {
		setupMu.Unlock()
		return &FeatureResult{
			Success: true,
			Message: fmt.Sprintf("Already connected to %s as %s", setupResult.Workspace, setupResult.User),
			Data: map[string]interface{}{
				"status":    "connected",
				"workspace": setupResult.Workspace,
				"user":      setupResult.User,
			},
		}, nil
	}

	// Start setup in background
	setupRunning = true
	setupResult = nil

	port, listener, err := setup.FindPort()
	if err != nil {
		setupRunning = false
		setupMu.Unlock()
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Could not find available port: %v", err),
		}, nil
	}

	setupMu.Unlock()

	url := fmt.Sprintf("http://localhost:%d", port)

	// Run setup server in background goroutine
	go func() {
		log.Printf("auth-setup: starting setup server on %s", url)
		team, user, setupErr := setup.RunSetupServer(listener, port)

		setupMu.Lock()
		setupRunning = false
		if setupErr != nil {
			setupResult = &setupOutcome{
				Status:  "error",
				Error:   setupErr.Error(),
				Message: fmt.Sprintf("Setup failed: %v", setupErr),
			}
		} else {
			setupResult = &setupOutcome{
				Status:    "connected",
				Workspace: team,
				User:      user,
				Message:   fmt.Sprintf("Connected to %s as %s", team, user),
			}
		}
		setupMu.Unlock()
		log.Printf("auth-setup: setup complete: %+v", setupResult)
	}()

	// Open browser
	setup.OpenBrowserURL(url)

	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Setup page running at %s — opened in browser", url),
		Data: map[string]interface{}{
			"status":  "waiting",
			"url":     url,
			"message": "Setup page is running. Waiting for user to complete the browser flow.",
		},
		Guidance:    "I've opened a setup page in your browser. Follow the steps there to connect your Slack workspace. Call auth-setup again to check if setup is complete.",
		NextActions: []string{"Call auth-setup with action='status' to check if setup completed"},
	}, nil
}

package features

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/setup"
)

// AuthSetup exposes the browser token extraction flow as an MCP tool.
// Progressive disclosure: the tool schema is minimal — all step guidance
// is embedded in each state's FlowResponse.
var AuthSetup = &Feature{
	Name:        "auth-setup",
	Description: "Manage Slack authentication. Call with action 'next' to begin or advance the setup flow. Supports automatic browser token extraction (Chrome/Edge) and guided Firefox setup.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action: 'next' advances the flow, 'status' checks state, 'select' picks an option, 'retry' retries after failure or lock, 'reset' starts over, 'clear' removes stored credentials",
				"default":     "next",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Value for 'select' action — browser name (e.g. 'chrome') or profile directory name (e.g. 'Default')",
			},
		},
	},
	Handler: authSetupHandler,
}

var (
	flowMu   sync.Mutex
	flowInst *setup.Flow
)

func getOrCreateFlow() (*setup.Flow, error) {
	if flowInst != nil {
		return flowInst, nil
	}

	f, err := setup.NewFlow()
	if err != nil {
		return nil, err
	}
	flowInst = f
	return flowInst, nil
}

func authSetupHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	action := "next"
	if a, ok := params["action"].(string); ok && a != "" {
		action = a
	}
	value := ""
	if v, ok := params["value"].(string); ok {
		value = v
	}

	// Clear credentials is independent of the flow
	if action == "clear" {
		return handleClearCredentials()
	}

	flowMu.Lock()
	flow, err := getOrCreateFlow()
	if err != nil {
		flowMu.Unlock()
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to initialize setup flow: %v", err),
		}, nil
	}
	flowMu.Unlock()

	var resp *setup.FlowResponse
	switch action {
	case "status":
		resp = flow.Status()
	case "reset":
		flowMu.Lock()
		resp = flow.Reset()
		flowInst = nil
		flowMu.Unlock()
	case "select":
		if value == "" {
			return &FeatureResult{
				Success: false,
				Message: "The 'select' action requires a 'value' parameter (browser name or profile directory).",
			}, nil
		}
		resp = flow.Advance("select:" + value)
	default:
		resp = flow.Advance(action)
	}

	result := flowToFeatureResult(resp)

	// After successful auth, hot-load the provider so tools work immediately
	if resp.Done && resp.OK {
		if setProvider, ok := params["_setProvider"].(func(*provider.ApiProvider)); ok {
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
					p := provider.NewWithTokens(ws.XoxcToken, ws.XoxdToken)
					setProvider(p)
					log.Printf("Provider hot-loaded for workspace %q after auth", wsName)
				}
			}
		}
	}

	return result, nil
}

func flowToFeatureResult(resp *setup.FlowResponse) *FeatureResult {
	data := map[string]interface{}{
		"state":   resp.State,
		"message": resp.Message,
	}
	if resp.Context != nil {
		for k, v := range resp.Context {
			data[k] = v
		}
	}
	if resp.Done {
		data["done"] = true
		data["ok"] = resp.OK
	}

	// Map FlowResponse actions to NextActions
	var nextActions []string
	for _, a := range resp.Actions {
		nextActions = append(nextActions, fmt.Sprintf("auth-setup action=%q", a))
	}

	return &FeatureResult{
		Success:     !resp.Done || resp.OK, // success unless terminal failure
		Data:        data,
		Message:     resp.Message,
		Guidance:    resp.Guidance,
		NextActions: nextActions,
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
			Message: "No credentials stored.",
			Data:    map[string]interface{}{"status": "no_credentials"},
		}, nil
	}

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

	// Reset flow if one exists
	flowMu.Lock()
	flowInst = nil
	flowMu.Unlock()

	return &FeatureResult{
		Success:  true,
		Message:  fmt.Sprintf("Cleared credentials for %d workspace(s).", len(workspaceNames)),
		Data:     map[string]interface{}{"status": "cleared", "workspaces": workspaceNames},
		Guidance: "Run auth-setup with action 'next' to connect a new workspace.",
	}, nil
}

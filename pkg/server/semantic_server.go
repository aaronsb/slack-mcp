package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"log"
	"os"
)

// SemanticMCPServer provides intent-based Slack operations
type SemanticMCPServer struct {
	server   *server.MCPServer
	registry *features.Registry
	provider *provider.ApiProvider
}

// NewSemanticMCPServer creates a new semantic MCP server
func NewSemanticMCPServer(provider *provider.ApiProvider) *SemanticMCPServer {
	// Get personality from environment, default to "slack-user"
	personality := os.Getenv("SLACK_MCP_PERSONALITY")
	if personality == "" {
		personality = "slack-user"
	}

	serverName := fmt.Sprintf("Slack MCP Server (%s)", personality)

	s := server.NewMCPServer(
		serverName,
		"2.0.0",
		server.WithLogging(),
		server.WithRecovery(),
	)

	// Create feature registry
	registry := features.NewRegistry()

	// Register all available features
	registry.Register(features.CheckUnreads)
	registry.Register(features.CatchUpOnChannel)
	registry.Register(features.ListChannels)
	registry.Register(features.CheckMyMentions)
	registry.Register(features.FindDiscussion)
	registry.Register(features.PaceConversation)
	registry.Register(features.WriteMessage)
	registry.Register(features.MarkAsRead)
	registry.Register(features.GetContext)
	registry.Register(features.React)
	registry.Register(features.ListUsers)
	registry.Register(features.AuthSetup)

	semanticServer := &SemanticMCPServer{
		server:   s,
		registry: registry,
		provider: provider,
	}

	// For now, register all features regardless of personality
	// In future, we'll filter based on personality config
	for _, feature := range registry.All() {
		semanticServer.registerFeature(feature)
	}

	// Register help resources
	semanticServer.registerResources()

	log.Printf("Initialized Slack MCP Server with personality: %s", personality)

	return semanticServer
}

// registerFeature adds a semantic feature as an MCP tool
func (s *SemanticMCPServer) registerFeature(feature *features.Feature) {
	// Convert feature schema to MCP tool options
	toolOptions := []mcp.ToolOption{
		mcp.WithDescription(feature.Description),
	}

	// Add schema properties
	if schemaMap, ok := feature.Schema.(map[string]interface{}); ok {
		if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
			required := []string{}
			if req, ok := schemaMap["required"].([]string); ok {
				required = req
			}
			for name, prop := range props {
				propMap := prop.(map[string]interface{})
				toolOptions = append(toolOptions, s.createToolOption(name, propMap, required)...)
			}
		}
	}

	// Create handler wrapper
	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract parameters
		params := make(map[string]interface{})
		for k, v := range request.GetArguments() {
			params[k] = v
		}

		// Add provider to params for features that need it
		if s.provider == nil && feature.Name != "auth-setup" {
			guidance := map[string]interface{}{
				"status":  "setup_needed",
				"message": "Slack credentials are not configured yet. Use the auth-setup tool to connect a workspace.",
				"hint":    "Call auth-setup to start a browser-based setup flow. Tokens are stored locally and never leave your machine.",
			}
			jsonData, _ := json.MarshalIndent(guidance, "", "  ")
			return mcp.NewToolResultText(string(jsonData)), nil
		}
		params["_provider"] = s.provider

		// Execute feature
		result, err := feature.Handler(ctx, params)
		if err != nil {
			return nil, err
		}

		// Convert to JSON for MCP response
		jsonData, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, err
		}

		return mcp.NewToolResultText(string(jsonData)), nil
	}

	// Register the tool
	s.server.AddTool(mcp.NewTool(feature.Name, toolOptions...), handler)
}

// createToolOption converts schema properties to MCP tool options
func (s *SemanticMCPServer) createToolOption(name string, prop map[string]interface{}, required []string) []mcp.ToolOption {
	options := []mcp.ToolOption{}

	// Check if required
	isRequired := false
	for _, r := range required {
		if r == name {
			isRequired = true
			break
		}
	}

	propType := prop["type"].(string)
	desc := ""
	if d, ok := prop["description"].(string); ok {
		desc = d
	}

	switch propType {
	case "string":
		opt := mcp.WithString(name, mcp.Description(desc))
		if isRequired {
			opt = mcp.WithString(name, mcp.Required(), mcp.Description(desc))
		}
		if def, ok := prop["default"].(string); ok {
			opt = mcp.WithString(name, mcp.DefaultString(def), mcp.Description(desc))
		}
		options = append(options, opt)

	case "boolean":
		opt := mcp.WithBoolean(name, mcp.Description(desc))
		if isRequired {
			opt = mcp.WithBoolean(name, mcp.Required(), mcp.Description(desc))
		}
		// Default values for booleans are handled differently in mcp-go
		// Just skip default for now
		options = append(options, opt)

	case "array":
		items := map[string]any{"type": "string"}
		if itemsProp, ok := prop["items"].(map[string]interface{}); ok {
			items = itemsProp
		}
		opt := mcp.WithArray(name, mcp.Description(desc), mcp.Items(items))
		if isRequired {
			opt = mcp.WithArray(name, mcp.Required(), mcp.Description(desc), mcp.Items(items))
		}
		options = append(options, opt)
	}

	return options
}

// registerResources adds MCP resources for help content
func (s *SemanticMCPServer) registerResources() {
	// Identity resource — tells the agent who it's operating as
	s.server.AddResource(
		mcp.Resource{
			URI:         "slack-mcp://identity",
			Name:        "Current User Identity",
			Description: "The authenticated Slack user this server is operating as. Read this to know your name, team, and role before interacting with others.",
			MIMEType:    "application/json",
		},
		func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			if s.provider == nil {
				return []mcp.ResourceContents{
					mcp.TextResourceContents{
						URI:      "slack-mcp://identity",
						MIMEType: "application/json",
						Text:     `{"status": "not_authenticated", "message": "Use auth-setup to connect a workspace"}`,
					},
				}, nil
			}

			identity := s.provider.ProvideIdentity()
			if identity == nil {
				return []mcp.ResourceContents{
					mcp.TextResourceContents{
						URI:      "slack-mcp://identity",
						MIMEType: "application/json",
						Text:     `{"status": "unknown", "message": "Identity not yet available — provider may still be booting"}`,
					},
				}, nil
			}

			data, _ := json.MarshalIndent(identity, "", "  ")
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "slack-mcp://identity",
					MIMEType: "application/json",
					Text:     string(data),
				},
			}, nil
		},
	)

	s.server.AddResource(
		mcp.Resource{
			URI:         "slack-mcp://help/browser-setup",
			Name:        "Browser Setup Guide",
			Description: "Step-by-step instructions for extracting Slack tokens using Chrome or Firefox DevTools. Read this resource when the user needs help with the auth-setup browser flow.",
			MIMEType:    "text/plain",
		},
		func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "slack-mcp://help/browser-setup",
					MIMEType: "text/plain",
					Text: `Slack MCP Browser Token Setup Guide

The auth-setup tool opens a local web page that guides you through connecting your Slack workspace.
If you need to extract tokens manually, follow these steps:

== Chrome ==

1. Open Slack in your browser (app.slack.com)
2. Make sure you're logged into the workspace you want to connect
3. Open DevTools: F12 or Ctrl+Shift+I (Cmd+Option+I on Mac)
4. Go to the Application tab
5. In the left sidebar, expand "Local Storage" and click on "https://app.slack.com"
6. Find the key "localConfig_v2" — the xoxc token is in this JSON blob
7. For the xoxd cookie: In the same Application tab, expand "Cookies" > "https://app.slack.com"
8. Find the cookie named "d" — this is your xoxd token value

== Firefox ==

1. Open Slack in your browser (app.slack.com)
2. Open DevTools: F12 or Ctrl+Shift+I
3. Go to the Storage tab
4. Expand "Local Storage" > "https://app.slack.com"
5. Find "localConfig_v2" for the xoxc token
6. Expand "Cookies" > "https://app.slack.com" for the "d" cookie (xoxd token)

== Using the Setup Page ==

The easier approach: when auth-setup opens the browser page, it provides a
JavaScript snippet to paste into the browser console. The snippet automatically
extracts both tokens and sends them to the local setup server.

1. Open any Slack tab in your browser
2. Open the browser console (F12 > Console tab)
3. Paste the snippet shown on the setup page
4. Press Enter
5. The setup page will confirm when tokens are received and validated

== Security Notes ==

- Tokens are sent directly from your browser to localhost — they never leave your machine
- The setup server runs on a high port (51837+) and auto-shuts down after receiving tokens
- Config is saved to ~/.config/slack-mcp/config.json with 0600 permissions
- These are session tokens tied to your browser session, not permanent API keys
`,
				},
			}, nil
		},
	)
}

// ServeSSE starts the SSE server
func (s *SemanticMCPServer) ServeSSE(addr string) *server.SSEServer {
	return server.NewSSEServer(s.server,
		server.WithBaseURL(fmt.Sprintf("http://%s", addr)),
		server.WithSSEContextFunc(authFromRequest),
	)
}

// ServeStdio starts the stdio server
func (s *SemanticMCPServer) ServeStdio() error {
	return server.ServeStdio(s.server)
}

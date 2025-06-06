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
	registry.Register(features.CatchUpOnChannel)
	registry.Register(features.CheckMyMentions)
	registry.Register(features.FindDiscussion)
	registry.Register(features.CheckUnreads)
	registry.Register(features.MarkAsRead)
	registry.Register(features.ListChannels)
	registry.Register(features.DecideNextAction)
	registry.Register(features.PaceConversation)
	registry.Register(features.WriteMessage)

	// Debug tool (only in development)
	if os.Getenv("SLACK_MCP_DEBUG") == "true" {
		registry.Register(features.DebugInternal)
	}

	// Future features to implement:
	// - browse-team-activity
	// - search-shared-files
	// - get-channel-insights
	// - find-decisions
	// - review-action-items

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
		for k, v := range request.Params.Arguments {
			params[k] = v
		}

		// Add provider to params for features that need it
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

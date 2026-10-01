package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// SemanticMCPServer provides intent-based Slack operations
type SemanticMCPServer struct {
	server   *server.MCPServer
	registry *features.Registry
	provider atomic.Pointer[provider.ApiProvider]
	// pacer watches read-call timing for ADR-010's frequency hint.
	pacer features.ReadPacer
	// sse is set once the server serves SSE, whose sessions are never
	// asked for an approval in-band (ADR-013, Elicitation).
	sse atomic.Bool
	// clients holds the client lines noteClient has logged; clientLines
	// counts them against maxClientLines.
	clients     sync.Map
	clientLines atomic.Int64
}

// Option configures the server at construction.
type Option func(*options)

type options struct {
	handle string
	org    safety.Org
}

// WithAccount names the account auth.test reported at startup: its handle
// for ADR-014's identity wording, and its organization, whose quarantines
// the server instructions list.
func WithAccount(handle string, org safety.Org) Option {
	return func(o *options) { o.handle, o.org = handle, org }
}

// bannered are the read nouns whose results open with ADR-013's banner.
var bannered = map[string]bool{"inbox": true, "messages": true, "estate": true, "batch": true}

// approvalKey names the approval form in an input-required result.
const approvalKey = "operator-approval"

// NewSemanticMCPServer creates a new semantic MCP server
func NewSemanticMCPServer(provider *provider.ApiProvider, opts ...Option) *SemanticMCPServer {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	personality := sanitizePersonality(os.Getenv("SLACK_MCP_PERSONALITY"))

	serverName := fmt.Sprintf("Slack MCP Server (%s)", personality)

	// The build stamps version.Version by ldflags; a literal here drifted to
	// 2.0.0 while releases went on without it (#87).
	s := server.NewMCPServer(
		serverName,
		version.Version,
		server.WithLogging(),
		server.WithRecovery(),
		server.WithElicitation(),
		server.WithInstructions(features.Instructions(o.org, o.handle)),
	)

	// Create feature registry
	registry := features.NewRegistry()

	// The v2 surface (ADR-009): three read-only nouns carrying the depth,
	// five verbs whose names state their blast radius, and batch — ADR-010's
	// executor over the nouns. The v1 features stay exported for the nouns
	// to delegate to; only these nine are advertised.
	registry.Register(features.Inbox)
	registry.Register(features.Messages)
	registry.Register(features.EstateViews)
	registry.Register(features.Batch)
	registry.Register(features.Say)
	registry.Register(features.Dismiss)
	registry.Register(features.MarkAsRead)
	registry.Register(features.Auth)
	registry.Register(features.Download)

	semanticServer := &SemanticMCPServer{
		server:   s,
		registry: registry,
	}
	if provider != nil {
		semanticServer.provider.Store(provider)
	}

	// For now, register all features regardless of personality
	// In future, we'll filter based on personality config
	for _, feature := range registry.All() {
		semanticServer.registerFeature(feature, o.handle)
	}

	// Register help resources
	semanticServer.registerResources()

	log.Printf("Initialized Slack MCP Server with personality: %s", personality)

	return semanticServer
}

// registerFeature adds a semantic feature as an MCP tool. say's
// description carries the identity wording for handle (ADR-014).
func (s *SemanticMCPServer) registerFeature(feature *features.Feature, handle string) {
	description := feature.Description
	if feature == features.Say {
		description = features.SayDescription(safety.Current().Identity, features.AccountName(handle))
	}
	toolOptions := []mcp.ToolOption{
		mcp.WithDescription(description),
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

		// Provide a callback so auth-setup can hot-load the provider after success
		params["_setProvider"] = s.swapProvider

		// Add provider to params for features that need it
		p := s.provider.Load()
		if p == nil && feature.Name != "auth" {
			guidance := map[string]interface{}{
				"status":  "setup_needed",
				"message": "Slack credentials are not configured yet. Use the auth tool to connect a workspace.",
				"hint":    "Call auth to start a browser-based setup flow. Tokens are stored locally and never leave your machine.",
			}
			jsonData, _ := json.MarshalIndent(guidance, "", "  ")
			return mcp.NewToolResultText(string(jsonData)), nil
		}
		params["_provider"] = p

		// Execute feature
		result, err := feature.Handler(features.WithElicitation(ctx, s.elicitation(ctx, request)), params)
		if err != nil {
			if bannered[feature.Name] {
				if banner := features.SafetyBanner(p); banner != "" {
					return mcp.NewToolResultError(banner + "\n\n" + err.Error()), nil
				}
			}
			return nil, err
		}
		if in := result.InputRequest; in != nil {
			return approvalForm(in), nil
		}

		// Format as markdown for AI consumption
		text := features.FormatResult(feature.Name, result)
		if bannered[feature.Name] {
			if banner := features.SafetyBanner(p); banner != "" {
				text = banner + "\n\n" + text
			}
		}
		if hint := s.pacer.Observe(feature.Name, time.Now()); hint != "" {
			text += "\n\n" + hint
		}
		return mcp.NewToolResultText(text), nil
	}

	// Register the tool
	s.server.AddTool(mcp.NewTool(feature.Name, toolOptions...), handler)
}

// elicitation reports whether the current request's client can be asked
// for an approval in-band, and its answer on a retry: only a client on
// protocol 2026-07-28 or later whose current request declares elicitation
// in its own capabilities, and never on SSE. An earlier client would have
// mcp-go wait on an elicitation nobody may answer, so it gets the CLI path.
func (s *SemanticMCPServer) elicitation(ctx context.Context, request mcp.CallToolRequest) features.Elicitation {
	info := server.RequestProtocolInfoFromContext(ctx)
	var caps *mcp.ClientCapabilities
	if info != nil {
		caps = info.ClientCapabilities
	}
	if caps == nil {
		caps = request.Params.Meta.ClientCapabilities()
	}
	modern := info != nil && info.Modern && info.ProtocolVersion >= mcp.ProtocolVersion20260728
	declared := caps != nil && caps.Elicitation != nil
	sse := s.sse.Load()
	s.noteClient(ctx, info, modern, declared, sse)
	if sse || !modern || !declared {
		return features.Elicitation{}
	}
	e := features.Elicitation{Offer: true}
	if ans := server.ElicitationResponse(request.Params.InputResponses, approvalKey); ans != nil {
		e.Answered = true
		e.Action = string(ans.Action)
		e.State = request.Params.RequestState
		if content, ok := ans.Content.(map[string]any); ok {
			e.Choice, _ = content["choice"].(string)
		}
	}
	return e
}

// noteClient logs, once per distinct answer, what a client's requests say
// about in-band approval: its name and version, the protocol a request
// declared, whether it declared elicitation, and whether the approval form
// is offered. Without it a live check cannot tell which condition kept the
// form from a client.
func (s *SemanticMCPServer) noteClient(ctx context.Context, info *server.RequestProtocolInfo, modern, declared, sse bool) {
	var client mcp.Implementation
	protocol := "legacy"
	if info != nil {
		if info.ClientInfo != nil {
			client = *info.ClientInfo
		}
		if info.ProtocolVersion != "" {
			protocol = info.ProtocolVersion
		}
	}
	if client.Name == "" {
		if cs, ok := server.ClientSessionFromContext(ctx).(server.SessionWithClientInfo); ok {
			client = cs.GetClientInfo()
		}
	}
	// Every string here is the client's own, so each is clipped and quoted:
	// a newline cannot forge a log line, and a client varying them per
	// request is bounded by maxClientLines.
	line := fmt.Sprintf("client: name=%q version=%q protocol=%q elicitation=%t sse=%t approval-form=%t",
		clip(client.Name), clip(client.Version), clip(protocol), declared, sse, modern && declared && !sse)
	if _, seen := s.clients.Load(line); seen {
		return
	}
	if n := s.clientLines.Add(1); n > maxClientLines {
		if n == maxClientLines+1 {
			log.Printf("client: more than %d distinct client answers; no more are logged", maxClientLines)
		}
		return
	}
	if _, seen := s.clients.LoadOrStore(line, true); !seen {
		log.Print(line)
	}
}

// maxClientLines bounds the distinct client lines noteClient keeps and logs.
const maxClientLines = 64

// clip bounds a client-supplied string for a log line.
func clip(v string) string {
	const max = 64
	if len(v) > max {
		return v[:max] + "…"
	}
	return v
}

// approvalForm is the input-required result asking the operator to answer
// a pending request. The request state is signed; the server holds nothing
// open until the retry.
func approvalForm(in *features.InputRequest) *mcp.CallToolResult {
	return server.NewInputRequestBuilder(in.State).
		Elicit(approvalKey, mcp.ElicitationParams{
			Mode:    mcp.ElicitationModeForm,
			Message: in.Message,
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"choice": map[string]any{
						"type":        "string",
						"enum":        in.Choices,
						"description": "approve-once sends this call once; approve-and-trust also skips this check for the destination from now on; deny refuses it",
					},
				},
				"required": []string{"choice"},
			},
		}).
		ToolResult()
}

// swapProvider installs p as the live provider after auth and boots it in the
// background. The old provider is shut down first: flock is per open file, so
// while it still holds the estate and attention ledgers the new provider's
// Open loses the writer election and boots read-only (#108). Swap makes each
// replaced provider shut down exactly once, by the call that replaced it.
//
// Invariant: the new provider opens its ledgers only inside Provide(), and
// the goroutine that calls it starts after old.Shutdown() returns. Shutdown
// also waits out a ledger open still in flight on the old provider's boot.
//
// Residual race: a tool call that loaded the old provider before the swap may
// still be running on it and will see a shut-down provider.
func (s *SemanticMCPServer) swapProvider(p *provider.ApiProvider) {
	if old := s.provider.Swap(p); old != nil && old != p {
		old.Shutdown()
	}
	log.Println("Provider hot-loaded after successful auth setup")
	go provider.Guard("post-auth-boot", func() {
		log.Println("Booting provider in background after auth...")
		if _, err := p.Provide(); err != nil {
			log.Printf("Warning: post-auth provider boot failed: %v", err)
		} else {
			log.Println("Provider booted successfully after auth")
			if id := p.ProvideIdentity(); id != nil {
				s.registerFeature(features.Say, id.Username)
			}
		}
	})
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

	case "number":
		opt := mcp.WithNumber(name, mcp.Description(desc))
		if isRequired {
			opt = mcp.WithNumber(name, mcp.Required(), mcp.Description(desc))
		}
		if def, ok := prop["default"].(float64); ok {
			opt = mcp.WithNumber(name, mcp.DefaultNumber(def), mcp.Description(desc))
		} else if def, ok := prop["default"].(int); ok {
			opt = mcp.WithNumber(name, mcp.DefaultNumber(float64(def)), mcp.Description(desc))
		}
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
			p := s.provider.Load()
			if p == nil {
				return []mcp.ResourceContents{
					mcp.TextResourceContents{
						URI:      "slack-mcp://identity",
						MIMEType: "application/json",
						Text:     `{"status": "not_authenticated", "message": "Use the auth tool to connect a workspace"}`,
					},
				}, nil
			}

			identity := p.ProvideIdentity()
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
	s.sse.Store(true)
	return server.NewSSEServer(s.server,
		server.WithBaseURL(fmt.Sprintf("http://%s", addr)),
	)
}

// NewSSEHTTPServer builds the authenticated HTTP server for the SSE transport.
// It validates the host/key combination first. ReadHeaderTimeout is set but
// ReadTimeout/WriteTimeout are not: they would kill the long-lived SSE stream.
func (s *SemanticMCPServer) NewSSEHTTPServer(host, port, apiKey string) (*http.Server, *server.SSEServer, error) {
	if err := ValidateSSEConfig(host, apiKey); err != nil {
		return nil, nil, err
	}
	sseServer := s.ServeSSE(":" + port)
	return &http.Server{
		Addr:              net.JoinHostPort(strings.Trim(host, "[]"), port),
		Handler:           RequireBearer(apiKey, sseServer),
		ReadHeaderTimeout: 10 * time.Second,
	}, sseServer, nil
}

// ServeStdio serves MCP over in and stdout until in reaches EOF or ctx ends.
// The caller owns signals and the lifecycle watchers (pkg/lifecycle); tool
// calls run under ctx, so cancelling it also reaches in-flight Slack calls.
func (s *SemanticMCPServer) ServeStdio(ctx context.Context, in io.Reader) error {
	stdio := server.NewStdioServer(s.server)
	// mcp-go's stdio server writes errors to its own os.Stderr logger by
	// default, which would bypass the log file and its redaction. Route it
	// through the process logger. (Its SSE server already uses log.Printf.)
	stdio.SetErrorLogger(log.Default())
	return stdio.Listen(ctx, in, os.Stdout)
}

// Shutdown shuts down the current provider, if any, giving up after timeout.
// The provider is read at call time because auth can hot-load one after boot.
func (s *SemanticMCPServer) Shutdown(timeout time.Duration) {
	p := s.provider.Load()
	if p == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("Provider shutdown still running after %s; abandoning it", timeout)
	}
}

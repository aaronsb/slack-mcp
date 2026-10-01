package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/lifecycle"
	"github.com/aaronsb/slack-mcp/pkg/logsink"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/server"
	"github.com/aaronsb/slack-mcp/pkg/setup"
)

var defaultSseHost = "127.0.0.1"
var defaultSsePort = 13080

// logSink is the process log's redacting writer. exit flushes it, since
// os.Exit skips deferred calls.
var logSink = logsink.NewRedactingWriter(os.Stderr)

func exit(code int) {
	_ = logSink.Flush()
	os.Exit(code)
}

func main() {
	log.SetOutput(logSink)
	defer func() { _ = logSink.Flush() }()

	// Check for subcommands before flag parsing
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := setup.RunSetup(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && isSafetyCommand(os.Args[1]) {
		exit(runSafetyCommand(os.Args[1:]))
	}

	var transport string
	flag.StringVar(&transport, "t", "stdio", "Transport type (stdio or sse)")
	flag.StringVar(&transport, "transport", "stdio", "Transport type (stdio or sse)")
	flag.Parse()

	// Every log line passes through the redacting writer, a safety net
	// under the rule that log sites never print credentials. For stdio the
	// log goes to a private file (0600 in the XDG state directory, or
	// SLACK_MCP_LOG_FILE) so it cannot interfere with the protocol.
	var logOut io.Writer = os.Stderr
	if transport == "stdio" {
		logFile, err := logsink.Open(logsink.Path())
		if err == nil {
			logOut = logFile
			defer logFile.Close()
		} else {
			fmt.Fprintln(os.Stderr, "slack-mcp: logging disabled:", err)
			logOut = io.Discard
		}
	}
	logSink = logsink.NewRedactingWriter(logOut)
	log.SetOutput(logSink)

	// .env may set only the allowlisted keys; anything else must come from
	// the client environment. Stderr, because stdio logging goes to a file.
	if err := loadDotEnv(".env", os.LookupEnv, os.Setenv); err != nil {
		log.Print(err)
		fmt.Fprintln(os.Stderr, "slack-mcp:", err)
		exit(1)
	}

	// SLACK_MCP_IDENTITY and SLACK_MCP_SAFETY are declared in the client
	// config (ADR-014); an unknown value refuses to start.
	settings, err := safety.SettingsFromEnv(os.LookupEnv)
	if err != nil {
		log.Print(err)
		fmt.Fprintln(os.Stderr, "slack-mcp:", err)
		exit(1)
	}
	safety.SetCurrent(settings)
	log.Printf("Account identity: %s; safety posture: %s", settings.Identity, settings.Posture)

	// Refuse a deployment and idle-timeout combination that cannot help
	// before anything boots. Stderr, because stdio logging goes to a file.
	idle, err := lifecycle.IdleTimeoutFromEnv(transport)
	if err != nil {
		log.Print(err)
		fmt.Fprintln(os.Stderr, "slack-mcp:", err)
		exit(1)
	}

	// Build provider: try config file, then env vars, then start without auth
	p, authErr := loadProvider()

	s := server.NewSemanticMCPServer(p)

	if authErr != nil {
		// Register the server but log that auth is needed
		log.Printf("No Slack credentials found: %v", authErr)
		log.Printf("Tools will return auth errors. Run 'slack-mcp setup' to configure.")
	}

	// Boot provider asynchronously after server starts
	if authErr == nil {
		go func() {
			log.Println("Booting provider in background...")

			_, err := p.Provide()
			if err != nil {
				log.Printf("Warning: Provider boot failed: %v", err)
				log.Println("Some features may be limited until cache is loaded")
			} else {
				log.Println("Provider booted successfully in background")
			}
		}()
	}

	switch transport {
	case "stdio":
		exit(runStdio(s, idle))
	case "sse":
		host := os.Getenv("SLACK_MCP_HOST")
		if host == "" {
			host = defaultSseHost
		}
		port := os.Getenv("SLACK_MCP_PORT")
		if port == "" {
			port = strconv.Itoa(defaultSsePort)
		}

		httpServer, sseServer, err := s.NewSSEHTTPServer(host, port, os.Getenv(server.SSEAPIKeyEnv))
		if err != nil {
			log.Fatalf("%v", err)
		}
		exit(runSSE(s, httpServer, sseServer))
	default:
		log.Fatalf("Invalid transport type: %s. Must be 'stdio' or 'sse'", transport)
	}
}

// looksLikeToken returns true if the value matches Slack token format.
// Env vars from mcpb may contain stale or placeholder values — only use
// them when they look like real tokens.
func looksLikeToken(v, prefix string) bool {
	return strings.HasPrefix(v, prefix)
}

// loadProvider resolves Slack credentials with this priority:
//
//  1. Config file (~/.config/slack-mcp/config.json) — always checked first
//  2. Env vars matching token format (xoxc-/xoxd-) — manual override
//  3. Nothing → return error (server starts, tools prompt for auth-setup)
//
// All token sources are validated against auth.test before use.
// Invalid tokens are rejected so the server starts in no-auth mode
// with clear guidance, rather than silently failing on every tool call.
func loadProvider() (*provider.ApiProvider, error) {
	// Config file is the source of truth — shared across all MCP hosts
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
			log.Printf("Validating workspace %q from config file...", wsName)
			if _, _, _, err := setup.ValidateTokens(ws.XoxcToken, ws.XoxdToken); err != nil {
				log.Printf("Config tokens for %q failed validation: %v", wsName, err)
				return nil, fmt.Errorf("stored tokens for workspace %q are invalid (%v) — run auth-setup to re-authenticate", wsName, err)
			}
			log.Printf("Workspace %q authenticated successfully", wsName)
			// Clear any stale setup flow state — tokens are valid
			if cfg.SetupFlow != nil {
				log.Println("Clearing stale setup flow state")
				cfg.ClearFlow()
			}
			return provider.NewWithTokens(ws.XoxcToken, ws.XoxdToken), nil
		}
	}

	// Env vars as fallback — only if they look like real Slack tokens
	token := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	cookie := os.Getenv("SLACK_MCP_XOXD_TOKEN")

	if looksLikeToken(token, "xoxc-") && looksLikeToken(cookie, "xoxd-") {
		log.Println("Validating tokens from environment variables...")
		if _, _, _, err := setup.ValidateTokens(token, cookie); err != nil {
			log.Printf("Env var tokens failed validation: %v", err)
			return nil, fmt.Errorf("environment tokens are invalid (%v) — run auth-setup to configure", err)
		}
		log.Println("Environment tokens authenticated successfully")
		return provider.NewWithTokens(token, cookie), nil
	}

	if token != "" || cookie != "" {
		log.Println("Ignoring env var tokens: they lack the xoxc/xoxd prefixes")
	}

	// No credentials found anywhere
	return nil, fmt.Errorf("no Slack credentials found in config (%s) or environment", setup.ConfigPath())
}

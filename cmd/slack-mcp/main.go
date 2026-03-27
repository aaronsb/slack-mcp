package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/server"
	"github.com/aaronsb/slack-mcp/pkg/setup"
	"github.com/joho/godotenv"
)

var defaultSseHost = "127.0.0.1"
var defaultSsePort = 13080

func main() {
	// Check for subcommands before flag parsing
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := setup.RunSetup(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	var transport string
	flag.StringVar(&transport, "t", "stdio", "Transport type (stdio or sse)")
	flag.StringVar(&transport, "transport", "stdio", "Transport type (stdio or sse)")
	flag.Parse()

	// For stdio transport, redirect logs to a file to avoid interfering with protocol
	if transport == "stdio" {
		logFile, err := os.OpenFile("/tmp/slack-mcp.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			defer logFile.Close()
		} else {
			log.SetOutput(io.Discard)
		}
	}

	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
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
		if err := s.ServeStdio(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case "sse":
		host := os.Getenv("SLACK_MCP_HOST")
		if host == "" {
			host = defaultSseHost
		}
		port := os.Getenv("SLACK_MCP_PORT")
		if port == "" {
			port = strconv.Itoa(defaultSsePort)
		}

		sseServer := s.ServeSSE(":" + port)
		log.Printf("SSE server listening on %s:%s", host, port)
		if err := sseServer.Start(host + ":" + port); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	default:
		log.Fatalf("Invalid transport type: %s. Must be 'stdio' or 'sse'", transport)
	}
}

// loadProvider creates a provider from config file or env vars.
// Returns a provider and nil error on success, or a nil provider and error
// describing what's missing. The server can still start without a provider —
// tools will return helpful auth errors telling the agent to run setup.
func loadProvider() (*provider.ApiProvider, error) {
	// Try config file first
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
			log.Printf("Using workspace %q from config file", wsName)
			return provider.NewWithTokens(ws.XoxcToken, ws.XoxdToken), nil
		}
	}

	// Try env vars
	token := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	cookie := os.Getenv("SLACK_MCP_XOXD_TOKEN")

	if token != "" && cookie != "" {
		if token == "demo" && cookie == "demo" {
			log.Println("Demo mode — provider will not boot")
			return provider.NewWithTokens(token, cookie), nil
		}
		log.Println("Using tokens from environment variables")
		return provider.NewWithTokens(token, cookie), nil
	}

	// No credentials found anywhere
	return nil, fmt.Errorf("no Slack credentials found in config (%s) or environment", setup.ConfigPath())
}

package main

import (
	"flag"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/server"
	"github.com/joho/godotenv"
	"io/ioutil"
	"log"
	"os"
	"strconv"
)

var defaultSseHost = "127.0.0.1"
var defaultSsePort = 13080

func main() {
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
			// If we can't open log file, disable logging entirely for stdio
			log.SetOutput(ioutil.Discard)
		}
	}

	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		// It's okay if .env doesn't exist
		log.Println("No .env file found, using environment variables")
	}

	p := provider.New()

	s := server.NewSemanticMCPServer(p)

	// Boot provider asynchronously after server starts
	go func() {
		log.Println("Booting provider in background...")

		if os.Getenv("SLACK_MCP_XOXC_TOKEN") == "demo" && os.Getenv("SLACK_MCP_XOXD_TOKEN") == "demo" {
			log.Println("Demo credentials are set, skip provider boot.")
			return
		}

		_, err := p.Provide()
		if err != nil {
			log.Printf("Warning: Provider boot failed: %v", err)
			log.Println("Some features may be limited until cache is loaded")
		} else {
			log.Println("Provider booted successfully in background")
		}
	}()

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
		log.Printf("SSE server listening on " + host + ":" + port)
		if err := sseServer.Start(host + ":" + port); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	default:
		log.Fatalf("Invalid transport type: %s. Must be 'stdio' or 'sse'",
			transport,
		)
	}
}

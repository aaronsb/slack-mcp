# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

This is a Slack MCP (Model Context Protocol) Server written in Go that provides tools for interacting with Slack workspaces. It supports both stdio and SSE transports and doesn't require special workspace permissions.

## Development Commands

### Build Commands
```bash
# Build for current platform
make build

# Build for all platforms (darwin, linux, windows × amd64, arm64)
make build-all-platforms

# Run tests
make test

# Format code
make format

# Tidy dependencies
make tidy

# Clean build artifacts
make clean
```

### Running the Server
```bash
# Run with stdio transport (default)
go run cmd/slack-mcp-server/main.go --transport stdio

# Run with SSE transport
go run cmd/slack-mcp-server/main.go --transport sse
```

### NPM Publishing
```bash
# Copy binaries to npm packages and publish
make npm-publish NPM_TOKEN=your-token NPM_VERSION=x.y.z

# Or just copy binaries without publishing
make npm-copy-binaries
```

### Release Management
```bash
# Create a new release tag
make release TAG=v1.2.3
```

## Architecture

### Package Structure
- `cmd/slack-mcp-server/`: Entry point with transport selection (stdio/sse)
- `pkg/server/`: MCP server implementation with tool registration
- `pkg/provider/`: Slack API client provider with authentication and caching
- `pkg/handler/`: Tool handlers for Slack operations
  - `channels.go`: Lists channels with filtering and sorting
  - `conversations.go`: Fetches message history with pagination
- `pkg/transport/`: HTTP transport wrapper for authentication
- `pkg/text/`: Text processing utilities (stopword filtering)

### Key Components

1. **API Provider** (`pkg/provider/api.go`):
   - Manages Slack client initialization with xoxc/xoxd tokens
   - Handles user caching to `.users_cache.json`
   - Supports proxy configuration and custom CA certificates
   - Bootstraps dependencies on startup

2. **MCP Server** (`pkg/server/server.go`):
   - Registers two tools: `conversations_history` and `channels_list`
   - Supports both stdio and SSE transports
   - SSE mode includes authentication middleware

3. **Tool Handlers**:
   - Return data in CSV format using `gocsv`
   - Support pagination via cursor parameters
   - Include time-based limits (e.g., "1d", "30d") for message history

### Environment Variables

Required:
- `SLACK_MCP_XOXC_TOKEN`: Slack workspace token (xoxc-...)
- `SLACK_MCP_XOXD_TOKEN`: Slack cookie value (xoxd-...)

Optional:
- `SLACK_MCP_SERVER_HOST`: Server host (default: 127.0.0.1)
- `SLACK_MCP_SERVER_PORT`: Server port (default: 3001)
- `SLACK_MCP_SSE_API_KEY`: Bearer token for SSE authentication
- `SLACK_MCP_PROXY`: HTTP proxy URL
- `SLACK_MCP_SERVER_CA`: Path to CA certificate
- `SLACK_MCP_SERVER_CA_INSECURE`: Skip TLS verification (not recommended)
- `SLACK_MCP_USERS_CACHE`: User cache file path (default: .users_cache.json)

## Docker Support

The project includes Docker configurations:
- `Dockerfile`: Multi-stage build for minimal image
- `docker-compose.yml`: Production deployment
- `docker-compose.dev.yml`: Development setup
- `docker-compose.toolkit.yml`: Additional tools

Docker images are published to `ghcr.io/korotovsky/slack-mcp-server`.
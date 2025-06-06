# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

This is a Slack MCP (Model Context Protocol) Server written in Go that provides semantic tools for intelligent Slack workspace interaction. It implements an OODA loop (Observe-Orient-Decide-Act) pattern for comprehensive workspace awareness and response workflows. The server supports both stdio and SSE transports and doesn't require special workspace permissions.

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

### OODA Loop Semantic Workflow

The server implements an **Observe-Orient-Decide-Act** pattern for intelligent Slack interaction:

**Observe** (Situational Awareness):
- `check-unreads`: Overview of unread activity across workspace
- `catch-up-on-channel`: Detailed channel activity summaries
- `check-my-mentions`: Personal mentions and direct attention items
- `list-channels`: Available channels and membership status

**Orient** (Context Analysis):
- Message prioritization and filtering based on content analysis
- Thread detection and importance scoring
- User relationship mapping and relevance assessment
- Smart next-action suggestions with pre-populated parameters

**Decide** (Action Planning):
- `decide-next-action`: Basic reflection tool for next steps
- Extensible to specialized reasoning tools (sequential-thinking, decision-analysis)
- Context-aware workflow recommendations

**Act** (Response Execution):
- `find-discussion`: Deep exploration of specific threads/topics
- `mark-as-read`: Bulk management of read states
- Future: Composition and sending tools (architectural consideration)

### Tool Recommendation Pattern

Tools implement **self-deprecating recommendations** - basic tools actively suggest better specialized alternatives:
- Fallback tools work standalone but advertise superior options
- Creates upgrade paths from simple to sophisticated workflows
- Maintains tool hierarchy: specialized > fallback > manual construction

### Package Structure
- `cmd/slack-mcp/`: Entry point with transport selection (stdio/sse)
- `pkg/server/`: MCP server implementation with semantic tool registration
- `pkg/provider/`: Slack API client provider with two-phase caching architecture
- `pkg/features/`: Semantic tool implementations organized by OODA phase
- `pkg/transport/`: HTTP transport wrapper for authentication
- `pkg/text/`: Text processing utilities (stopword filtering, analysis)

### Key Components

1. **API Provider** (`pkg/provider/api.go`):
   - **Two-Phase Channel Caching**: Fast member channels first, then complete workspace inventory
     - Phase 1: `GetConversationsForUser` - loads user's member channels immediately
     - Phase 2: `GetConversations` - background loading of all workspace channels
     - Preserves IsMember status, prevents overwrites between phases
   - **Channel ID Security**: All tools use channel names, IDs hidden from AI exposure
   - **Smart Cache Management**: Reactive (Slack RetryAfter) + proactive (2-second) delays
   - User caching to `.users_cache.json` with persistent channel mappings
   - Supports proxy configuration and custom CA certificates

2. **Semantic Server** (`pkg/server/semantic_server.go`):
   - Registers semantic tools organized by OODA loop phases
   - Supports both stdio and SSE transports with authentication middleware
   - Non-blocking startup with progressive channel cache building

3. **Feature Tools** (`pkg/features/`):
   - **Cache-based Resolution**: All tools use `provider.ResolveChannelID()` instead of direct API calls
   - **Context-aware Results**: Include next-action suggestions with pre-populated parameters
   - **OODA Phase Alignment**: Tools designed for specific workflow phases
   - Support pagination, time-based filtering, and intelligent content analysis

### Semantic Tool Implementations

**Observe Phase Tools**:
- `check-unreads`: Enhanced with count-based windowing and auto-mark-as-read
  - 1-3 messages: Full content + context, auto-mark as read
  - 4-15 messages: Full content with urgency analysis, auto-mark as read  
  - 16-50 messages: Summary with highlights, preserve unread status
  - 50+ messages: High-level summary with pagination
- `catch-up-on-channel`: Analyzes recent channel activity with importance scoring
- `check-my-mentions`: Focuses on personal mentions with urgency assessment
- `list-channels`: Shows available channels with membership and activity status

**Orient Phase Tools**:
- Enhanced message content reading via count-based windowing in `check-unreads`
- Context-aware filtering and relevance scoring with actual message text
- Smart next-action generation with pre-populated parameters
- Automatic read state management based on consumption depth

**Decide Phase Tools**:
- `decide-next-action`: Basic reflection tool with extensibility recommendations
- Self-deprecating design that promotes specialized reasoning tools

**Act Phase Tools**:
- `find-discussion`: Deep thread exploration and search functionality
- `mark-as-read`: Bulk read state management with scope controls
- Future: `write-message` - Message composition and sending tools

### OODA Loop Workflow Examples

**Complete workflow: "Anything new from [Person]?"**
1. **Observe**: `check-unreads` → detects unread DMs and mentions
2. **Orient**: Enhanced tool shows actual message content with count-based windowing
3. **Decide**: `decide-next-action` → analyzes context and suggests response approach
4. **Act**: `write-message` → compose and send appropriate response (planned)

**Count-based Reading Policy**:
- **1-3 unread**: Read full content + context → Auto-mark as read (full consumption)
- **4-15 unread**: Read full content + urgency → Auto-mark as read (reviewed thoroughly)  
- **16-50 unread**: Summary + highlights → Keep unread (triaged, needs follow-up)
- **50+ unread**: Overview + pagination → Keep unread (surface-level awareness)

This policy ensures reading correlates with actual content consumption and prevents accidentally "clearing" conversations that haven't been fully processed.

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
# Slack MCP Server

Go-based MCP server for Slack workspace interaction using session tokens (xoxc/xoxd). No OAuth, no bot permissions, no admin approval.

## Build

```bash
make build              # Build for current platform
make build-all-platforms # Cross-compile (darwin/linux/windows x amd64/arm64)
make test               # Run tests
make format             # Format code
make tidy               # Tidy go modules
make clean              # Remove build artifacts
make npm-publish NPM_TOKEN=... # Publish to npm
```

## Architecture

- `cmd/slack-mcp/` — Entry point, transport selection (stdio/sse), setup command
- `pkg/server/` — MCP server, tool registration
- `pkg/provider/` — Slack API client, two-phase channel caching
- `pkg/features/` — Tool implementations
- `pkg/text/` — Text processing utilities
- `npm/` — npm wrapper packages (platform binary resolver)

## Tools

| Tool | What it does |
|------|-------------|
| `check-unreads` | Unread messages across DMs/channels/mentions |
| `catch-up` | Recent channel activity (time-filtered) |
| `list-channels` | Browse channels + membership |
| `check-mentions` | Your @-mentions by urgency |
| `search` | Find messages (full Slack query syntax) |
| `get-context` | Thread/conversation history |
| `check-timing` | Conversation pacing analysis |
| `send-message` | Post to channel/DM/thread |
| `mark-read` | Mark conversations as read |
| `react` | Add/remove emoji reactions |

## Environment

Required: `SLACK_XOXC_TOKEN`, `SLACK_XOXD_TOKEN`
Optional: `SLACK_MCP_HOST`, `SLACK_MCP_PORT`, `SLACK_MCP_SSE_API_KEY`, `SLACK_MCP_DEBUG`

## Key Design Decisions

- Session tokens over OAuth — no workspace permissions needed
- Stealth reads — only `mark-read` triggers read receipts
- Channel names over IDs — never expose internal IDs to AI
- Two-phase caching — fast startup with member channels, background load all
- Setup command uses embedded web server (go:embed) — tokens never leave localhost

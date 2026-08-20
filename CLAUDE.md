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

| Tool | Kind | What it does |
|------|------|-------------|
| `inbox` | noun | What needs you: `view='new'` (since your last dismiss), `'unreads'`, `'mentions'` |
| `messages` | noun | Conversation content: `target=` reads in full, `+around=` context, `+since=` time window, `query=` full Slack search syntax |
| `estate` | noun | Workspace shape and relationships: `view='about'\|'families'\|'person'\|'initiatives'\|'convergence'\|'people'\|'channels'` |
| `say` | verb | Contribute content (Slack-visible): a message, or an emoji reaction |
| `dismiss` | verb | Mark inbox items handled — private watermark, invisible to Slack |
| `mark-read` | verb | Fire read receipts — the one visibly-public read signal |
| `auth` | verb | Interactive token setup (localhost only) |
| `download` | verb | Download a shared file |

Verb encodes effect, noun encodes domain, parameter encodes scope (ADR-009). Every noun echoes its effective parameters and pages every capped list.

## Environment

Required: `SLACK_MCP_XOXC_TOKEN`, `SLACK_MCP_XOXD_TOKEN` (or config file at `~/.config/slack-mcp/config.json`)
Optional: `SLACK_MCP_HOST`, `SLACK_MCP_PORT`, `SLACK_MCP_SSE_API_KEY`, `SLACK_MCP_DEBUG`

## Key Design Decisions

- Session tokens over OAuth — no workspace permissions needed
- Stealth reads — only `mark-read` triggers read receipts
- Channel names over IDs — never expose internal IDs to AI
- Two-phase caching — fast startup with member channels, background load all
- Setup command uses embedded web server (go:embed) — tokens never leave localhost

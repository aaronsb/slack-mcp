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
| `estate` | noun | Workspace shape and relationships: `view='about'\|'families'\|'person'\|'initiatives'\|'convergence'\|'people'\|'channels'`; `about`/`person` take `render='graph'` to also write a static HTML graph page (not in `batch`) |
| `batch` | executor | Run a held plan of reads in one call: `commands=[{tool, params}...]`; playbooks via `save=`/`run=`/`list=`/`delete=` |
| `say` | verb | Contribute content (Slack-visible): a message (a thread reply can also go to the channel with `broadcast=true`), or an emoji reaction |
| `dismiss` | verb | Mark inbox items handled — private watermark, invisible to Slack |
| `mark-read` | verb | Fire read receipts — the one visibly-public read signal |
| `auth` | verb | Interactive token setup (localhost only) |
| `download` | verb | Download a shared file |

Verb encodes effect, noun encodes domain, parameter encodes scope (ADR-009); the batch executor encodes composition, never effect, and admits only the read nouns (ADR-010). Every noun echoes its effective parameters and pages every capped list.

## Environment

Required: `SLACK_MCP_XOXC_TOKEN`, `SLACK_MCP_XOXD_TOKEN` (or config file at `~/.config/slack-mcp/config.json`)
Optional: `SLACK_MCP_HOST`, `SLACK_MCP_PORT`, `SLACK_MCP_SSE_API_KEY`, `SLACK_MCP_LOG_FILE` (stdio log path, `/dev/null` discards; a symlink is refused; client env only, `.env` may not set it), `SLACK_MCP_DEPLOYMENT` (experimental, untested over SSH; `local`\|`remote`; default `local`, `remote` must be declared in the MCP client config, never detected), `SLACK_MCP_IDLE_TIMEOUT` (remote stdio only, default `2h`; `0`/`off` disables; set on a local or SSE server, it refuses to start)
Log: stdio logs to `$XDG_STATE_HOME/slack-mcp/slack-mcp.log` (default `~/.local/state/slack-mcp/`; dir `0700`, file `0600`, an existing file is tightened), SSE to stderr. Log sites name fields and never print a token, cookie, or whole response; the sink also redacts token- and cookie-shaped values. There is no debug switch.
`.env`: may set only `SLACK_MCP_PERSONALITY` and `SLACK_MCP_NO_BROWSER` (never overriding the environment). Every other setting comes from the MCP client config; any other key in `.env` that the environment does not already set refuses startup, naming the key.
SSE auth: when `SLACK_MCP_SSE_API_KEY` is set, every SSE/message request needs `Authorization: Bearer <key>` (else 401). A non-loopback `SLACK_MCP_HOST` without a key of at least 16 characters refuses to start.
Binding to loopback is not the same as local-only: set the key whenever a tunnel or reverse proxy fronts the port, and terminate TLS at the proxy since the key travels in a header.

## Key Design Decisions

- Session tokens over OAuth — no workspace permissions needed
- Stealth reads — only `mark-read` triggers read receipts
- Channel names over IDs — never expose internal IDs to AI
- Two-phase caching — fast startup with member channels, background load all
- Setup command uses embedded web server (go:embed) — tokens never leave localhost

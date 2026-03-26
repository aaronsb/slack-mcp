# ADR-001: Slack MCP Fusion

## Status

Proposed

## Context

Two existing Slack MCP servers serve overlapping but complementary purposes:

- **aaronsb/slack-mcp**: Go-based, semantic tool design, two-phase channel caching, internal Slack APIs (`client.counts`), npm-wrapped Go binaries, stdio + SSE transports.
- **forayconsulting/slack-stealth-mcp**: Python + TypeScript (Cloudflare), stealth-focused (no read receipts), parallel batch API calls, multi-workspace support, emoji reactions, thread context fetching, browser-based token extraction.

Neither is complete alone. The stealth repo's Python local + Cloudflare remote split fragments the codebase. Both use session tokens (xoxc/xoxd) and avoid OAuth/bot permissions.

**Goal**: Fuse the best of both into the existing Go codebase. Publish via npm under `@aaronsb/slack-mcp` using the proven Go binary + npm wrapper pattern. Add a setup CLI for token extraction.

## Decision

### Language: Go (keep existing codebase)

- Proven, working code with existing architecture
- Static binaries — zero runtime deps for the user
- Cross-compiles to all platforms trivially
- npm wrapper already solved (bin resolver picks platform-specific binary)
- GitHub Actions handles build + publish

### Tool Set: 10 tools, plain names

| Tool | What it does | Origin |
|------|-------------|--------|
| `check-unreads` | Unread messages across DMs/channels/mentions | Both — uses `client.counts` internal API + parallel batching |
| `catch-up` | Recent activity in a channel (time-filtered) | slack-mcp |
| `list-channels` | Browse available channels + membership | Both |
| `check-mentions` | Your @-mentions, grouped by urgency | slack-mcp |
| `search` | Find messages (full Slack query syntax) | Both — stealth's query builder |
| `get-context` | Thread history / conversation around a message | stealth (new) |
| `check-timing` | When's natural to reply (conversation pacing) | slack-mcp |
| `send-message` | Post to channel/DM/thread, auto-opens DMs | Both — stealth's auto-DM-open |
| `mark-read` | Mark conversations as read | Both |
| `react` | Add/remove emoji reactions | stealth (new) |

Dropped: `decide-next-action` (the AI already does that), `debug-internal` (add behind flag later if needed).

Renamed from OODA terminology to plain names (e.g., `catch-up-on-channel` -> `catch-up`, `find-discussion` -> `search`, `write-message` -> `send-message`, `pace-conversation` -> `check-timing`).

### New features to add (from stealth)

1. **`get-context` tool** — fetch thread history and conversation around a specific message
2. **`react` tool** — add/remove emoji reactions
3. **Parallel batch API calls** — 15-concurrent batches for unread/channel scanning (goroutines)
4. **Multi-workspace support** — config file holds multiple workspaces with default selection; each tool accepts optional `workspace` parameter
5. **Auto-DM-open** — `send-message` auto-opens DM conversations when given a username/user ID
6. **Stealth by default** — reads never trigger read receipts; only `mark-read` does

### Authentication: Session tokens (xoxc/xoxd)

- No OAuth app, no bot permissions, no admin approval
- Tokens provided via env vars (`SLACK_XOXC_TOKEN`, `SLACK_XOXD_TOKEN`) or config file (`~/.config/slack-mcp/config.json`)
- **Setup command** (`slack-mcp setup` / `npx @aaronsb/slack-mcp setup`) — built into the Go binary using `embed`:
  1. Bind to high port (default 51837, auto-increment on collision, up to 10 retries)
  2. Open browser to embedded setup page on localhost
  3. Page instructs user to run a small JS snippet in browser console on any Slack tab
  4. Snippet POSTs tokens directly to `localhost:<port>/callback`
  5. Server validates with `auth.test`, writes config, shuts down
- Setup page HTML/JS is embedded in the binary at compile time via `//go:embed` — no external assets, tamper-proof, single binary does everything
- Tokens never leave localhost. Never appear in URLs or browser history.

### Architecture highlights carried forward

- **Two-phase channel caching** (slack-mcp): member channels first (fast startup), all channels in background
- **Channel name resolution**: tools use names, never expose IDs to AI
- **Count-based read policy** (slack-mcp): auto-mark behavior scales with message count
- **Rate limiting**: token bucket (2 req/s) + exponential backoff on 429

### Transports

- stdio (default) — standard MCP client integration
- SSE with optional API key auth — remote/shared access

### Binary modes

```
slack-mcp                      # MCP server, stdio (default)
slack-mcp --transport sse      # MCP server, SSE
slack-mcp setup                # Token setup flow (embedded web server)
```

One binary does everything. npm wrapper just resolves platform binary and passes args through.

### Distribution

- **npm**: `@aaronsb/slack-mcp` (main wrapper + platform-specific binary packages)
- **GitHub Releases**: Raw Go binaries per platform (direct download, CI, Docker)
- **.mcpb**: Packaged Go binary installer for MCP clients with native package support
- GitHub Actions: cross-compile Go, publish to all three channels

## Consequences

### Positive

- Keeps proven Go codebase — no rewrite risk
- Best features from both repos in one package
- Agent-agnostic — works with Claude, Goose, or any MCP client
- Setup flow is simple and secure (localhost-only token handling)
- Static binaries mean zero runtime deps (Node.js only needed for npm install, not for running)
- Setup flow embedded in binary — no external assets or scripts needed
- GitHub Actions automates build/publish to npm, GitHub Releases, and .mcpb

### Negative

- New features (get-context, react, multi-workspace, parallel batching) are net-new Go code
- Internal Slack API (`client.counts`) may change without notice
- Session tokens are technically unofficial — Slack could break them
- npm wrapper requires Node.js for install, even though the server itself doesn't

### Risks

- Token extraction snippet may need updates as Slack changes its web app
- Rate limiting needs careful testing under real workspace loads
- Multi-workspace config adds complexity vs single-workspace env vars
- Tool renames may break existing users' MCP client configs (document migration)

# Slack MCP

![License](https://img.shields.io/github/license/aaronsb/slack-mcp)
![GitHub stars](https://img.shields.io/github/stars/aaronsb/slack-mcp?style=social)
![Latest Release](https://img.shields.io/github/v/release/aaronsb/slack-mcp?include_prereleases&label=version)

MCP server that gives AI agents access to your Slack workspaces using session tokens. No OAuth apps, no bot permissions, no admin approval required.

## How it works

Slack MCP uses your existing browser session tokens (`xoxc`/`xoxd`) to interact with Slack on your behalf. It reads stealthily by default — only the `mark-read` tool triggers read receipts. Everything else is invisible to other users.

**Token extraction** is built into the binary. If you're logged into Slack in Chrome or Firefox, the setup flow can extract tokens automatically — no copy-pasting from DevTools.

## Prerequisites

You need an active Slack session in your browser. Log into your workspace at [app.slack.com](https://app.slack.com) in **Chrome**, **Chromium**, **Edge**, or **Firefox** before running setup.

## Install

### Claude Code

```bash
claude mcp add slack-mcp -- npx -y @aaronsb/slack-mcp
```

Then ask Claude to run the `auth-setup` tool. It will guide you through browser selection, profile selection, and automatic token extraction.

### Claude Desktop

Download the `.mcpb` file for your platform from the [latest release](https://github.com/aaronsb/slack-mcp/releases/latest):

| Platform | File |
|----------|------|
| macOS (Apple Silicon) | `slack-mcp-darwin-arm64.mcpb` |
| macOS (Intel) | `slack-mcp-darwin-x64.mcpb` |
| Linux (x64) | `slack-mcp-linux-x64.mcpb` |
| Linux (ARM) | `slack-mcp-linux-arm64.mcpb` |
| Windows (x64) | `slack-mcp-windows-x64.mcpb` |

Open the file (double-click or drag into Claude Desktop). When prompted for tokens, you can either:
- Leave them blank and use the `auth-setup` tool after connecting
- Paste tokens if you already have them

### Standalone binary

Download the binary for your platform from [releases](https://github.com/aaronsb/slack-mcp/releases/latest), then:

```bash
# Extract tokens from your browser (interactive)
./slack-mcp setup

# Run as MCP server (stdio, default)
./slack-mcp

# Run as MCP server (SSE, for remote/shared access)
./slack-mcp --transport sse
```

### npm (global)

```bash
npm install -g @aaronsb/slack-mcp
slack-mcp setup
```

## Token setup

There are three ways to get your Slack tokens, from easiest to most manual:

### Automatic (Chrome/Edge)

The `auth-setup` MCP tool or `slack-mcp setup` CLI command will:

1. Detect your installed browsers
2. Let you pick which browser and profile has Slack
3. Open the browser, navigate to Slack, and extract tokens via Chrome DevTools Protocol
4. Validate and save them

**Requires Chrome to be fully closed** before extraction — your tabs will restore when you reopen it.

### Semi-automatic (Firefox)

The setup flow writes a temporary browser extension to a temp directory, then guides you to load it in Firefox via `about:debugging`. The extension extracts tokens and sends them to the local callback server. It's removed automatically when Firefox closes.

### Manual

Run `slack-mcp setup` or use the `auth-setup` tool — if no browser is detected or automatic extraction fails, it falls back to a localhost web page with step-by-step DevTools instructions.

You can also set tokens directly via environment variables:

```bash
export SLACK_XOXC_TOKEN="xoxc-..."
export SLACK_XOXD_TOKEN="xoxd-..."
./slack-mcp
```

## Tools

| Tool | What it does |
|------|-------------|
| `check-unreads` | Unread messages across DMs, channels, and mentions |
| `catch-up` | Recent channel activity with time filtering |
| `list-channels` | Browse channels and membership |
| `check-mentions` | Your @-mentions grouped by urgency |
| `search` | Find messages (full Slack query syntax) |
| `get-context` | Thread history and conversation context |
| `check-timing` | Conversation pacing analysis |
| `send-message` | Post to channel, DM, or thread |
| `mark-read` | Mark conversations as read (only tool that triggers read receipts) |
| `react` | Add or remove emoji reactions |
| `auth-setup` | Browser-automated token extraction |

## Privacy

- **Stealth by default** — reads never trigger read receipts; only `mark-read` does
- **Channel names, not IDs** — the AI never sees internal Slack identifiers
- **Tokens stay local** — stored in `~/.config/slack-mcp/config.json` with `0600` permissions
- **No network traffic except Slack** — the binary connects only to `slack.com/api/*`
- **No browser downloads** — uses your installed browser, never fetches binaries from CDNs

## Development

```bash
make build          # Build for current platform
make test           # Run tests
make build-all-platforms  # Cross-compile (6 platforms)
make release TAG=v1.3.0   # Tag and push (CI handles the rest)
```

## License

MIT

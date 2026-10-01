# Slack MCP

![License](https://img.shields.io/github/license/aaronsb/slack-mcp)
![GitHub stars](https://img.shields.io/github/stars/aaronsb/slack-mcp?style=social)
![Latest Release](https://img.shields.io/github/v/release/aaronsb/slack-mcp?include_prereleases&label=version)

MCP server that gives AI agents access to your Slack workspaces using session tokens. No OAuth apps, no bot permissions, no admin approval required.

Beyond reading and writing messages, it accumulates a durable picture of the workspace — every channel and user's state over time, departures kept as dated tombstones — and synthesizes a relationship graph from it on demand: `estate view='about' person='X'` answers "tell me about X" with their footprint, circle, and a ranked reading plan. The graph is never maintained as a store; it is computed per query from two small local ledgers that hold no message content ([design note](docs/design-notes/runtime-graph-synthesis.md)). Add `render='graph'` to draw one view as a static HTML page you open locally.

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

Then ask Claude to run the `auth` tool. It will guide you through browser selection, profile selection, and automatic token extraction.

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
- Leave them blank and use the `auth` tool after connecting
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

The `auth` MCP tool or `slack-mcp setup` CLI command will:

1. Detect your installed browsers
2. Let you pick which browser and profile has Slack
3. Open the browser, navigate to Slack, and extract tokens via Chrome DevTools Protocol
4. Validate and save them

**Requires Chrome to be fully closed** before extraction — your tabs will restore when you reopen it.

### Semi-automatic (Firefox)

The setup flow writes a temporary browser extension to a temp directory, then guides you to load it in Firefox via `about:debugging`. The extension extracts tokens and sends them to the local callback server. It's removed automatically when Firefox closes.

### Manual

Run `slack-mcp setup` or use the `auth` tool — if no browser is detected or automatic extraction fails, it falls back to a localhost web page with step-by-step DevTools instructions.

You can also set tokens directly via environment variables:

```bash
export SLACK_MCP_XOXC_TOKEN="xoxc-..."
export SLACK_MCP_XOXD_TOKEN="xoxd-..."
./slack-mcp
```

Settings come from the environment your MCP client config provides. A `.env` file in the working directory may set only `SLACK_MCP_PERSONALITY` and `SLACK_MCP_NO_BROWSER`, and never overrides a variable the environment already sets. If `.env` sets any other key the environment does not, the server refuses to start and names the key; set it in the client config instead.

## Lifecycle

Over stdio the server exits cleanly when its client is gone: stdin closes, a termination signal arrives (`SIGTERM`, `SIGINT`, `SIGHUP`), or its parent process exits. Each exit flushes the caches and releases the ledger locks, so the next instance is not left read-only. On Windows, the npm wrapper's `child.kill` terminates the server forcibly, so this graceful shutdown does not run there.

### Remote deployment (experimental)

Remote deployment is experimental and has not been tested against a real SSH session. Whether the server supports remote use at all, and in what form, is still open (see #102). Nothing below applies unless you set `SLACK_MCP_DEPLOYMENT=remote`.

A wedged connection, such as a half-open SSH session, leaves the pipe open with nobody reading it. Only an idle timeout catches that, and it is valid only where it can happen, so the server ties the timeout to how it is deployed:

| Deployment | `SLACK_MCP_IDLE_TIMEOUT` |
|---|---|
| stdio, local | Not allowed. A stdio client does not respawn a server that exits, so an idle-but-healthy session would find it disconnected. |
| stdio, remote (over SSH) | Defaults to `2h`. Set a duration (`90m`) to change it, or `off` to disable. |
| SSE | Not allowed. Each session is cleaned up when its connection ends. |

The deployment is `local` unless you declare it. The server does not detect it: `SSH_CONNECTION` only means some ancestor logged in over SSH (running `claude` in an SSH session, VS Code Remote-SSH), not that the MCP pipe crosses SSH. Only the MCP client config that launches the server knows, so declare `remote` there, in the entry that starts the server over SSH. `ssh` does not forward your environment, so pass the variable through the remote command line with `env`:

```json
{
  "mcpServers": {
    "slack": {
      "command": "ssh",
      "args": ["host", "env", "SLACK_MCP_DEPLOYMENT=remote", "slack-mcp"]
    }
  }
}
```

(ssh joins the arguments into one remote command line, so this runs `env SLACK_MCP_DEPLOYMENT=remote slack-mcp` on the host. Setting it in the client's `env` block does not work, because that only reaches the local `ssh` process.) A timeout set where it is not allowed, or a value that does not parse, stops the server at startup with the reason on stderr.

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

## Privacy

- **Stealth by default** — reads never trigger read receipts; only `mark-read` does
- **Channel names, not IDs** — the AI never sees internal Slack identifiers
- **Tokens stay local** — stored in `~/.config/slack-mcp/config.json` with `0600` permissions
- **The log is private and holds no credentials** — on stdio it goes to `$XDG_STATE_HOME/slack-mcp/slack-mcp.log` (default `~/.local/state/slack-mcp/`), directory `0700`, file `0600`; set `SLACK_MCP_LOG_FILE` in your MCP client config to move it. It records workspace and user names, never tokens or cookies, and anything token- or cookie-shaped is redacted before it is written
- **Ledgers hold no message content** — the durable estate ledger stores entity facts (names, lifecycle, tombstones); the attention ledger stores `{user, conversation, day}` encounters with a 90-day window; both live under XDG with `0600`, and deleting them deletes the graph
- **Hour-level activity is recorded only for you** — colleagues bucket by day, by design
- **Graph reports are local, private files** — `estate render='graph'` writes a self-contained HTML page to `$XDG_DATA_HOME/slack-mcp/reports/` (default `~/.local/share/slack-mcp/reports/`), directory `0700`, file `0600`, one per view and person, replaced on each render. A report holds relationship data — the same names and counts the view shows, never IDs or colleagues' hours — so treat it like the ledgers; delete the directory to remove them. The page loads nothing from the network (bundled Cytoscape.js, strict Content-Security-Policy), and nothing is served or opened for you
- **No network traffic except Slack** — the binary connects only to `slack.com/api/*`
- **No browser downloads** — uses your installed browser, never fetches binaries from CDNs

## Development

```bash
make build          # Build for current platform
make test           # Run tests
make build-all-platforms  # Cross-compile (6 platforms)
make release TAG=v2.1.1   # Tag and push (CI handles the rest)
```

## License

MIT

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
| `messages` | noun | Conversation content: `target=` reads in full, `+around=` context, `+since=` time window, `query=` raw Slack search syntax (passed as written) plus resolved filters `in`, `from`, `after`, `before`, `has` (link, pin, :emoji:), `thread` |
| `estate` | noun | Workspace shape and relationships: `view='about'\|'families'\|'person'\|'initiatives'\|'convergence'\|'people'\|'channels'`; `about`/`person` take `render='graph'` to also write a static HTML graph page (not in `batch`) |
| `batch` | executor | Run a held plan of reads in one call: `commands=[{tool, params}...]`; playbooks via `save=`/`run=`/`list=`/`delete=` |
| `say` | verb | Contribute content (Slack-visible): a message (a thread reply can also go to the channel with `broadcast=true`), files from the exchange directory (`files=['report.pdf']`, bare names, at most 10, shared as one message with `text` as the comment), or an emoji reaction; scanned for secrets and gated before anything is sent (ADR-013) |
| `dismiss` | verb | Mark inbox items handled — private watermark, invisible to Slack |
| `mark-read` | verb | Fire read receipts — the one visibly-public read signal; refused at a quarantined destination, and never opens a DM |
| `auth` | verb | Interactive token setup (localhost only) |
| `download` | verb | Download a shared file into the exchange directory (`filename=` is a bare name, not a path; a taken name is saved as `name (n).ext` and the result says so) |
| `put` | verb | Write a new file into the exchange directory for `say files=` to attach, for clients whose file tools cannot reach the server's filesystem: `name=` (bare name) and exactly one of `content=` (text) or `base64=` (standard alphabet, padding optional); at most 5 MiB; never overwrites (a taken name is saved as `name (n).ext`), never reads, lists, or deletes (ADR-012) |
| `unlock` | verb | Open a local page in your browser to review and clear quarantines and the strike lock; the page clears, the tool returns no link and never reports what was cleared (ADR-013) |

Verb encodes effect, noun encodes domain, parameter encodes scope (ADR-009); the batch executor encodes composition, never effect, and admits only the read nouns (ADR-010). Every noun echoes its effective parameters and pages every capped list.

## Outbound safety

Everything this server posts is attributed to your account, and the agent reads text anyone in the workspace can write. So every `say` passes a fixed order before any content reaches Slack or any DM is opened (ADR-013):

1. **Strike lock** — after too many blocks, every `say` and `mark-read` is refused until you clear it (CLI, or the `unlock` page).
2. **Exchange directory** — file names are bare names inside it (ADR-012).
3. **Quarantine** — a destination a block closed refuses `say` and `mark-read`; reads keep working.
4. **Secret scanner** — the text as sent, a reaction's emoji, and each file's name and bytes, including base64, hex, URL-encoded, gzip, and zlib content. A match is a block: nothing is sent, and the agent is told the class and the field, never the value.
5. **Approval gate** — a destination outside your organization, or (in `strict`) a file `download` took from another conversation, waits for you as a pending request. Nothing is sent until you approve it.

Two settings in your MCP client config shape this (ADR-014); `.env` cannot set them:

| Setting | Values | Default | Effect |
|---|---|---|---|
| `SLACK_MCP_IDENTITY` | `human`, `agent` | `human` | Whose account this is: the agent writes on your behalf, or as itself. Sets the `say` description, the server instructions, and the notice a block posts: "[automated] A message from this account was blocked by a safety filter." or "I can't share that." |
| `SLACK_MCP_SAFETY` | `strict`, `soft` | `strict` | `strict` quarantines on the first block and locks at two strikes. `soft` warns on the first, quarantines from the second, locks at three, and lets a downloaded file move between conversations with a warning. |

A block posts the notice into the quarantined conversation, never to a destination outside your organization and never to a person with no DM. Blocks, pending requests, and sends trust let through are logged as `outbound-safety: BLOCKED`, `PENDING`, and `TRUSTED` lines (destination, IDs, class, counts; never content). When state needs you, `inbox`, `messages`, `estate`, and `batch` results open with a banner.

A block that cannot be written to the quarantine file holds every `say` and `mark-read` in that server process (in memory) and issues a lift request (`lift=strikes`). Fix the file, then approve the request or run `slack-mcp quarantine clear strikes`; either clears every recorded strike. The clear releases the hold only when the file was readable as the hold engaged and has not been replaced or edited since; otherwise approve the request. A restart also releases the hold, and the unrecorded strike is lost. A download whose provenance cannot be recorded fails and its file is deleted (the result says so if the deletion fails too); provenance that cannot be read, or has a malformed line, makes attachments with no record need approval in `strict`, and the request (and its `PENDING` log line, `provenance=…`) says to repair `provenance.jsonl`. Reaction names must be emoji names (`[a-z0-9_+'-]`, optionally `::skin-tone-2`…`6`).

You answer from a terminal:

```bash
slack-mcp approve            # list pending requests
slack-mcp approve p7k2       # let the next matching call through once
slack-mcp deny p7k2
slack-mcp quarantine list
slack-mcp quarantine clear '#general'   # or @handle, or strikes
slack-mcp trust add '#partner' --case external --for 30d
slack-mcp trust list
slack-mcp trust remove '#partner'
```

A client on MCP protocol 2026-07-28 or later that declares elicitation on the call is also asked in-band to approve once, deny, or (in `soft`) approve and trust; every other client, and every SSE session, uses the CLI. Elicitation never lifts a quarantine or the lock.

Without a terminal (Claude Desktop and other desktop clients), ask the agent to clear a lock. It calls `unlock`, which opens a page on `127.0.0.1` in your browser listing each quarantined person or conversation and the strike lock, with what the scanner found (the kind of secret, the field, where it was going, and when; never the value). Check what to clear and press **Clear**, or press **Done** to close without clearing; either closes the page, and the link stops working. If the locks changed after you loaded the page, Clear applies nothing and shows them again. A page nobody loads or answers for fifteen minutes closes. Clears from the page are recorded as `by=web`, beside the CLI's `by=cli`. The agent never sees the page's link; if no browser can be opened (or `SLACK_MCP_NO_BROWSER` is set), `unlock` refuses and points at the CLI. It is also unavailable under `SLACK_MCP_DEPLOYMENT=remote` and over SSE, where the page would open on a host that need not be yours.

The page is friction, not a barrier: an agent that can drive your browser can click Clear. For an unattended agent, deny it the `unlock` tool and any browser automation in the client's permissions, so only the CLI clears.

The state lives beside the estate ledger, in `$XDG_DATA_HOME/slack-mcp/ledger/<team>/` (`quarantine.jsonl`, `pending.jsonl`, `trust.jsonl`, `provenance.jsonl`, each `0600`). They are append-only JSON lines, and a running server rereads them on every gated call, so editing or deleting a line takes effect at once; prefer the CLI, which keeps the history.

## Privacy

- **Stealth by default** — reads never trigger read receipts; only `mark-read` does
- **Channel names, not IDs** — the AI never sees internal Slack identifiers
- **Tokens stay local** — stored in `~/.config/slack-mcp/config.json` with `0600` permissions
- **The log is private and holds no credentials** — on stdio it goes to `$XDG_STATE_HOME/slack-mcp/slack-mcp.log` (default `~/.local/state/slack-mcp/`), directory `0700`, file `0600`; set `SLACK_MCP_LOG_FILE` in your MCP client config to move it. It records workspace and user names, never tokens or cookies, and anything token- or cookie-shaped is redacted before it is written
- **Ledgers hold no message content** — the durable estate ledger stores entity facts (names, lifecycle, tombstones); the attention ledger stores `{user, conversation, day}` encounters with a 90-day window; both live under XDG with `0600`, and deleting them deletes the graph
- **Hour-level activity is recorded only for you** — colleagues bucket by day, by design
- **Graph reports are local, private files** — `estate render='graph'` writes a self-contained HTML page to `$XDG_DATA_HOME/slack-mcp/reports/` (default `~/.local/share/slack-mcp/reports/`), directory `0700`, file `0600`, one per view and person, replaced on each render. A report holds relationship data — the same names and counts the view shows, never IDs or colleagues' hours — so treat it like the ledgers; delete the directory to remove them. The page loads nothing from the network (bundled Cytoscape.js, strict Content-Security-Policy), and nothing is served or opened for you
- **Files move only through the exchange directory** — `say files=` reads only from it and `download` and `put` write only into `$XDG_DATA_HOME/slack-mcp/exchange/` (default `~/.local/share/slack-mcp/exchange/`, or `SLACK_MCP_EXCHANGE_DIR` set in the MCP client config), directory `0700`, files `0600`, never overwriting. File parameters take bare names, never paths, so injected text cannot point a tool at `~/.ssh` or an autostart folder; copy a file in to attach it, and out after downloading it, with your own tools; a client whose tools cannot reach the directory (a sandbox on another machine) writes a file in with `put`, which never overwrites, reads, lists, or deletes (ADR-012)
- **No network traffic except Slack** — the binary connects only to Slack hosts (`slack.com` and `*.slack.com`); an upload address or redirect to any other host is refused before a byte is sent
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

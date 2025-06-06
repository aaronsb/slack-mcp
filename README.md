# Slack MCP Server

Model Context Protocol (MCP) server for Slack Workspaces. This integration supports both Stdio and SSE transports, proxy settings and does not require any permissions or bots being created or approved by Workspace admins 😏.

### Feature Demo

![ezgif-316311ee04f444](https://github.com/user-attachments/assets/35dc9895-e695-4e56-acdc-1a46d6520ba0)

## Semantic Tools & OODA Loop

This server implements an **OODA Loop** (Observe-Orient-Decide-Act) pattern for intelligent Slack interaction. Tools are organized by their role in the decision cycle:

### 🔍 Observe Phase - Situational Awareness
1. **`check-unreads`** - "What do I need to pay attention to?"
   - Shows unread DMs, mentions, and channel activity with smart windowing
   - Auto-marks messages as read based on consumption depth
   - Groups results by importance and urgency
   - Natural entry point to the OODA loop

2. **`catch-up-on-channel`** - "Show me what happened in [channel]"
   - Primary tool for exploring specific channels
   - Time-based filtering (e.g., "7d", "24h") 
   - Highlights important messages (reactions, threads, mentions)
   - Flows naturally into Orient phase for deeper understanding

3. **`list-channels`** - "What channels are available?"
   - Shows all channels with membership status
   - Supports filtering and search
   - Uses two-phase caching for fast startup

4. **`check-my-mentions`** - "Where am I mentioned?"
   - Scans channels for your mentions
   - Categorizes by urgency
   - Shows if you've already responded

5. **`find-discussion`** - "Search for specific topics or conversations"
   - Uses Slack's internal search API for comprehensive results
   - Natural language queries across all accessible messages
   - Filters by channel, user, or timeframe
   - Specialized tool for targeted discovery

### 🎯 Orient Phase - Context Understanding
6. **Enhanced `check-unreads`** - Reads actual message content
   - 1-3 messages: Full content + auto-mark as read
   - 4-15 messages: Full content with urgency analysis
   - 16-50 messages: Summary only (preserves unread status)
   - 50+ messages: High-level overview

### 🤔 Decide Phase - Action Planning
6. **`decide-next-action`** - "What should I do next?"
   - Basic reflection tool for workflow guidance
   - Suggests specialized tools when available
   - Context-aware recommendations

7. **`pace-conversation`** - "Should I respond now or wait?"
   - Analyzes conversation timing without delays
   - Uses thinking prompts for natural pacing
   - Prevents spam loops with engagement modes

### ⚡ Act Phase - Response Execution
8. **`mark-as-read`** - "Clear my unreads"
   - Bulk management of read states
   - Supports various scopes and filters
   - Channel name resolution

9. **`write-message`** - "Send a message"
    - Smart channel/user resolution
    - Thread support for replies
    - Semantic flow to next actions

For technical details and flow diagrams, see [docs/ooda-loop-workflow.md](docs/ooda-loop-workflow.md).

## Setup Guide

### 1. Authentication Setup

Open up your Slack in your browser and login.

#### Lookup `SLACK_MCP_XOXC_TOKEN`

- Open your browser's Developer Console.
- In Firefox, under `Tools -> Browser Tools -> Web Developer tools` in the menu bar
- In Chrome, click the "three dots" button to the right of the URL Bar, then select
`More Tools -> Developer Tools`
- Switch to the console tab.
- Type "allow pasting" and press ENTER.
- Paste the following snippet and press ENTER to execute:
  `JSON.parse(localStorage.localConfig_v2).teams[document.location.pathname.match(/^\/client\/([A-Z0-9]+)/)[1]].token`

Token value is printed right after the executed command (it starts with
`xoxc-`), save it somewhere for now.

#### Lookup `SLACK_MCP_XOXD_TOKEN`

 - Switch to "Application" tab and select "Cookies" in the left navigation pane.
 - Find the cookie with the name `d`.  That's right, just the letter `d`.
 - Double-click the Value of this cookie.
 - Press Ctrl+C or Cmd+C to copy it's value to clipboard.
 - Save it for later.

### 2. Installation

Choose one of these installation methods:

- [Local Development](#local-development) (recommended for testing/development)
- [npx](#Using-npx)
- [Docker](#Using-Docker)

### 3. Adding to MCP Clients

This server can be used with any MCP-compatible client:

#### Claude Desktop App

1. Find your Claude Desktop configuration file:
   - **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
   - **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`
   - **Linux**: `~/.config/claude/claude_desktop_config.json`

2. Add the Slack MCP server to your configuration (see examples below)

3. Restart Claude Desktop

4. Look for the 🔌 icon in Claude to verify the server is connected

#### Claude Code (VS Code Extension)

1. Open the command palette (Cmd/Ctrl + Shift + P)
2. Run "Claude Code: Edit MCP Settings"
3. Add the Slack MCP server configuration
4. Reload VS Code window

#### Other MCP-Compatible Agents

Any agent that supports the Model Context Protocol can use this server. Consult your agent's documentation for:
- How to add MCP servers
- Whether stdio or SSE transport is preferred
- How to pass environment variables

### 4. Configuration Examples

You can configure the MCP server using command line arguments and environment variables.

#### Local Development

Running from source is recommended for development and testing, especially to understand the caching behavior:

1. **Clone and build:**
   ```bash
   git clone https://github.com/korotovsky/slack-mcp-server.git
   cd slack-mcp-server
   go build ./cmd/slack-mcp
   ```

2. **Configure Claude Desktop** (`claude_desktop_config.json`):
   ```json
   {
     "mcpServers": {
       "slack": {
         "command": "/absolute/path/to/slack-mcp-server/slack-mcp",
         "args": ["--transport", "stdio"],
         "env": {
           "SLACK_MCP_XOXC_TOKEN": "xoxc-...",
           "SLACK_MCP_XOXD_TOKEN": "xoxd-..."
         }
       }
     }
   }
   ```

3. **Important notes about caching:**
   - The server uses a **two-phase channel cache**:
     - Phase 1: Loads your member channels immediately (fast startup)
     - Phase 2: Loads all workspace channels in background
   - Channel cache persists across restarts
   - User cache is saved to `.users_cache.json` in the working directory
   - When testing, you may need to delete cache files to force refresh

4. **Development workflow:**
   ```bash
   # Build and test
   make build
   make test
   
   # Run directly for testing
   go run cmd/slack-mcp/main.go --transport stdio
   
   # Clean cache if needed
   rm .users_cache.json
   ```

#### Using npx

If you have npm installed, this is the fastest way to get started with `slack-mcp-server` on Claude Desktop.

Open your `claude_desktop_config.json` and add the mcp server to the list of `mcpServers`:
``` json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": [
        "-y",
        "slack-mcp-server@latest",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXC_TOKEN": "xoxc-...",
        "SLACK_MCP_XOXD_TOKEN": "xoxd-..."
      }
    }
  }
}
```

<details>
<summary>Or, stdio transport with docker.</summary>

```json
{
  "mcpServers": {
    "slack": {
      "command": "docker",
      "args": [
        "run",
        "-i",
        "--rm",
        "-e",
        "SLACK_MCP_XOXC_TOKEN=$SLACK_MCP_XOXC_TOKEN",
        "-e",
        "SLACK_MCP_XOXD_TOKEN=$SLACK_MCP_XOXD_TOKEN",
        "ghcr.io/korotovsky/slack-mcp-server",
        "mcp-server",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXC_TOKEN": "xoxc-...",
        "SLACK_MCP_XOXD_TOKEN": "xoxd-..."
      }
    }
  }
}
```

Please see [Docker](#Using-Docker) for more information.
</details>

#### Using npx with `sse` transport:

In case you would like to run it in `sse` mode, then you  should use `mcp-remote` wrapper for Claude Desktop and deploy/expose MCP server somewhere e.g. with `ngrok` or `docker-compose`.

```json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "https://x.y.z.q:3001/sse",
        "--header",
        "Authorization: Bearer ${SLACK_MCP_SSE_API_KEY}"
      ],
      "env": {
        "SLACK_MCP_SSE_API_KEY": "my-$$e-$ecret"
      }
    }
  }
}
```

<details>
<summary>Or, sse transport for Windows.</summary>

```json
{
  "mcpServers": {
    "slack": {
      "command": "C:\\Progra~1\\nodejs\\npx.cmd",
      "args": [
        "-y",
        "mcp-remote",
        "https://x.y.z.q:3001/sse",
        "--header",
        "Authorization: Bearer ${SLACK_MCP_SSE_API_KEY}"
      ],
      "env": {
        "SLACK_MCP_SSE_API_KEY": "my-$$e-$ecret"
      }
    }
  }
}
```
</details>

#### TLS and Exposing to the Internet

There are several reasons why you might need to setup HTTPS for your SSE.
- `mcp-remote` is capable to handle only https schemes;
- it is generally a good practice to use TLS for any service exposed to the internet;

You could use `ngrok`:

```bash
ngrok http 3001
```

and then use the endpoint `https://903d-xxx-xxxx-xxxx-10b4.ngrok-free.app` for your `mcp-remote` argument.

#### Using Docker

For detailed information about all environment variables, see [Environment Variables](https://github.com/korotovsky/slack-mcp-server?tab=readme-ov-file#environment-variables).

```bash
export SLACK_MCP_XOXC_TOKEN=xoxc-...
export SLACK_MCP_XOXD_TOKEN=xoxd-...

docker pull ghcr.io/korotovsky/slack-mcp-server:latest
docker run -i --rm \
  -e SLACK_MCP_XOXC_TOKEN \
  -e SLACK_MCP_XOXD_TOKEN \
  slack-mcp-server --transport stdio
```

Or, the docker-compose way:

```bash
wget -O docker-compose.yml https://github.com/korotovsky/slack-mcp-server/releases/latest/download/docker-compose.yml
wget -O .env https://github.com/korotovsky/slack-mcp-server/releases/latest/download/default.env.dist
nano .env # Edit .env file with your tokens from step 1 of the setup guide
docker-compose up -d
```

#### Console Arguments

| Argument              | Required ? | Description                                                              |
|-----------------------|------------|--------------------------------------------------------------------------|
| `--transport` or `-t` | Yes        | Select transport for the MCP Server, possible values are: `stdio`, `sse` |

#### Environment Variables

| Variable                       | Required ? | Default     | Description                                                                   |
|--------------------------------|------------|-------------|-------------------------------------------------------------------------------|
| `SLACK_MCP_XOXC_TOKEN`         | Yes        | `nil`       | Authentication data token field `token` from POST data field-set (`xoxc-...`) |
| `SLACK_MCP_XOXD_TOKEN`         | Yes        | `nil`       | Authentication data token from cookie `d` (`xoxd-...`)                        |
| `SLACK_MCP_SERVER_PORT`        | No         | `3001`      | Port for the MCP server to listen on                                          |
| `SLACK_MCP_SERVER_HOST`        | No         | `127.0.0.1` | Host for the MCP server to listen on                                          |
| `SLACK_MCP_SSE_API_KEY`        | No         | `nil`       | Authorization Bearer token when `transport` is `sse`                          |
| `SLACK_MCP_PROXY`              | No         | `nil`       | Proxy URL for the MCP server to use                                           |
| `SLACK_MCP_SERVER_CA`          | No         | `nil`       | Path to the CA certificate of the trust store                                 |
| `SLACK_MCP_SERVER_CA_INSECURE` | No         | `false`     | Trust all insecure requests (NOT RECOMMENDED)                                 |

## Troubleshooting

### Common Issues

1. **Server not showing in Claude**
   - Check the 🔌 icon in Claude - it should show "slack" when connected
   - Verify your configuration file syntax (must be valid JSON)
   - Check Claude logs: `~/Library/Logs/Claude/mcp*.log` (macOS)
   - Ensure tokens are correctly set in the env section

2. **Authentication errors**
   - Tokens expire when you log out of Slack - get fresh tokens
   - Make sure both `xoxc` and `xoxd` tokens are set
   - Verify tokens don't have extra quotes or spaces

3. **Channels not found**
   - The server caches channels in two phases - wait a few seconds for full load
   - Use `list-channels` to see available channels
   - Private channels require you to be a member

4. **SSE transport issues**
   - Ensure your SSE endpoint is HTTPS (use ngrok if needed)
   - Check that `SLACK_MCP_SSE_API_KEY` matches in server and client config
   - Verify firewall/network allows connection to your SSE port

### Debugging Tools

```bash
# Test the server directly
npx slack-mcp-server --transport stdio

# Run the MCP inspector
npx @modelcontextprotocol/inspector npx slack-mcp-server --transport stdio

# View Claude logs (macOS)
tail -n 20 -f ~/Library/Logs/Claude/mcp*.log

# View Claude logs (Windows)
# Check: %APPDATA%\Claude\logs\

# Test with curl (SSE mode)
curl -H "Authorization: Bearer YOUR_API_KEY" https://localhost:3001/sse
```

## Security

- Never share API tokens
- Keep .env files secure and private

## License

This project is licensed under the MIT License - see [LICENSE](LICENSE) file for details.

- Original slack-mcp-server: Copyright (c) 2025 Dmitrii Korotovskii
- Semantic OODA loop enhancements: Copyright (c) 2025 Aaron Bockelie

This is not an official Slack product.

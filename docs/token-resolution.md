# Token Resolution Flow

How slack-mcp resolves credentials across MCP hosts (Claude Desktop, Claude Code, etc.).

## Process Startup

```mermaid
flowchart TD
    Launch([Binary launches]) --> SubCmd{"args[1] == setup?"}

    SubCmd -->|yes| CLI[Run interactive CLI setup]
    CLI --> Exit([Exit])

    SubCmd -->|no| Flags[Parse flags<br>-t stdio or sse]
    Flags --> LogSetup{Transport?}

    LogSetup -->|stdio| LogFile[Redirect logs to<br>/tmp/slack-mcp.log]
    LogSetup -->|sse| LogStdout[Keep logs on stdout]

    LogFile --> DotEnv[Load .env if present]
    LogStdout --> DotEnv

    DotEnv --> LoadProvider[loadProvider]

    LoadProvider --> CheckEnv{Env vars<br>non-empty?}
    CheckEnv -->|yes| UseEnv[Use env tokens]
    CheckEnv -->|no| CheckConfig{Config file<br>has workspaces?}
    CheckConfig -->|yes| UseConfig[Use config tokens]
    CheckConfig -->|no| NoAuth[provider = nil]

    UseEnv --> CreateServer[NewSemanticMCPServer]
    UseConfig --> CreateServer
    NoAuth --> CreateServer

    CreateServer --> RegisterTools[Register all tools<br>+ resources]

    RegisterTools --> HasAuth{provider != nil?}

    HasAuth -->|yes| AsyncBoot[Background goroutine:<br>boot provider + hydrate cache]
    HasAuth -->|no| ToolsLimited[Tools return<br>setup_needed guidance]

    AsyncBoot --> Serve
    ToolsLimited --> Serve

    Serve{Transport?}
    Serve -->|stdio| Stdio[ServeStdio<br>JSON-RPC over stdin/stdout]
    Serve -->|sse| SSE[ServeSSE<br>HTTP on host:port]

    Stdio --> Ready([Accepting tool calls])
    SSE --> Ready
```

## Token Resolution

```mermaid
flowchart TD
    Start([loadProvider]) --> CheckEnv{Env vars set?<br>SLACK_MCP_XOXC_TOKEN<br>SLACK_MCP_XOXD_TOKEN}

    CheckEnv -->|non-empty| UseEnv[Create provider<br>with env tokens]
    CheckEnv -->|empty / unset| CheckConfig{Config file exists?<br>~/.config/slack-mcp/config.json}

    CheckConfig -->|has workspaces| UseConfig[Create provider<br>from config file]
    CheckConfig -->|empty / missing| NoAuth[Return nil provider<br>+ error]

    UseEnv --> Done([Provider ready])
    UseConfig --> Done
    NoAuth --> Setup([Server starts without auth<br>tools prompt for auth-setup])
```

## Auth-Setup State Machine

```mermaid
stateDiagram-v2
    [*] --> detect: action=next

    detect --> select_browser: browsers found
    detect --> manual_setup: no browsers

    select_browser --> select_profile: browser selected
    select_profile --> check_lock: profile selected

    check_lock --> extracting: profile unlocked
    check_lock --> locked: profile in use
    locked --> select_profile: action=retry

    extracting --> validating: tokens extracted
    extracting --> manual_setup: extraction failed

    validating --> complete: auth.test OK
    validating --> detect: tokens invalid

    manual_setup --> validating: tokens submitted via localhost callback

    complete --> hot_load: save to config.json
    hot_load --> [*]: provider created, cache boots
```

## Token Sources and Persistence

```mermaid
flowchart LR
    subgraph Input["Token Sources"]
        MCPB[mcpb UI fields]
        Manual[Manual export<br>SLACK_MCP_XOXC_TOKEN]
        DotEnv[.env file]
        Browser[Browser extraction<br>via auth-setup]
    end

    subgraph Resolution["loadProvider()"]
        EnvCheck{env vars<br>non-empty?}
    end

    subgraph Storage["Shared Config"]
        ConfigJSON[~/.config/slack-mcp/config.json]
    end

    subgraph Runtime["Server"]
        Provider[ApiProvider]
        Cache[Channel cache<br>~/.local/share/slack-mcp/]
    end

    MCPB --> EnvCheck
    Manual --> EnvCheck
    DotEnv --> EnvCheck

    EnvCheck -->|yes| Provider
    EnvCheck -->|no| ConfigJSON
    ConfigJSON --> Provider

    Browser --> ConfigJSON

    Provider --> Cache
```

## Cross-Host Sharing

All MCP hosts share the same config file. Setup once, use everywhere.

```mermaid
flowchart TD
    subgraph Hosts["MCP Hosts"]
        CC[Claude Code]
        CD[Claude Desktop]
        Other[Other MCP clients]
    end

    subgraph Binary["slack-mcp binary"]
        LP[loadProvider]
    end

    subgraph Shared["Shared State"]
        Config[~/.config/slack-mcp/config.json<br>tokens + workspace metadata]
        Cache[~/.local/share/slack-mcp/<br>channel cache]
    end

    CC --> Binary
    CD --> Binary
    Other --> Binary

    LP --> Config
    LP --> Cache
```

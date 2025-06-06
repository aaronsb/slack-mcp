# Slack MCP Server - Semantic Intent Edition

A semantic, intent-based Model Context Protocol (MCP) server for Slack that provides intelligent, user-focused tools for interacting with Slack workspaces.

## What's Different?

This fork reimagines the Slack MCP server with a **semantic intent** approach inspired by [wordpress-author-mcp](https://github.com/aaronsb/wordpress-author):

### Before (API-Centric):
```
- channels_list: Raw channel listing
- conversations_history: Raw message fetching
```

### After (User Intent-Based):
```
- catch-up-on-channel: "What did I miss in #engineering?"
- check-my-mentions: "What needs my attention?"
- find-discussion: "Where was that API redesign conversation?"
```

## Features

### Current Semantic Tools

1. **catch-up-on-channel**
   - Smart summarization of channel activity
   - Highlights important messages, mentions, and decisions
   - Time-aware filtering (e.g., "last 6 hours", "since Monday")
   - Focus modes: all, mentions, threads, important

2. **check-my-mentions**
   - Groups mentions by urgency (urgent, questions, FYI)
   - Shows thread context
   - Filters resolved vs unresolved
   - Provides actionable next steps

3. **find-discussion**
   - Natural language search across channels
   - Finds threads, decisions, and related discussions
   - Returns full thread context with key points
   - Suggests related discussions

### Planned Features
- browse-team-activity
- search-shared-files
- get-channel-insights
- find-decisions
- review-action-items

## Architecture

### Personality-Based Design

The server supports different "personalities" (roles) configured via environment:

```bash
SLACK_MCP_PERSONALITY=slack-user  # Read-only user (default)
# Future: slack-contributor, slack-admin
```

This allows running multiple instances with different capabilities:
- **slack-user**: Read-only consumption and discovery
- **slack-admin** (future): Workspace analytics and management

### Semantic Benefits

1. **User-Friendly**: Tools map to how people actually think about Slack
2. **Efficient**: One semantic operation vs multiple API calls
3. **Contextual**: Every result includes guidance and next steps
4. **Safe**: Read-only operations prevent accidental changes
5. **Smart**: Built-in filtering, relevance ranking, and summarization

## Quick Start

### 1. Environment Setup

Create a `.env` file:

```bash
# Slack Authentication
SLACK_MCP_XOXC_TOKEN=xoxc-your-token-here
SLACK_MCP_XOXD_TOKEN=xoxd-your-token-here

# Personality Configuration
SLACK_MCP_PERSONALITY=slack-user
```

### 2. Build

```bash
make build
```

### 3. Configure Claude Desktop

Add to your Claude Desktop config:

```json
{
  "mcpServers": {
    "slack": {
      "command": "/path/to/slack-mcp",
      "args": ["--transport", "stdio"],
      "env": {
        "SLACK_MCP_XOXC_TOKEN": "xoxc-your-token",
        "SLACK_MCP_XOXD_TOKEN": "xoxd-your-token",
        "SLACK_MCP_PERSONALITY": "slack-user"
      }
    }
  }
}
```

Or use the binary with `.env` file support.

## Example Usage

### Natural Language Workflows

```
User: "What happened in engineering today?"
→ AI uses catch-up-on-channel with channel="engineering" since="today"
→ Returns key discussions, decisions, and your mentions

User: "Find that conversation about the database migration"
→ AI uses find-discussion with query="database migration"
→ Returns relevant threads with participants and outcomes

User: "Do I have any urgent messages?"
→ AI uses check-my-mentions with urgencyFilter="urgent"
→ Returns urgent mentions with context and suggested actions
```

### Rich Responses

Each semantic operation returns:
- **Structured data** for the specific use case
- **Summary statistics** for quick overview
- **Next actions** to guide workflow
- **Contextual guidance** based on findings

Example response:
```json
{
  "success": true,
  "data": { /* structured results */ },
  "message": "Found 2 urgent mentions requiring attention",
  "nextActions": [
    "Use 'find-discussion' with threadId='1234' for full context",
    "Use 'catch-up-on-channel' to see related activity"
  ],
  "guidance": "🚨 You have a blocking PR review request",
  "resultCount": 2
}
```

## Credits

Based on the excellent [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server).

Semantic design inspired by [wordpress-author-mcp](https://github.com/aaronsb/wordpress-author).

## License

Same as original project.
# Semantic Tool Architecture

## Overview

This document describes the semantic tool architecture of the Slack MCP Server - an "AI-first Slack client" that presents intent-based tools rather than raw API operations.

## Core Concept

The semantic layer transforms Slack from a "message database" into an "intelligent communication assistant." Instead of exposing technical details like channel IDs, API tokens, or pagination cursors, it provides natural, intent-based interfaces that AI assistants can use effectively.

## Current Tool Set

### 1. check-unreads
**Purpose**: "What do I need to pay attention to?"

**Implementation**:
- Uses internal `/api/client.counts` endpoint for accurate counts
- Falls back to standard API if internal endpoints fail
- Resolves channel IDs to human-readable names

**Output Structure**:
```json
{
  "stats": {
    "totalDMs": 0,
    "totalMentions": 0,
    "totalChannels": 0
  },
  "unreads": {
    "dms": [{"from": "Person Name", "unreadCount": 0}],
    "mentions": [{"channel": "channel-name", "mentionCount": 0}],
    "channels": [{"channel": "channel-name"}]
  }
}
```

### 2. catch-up-on-channel
**Purpose**: "Show me what happened in [channel]"

**Implementation**:
- Accepts channel names or IDs
- Time-based filtering (e.g., "7d", "24h")
- Smart importance scoring for messages

**Features**:
- Identifies important items (threads, reactions, mentions)
- Provides pagination for large result sets
- Includes next actions and guidance

### 3. check-my-mentions
**Purpose**: "Where am I mentioned?"

**Implementation**:
- Scans accessible channels for user mentions
- Categorizes by urgency
- Checks if mentions have been responded to

**Limitations**:
- Currently scans subset of channels due to API constraints
- Being enhanced with internal endpoints

### 4. find-discussion
**Purpose**: "Find conversations about [topic]"

**Status**: In development
- Will use internal search.modules endpoint
- Support natural language queries
- Return contextual thread information

## Architecture Patterns

### Feature Registry Pattern

Each tool is defined as a Feature:

```go
type Feature struct {
    Name        string
    Description string
    Schema      interface{}  // JSON Schema for parameters
    Handler     func(context.Context, map[string]interface{}) (*FeatureResult, error)
}
```

### Semantic Result Pattern

All tools return structured, semantic results:

```go
type FeatureResult struct {
    Success     bool                    // Operation status
    Data        map[string]interface{}  // Structured data
    Message     string                  // Human-readable summary
    Guidance    string                  // AI-friendly guidance
    NextActions []string                // Suggested follow-ups
    Pagination  *Pagination             // For large datasets
    ResultCount int                     // Number of items
}
```

### Provider Pattern

The `ApiProvider` centralizes all Slack access:
- Manages standard Slack client
- Provides internal client for browser endpoints
- Handles user and channel caching
- Resolves names to IDs transparently

## Key Implementation Details

### 1. Internal Endpoints Integration

The `InternalClient` accesses Slack's internal endpoints:
- `/api/client.counts` - Accurate unread counts
- `/api/client.boot` - Initial state data
- `/api/search.modules` - Advanced search

These endpoints provide data not available through standard APIs with xoxc/xoxd tokens.

### 2. Channel Name Resolution

Channel IDs are hidden from the AI layer:
- Automatic caching of channel information
- Bidirectional name/ID mapping
- Human-readable names in all outputs

### 3. Smart Message Filtering

Messages are analyzed for importance:
- Thread activity (reply counts)
- Reactions (popularity indicators)
- Mention patterns
- Decision keywords
- Urgency indicators

### 4. Graceful Degradation

All features implement fallback strategies:
- Internal endpoints → Standard API → Cached data
- Rich data → Basic data → Error with guidance

## Personality System

Different personalities can be configured via `SLACK_MCP_PERSONALITY`:
- `slack-user`: General Slack user
- `team-lead`: Focus on team coordination
- `sales-rep`: Prioritize customer channels
- `engineer`: Technical discussions priority

## Success Patterns

### What Works Well

1. **Clean Abstractions**: No technical details exposed to AI
2. **Human-Readable Output**: Names instead of IDs everywhere
3. **Accurate Data**: Internal endpoints provide real counts
4. **Rich Context**: Guidance and next actions included
5. **Flexible Time Ranges**: Natural language time parsing

### Current Limitations

1. **Search Functionality**: Still using mock data
2. **Channel Coverage**: Mention scanning limited by API
3. **Write Operations**: No message sending yet
4. **Thread Navigation**: Could be more sophisticated

## Future Enhancements

### Additional Semantic Tools

1. **summarize-threads**: "What decisions were made?"
2. **find-files**: "Show me documents about X"
3. **track-action-items**: "What am I supposed to do?"
4. **analyze-sentiment**: "What's the team mood?"

### Write Operations

1. **mark-as-read**: Clear notifications intelligently
2. **quick-reply**: Respond to mentions
3. **schedule-reminder**: Set follow-ups
4. **draft-message**: Prepare responses

### Intelligence Layer

1. **Pattern Learning**: Understand user habits
2. **Priority Scoring**: Smarter importance detection
3. **Proactive Alerts**: Notify about unusual activity
4. **Context Preservation**: Remember previous queries

## Example Interactions

### Before (Technical)
```
Channel C123ABC has unread_count: 15
User U456DEF sent message at timestamp 1234567890.123
```

### After (Semantic)
```
You have 15 mentions in the event planning channel that need attention.
Sarah Johnson asked about the Q2 roadmap 2 hours ago.
```

## Conclusion

The semantic tool architecture transforms Slack interaction from API calls to natural conversations. By hiding complexity and presenting intent-based interfaces, it enables AI assistants to help users manage their communications effectively without technical knowledge.
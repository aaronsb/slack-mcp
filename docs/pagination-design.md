# Pagination Design for Slack MCP

## Overview

We've implemented cursor-based pagination across all semantic tools using Slack's native pagination system.

## Implementation

### 1. Pagination Structure

Each `FeatureResult` now includes optional pagination info:

```go
type Pagination struct {
    Cursor     string `json:"cursor,omitempty"`     // Current cursor
    NextCursor string `json:"nextCursor,omitempty"` // Cursor for next page  
    HasMore    bool   `json:"hasMore"`              // More results available
    PageSize   int    `json:"pageSize"`             // Items in this page
    TotalCount int    `json:"totalCount,omitempty"` // Total items (if known)
}
```

### 2. Tool Parameters

All tools that return large data sets now support:
- `cursor`: Pagination cursor from previous request
- `limit`: Maximum items per page (with sensible defaults and caps)

### 3. Usage Pattern

```json
// First request
{
  "tool": "catch-up-on-channel",
  "arguments": {
    "channel": "engineering",
    "since": "1d",
    "limit": 20
  }
}

// Response includes pagination
{
  "success": true,
  "data": { ... },
  "pagination": {
    "hasMore": true,
    "nextCursor": "dGVhbTpDMUg5RENTVEc=",
    "pageSize": 20
  }
}

// Next page request
{
  "tool": "catch-up-on-channel", 
  "arguments": {
    "channel": "engineering",
    "since": "1d",
    "cursor": "dGVhbTpDMUg5RENTVEc=",
    "limit": 20
  }
}
```

## Benefits

1. **Efficient**: Only fetches what's needed
2. **Scalable**: Handles large channels/workspaces
3. **Native**: Uses Slack's built-in cursor system
4. **Consistent**: Same pattern across all tools
5. **User-friendly**: Clear next actions for pagination

## Tool Limits

- `catch-up-on-channel`: 20 default, 50 max
- `check-my-mentions`: 20 default, 50 max  
- `find-discussion`: 10 default, 25 max (search is expensive)

## Future Improvements

1. Add streaming support for real-time updates
2. Implement result caching for common queries
3. Add parallel fetching for multi-channel operations
4. Support for bookmark-based resume (save position)
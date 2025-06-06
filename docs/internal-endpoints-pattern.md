# Internal Slack Endpoints Pattern

## Overview

This document describes the pattern for accessing Slack's internal/undocumented endpoints to work around limitations of xoxc/xoxd tokens, particularly for getting accurate unread counts.

## Implementation

### 1. Internal Client (`pkg/provider/internal_client.go`)

The `InternalClient` provides a clean interface for calling internal Slack endpoints:

```go
type InternalClient struct {
    httpClient *http.Client
    xoxcToken  string
    xoxdToken  string
    baseURL    string
}
```

Key features:
- Mimics browser headers (User-Agent, Origin, Referer)
- Handles both GET and POST requests
- Uses proper authentication with Bearer token and cookie
- Provides typed responses for each endpoint

### 2. Implemented Endpoints

#### `/api/client.counts`
- Returns unread counts for all channels, DMs, and threads
- Includes mention counts and badges
- Much more reliable than the standard API's UnreadCount

#### `/api/client.boot`
- Returns initial client state
- Includes channel list with unread info
- Heavier but comprehensive

#### `/api/search.modules`
- Internal search endpoint
- More flexible than the standard search API

### 3. Integration Pattern

The internal client is integrated into the existing provider:

```go
// In ApiProvider
type ApiProvider struct {
    slackClient    *slack.Client
    internalClient *InternalClient
    // ...
}

// Access method
func (ap *ApiProvider) ProvideInternalClient() *InternalClient {
    return ap.internalClient
}
```

### 4. Feature Implementation

Features can use internal endpoints with graceful fallback:

```go
// Get internal client
internalClient := apiProvider.ProvideInternalClient()
if internalClient == nil {
    // Fallback to standard API
    return checkUnreadsHandler(ctx, params)
}

// Use internal endpoint
counts, err := internalClient.GetClientCounts(ctx)
if err != nil {
    // Fallback on error
    log.Printf("Failed to get client counts: %v", err)
    return checkUnreadsHandler(ctx, params)
}
```

## Benefits

1. **Accurate Unread Counts**: The internal endpoints provide real unread counts that match what users see in the Slack UI
2. **Mention Counts**: Separate mention counts for channels, DMs, and threads
3. **Thread Unreads**: Proper thread unread tracking
4. **Channel Badges**: Summary counts that match the UI badges

## Testing

Enable debug mode to test internal endpoints:

```bash
# In .env
SLACK_MCP_DEBUG=true
```

Then use the `debug-internal` tool to verify endpoints are working.

## Future Patterns

Other patterns that could be implemented:

1. **WebSocket Connection**: For real-time updates
2. **Batch Operations**: Using internal batch endpoints
3. **Rich Presence**: Using internal presence endpoints
4. **File Operations**: Using internal file endpoints

## Security Considerations

- Internal endpoints may change without notice
- Always implement fallbacks to standard API
- Don't expose internal endpoint details in user-facing messages
- Rate limiting may be different from documented API limits
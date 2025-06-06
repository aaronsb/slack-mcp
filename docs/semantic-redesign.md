# Slack MCP Semantic Redesign

## Current State: API-Centric
- `channels_list` - Raw channel listing
- `conversations_history` - Raw message fetching

## Proposed: User Intent-Based Tools

### Core Semantic Operations for Slack User Role

#### 1. `catch-up-on-channel`
**Intent**: "What did I miss in #engineering while I was away?"
- Smart time-based filtering (since last read)
- Summarizes key discussions
- Highlights mentions and important messages
- Returns actionable items

#### 2. `find-discussion`
**Intent**: "Find that conversation about the API redesign"
- Natural language search across channels
- Context-aware results (not just keyword matching)
- Shows thread context automatically
- Suggests related discussions

#### 3. `check-my-mentions`
**Intent**: "What do I need to respond to?"
- Unread mentions across all channels
- Grouped by urgency/channel
- Shows thread context
- Filters out resolved items

#### 4. `browse-team-activity`
**Intent**: "What's the team working on?"
- Activity summary across relevant channels
- Key decisions and announcements
- Project updates
- New initiatives

#### 5. `search-shared-files`
**Intent**: "Find that design doc someone shared last week"
- File search with context
- Shows who shared and when
- Related discussion snippets
- Direct links to files

#### 6. `get-channel-insights`
**Intent**: "How active is #product-feedback?"
- Channel health metrics
- Key contributors
- Recent topics
- Engagement patterns

### Implementation Approach

1. **Single User Persona**: "slack-user"
   - Read-only operations
   - Focused on consumption and discovery
   - No posting/editing (security-conscious)

2. **Smart Defaults**
   - Automatically filter archived channels
   - Respect user's channel membership
   - Time-aware queries (business hours, timezones)
   - Relevance ranking

3. **Rich Context**
   - Every result includes suggested next actions
   - Natural language guidance
   - Workflow hints ("To see the full thread, use...")

4. **Efficient Data Handling**
   - Built-in pagination
   - Smart limits based on use case
   - Response formatting for readability
   - CSV → Structured JSON responses

### Example Workflows

```
User: "What happened in the engineering channels today?"
→ AI uses `browse-team-activity` with intent="engineering" timeframe="today"
→ Returns summary of key discussions, decisions, and mentions

User: "Find messages about the database migration"
→ AI uses `find-discussion` with query="database migration"
→ Returns relevant threads with context, participants, and outcomes

User: "Check if anyone needs me"
→ AI uses `check-my-mentions` 
→ Returns unread mentions grouped by urgency with thread context
```

### Benefits of Semantic Approach

1. **User-Friendly**: Tools map to how people think about Slack
2. **Efficient**: One semantic operation vs multiple API calls
3. **Contextual**: Results include guidance and next steps
4. **Safe**: Read-only operations prevent accidental changes
5. **Smart**: Built-in filtering and relevance ranking
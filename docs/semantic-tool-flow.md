# Semantic Tool Flow Design

## Overview
Each tool should guide users to natural next steps based on context and results. This creates a conversational flow where the AI can help users navigate Slack efficiently.

## Tool Flow Map

### 1. Morning Routine Flow
```
check-unreads → 
  ├─ [if mentions] → check-my-mentions → catch-up-on-channel
  ├─ [if DMs] → catch-up-on-channel (specific DM)
  └─ [if channels] → list-channels → catch-up-on-channel
```

### 2. Search & Discovery Flow
```
find-discussion →
  ├─ [found threads] → find-discussion (with threadId)
  ├─ [found in channel] → catch-up-on-channel
  └─ [nothing found] → list-channels → catch-up-on-channel
```

### 3. Cleanup Flow
```
mark-as-read →
  ├─ [partial cleanup] → check-unreads
  ├─ [all clear] → catch-up-on-channel (important channels)
  └─ [mentions remain] → check-my-mentions
```

### 4. Channel Navigation Flow
```
list-channels →
  ├─ [specific channel] → catch-up-on-channel
  ├─ [unread channels] → check-unreads
  └─ [search needed] → find-discussion
```

## Semantic Next Actions by Tool

### catch-up-on-channel
**Context-based suggestions:**
- If thread found → `find-discussion threadId='...'`
- If mentions → `check-my-mentions`
- If pagination → `catch-up-on-channel cursor='...'`
- If important decision → `mark-as-read channel='...'`
- General → `find-discussion query='...'`

### check-unreads
**Priority-based suggestions:**
- If urgent mentions → `check-my-mentions`
- If DMs → `catch-up-on-channel channel='[dm-name]'`
- If many unreads → `mark-as-read target='all-channels' filter='no-mentions'`
- If specific channel → `catch-up-on-channel channel='...'`

### check-my-mentions
**Action-based suggestions:**
- If thread mention → `find-discussion threadId='...'`
- If channel mention → `catch-up-on-channel channel='...'`
- If resolved → `mark-as-read channel='...'`
- If need context → `find-discussion query='[topic]'`

### find-discussion
**Result-based suggestions:**
- If thread found → `find-discussion threadId='...'` (full context)
- If in channel → `catch-up-on-channel channel='...'`
- If decision found → `check-my-mentions` (see if action needed)
- If nothing → `list-channels search='...'`

### mark-as-read
**Completion-based suggestions:**
- After marking → `check-unreads` (verify status)
- If selective → `check-my-mentions` (see what remains)
- If all clear → `catch-up-on-channel channel='general'`

### list-channels
**Discovery-based suggestions:**
- If found target → `catch-up-on-channel channel='...'`
- If unreads shown → `check-unreads`
- If search needed → `find-discussion query='...'`
- If refresh needed → `list-channels forceRefresh=true`

## Personality-Driven Flows

### slack-user (Read-only)
**Morning flow:**
1. `check-unreads` - "What do I need to see?"
2. `check-my-mentions` - "Am I needed anywhere?"
3. `catch-up-on-channel` - "What's happening in [channel]?"
4. `mark-as-read` - "Clear my inbox"

**Research flow:**
1. `find-discussion` - "What was decided about X?"
2. `list-channels` - "Where do we discuss Y?"
3. `catch-up-on-channel` - "Show me the context"

### slack-contributor (Future)
Would add:
- After reading → `reply-to-thread`
- After catching up → `post-message`
- After finding → `react-to-message`

## Implementation Pattern

Each tool should:
1. Analyze the result context
2. Consider the user's workflow stage
3. Suggest 2-4 most relevant next actions
4. Include specific parameters when helpful
5. Guide toward task completion

Example:
```go
// In check-unreads handler
if urgentMentions > 0 {
    result.NextActions = append(result.NextActions, 
        "Urgent: check-my-mentions")
}
if topUnreadDM != "" {
    result.NextActions = append(result.NextActions,
        fmt.Sprintf("Read DM: catch-up-on-channel channel='%s'", topUnreadDM))
}
if totalUnreads > 20 {
    result.NextActions = append(result.NextActions,
        "Clear inbox: mark-as-read target='everything' filter='no-mentions'")
}
```

## Guidance Principles

1. **Be specific**: Include channel names, thread IDs when known
2. **Prioritize**: Put most important/urgent actions first  
3. **Limit choices**: 2-4 suggestions maximum
4. **Complete loops**: Guide users to resolution
5. **Context aware**: Consider time of day, unread counts, etc.

## Future Enhancement: Workflow Templates

Could add predefined workflows:
- "morning-review": check-unreads → check-my-mentions → mark-as-read
- "research-topic": find-discussion → catch-up-on-channel → list-channels
- "inbox-zero": check-unreads → mark-as-read target='everything'

These could be triggered by natural language: "Help me do my morning Slack review"
# For the Sloppy Slack User

## The Problem
You're "sloppy with Slack" - messages pile up, you miss important stuff, and catching up feels overwhelming.

## The Solution
Semantic tools that understand how you actually use (or don't use) Slack:

### Instead of relying on Slack's unread markers, we should:

1. **Time-based catch-up**: "What happened in the last day/week?"
2. **Priority scanning**: Look for urgent keywords, questions directed at you
3. **Smart summarization**: Group related messages, highlight decisions
4. **Guided workflows**: Tell you what to respond to first

### Better approach for sloppy users:

```
User: "What did I miss?"
→ Scans recent activity across all channels
→ Finds mentions, questions, decisions
→ Prioritizes by urgency
→ Gives you a manageable list

User: "What needs my response?"
→ Looks for unanswered questions
→ Checks threads you were mentioned in
→ Filters out FYI/announcements
→ Shows only actionable items
```

## Current Limitation
The Slack API's unread counts don't work reliably with browser tokens (xoxc/xoxd). We need to implement our own "unread" logic based on:
- Last activity timestamps
- Message patterns (questions, mentions)
- Thread participation
- Channel importance

This is actually better for sloppy users - instead of 10,000 unread messages, you get "5 things that actually need your attention."
# Pull Request Instructions

Since the repositories have diverged significantly, please create the pull request manually:

1. Visit: https://github.com/aaronsb/slack-mcp/pull/new/semantic-ooda-enhancement

2. Click "compare across forks" 

3. Set:
   - base repository: korotovsky/slack-mcp-server
   - base: master
   - head repository: aaronsb/slack-mcp
   - compare: semantic-ooda-enhancement

4. Use this PR description:

---

## Overview

This PR introduces a significant enhancement to the Slack MCP Server by implementing an OODA (Observe-Orient-Decide-Act) loop pattern for intelligent workspace interaction. We understand this represents a substantial departure from the original vision and may not align with your project goals. We're offering this contribution with full understanding that you may choose not to merge it.

## Key Changes

### 1. Semantic Tool Architecture
- Reorganized tools into OODA loop phases (Observe, Orient, Decide, Act)
- Added semantic flow between tools with pre-populated next actions
- Implemented "self-deprecating" tool design that guides users to optimal workflows

### 2. Enhanced Message Reading
- Count-based windowing for check-unreads:
  - 1-3 messages: Full content + auto-mark as read
  - 4-15 messages: Full with urgency analysis + auto-mark
  - 16-50 messages: Summary only (preserve unread)
  - 50+ messages: High-level overview
- Correlates reading depth with actual content consumption

### 3. New Tools
- **decide-next-action**: Basic reflection tool for workflow guidance
- **pace-conversation**: Analyzes conversation timing without delays
- **write-message**: Send messages with smart channel/user resolution

### 4. Enhanced Documentation
- Comprehensive OODA loop workflow documentation
- Mermaid flow diagrams showing tool interactions
- Setup instructions for various MCP clients
- Troubleshooting guide

## Important Considerations

### Potential Compatibility Issues
1. **Local Caching**: The enhanced caching system may not work well with SSE or remote MCP implementations
2. **Import Paths**: Currently uses forked import paths that would need updating
3. **Breaking Changes**: The semantic architecture may break existing workflows

### Architecture Changes
- Two-phase channel caching (member channels first, then all)
- Channel ID security (IDs hidden from AI exposure)
- Non-blocking startup with progressive loading
- Persistent user cache (`.users_cache.json`)

## Testing

All changes have been tested locally with Claude Desktop. The OODA loop provides intelligent conversation flow while maintaining backward compatibility with existing tool usage.

## Next Steps

We completely understand if this doesn't align with your vision for the project. If you're interested in any specific features but not the full implementation, we'd be happy to extract those into smaller, more focused PRs.

Thank you for creating the original slack-mcp-server - it provided an excellent foundation to build upon!

---

**Note**: This is a significant architectural change. We recommend reviewing the [OODA workflow documentation](docs/ooda-loop-workflow.md) to understand the full scope of changes.
# Credits

## Original Author

This project is based on the excellent work by Dmitry Korotovsky ([korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server)).

## Semantic Redesign & OODA Loop Implementation

The semantic intent-based approach and OODA (Observe-Orient-Decide-Act) loop pattern was inspired by design patterns in [wordpress-author-mcp](https://github.com/aaronsb/wordpress-author).

## Contributors

- Original implementation: Dmitry Korotovsky (korotovsky)
- Semantic redesign & OODA loop: Aaron Bockelie (aaronsb)
- OODA loop implementation assistance: Claude (Anthropic)

## Key Contributions

### Original slack-mcp-server (korotovsky)
- Core MCP server implementation
- Slack API integration
- Basic tool set (channels_list, conversations_history)
- SSE and stdio transport support

### Semantic Enhancement (aaronsb & Claude)
- OODA loop architectural pattern
- Semantic tool reorganization
- Enhanced message reading with count-based windowing
- New tools: decide-next-action, pace-conversation, write-message
- Two-phase channel caching
- Comprehensive documentation and flow diagrams

## License

This project maintains the original MIT license terms.
# Security

## What this server holds

slack-mcp authenticates with **session tokens** (`xoxc` + `xoxd`) rather than
OAuth. Those tokens are your Slack session. Anyone holding both can read and
write everything your account can, in every workspace channel you belong to,
with no scope limits and no audit trail distinguishing them from you.

Treat them as you would your password.

Where they live:

| Path | Contents | Mode |
|---|---|---|
| `$XDG_CONFIG_HOME/slack-mcp/config.json` | `xoxc` and `xoxd` per workspace | `0600` |
| `$XDG_DATA_HOME/slack-mcp/watermarks/` | which messages an agent has seen | `0600` |
| `$XDG_DATA_HOME/slack-mcp/*.json` | channel and user caches | `0700` dir |

Tokens are never transmitted anywhere except `slack.com`. The setup flow runs a
web server bound to localhost so they never traverse a network.

## Reporting a vulnerability

Open a [security advisory](https://github.com/aaronsb/slack-mcp/security/advisories/new).
Please don't open a public issue for anything that would expose tokens or allow
reading a workspace without authorisation.

Include what you did, what happened, and what you expected. A proof of concept
helps but is not required to report.

## Scope

In scope:

- Anything that leaks `xoxc`/`xoxd` off the machine
- Anything that lets a prompt or a message cause writes the operator did not ask for
- Path traversal or injection through channel names, user names, or message text
- File permissions on anything under the paths above

Out of scope:

- Slack's own internal endpoints changing or being rate-limited
- The inherent breadth of session-token access, which is the documented design
  (see [ADR-001](docs/architecture/001-slack-mcp-fusion.md))
- Anything requiring an attacker who already has read access to your home
  directory — at that point they have the tokens directly

## Notes for operators

- **Reads are stealthy by default.** Only `mark-read` moves your Slack read
  marker. `poll`, `read` and `search` leave unread state untouched, so an agent
  looking at your workspace does not clear anyone's badge or signal that you saw
  their message.
- **`ack` is not `mark-read`.** Acknowledging records what an agent has seen, in
  local state, invisible to Slack and to your colleagues.
- **Revoking access** means signing out of the Slack session the tokens came
  from. Deleting `config.json` stops this server using them; it does not
  invalidate them.

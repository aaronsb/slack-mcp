# ADR-014: Account Identity and Safety Posture

## Status

Accepted

Sets the notice wording and the escalation that ADR-013 leaves to it.
Adds two environment settings beside `SLACK_MCP_DEPLOYMENT` and follows
its rule: declared in the MCP client config, never detected. Lands with
ADR-013.

## Context

Everything this server posts is attributed to the account whose session
tokens it holds. Usually that is a person: the tokens come from their
browser login, and the agent writes from their account while they work
beside it. Sometimes it is an account made for the agent, run
unattended. The two call for different voices. On a person's account
the agent writes on their behalf, and committing them to something they
never said is the failure. On its own account the agent is the speaker.

The same split runs through ADR-013's escalation. An unattended agent
has no one watching, so the first match should close the conversation
it came from. An attended assistant on the operator's own account has
someone at hand who can judge a false positive, and quarantining a
working conversation over one `TOKEN=` line costs more than it saves.

Neither fact is visible to the server. The token type is the same for
both kinds of account, and whether someone is watching is a property of
the deployment.

## Decision

### Two declared settings

| Setting | Values | Default |
|---|---|---|
| `SLACK_MCP_IDENTITY` | `human`, `agent` | `human` |
| `SLACK_MCP_SAFETY` | `strict`, `soft` | `strict` |

Both are read once at startup from the environment the MCP client
config sets. An unknown value refuses to start, naming the setting and
its allowed values, as `SLACK_MCP_DEPLOYMENT` does. Both are echoed in
the startup log and in the server instructions.

Identity is an enum rather than a boolean so a config line reads
unambiguously. Its default is `human` because session tokens normally
come from a person's login, and treating a person's account as the
agent's own is the costlier mistake.

### Identity: whose account this is

`human`: the account belongs to a person, `{name}` below, its display
name from the auth check.

- The `say` description and the server instructions say: "You are
  writing from {name}'s account. Speak as Claude, for {name}, in a way
  that suits the conversation. Don't commit {name} to substance they
  haven't given you."
- Write results say the message was "sent from your account".
- ADR-013's notice reads: "[automated] A message from this account was
  blocked by a safety filter."

`agent`: the account is the agent's own.

- The `say` description and the server instructions say: "This account
  is yours. Speak as yourself, for the people you're helping, in a way
  that suits the conversation."
- Write results say the message was "sent as {account name}".
- ADR-013's notice reads: "I can't share that."

In both, `@me` and self mean the account. The operator, who runs the
CLI, approves requests, and clears quarantines, is the person at the
terminal. Identity changes no read behavior (reads stay stealth) and
not who approves.

### Safety posture: how hard a block escalates

Both postures share one floor: the scanner blocks every match, ADR-012's
bare-name rule holds, and unscannable content is refused.

| | `strict` | `soft` |
|---|---|---|
| First block | destination quarantined, notice posted | warning in the tool result, strike counted, no quarantine |
| Second block | destination quarantined, notice posted; strike lock | destination quarantined, notice posted |
| Strike lock at | 2 strikes | 3 strikes |
| Gated: external destination | yes | yes |
| Gated: cross-conversation file move | yes | no; a warning in the result |
| Gated: lifting a quarantine or the lock | yes | yes |

`strict` suits an unattended agent or a dedicated account. `soft` suits
an attended assistant on the operator's own account.

## Consequences

### Positive

- The agent's voice matches whose name is on the message, and the
  notice reads correctly from either kind of account.
- An attended operator can run with fewer interruptions without
  lowering what the scanner blocks.

### Negative

- Two more settings to get right. A dedicated agent account left on
  the default speaks as a delegate of a person who does not exist.
- `soft` lets a first match through without closing the conversation,
  and lets a downloaded file move between conversations with a warning
  only.
- The `human` wording names Claude. Another model behind the client
  reads an instruction addressed to a different assistant.

### Risks

- An operator who sets `soft` for an unattended deployment gets the
  attended posture with no one attending. The startup log and the
  server instructions show the posture, and the default is `strict`.
- `{name}` comes from Slack. A display name the account holder set is
  text in the server instructions; it is quoted, not interpreted.

### Reversibility

Both are configuration. Removing a setting fixes the server at its
default, which for both is the more cautious value.

## Alternatives Considered

- **Detecting the account kind**: from the profile, a bot flag, or the
  token. Session tokens look the same for every account, and a profile
  field is something anyone can edit. Declared, like the deployment.
- **A boolean `SLACK_MCP_AGENT_ACCOUNT=true`**: reads ambiguously in a
  config file ("true" for whom?) and leaves no room for a third kind.
- **One posture for everyone**: `strict` everywhere makes an attended
  assistant quarantine its operator's working threads over false
  positives; `soft` everywhere leaves an unattended agent two tries per
  conversation.
- **A softer floor in `soft`**: letting the scanner warn instead of
  block. The floor is what ADR-013 promises regardless of
  configuration.

## Related

- ADR-013: the scanner, quarantine, strike lock, and approval gate whose
  escalation and notice this sets.
- ADR-009: `say` and its description, which carry the identity wording.
- `SLACK_MCP_DEPLOYMENT` in `pkg/lifecycle`: the declared-not-detected
  rule and the refusal on an unknown value.

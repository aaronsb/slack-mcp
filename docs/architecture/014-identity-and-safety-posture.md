# ADR-014: Account Identity and Safety Posture

## Status

Accepted

Sets the notice wording and the escalation that ADR-013 leaves to it.
Adds two environment settings beside `SLACK_MCP_DEPLOYMENT` and follows
its rule: declared in the MCP client config, never detected. Closes the
`.env` route to every security-relevant setting. Lands with ADR-013.

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

The server also loads a `.env` file from its working directory at
startup, filling any variable the environment left unset. An agent with
file tools can write that file, and the next restart would read it.

## Decision

### Two declared settings

| Setting | Values | Default |
|---|---|---|
| `SLACK_MCP_IDENTITY` | `human`, `agent` | `human` |
| `SLACK_MCP_SAFETY` | `strict`, `soft` | `strict` |

Both are read once at startup. An unknown value refuses to start,
naming the setting and its allowed values, as `SLACK_MCP_DEPLOYMENT`
does. Both are echoed in the startup log and in the server instructions.

Identity is an enum rather than a boolean so a config line reads
unambiguously. Its default is `human` because session tokens normally
come from a person's login, and treating a person's account as the
agent's own is the costlier mistake.

### Security settings come only from the client's environment

`SLACK_MCP_SAFETY`, `SLACK_MCP_IDENTITY`, `SLACK_MCP_EXCHANGE_DIR`,
`SLACK_MCP_DEPLOYMENT`, `SLACK_MCP_IDLE_TIMEOUT`,
`SLACK_MCP_SSE_API_KEY`, and `SLACK_MCP_HOST` are taken only from the
process environment the MCP client sets. The server snapshots those keys
before it loads `.env`, and refuses to start if `.env` supplies any of
them, naming the key. Other variables may still come from `.env`.

### Identity: whose account this is

`{name}` is the account's handle as `auth.test` reports it at startup.
Tool descriptions and server instructions are built before the provider
boots and before the users cache loads, so the handle is the name
available when they are written. Before use it is stripped of control
characters, capped in length, and quoted; that
reduces the chance a crafted handle is read as an instruction, and does
not remove it. Without credentials, the wording below is used with
"this account's owner" in place of `{name}` until `auth` completes.

`human`: the account belongs to a person.

- The `say` description and the server instructions say: "You are
  writing from {name}'s account. Speak as yourself, the assistant, for
  {name}, in a way that suits the conversation. Don't commit {name} to
  substance they haven't given you."
- Write results say the message was "sent from your account".
- ADR-013's notice reads: "[automated] A message from this account was
  blocked by a safety filter."

`agent`: the account is the agent's own.

- The `say` description and the server instructions say: "This account
  is yours. Speak as yourself, for the people you're helping, in a way
  that suits the conversation."
- Write results say the message was "sent as {name}".
- ADR-013's notice reads: "I can't share that."

In both, `@me` and self mean the account. The operator, who runs the
CLI, approves requests, and clears quarantines, is the person at the
terminal. Identity changes no read behavior (reads stay stealth) and
not who approves.

### Safety posture: how hard a block escalates

Both postures share one floor: the scanner blocks every match, ADR-012's
bare-name rule holds, and unscannable content is refused. In both,
strikes count across all destinations, and ADR-013's carve-outs hold:
the self-DM is never quarantined, and an external destination never
gets a notice.

| | `strict` | `soft` |
|---|---|---|
| 1st block, anywhere | destination quarantined, notice posted | warning in the result, no quarantine |
| 2nd block | destination quarantined, notice posted; strike lock | that destination quarantined, notice posted; the first block's destination stays open |
| 3rd block | (already locked) | strike lock |
| Gated: external destination | yes | yes |
| Gated: cross-conversation file move | yes | no; a warning in the result |
| Gated: lifting a quarantine or the lock | yes | yes |

Every block counts a strike in both postures.

`strict` suits an unattended agent or a dedicated account. `soft` suits
an attended assistant on the operator's own account.

## Consequences

### Positive

- The agent's voice matches whose name is on the message, and the
  notice reads correctly from either kind of account.
- An attended operator can run with fewer interruptions without
  lowering what the scanner blocks.
- No file the agent can write changes the server's security settings.

### Negative

- Two more settings to get right. A dedicated agent account left on
  the default speaks as a delegate of a person who does not exist.
- `soft` lets a first match through without closing the conversation,
  and lets a downloaded file move between conversations with a warning
  only.
- An operator who kept these settings in `.env` must move them to the
  client config before the server will start.
- The identity wording uses the handle, not the display name a reader
  in Slack sees.

### Risks

- An operator who sets `soft` for an unattended deployment gets the
  attended posture with no one attending. The startup log and the
  server instructions show the posture, and the default is `strict`.
- The handle is text the account holder chose, placed in descriptions
  and instructions. Sanitizing it reduces what it can say; a short
  handle can still read as words.
- The `.env` guard covers the named keys. A future security-relevant
  setting has to join the list, or `.env` can set it.
- The client's own config file is outside this server's control. An
  agent that can write it can change every setting here.

### Reversibility

Both settings are configuration. Removing one fixes the server at its
default, which for both is the more cautious value. Removing the `.env`
guard reopens the route in Context.

## Alternatives Considered

- **Detecting the account kind**: from the profile, a bot flag, or the
  token. Session tokens look the same for every account, and a profile
  field is something anyone can edit. Declared, like the deployment.
- **A boolean `SLACK_MCP_AGENT_ACCOUNT=true`**: reads ambiguously in a
  config file ("true" for whom?) and leaves no room for a third kind.
- **One posture for everyone**: `strict` everywhere makes an attended
  assistant quarantine its operator's working threads over false
  positives; `soft` everywhere leaves an unattended agent's first block
  with no quarantine, and a third match before the lock.
- **A softer floor in `soft`**: letting the scanner warn instead of
  block. The floor is what ADR-013 promises regardless of
  configuration.
- **Letting `.env` override and logging it**: a log line after the fact
  does not stop the restart that applies the change.
- **Dropping `.env` support entirely**: it also carries non-security
  settings operators use; refusing the security keys closes the route
  without that break.
- **The display name from the users cache**: not loaded when
  descriptions and instructions are built.

## Related

- ADR-013: the scanner, quarantine, strike lock, and approval gate whose
  escalation and notice this sets.
- ADR-012: the exchange-directory override, one of the settings kept out
  of `.env`.
- ADR-009: `say` and its description, which carry the identity wording.
- `SLACK_MCP_DEPLOYMENT` in `pkg/lifecycle`: the declared-not-detected
  rule and the refusal on an unknown value.

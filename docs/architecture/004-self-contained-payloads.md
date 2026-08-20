# ADR-004: Self-Contained Payloads

## Status

Proposed

## Context

ADR-003 asked what a caller must know to *make* a call, and required every parameter to be
answerable from what the caller already has. It says nothing about what the caller knows
after the call returns.

A test-drive of the server as a daily-briefing agent (2026-08-17, issues #44–#47) found the
inbound half working and the outbound half failing in three ways. Each ended the same:
the agent turned to the human for a fact the server already held.

### The payload names things it does not define

Message bodies render mentions as raw internal identifiers — `<@U0AAAAAAAAA>`. The agent is
handed a pointer into a namespace the response gives it no way to dereference.

`list-users` is the documented dereference. Its description promises "matching display names,
usernames, and IDs"; `list_users.go:86` puts `id` into the data map; `formatUsers` in
`format.go:339` renders display name and username and drops the ID. The one path from a name
back to an identifier is closed at the last step.

The server knows every one of those names without a network call. `provider/api.go:35` holds
`users map[string]slack.User` keyed by ID, XDG-cached and loaded at startup (issue #5).
Resolution is a map lookup.

Nothing exposes who the authed user is. `AuthTest()` is called at `check_unreads.go:78`,
`mentions_real.go:56`, and `check_unreads_real.go:72`, used to build a self-mention pattern,
and discarded. The agent cannot answer "is this me?" about a message assigning it work.

### The payload drops content it received

Messages whose text lives in Block Kit blocks or attachment fallbacks, with no top-level
`text`, render as empty bodies. Two announcement posts in `#all-hands` — 33 and 51 reactions,
the highest-signal messages of the day — came back as:

```
**unknown** (1787001371.622709) [popular] [33 reactions]

**unknown** (1786995972.214599) [popular] [51 reactions]
```

The tags assert significance; the body asserts nothing. An empty string reads as "no content,"
so the agent dropped both and reconstructed the announcements from congratulatory replies.

No block traversal exists anywhere in `pkg/features` or `pkg/provider`. The only
attachment-aware code is `find_discussion_official.go:86`, which sets a `hasAttachments`
boolean.

### The payload asserts effects that did not occur

`catchup_impl.go:200` and `:209` emit "💬 Full content displayed - marking as read" and
"🔍 Thorough review complete - marking as read", each directly above `// TODO: Actually mark
as read`. `NextActions` adds "Messages auto-marked as read (full consumption)".

`MarkConversation` is called only from `mark_as_read.go`. Read state is untouched.

The reporting agent believed the output and told its human that five channels had been
cleared. The agent's reasoning was sound and its report was false. ADR-003 already lists this
string among defects to retire by deletion.

### One rendering path does not exist

The three failures share a fix site that has never been built. `format.go` carries five
formatters — `formatUnreads`, `formatMentions`, `formatCatchUp`, `formatContext`,
`formatSearch` — each hand-rolling its own message line, with key fallbacks that have drifted
apart: `author` or `user`, `message` or `text`, `timestamp` or `time`. Resolution added at five
sites is resolution added at three.

### What ADR-003 settled, and its arrears

Issues #46 and #47 restate decisions already in force. #46 asks that `catch-up` be read-only;
it already is, modulo the false string. #47 proposes 16 tools become 8; ADR-003's "Resulting
surface" table already retires `check-unreads`, `check-mentions`, `catch-up`, and
`check-timing`. #47 cites #24's 177 channels, which ADR-003 corrected to 30 against a live
workspace.

That rediscovery is itself a finding. ADR-003's additive half shipped — `poll`, `read`, and
`ack` are registered. Its subtractive half did not. All sixteen tools are live, none carries a
deprecation note, and `catchup.go:13` still describes `catch-up` as the way to see what you
missed. An agent selecting by name lands on a tool the architecture retired, and rediscovers
the defects that retirement was meant to delete.

This ADR does not re-decide that. It decides the two things ADR-003 left open, and states
where their fix belongs given that retirement is pending.

### Approaches considered and rejected

**Resolve mentions in each formatter.** Five sites, drifting key names, and three of the five
belong to tools ADR-003 retires. Work applied to code scheduled for deletion. Rejected.

**Block on `users.info` for cache misses.** Correct output at the cost of a per-message network
call during cold start, on a two-phase cache designed to avoid exactly that. Rejected.

**Render unresolvable content as an empty string, as today.** Indistinguishable from absence.
Rejected — silence is the failure mode under repair.

**Prose warnings in `Guidance` when content is incomplete.** `Guidance` is where the "marking
as read" claim lives, and where ADR-003 found the dead `focus` parameter and the `containsWord`
stub described. Prose has already proven it drifts from behavior. Rejected in favor of a field.

## Decision

### The payload test

> Everything a response refers to must be resolvable from that response.

The mirror of ADR-003's parameter test. Anything failing it gets one of three dispositions:

| Disposition | Meaning | Applies to |
|---|---|---|
| **Resolve it** | replace the internal reference with what it points to | `<@Uxxx>`, `<#Cxxx>`, link labels |
| **Extract it** | recover text the payload carries somewhere non-obvious | Block Kit blocks, attachment fallbacks, bot and app author names |
| **Declare it** | when neither is possible, say so in a field the caller can test | unrendered blocks, unresolved identifiers, coverage gaps |

**A response never asserts an effect it did not perform.** Guidance text describes what the
caller may do next. It does not describe what the server did.

### One rendering seam

A single `renderMessage` normalizes a Slack message into `{author, body, tags, unresolved}`.
Resolution and extraction happen inside it. Formatters call it and do no message assembly of
their own, which retires the `author`/`user` and `message`/`text` key drift by construction.

The seam is built for `poll`, `read`, and `search` — the surface ADR-003 keeps. Legacy
formatters inherit it where they are trivially switched and are otherwise left alone; they are
scheduled for deletion, and repairing them is work against a decision already taken.

### Identity renders resolved

`<@Uxxx>` renders as `@Firstname Lastname`. The authed user renders as
`@Firstname Lastname (you)` — the marking that answers "is this me?" from the body itself,
with no follow-up call.

How an identifier becomes a name, how the backing cache stays current, and how a name fragment
becomes an identifier are decided in ADR-005. This ADR fixes only that rendered output carries
names rather than identifiers.

### A cache miss is declared, never silent and never blocking

Resolution can miss. On a miss the body keeps the raw identifier and the response carries it in
`unresolved`, so a caller can test whether what it read was complete and escalate specifically.
Rendering never blocks on a network call to close the gap.

`unresolved` is also the input to ADR-005's cache repair: an identifier declared here is one
the agent is actively encountering, and resolving it once removes it from every later payload.

### Block Kit is a text source

`section`, `header`, `rich_text`, and `context` blocks are walked for text when `text` is
empty, and attachment `fallback` is used after that. Author falls back to the bot or app name
before `unknown`.

When content remains unrenderable, the body carries an explicit marker rather than an empty
string:

```
[unrendered: 3 blocks — header, section, image]
```

Bounded and specific. The agent knows content exists, knows how much, and can escalate to the
human with a question that has an answer.

### Coverage extends to the message

ADR-003 made coverage a field at the query level — what was scanned, whether the scan was
complete. This ADR extends the same contract downward. A response reports gaps at message
granularity, in the same structured form, so partial rendering is as testable as a partial
scan.

## Consequences

### Positive

- The human stops being a lookup service for facts the server already holds.
- "Is this me?" is answerable from any rendered body, with no follow-up call.
- Blocks-only posts — workflow output, app announcements, form submissions, and the
  company-wide messages that are most often exactly these — stop vanishing.
- Silent drops become bounded escalations. An agent that cannot read something says what and
  how much.
- Five hand-rolled message renderers collapse to one, and the key-name drift between them
  ends.
- Issues #44 and #45 close. #46 closes as a deletion already required by ADR-003. #47 closes
  as a duplicate of ADR-003's resulting surface.

### Negative

- Rendering gains a dependency on the users cache. A cold cache degrades output quality in a
  visible way, which is the intent, and a way the current code does not.
- `unresolved` and the `[unrendered: …]` marker are new contract surface that every consumer
  must tolerate.
- Resolution makes bodies longer. `@Aaron Bockelie` costs more tokens than `<@U0AAAAAAAAA>`
  and is worth it.

### Risks

- **The seam is added and the old paths survive anyway.** This is precisely how the current
  sprawl arose: ADR-003's replacement surface shipped and its retirements did not. Mitigation
  is to track the retirement explicitly rather than assume it follows.
- **Display-name collisions.** Two people rendering as the same name reintroduces the
  ambiguity resolution was meant to remove. ADR-003's candidate-list pattern is the available
  answer if it appears in practice.
- **Block traversal drifts from Slack's schema.** Block Kit gains types. Unhandled types must
  fall into the `[unrendered: …]` marker rather than into silence, so the failure mode of a
  stale traversal is a visible gap.

## Related

- ADR-003 — the inbound half. This ADR is its outbound counterpart.
- ADR-005 — the resolver this ADR consumes, and the owner of `unresolved`'s repair path.
- Issue #45 — the decision here. Issue #44's rendering half is here; its matching and cache
  halves are in ADR-005.
- Issues #46, #47 — settled by ADR-003; recorded here because they were rediscovered.

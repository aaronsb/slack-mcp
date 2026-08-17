# ADR-003: A Resolvable Tool Surface

## Status

Proposed

## Context

Two complaints recur in daily use, and they turn out to be the same defect.

**The caller must paste coordinates.** Referring to a conversation requires an exact
thread identifier — `C0B74M52CQ6:1782246118.543969` — pasted by the human. The agent
cannot derive it, so the human resolves the reference by hand.

**The caller must guess a window.** `search` and `catch-up` take a timeframe with no
sensible default. The human picks one, and cannot distinguish "nothing exists" from
"wrong window" in the result.

The instinct is to blame parameter count. The measured surface does not support that:

| Tool | Parameters | Required |
|------|-----------|----------|
| `check-timing` | channel, lastMessageTime, conversationContext, thinkingFocus | 3 |
| `search` | query, in, from, timeframe, threadId, cursor | 0 |
| `catch-up` | channel, since, focus, cursor, limit | 1 |
| `get-context` | channel, messageTs, count | 1 |

Thirteen tools, roughly fifty parameters, fewer than four each. `search` — the tool that
causes the most friction — requires nothing at all.

The problem is not how many parameters exist. It is that the parameters which matter are
**unanswerable by the caller**. `messageTs`, `threadId`, `cursor`, and `lastMessageTime`
can only come from a prior call whose result the caller must re-key. `timeframe` and
`since` ask the caller to guess.

Nor are the tools ambiguous. A human reference like "the thread with Sarah about the
deploy" *is* ambiguous, and is also perfectly resolvable given recent context. The tools
are the opposite of ambiguous — they are **over-specified**. They demand precision the
caller cannot produce, and offer no way to express uncertainty. Since the tool will not
resolve, the human does, in a copy-paste.

### The same defect produced the code we have

A surface that cannot express what it needs compensates in prose, and prose drifts away
from behavior:

- `catch-up` advertises `focus: all|mentions|threads|important`. `catchUpHandlerImpl`
  never reads the parameter.
- `catch-up` emits "Thorough review complete — marking as read" above a
  `// TODO: Actually mark as read`. Nothing is marked. The model is told otherwise.
- `catch-up` then suggests `mark-read` as a next action, and the model obeys — so the
  flow advances the human's read marker while the tool that claims to do so does not.
- `isImportantMessage` ends in a keyword loop calling `containsWord`, which is
  `return len(text) > 0`. Every non-empty message is "important"; the triage is fake.
- Thread identifiers use `channelId.threadTs` in `mark-read` and `channelId:threadTs` in
  `search`, and `send-message` hands the model the colon form.

Each is a place where guidance text stands in for a capability the surface lacks.

### Read state

The server shares Slack's read marker with the user's official client. `check-unreads`
and `check-mentions` read `client.counts`' `has_unreads` — the counter the desktop client
zeroes. Reading in one perturbs the other, so unread-driven tools go empty precisely when
the human has been using Slack normally.

### Constraints

- **Session tokens only** (xoxc/xoxd). Internal endpoints are reachable and undocumented;
  they can change without notice.
- **Stealth is a product property.** Reads must not perturb the human's unread state.
  Only `mark-read` may.
- **Single Go binary, no external state service.** Any server-held state is a local file
  under XDG.
- **The human uses the official client in parallel, always.** Any design that assumes
  otherwise fails in normal use.

### Endpoint findings

Probed against a live workspace before deciding:

| Endpoint / parameter | Result |
|---|---|
| `client.counts` | **Not read-state-gated.** Returned 30 channels (10 unread, 20 read), 27 IMs (7 unread, 20 read), 10 mpims — every entry carrying `latest`. |
| `client.counts?thread_counts_by_channel=true` | Works. Adds `mention_count_by_channel` and `unread_count_by_channel`, covering channels and DMs. Same call, one parameter. |
| `subscriptions.thread.getView` | Works. `root_msg` carries `channel`, `thread_ts`, `reply_count`, `latest_reply`, `subscribed`. Strictly unreads-only — `include_read`, `unreads_only=false`, and `include_all` are silently ignored. Caps at 10 per page regardless of `limit`. Pages via `current_ts`; `max_ts` is a server clock, not a cursor. |
| `conversations.replies?limit=1` | Works. Returns the root with `reply_count` and `latest_reply` — read-state-independent per-thread change detection. |
| `users.conversations` | 30 member channels, no pagination cursor, identical set to `client.counts`. |
| `activity.feed` | Exists, requires an undiscovered `mode` enum. |
| `subscriptions.list`, `activity.list`, `activity.count` | `unknown_method`. |

Two consequences follow directly. `client.counts` returns **read and unread conversations
alike, each with a `latest` timestamp** — so change detection independent of the human's
client is available in one call. And membership is **30 channels, not the 177 recorded in
issue #24**; that figure counted the browsable workspace directory, not membership. There
is no channel-pagination problem to solve.

One anomaly worth encoding: a channel came back with `last_read` *ahead* of `latest`.

### Approaches considered and rejected

**Monitor on `has_unreads`.** The obvious reading of `client.counts`, and what the code
does today. Read-state-gated: goes empty as soon as the human glances at Slack. Rejected.

**Monitor threads on `subscriptions.thread.getView` alone.** It is Slack's own Threads
view and returns rich entries, but it is unreads-only and caps at 10. A thread the human
reads first never appears. Rejected as a sole basis; retained as a seed.

**Poll `conversations.history` across all conversations.** Read-state-independent and
correct, at 30+ calls per tick. Rejected as the primary path; retained for hydrating
conversations already known to have moved.

**Keep the coordinate parameters and document them better.** Documentation does not help
a caller that cannot produce the value. Rejected.

## Decision

### The parameter test

> A tool parameter must be answerable from what the caller already knows.

Anything failing that test gets one of three dispositions:

| Disposition | Meaning | Applies to |
|---|---|---|
| **Resolve it** | server matches a description against recent context | `threadId`, `messageTs`, person-as-channel |
| **Default it from state** | falls back to server-held state; result reports what was covered | `since`, `timeframe`, `limit` |
| **Hand it back** | opaque handle from a prior call, never composed by the caller | `cursor`, `fileId`, `timestamp`, `lastMessageTime` |

**Required parameters are limited to things a human said out loud.**

### A server-owned watermark, separate from Slack's read marker

Persisted under XDG via `pkg/paths`, keyed by workspace and a scope string (defaulting to
`"default"`), recording per conversation the last timestamp shown to the agent, and per
tracked thread the last `latest_reply` seen.

Compare against `latest` only. Never derive from `last_read` — it can run ahead of
`latest`, as the probe found.

This state does double duty, and that is the central insight of this ADR. It makes change
detection independent of the human's client, **and** it is what makes reference resolution
possible. "The thread with Sarah" is resolvable only against a record of what was recently
surfaced and who was in it. Without it, every reference must be an absolute coordinate —
which is why coordinates are being pasted.

### Two verbs, two markers

| Verb | Advances | Means |
|---|---|---|
| `ack` | MCP watermark | this agent has seen it |
| `mark-read` | Slack `last_read` | the human has seen it |

A monitor may look a hundred times and change nothing the human observes.

### Handles, never composed identifiers

Every event carries an opaque handle. Follow-up tools accept handles. The model never
spells an identifier, which retires the `.` versus `:` split by construction. This extends
the existing "channel names over IDs" rule to messages and threads.

### Ambiguity is a result type

```
read("sarah, the deploy thread")
→ { ambiguous: true, candidates: [
      { handle: "ev_7f3a", who: "Sarah Chen", where: "#eng › deploy rollback", when: "2h ago" },
      { handle: "ev_91c2", who: "Sarah Chen", where: "DM", when: "yesterday" } ] }
```

One round-trip inside the agent loop replaces a human copy-paste.

### Coverage is a field

```
{ found: false, covered: "since watermark, 3d", complete: true }
```

Structured, not a trailing prose note. A caller can detect a partial scan
programmatically, which issue #24 asks for directly. Empty results say what was searched,
and `search` widens automatically rather than returning a bare miss.

### Detection is separate from judgment

Discovery returns everything that changed. Ranking sits above it as a visible layer, never
as a filter inside the fetch path. A broken heuristic then produces bad ordering instead of
silently swallowing data — the `containsWord` failure mode.

### The tick

```
idle:    client.counts?thread_counts_by_channel=true    (1 call)
       + subscriptions.thread.getView                    (1 call)
       → nothing exceeds the watermark → { changed: false }

active:  + conversations.history oldest=watermark, per moved conversation   (N)
         + conversations.replies?limit=1, per tracked thread                (T, recency-bounded)
```

Threads use both paths because only one is read-state-clean: `getView` seeds the tracked
set cheaply and catches new threads; `latest_reply` polls the tracked set and is
independent of the human's client.

### Resulting surface

| Tool | Disposition |
|---|---|
| `poll` | new — change detection, no required parameters |
| `read` | new — accepts a handle or a description; absorbs `get-context`, `catch-up`'s content half, `search`'s `threadId` path |
| `ack` | new — advances the watermark |
| `search` | kept — finding is a different question from catching up; window defaults to the watermark |
| `send-message`, `react`, `mark-read`, `list-channels`, `list-users`, `auth-setup`, `download-file` | kept |
| `check-unreads`, `check-mentions`, `catch-up` | retired into `poll` and `read` |
| `check-timing` | retired — three required parameters, one of which asks the caller to supply a prose summary it must already have built |

### Scope

This ADR decides slack-mcp. The parameter test and the three dispositions are written to
transfer: jira-cloud, confluence-cloud, and google-workspace-mcp carry the same
antipattern in the form of pasted issue keys and document IDs. Adoption there is a
follow-on decision in those repositories, tracked against the shared conventions in
issue #20.

## Consequences

### Positive

- The human stops pasting coordinates, and stops guessing windows.
- Monitoring becomes viable: two calls and a small response when nothing has happened.
- Reads stop fighting the official client; stealth becomes structural rather than a
  convention each tool must remember.
- Threads become a location like any other rather than a separate feature.
- Eight known defects are retired by deletion rather than repair: the `containsWord` stub,
  the dead `focus` parameter, the "marking as read" claim over a TODO, the two thread-ID
  formats, the non-paging cursor, the four-tier windowing ladder, `check-timing`'s required
  `conversationContext`, and the unregistered `catchUpHandler` mock.
- Issue #24 shrinks to a correction: coverage is 30 conversations, already returned whole
  by one call.

### Negative

- New persistent state to write, migrate, and corrupt. The server currently holds only
  caches, which are disposable; a watermark is not.
- Resolution can pick the wrong referent. The candidate-list result bounds the damage
  without eliminating it.
- Deeper dependence on undocumented internal endpoints —
  `thread_counts_by_channel` and `subscriptions.thread.getView` join `client.counts`.
- Breaking change for existing callers. Three tools are removed and two more change shape.
- More server-side inference means more behavior a user cannot see. Coverage fields are
  the mitigation and must be honest.

### Risks

- **Internal endpoints change without notice.** `getView`'s undocumented paging and the
  `thread_counts_by_channel` parameter are the exposed surface. Mitigation: both are
  additive to a tick that degrades to channels and DMs if they fail.
- **The thread blind spot.** A thread that both starts and is read in the official client
  between two ticks is never surfaced. `unread_count_by_channel` narrows it by naming the
  channel; it does not close it. Document rather than hide.
- **Watermark divergence.** An agent that reads without acking re-reads forever; one that
  acks without reading skips silently. `ack` must be explicit and never implied by `read`.
- **Scope creep into judgment.** Ranking is a layer, and layers accrete heuristics. The
  `containsWord` stub is the warning: a heuristic nobody tested, inside a path nobody
  could observe.

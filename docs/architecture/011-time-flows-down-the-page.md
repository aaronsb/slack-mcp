# ADR-011: Time Flows Down the Page

## Status

Accepted

Extends ADR-009's output laws (parameter echo, caps always page) by one.

## Context

Slack's API returns history newest-first. Its client renders a scrollback
anchored at the bottom, so the first element in the payload is the one
nearest the composer. That ordering is a property of the transport, and
the nouns inherited it unevenly. A bare `target=` read reverses the
payload before rendering, and so does a `target= around=` read of channel
history. The same read of a thread reversed the replies too, and
`conversations.replies` arrives oldest-first, so threads came out
last-reply-first. The `since=` window, `query=` search, the inbox
mentions view, and the unread previews render in the order Slack handed
them.

An agent reading a tool result has no scrollback. It reads top to bottom,
and causality in a conversation runs the same way: a question, then its
answer, then the follow-up. A newest-first list hands the reader the
ending before the beginning, and a surface where the direction depends on
the view makes every list a small puzzle before it is a transcript.

The one argument for newest-first is truncation. A cap of 100 on 500 hits
should keep the newest 100. That argument decides which messages to fetch.
It says nothing about the order to render them in.

## Decision

### One law: time flows down the page

Every list of messages a noun renders is oldest-first. The first entry is
the earliest, the last entry is the latest, and reading the list top to
bottom replays the conversation in the order it happened.

The law covers every message list on the surface:

| Path | Before | After |
|---|---|---|
| `messages target=` | oldest-first | unchanged |
| `messages target= around=` | oldest-first for history, newest-first for a thread | oldest-first |
| `messages target= since=` | newest-first | oldest-first |
| `messages query=` | newest-first | oldest-first |
| `inbox view='new'` | oldest-first per conversation | unchanged |
| `inbox view='mentions'` | newest-first per channel, channels in scan order | oldest-first across channels |
| `inbox view='unreads'` DM previews and mentions, on both the internal-endpoint path and the standard-API fallback | newest-first | oldest-first |

Lists of conversations, people, or channels are not message lists and
keep their own sort keys. The inbox `new` view orders moved conversations
newest-first so a truncated tick keeps the freshest movement, and that
ordering stands.

### Fetch newest, render oldest

The fetch direction does not change. History calls still page backward in
time from `latest`, and search still asks Slack for `timestamp desc`. A
cap therefore keeps the newest N, and the reverse happens once, at render
time, after paging and filtering are complete. A cursor returned to the
caller still points at the boundary Slack gave, so continuation semantics
are untouched.

Where a list is assembled from several fetches (the mentions scans walk
channels one at a time), the reverse is a sort by timestamp across the
whole collection rather than a per-fetch flip. Those scans apply their
cap in scan order, so the cap keeps the first N channels' mentions, and
the sort orders what survived. The bottom line of a capped mentions list
is the latest mention among the channels scanned. The mentions view says
how many channels it scanned; the unreads view does not.

### The cursor names the oldest shown

Under ADR-009's caps-always-page law, a `target=` or `around=` read
names the oldest timestamp shown as the way into earlier history (issue
#23). Under this ADR that is the first entry of every rendered list, so a
caller who wants more history reads the top line. Guidance text that
described a list as "newest first" is removed.

## Consequences

### Positive

- One rule an agent can hold without a footnote: the bottom of any
  message list is the present.
- A `since=` window reads as a transcript instead of a reverse transcript,
  which is the shape the triage guidance beside it assumes.
- Search hits and inbox mentions line up with the reads they point at, so
  following a result into `target=` does not flip the reader's direction.

### Negative

- Agents and playbooks that had learned "the first search hit is the
  newest" now find it last. The guidance line that said so is gone, and
  the change ships as a minor version.
- The mentions view now sorts across channels, so the per-channel
  grouping the scan order produced is gone. The `channel` field on each
  entry carries what the grouping carried.

### Risks

- A future noun that renders messages could forget the law. The test
  suite asserts oldest-first on every `messages` path and on both unreads
  paths, each fed a newest-first fixture. The inbox `new` view is covered
  by the poll tests' per-conversation fetch, which hands back oldest-first
  by construction.

## Related

- ADR-009: the output laws this joins, and the recomposition that
  introduced `around=`.
- Issue #23: the oldest-shown-is-the-cursor rule this keeps.

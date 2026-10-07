# ADR-015: Channel Acquisition Sources

## Status

Accepted

Widens ADR-005's resolution discipline from people to channels and adds one
network step on a channel-name miss. Leaves ADR-007's completeness rule
untouched: the new source never takes part in absence reasoning. Decided on a
live probe (2026-10-07), recorded in ADR-003's endpoint table.

## Context

A channel name resolves only if the cache holds it, and the one complete
source of channels the person has not joined is the full `conversations.list`
walk. A name is unreachable in three windows:

- **Before the first walk finishes.** On a first boot or a wiped cache the
  walk starts after boot and takes minutes; until it ends only member
  channels resolve.
- **Between sweeps.** A channel created or renamed after the last complete
  walk stays invisible for up to a day, and a restart does not close the
  window while the estate reports a fresh enumeration.
- **On large workspaces.** Walk time grows with channel count at the pacing
  Slack tolerates.

In each window the agent is told "no channel named X", which it cannot tell
apart from "X does not exist".

Slack's own quick switcher answers the same question by asking the server
for a name fragment. The probe found that its endpoint answers a session
token:

- `search.modules.channels` (and `search.modules` with `module=channels`)
  returns public channels the person is not in, archived channels, and private
  channels they are in. It never returns DMs or group DMs.
- It matches names and purposes alike.
- Its default order puts an exact name late: `#marketing` was 35th of 37
  hits, and `#sales` was absent from the first 100 of 940. With
  `sort=score`, the exact name came first in two of four checks, second in
  one, and third in one.
- Each item is a thin hit: id, name, membership, privacy, archive state,
  purpose, member count. It has no topic, no creation time, and no kind
  flags, so it is not a conversation record.
- Thirty calls at up to three per second drew no rate limit.

### Sources before this decision

| Source | Endpoint | When | Complete? |
|---|---|---|---|
| Snapshot and estate hydration | none (disk) | boot | as of the last write |
| Member load | `users.conversations` | boot | members only |
| Boot backfill | `conversations.list` | after boot, unless the estate is fresh | yes |
| Estate sweep | `users.list`, `users.conversations`, `conversations.list` | at most daily | yes |
| Explicit refresh | backfill respawn | `estate view='channels' forceRefresh=true`, rate-capped | yes |
| On-demand by ID | `conversations.info` | a cache miss on an ID | one record |

## Decision

### A name miss asks the switcher once

When someone names a channel the cache does not hold, the server makes one
`search.modules.channels` call with that name, `sort=score`, and a page of
100 hits, before answering that the channel is unknown. Two paths count as
naming a channel, and both require the name to be one Slack could hold
(lowercase letters, digits, hyphens, underscores):

- a `#name` target on any tool that takes a conversation, and
- a `messages target=` description that is a single channel-shaped word and
  matched nothing locally.

A bare word on the conversation resolver stays with the person ladder. A
multi-word description never reaches the switcher.

### Only a unique exact name resolves

The server scans every returned hit for a name equal to the query, ignoring
case. One such hit resolves, and the server fetches its full record through
`conversations.info`, the same path a cache miss on an ID already takes. That
call caches the channel, indexes its name, and records it in the estate. A
switcher hit is never written to the cache or the estate directly, because a
thin record would read as a change against the full one already folded.

Anything else is an answer, not a resolution. The hits come back as
candidates, named by `#name` and never by ID, with a note on each that
matched on its purpose rather than its name. When Slack reports more hits
than the page held, the answer says only the top page was checked. This holds for reads and writes
alike. A read loses one round trip. A write never posts to a channel the
agent did not name exactly.

### The lookup sits after the local checks, before the quarantine

For a write, the lookup runs where the destination is located: after the
strike lock and ADR-012's local checks, and before the destination's
quarantine (ADR-013). It sends Slack only the name the agent typed, and no
content. A write the strike lock refuses makes no lookup, and every other
local refusal (a malformed emoji name, a file outside the exchange
directory) comes first too. One window is narrower: before the account's
workspace is identified the lock cannot be read, so a lookup may run, and
the gate checks the lock again before anything is sent.

### One call per miss, at a human's pace

- At most one switcher call is in flight.
- A name that found nothing is not asked again for ten minutes.
- A rate-limit answer pauses every lookup for the time Slack names, or 30
  seconds when it names none, and the miss says so.
- Any failure falls back to today's "no channel named X" with a line saying
  Slack's channel search could not be reached.

### Never a sweep source

No sweep, backfill, or refresh calls the switcher. Its results are ranked and
capped, so nothing proves that a set of queries covered the workspace, and
ADR-007 admits only sweep-sourced completeness to absence reasoning.
Enumerating by prefix would also put machine-rate bursts on an undocumented
endpoint that no person's client produces.

A channel learned this way enters the estate as traffic (`src: traffic`),
through `conversations.info`, as any other on-demand fetch does. The next
complete walk confirms it. No envelope change is needed.

### Alternatives considered

- **Leave the walk as the only source.** The windows above stay open,
  and a miss inside them stays indistinguishable from absence.
- **Enumerate the workspace through the switcher.** Rejected under
  "Never a sweep source": it cannot prove completeness, and it costs dozens
  of machine-paced calls to learn what the daily walk already knows.
- **Write switcher hits straight into the cache and estate.** Rejected: a
  hit lacks topic, creation time, and kind flags, so folding it would
  record a spurious change against the full record; one `conversations.info`
  call per resolved name avoids that.
- **Resolve the top-ranked hit.** Rejected: the order is not documented and
  put exact names late without `sort=score`; a write must never land
  somewhere the agent did not name.

## Consequences

### Positive

- A channel created after the last walk, or named before the first walk
  ends, resolves on first use.
- The cache and the estate learn channels between sweeps, one name at a
  time.
- "Not found" now means Slack's own channel search found nothing, unless the
  result says the search could not be reached.

### Negative

- A miss costs a network round trip it did not cost before.
- The resolver depends on one more undocumented endpoint (ADR-003's
  endpoint-drift risk). A shape change degrades to today's behavior rather
  than failing.

### Risks

- Visibility of the queries to the person or an admin, such as entries in the
  switcher's recent searches, was not established by the probe.
- `sort=score` is not documented. If Slack drops it, the exact-name scan still
  finds the hit when it falls within the first 100, and misses it beyond;
  the answer then says only the top page was checked.

## Related

- ADR-003: the endpoint findings this rests on.
- ADR-005: the ladder discipline this applies to channels; ring 3 for people
  is the same endpoint family (`search.modules`, `module=people`).
- ADR-007: why the switcher is never a completeness source.
- ADR-013: where the lookup sits in the write order.
- Issue #100; issue #50 (the people-side probe).

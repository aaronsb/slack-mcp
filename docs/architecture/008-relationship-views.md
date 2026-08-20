# ADR-008: Relationship Views

## Status

Proposed

Depends on ADR-007's estate; implements ADR-005's encounter class; resolves
ADR-007's deferred "any estate query tool".

## Context

The estate answers questions about entities one at a time. The questions
that emerged from using it are about *relationships*: which channels belong
to one engagement, who is driving an initiative across several channels at
once, what one person's footprint looks like. Today an agent answering
those makes several listing calls, holds the result sets in context, and
performs set intersection by reasoning — expensive, error-prone, and it
spends the context window on raw material instead of answers. The corpus
already names this failure shape: the server holding the pieces owes the
resolution, not the parts (ADR-003, ADR-005).

The joins have three dimensions, in three states:

- **Structure** — channel families. The workspace encodes engagements in
  channel names (`#customer-engagement-sales`, `-assessment`,
  `-implementation`), and the estate holds each channel's lifecycle
  interval. Grouping by stem is fold math over data already held.
- **Ownership** — `slack.Channel.Creator` and `Created` pass through every
  walk and are currently dropped by the projection.
- **Activity** — who is active where, when. Not captured anywhere durable:
  messages flow through the render paths and vanish. ADR-005 designed the
  encounter class for this; ADR-006 gave it the attention ledger and its
  ninety-day bound; neither is built.

### Where content lives

Message content never lands in our stores. Slack is the system of record
for what was said; the fold is the system of record for the joins. A view
that surfaces an intersection returns handles, and the agent drills in
through `read`, which fetches the messages live. The durable local record
stays less sensitive than what it indexes — ADR-007's privacy posture,
extended.

### Approaches considered and rejected

**A query language.** A generic estate-query surface (filters, joins,
predicates) hands composition back to the agent — the reasoning hops return
as query construction, and the surface becomes SQL-shaped complexity every
caller must learn. Rejected for named views that each answer a whole
question.

**Agent-side joins over enriched listings.** Richer `list-channels` output
still leaves intersection to the agent, and interval overlap by reasoning
over dozens of channels is exactly the arithmetic models fumble. Rejected.

**Caching message content to compute activity.** Storage, privacy, and a
second system of record for content. Rejected; activity is metadata.

## Decision

### Terminology

Implementation names come from the established literatures, not this
project's working analogies:

- **Temporal interaction graph** — the whole structure: nodes and edges
  whose presence is time-qualified (temporal-networks literature).
- **Inferred edges** — every relationship is computed from observation
  events, never declared or stored (organizational network analysis; the
  informal graph inferred from interaction metadata).
- **Two time constants** — the durable lifecycle plane (estate: intervals,
  unbounded history) beneath the decaying activity plane (attention:
  strips inside the sliding ninety-day window).
- **Temporal motif** — a named time-qualified shape searched for in the
  graph (dynamic-networks literature). Each view instantiates one:
  `convergence` is the co-activity motif, `initiatives` the
  creator-activity motif, handoff detection the phase-transition motif.
- **Projection / read model** (CQRS) — the folds are projections of the
  event ledgers; a view is a parameterized read model executing a motif
  query.

### The projection gains ownership

`estate.channel/v1` gains `creator` (user ID) and `created` (Slack's
timestamp) — additive fields, no version bump, observed as honest first
sights on the next enumeration.

### The encounter observer

The render paths that already hold messages — poll, read, catch-up, the
unread scans — append encounter events to the attention ledger:

```json
{"v":1,"at":"2026-08-20T14:02:11Z","src":"traffic","kind":"encounter","user":"U0AAA","conv":"C0ENG","day":"2026-08-20"}
```

No message text, ever. Append on change bounds volume: one encounter per
user per conversation per bucket, with the fold's `lastSeen` scalar
absorbing repeats (ADR-006's rule). Strips are maximal bucket runs.

**Resolution is asymmetric: the session's own user buckets by hour,
everyone else by day.** Both pressures point the same way. Volume:
hour buckets for everyone press the fifty-thousand backstop inside the
ninety-day window, and the backstop would silently shorten the horizon;
hour buckets for one user are dozens of events a day. Privacy: hour-level
strips on colleagues is movement tracking, while "active in #eng Tuesday"
is what the initiative and handoff joins need; the one person whose hours
the ledger can hold without surveilling is the operator. Self-cadence
questions — how many conversations overlapped within an hour, the
parallelism distribution of a week — become fold math on the person view,
and they must say "as observed": strips record what the agent saw, so
their completeness follows the agent's own polling habit. The attention ledger carries ADR-006's
retention unchanged: ninety days, fifty-thousand-event backstop, boot-pass
compaction, one file per workspace and scope.

### The graph schema

Nodes come from the folds; edges carry provenance and one of two temporal
shapes, never conflated: **lifecycle intervals** (estate — unbounded
history with dated, `notBefore`-bounded endpoints) and **activity strips**
(attention — maximal runs of encounter day-buckets, gap tolerance one day,
the whole dimension inside the ninety-day window).

| Node | Source | Lifecycle |
|---|---|---|
| `User` | estate | firstSeen → tombstone, prior intervals |
| `Channel` | estate | created → archived interval → tombstone |
| `Family` | derived (name stem) | span = union of member lifecycles |

| Edge | Temporal shape | Source |
|---|---|---|
| `CREATED` User→Channel | point | estate `creator`/`created` |
| `PHASE_OF` Channel→Family | derived | stem parse |
| `COUNTERPART` Channel(IM)↔User | existence | estate `user` |
| `DM_WITH` User↔User | existence | dm-map |
| `MEMBER_OF` self→Channel | current flag | membership walk (self only) |
| `ACTIVE_IN` User→Channel | strips | attention encounters |
| `MENTIONED` User→User@Channel | events | render path — deferred |

Overlap is strip intersection — the interval math the fold already does
for tombstones. Every relation reduces to interval arithmetic plus
grouping; no view reads content.

### Joins run in the fold

The server computes intersections; the agent asks whole questions. Interval
overlap, stem grouping, and creator-activity correlation are deterministic
fold math — unit-testable against fixture ledgers, free of context-window
cost, and identical on every call.

### Two executors per view

A view is a question, not a data source, and it carries two execution
plans. The **fold executor** answers from local state: instant, free, the
ninety-day activity horizon, as-observed. The **compiled executor**
deterministically constructs a bounded set of Slack-side queries — the
counterpart ranking compiles to `search from:@handle after:date` grouped by
conversation, convergence to one such search per person over the window —
complete where Slack's index reaches, at API cost, paced under the rate
tiers.

This scales the resolution ladder's rule from parameters to plans: agents
hand-composing Slack query grammar fail silently, so the server owns the
grammar end to end. Same view and parameters, same compiled queries —
testable as string construction. Execution is fold-first; the compiled plan
runs when the question exceeds the fold's coverage (a window past ninety
days, or an ask where as-observed is not good enough), and the coverage
block names which executor answered, so a ranking says whether the agent's
reading habit or Slack's index is speaking.

### One read-only tool, named views

A new `estate` tool with a `view` parameter:

| View | Question it answers whole | Joins |
|---|---|---|
| `families` | "what happened with this engagement" — phase sequence, drivers, span; works day one, estate only | stem ⋈ lifecycle ⋈ creator |
| `initiatives` | "what moved this week" and its inverse, the stalled channel whose creator went quiet | creator ⋈ ACTIVE_IN ⋈ strip overlap |
| `person` | one footprint — strips, created channels, and ranked counterparts ("top talkers", from DM-encounter density plus channel co-occurrence); for the departed, the knowledge-risk query only remember-then-tombstone affords | created ⋈ ACTIVE_IN ⋈ DM_WITH ⋈ COUNTERPART ⋈ estate record |
| `convergence` | given a set of people, the conversation-window clusters where their strips co-occur, ranked by density above each person's baseline (so a shared #all-hands is noise and a suddenly-dense deal channel is signal) | ACTIVE_IN ⋈ ACTIVE_IN across users |

Every view returns handles for drill-down and declares its coverage: the
activity dimension carries the attention ledger's ninety-day horizon, and a
view must say "within the last ninety days" rather than let retention
masquerade as history (ADR-006's named risk). Activity-derived rankings
carry a second caveat: strips record what the agent observed, so "top
talkers" means "as seen by this agent's reading habit", said outright. And
a view reports observations, never judgments — "densest co-occurrence
windows", not "significant"; "created N, active in M", not "leads". Views degrade honestly — no
attention ledger yet means `families` works and `initiatives` says what it
is missing, not an empty result.

This resolves ADR-007's deferred "any estate query tool": one tool, because
the read surface is where the corpus is generous ("reads: unbounded and
complete") and relationship questions fit no existing listing. The surface
ADR-003 decided grows by exactly one read.

### Deferred, named

- Membership rosters (`conversations.members`) — the "who shares this
  channel" edge; a per-channel API cost with no observer yet.
- Mention edges (person tags inside bodies as directed edges) — the render
  path sees them; whether they are worth an event class is open.
- A visualization surface. The precedent exists in this binary:
  `pkg/setup` embeds a web UI (`go:embed`), serves it on localhost only,
  and launches the browser cross-platform. Because views are deterministic
  functions of the folds, a graph report is that same surface executing a
  view on request and rendering it into an embedded page — the interaction
  graph renders locally or not at all. Deferred until the views prove they
  query useful things.

## Consequences

### Positive

- Relationship questions cost one call and return answers, not raw
  material for agent-side set math.
- The initiative detector — creator authoring across several channels in
  overlapping strips — becomes a fold query.
- The attention ledger finally exists, giving ADR-005's encounter-recency
  ranking its data source as a side effect.
- Activity observation costs zero API calls; it rides fetches that already
  happen.

### Negative

- A twelfth tool, against #49's cut to eleven.
- The attention ledger is a second ledger file with its own compaction —
  the machinery ADR-006 specified, now actually built and tested.
- Day-bucketed encounters cap colleague-activity resolution at a day by
  design; only the operator's own strips carry hours.
- View definitions are product judgments; a view nobody uses is surface
  cost forever.

### Risks

- **The attention ledger is a local record of who the user watches.**
  Sharper than ADR-006's framing now that views aggregate it. Same
  handling: XDG, 0600, no content, ninety-day expiry — and issue #15's
  encryption-at-rest applies to it identically.
- **Initiative inference can be wrong in ways that read as claims.** A
  view result is an observation summary, not an org chart; phrasing must
  stay at "created N channels, active in M within the window", never
  "leads".

## Related

- ADR-005 — the encounter class this builds; recency ranking becomes
  possible.
- ADR-006 — the attention ledger's retention, compaction, and the
  retention-as-history risk the views must answer.
- ADR-007 — the estate the views join against; the deferred query tool
  this resolves; the privacy posture content-free events extend.
- Issue #15 — encryption at rest, now covering both ledgers.
- Issue #49 — the tool-surface cut this adds one read to.

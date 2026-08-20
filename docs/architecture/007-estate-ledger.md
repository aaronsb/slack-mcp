# ADR-007: The Estate Ledger

## Status

Accepted — implemented in #51–#57 and running against a live workspace:
first sweep enumerated 700 users (425 pre-existing deactivations
tombstoned) and 3,046 channels; restart-skip, walk resume, and the
zero-append idempotent pass all observed working on 2026-08-19.

Amends ADR-006 (retention, snapshot role, observer). Reverses one ADR-005 rejection, scoped
to estate observation.

## Context

ADR-006 named its own limit: "A rename that happens between two polls, while nothing was
being rendered, leaves no event. The ledger is bounded by what the agent looked at." That
section conflates two kinds of state under one policy.

**Estate** is what exists in the workspace — users and channels, their identifying
properties, over time. It is a property of the workspace, identical whichever agent
observes it. **Attention** is what this agent saw in traffic — ADR-005's encounters,
ADR-006's movement. ADR-006 gave both the attention treatment: 90-day retention, render-path
observation. The estate questions — did this ID exist, what was it called then, when did it
disappear — are exactly what that treatment surrenders.

The surrender is structural, not a tuning problem. A deactivated user produces no cache
miss, so ADR-005's repair-from-traffic never sees the deactivation. A deleted channel stops
appearing, and absence from traffic is indistinguishable from quiet. ADR-005 rejected the
periodic full-directory refresh for render-path freshness — correctly; lazy per-entry
revalidation answers that question at a fraction of the cost. But the rejection took the
only observer that can see absence down with it.

The agent-first constraint makes the gap concrete. The agent has no second window
(ADR-README); handed a `<@U0XXX>` belonging to someone deactivated in May, it can only
hedge — the README failure table, row two — and the human becomes a lookup service for a
fact the server watched disappear. Tombstones must be dated facts in payloads; absence may
be asserted only from a completed enumeration; and no recovery path may run through a human
deleting a cache file by hand (ADR-005's Context names that as the current recourse).

Code reality that shapes the decision: the caches persist full slack-go structs while
readers consume roughly ten fields; the `conversations.list` walk already includes archived
channels (no `ExcludeArchived` — an accident this ADR converts into a requirement);
`slack.User` carries `Updated` and `Deleted`; `ProvideIdentity` carries `TeamID` while the
watermark keys by team display name.

### Approaches considered and rejected

**One ledger file with a kind discriminator.** Retention tiers are file lifecycles, not
line filters. A mixed file forces the boot compaction pass to rewrite an ever-growing file
forever, re-copying every permanent estate line to drop expired attention lines. Rejected;
one file per class keeps ADR-006's "one read, one write, at startup" bounded by the
attention class alone.

**Field-level diffs as the event payload.** ADR-006's torn-line tolerance skips a damaged
trailing line. Under field diffs, a skipped line silently corrupts every subsequent state
for that entity; under full records, it loses one observation. Diffs are also not
idempotent under the two-writer interleaving ADR-006 names as a risk — duplicate full
records fold to the same state. Rejected.

**Full slack-go structs in events.** A library upgrade that reshapes the struct produces a
spurious full-directory delta storm. `Profile` carries email and phone, which an
indefinitely-retained file must not hold (issue #15). Diffs over a hundred fields are noise
when readers consume ten. Rejected for a named projection.

**Snapshot-diffing without a ledger.** Diffing consecutive `users.json` writes answers
"now" and never "then": no tombstone dates, no history, and the snapshot's map-iteration
ordering is nondeterministic between flushes. Rejected.

**A hard count cap on estate events.** A cap that silently drops tombstones reintroduces
the silent-absence failure this ADR exists to remove. Rejected; growth is bounded
structurally and gauged, not capped.

## Decision

### Two classes, two files, two retentions

| Class | File | Keyed by | Retention |
|---|---|---|---|
| Estate | `ledger/<team>/estate.jsonl` | workspace (`TeamID`) | indefinite |
| Attention | `ledger/<team>/attention-<scope>.jsonl` | workspace + scope | ADR-006's 90 days + 50k backstop |

Both live under the XDG data dir, dirs `0700`, files `0600`, path elements
watermark-sanitized. The estate is keyed by `TeamID` — the stable identifier `ProvideIdentity`
already carries — where the watermark keys by team display name. The inconsistency is named
here and its resolution deferred to issue #14; rekeying the watermark is not this decision.

The attention file is future work (ADR-005's encounters and movement land there); this ADR
fixes its class boundary and retention so that work changes nothing here. ADR-006's
`kind:"profile"` render-path deltas are reclassified: a rename is an estate fact whichever
observer caught it, so traffic-observed profile changes append to the estate file with
their source marked.

### The envelope

```json
{"v":1,"at":"2026-08-19T03:00:02Z","src":"sweep","kind":"...","entity":"user","id":"U0AAA", ...}
```

`at` is observed-at, always — the ledger records when we saw a state, never claims to know
when Slack changed it. `src` is `sweep`, `traffic`, or `boot`; only sweep-sourced
completeness participates in absence reasoning. `v` is the line schema version: adding a
projected field does not bump it (a field absent from an old record means unobserved then,
never empty), removing or re-meaning one does, and every historical version stays readable
forever because the estate file is never rewritten. Replay order is file order.

### Four event kinds

**`first-seen`** — the entity enters the estate, including re-entry after a tombstone
(reactivation, restored visibility). Carries the full projected record.

**`changed`** — a projected field differs from the fold. Carries the full projected record
after the change, the changed field names, and `notBefore`: the timestamp at which the
prior state was last positively confirmed. The true change lies in `(notBefore, at]`, and
each line states its own interval without replay. A channel archive is a `changed` event,
not a tombstone — archived channels remain enumerable and unarchivable.

```json
{"v":1,"at":"2026-08-19T03:00:02Z","src":"sweep","kind":"changed","entity":"user","id":"U0AAA",
 "changed":["displayName","title"],
 "rec":{"name":"dana","realName":"Dana Okafor","displayName":"Dana Okafor-Reyes","title":"Staff Eng","isBot":false,"deleted":false},
 "notBefore":"2026-08-18T03:00:11Z","slackUpdated":1755646800}
```

**`tombstone`** — the entity left the estate. Two reasons, matching Slack's two
disappearance modes: `deactivated` (a user observed with `Deleted:true` — a positive
observation, valid from any source) and `absent` (missing from a completed enumeration that
previously contained it — sweep-only; tombstoning from a partial listing would be ADR-004's
false assertion). A tombstone carries `notBefore` and `last`, the final projected record
inline, so "who was U0XXX" never requires replaying past it. Tombstones are retained
forever. Re-entry appends a new `first-seen`; the fold keeps both existence intervals.

```json
{"v":1,"at":"2026-08-19T03:00:02Z","src":"sweep","kind":"tombstone","entity":"user","id":"U0BBB",
 "reason":"deactivated","notBefore":"2026-08-18T03:00:11Z",
 "last":{"name":"dana.o","realName":"Dana O.","displayName":"Dana O.","title":"Design","isBot":false,"deleted":true}}
```

**`sweep`** — one per sweep attempt, carrying per-class completeness. Coverage reporting
reads this line.

```json
{"v":1,"at":"2026-08-19T03:00:05Z","src":"sweep","kind":"sweep",
 "users":{"complete":true,"count":412},
 "channels":{"complete":true,"count":31,"archivedIncluded":true},
 "membership":{"complete":true},"appended":4,"durationMs":2140}
```

### The projection

Events carry a named, versioned projection, not the slack-go struct.

- `estate.user/v1`: `name`, `realName`, `displayName`, `title`, `isBot`, `deleted`, with
  `slackUpdated` on the envelope as a happened-at hint, never authoritative for absence.
- `estate.channel/v1`: `name`, `isArchived`, `isMember`, `isPrivate`, `isIM`, `isMpim`,
  `user` (IM counterpart), `purpose`.

Email, phone, avatar URLs, and status are excluded by decision: the indefinitely-retained
file holds strictly less personal data than the disposable snapshot beside it.

### Append on change; the fold; observed-at honesty

ADR-006's mechanisms stand unmodified: append on change not observation, the fold built by
boot replay and mutated on every append, an observation matching the fold advancing only an
in-memory scalar, torn trailing lines skipped on replay, positions never pruned, no SQLite,
no background compactor.

The fold holds, per entity: the current record (nil if tombstoned), `firstSeenAt`,
`lastChangedAt`, `lastConfirmedAt`, the tombstone with its interval, and prior existence
intervals. Per workspace: `lastFullSweepAt` and per-class sweep watermarks, advancing
independently so one failed cursor loop does not zero out the class that finished.

`FoldAsOf(t)` — replay with cutoff `at ≤ t` — is a package function, not a tool. "As of T"
means the estate as observed by events at or before T, and every reconstruction carries
`lastFullSweepBefore(T)` so the consumer confronts the observation lag. Answers derived
from the fold report intervals, never points: "deactivated between 2026-08-18 and
2026-08-19".

### The sweep

A background full enumeration, never blocking startup: `users.list` (deactivated users
appear with `deleted:true` — a positive observation), `conversations.list` to cursor
exhaustion with archived channels included (the requirement the code currently meets by
accident), and `users.conversations` to keep `isMember` honest.

The sweep diffs each live projected record against the fold and appends `first-seen` and
`changed` events. A fold-live entity absent from its completed enumeration gets
`tombstone reason:"absent"`. A class whose enumeration failed appends no absence tombstones
and reports `complete:false`; its positively-observed changes still append. The sweep event
closes the pass.

**Mass-tombstone guard.** A sweep proposing to tombstone more than a fixed fraction of the
live fold (twenty percent) aborts absence-processing for the pass and reports the abort in
the sweep event. The product rests on undocumented endpoints whose degradation today
produces silently empty results (ADR-README); a shortened listing must not write durable
false tombstones into a file that is never pruned.

Cadence: at boot when `lastFullSweepAt` is older than 24 hours, then every 24 hours while
running. The fold's sweep watermark makes the cadence honest across restarts — a server
bounced hourly does not sweep hourly. 24 hours matches ADR-005's drift-TTL judgment. The
first sweep ever seeds a `first-seen` per directory entry — roughly a megabyte per five
thousand users, once.

The enumeration itself survives restarts. Pages are observed into the ledger as they land,
asserting no absences, so an interrupted walk's knowledge is durable; the walk checkpoints
its cursor and seen set per page, and the next boot resumes from the checkpoint, running
the absence pass against the union. A checkpoint older than an hour, or a cursor Slack no
longer accepts, restarts the enumeration clean — only the API cost is lost, never the
knowledge. A single-process walk is already temporally smeared over its own duration, so a
resumed walk claims nothing weaker, and the mass-tombstone guard backstops the stitch.

This reverses, scoped, ADR-005's "Periodic full-directory refresh… Rejected as the primary
freshness mechanism." That rejection was argued for render-path freshness and stands there:
lazy per-entry revalidation remains the freshness answer. The sweep exists because absence
is invisible to traffic.

### Retention, amended

Estate events — `first-seen`, `changed`, `tombstone`, `sweep` — are retained indefinitely.
ADR-006's "Retention: ninety days, with a count backstop" now governs the attention class
only, backstop included. The estate file is never rewritten: boot is read-only replay, and
the only write besides appends is truncating a torn tail. Growth is bounded structurally —
change-only appends, a bounded projection, tombstones closing entities — and gauged, not
capped: boot logs the estate's line and byte counts. If a workspace ever proves the
structural bound wrong, estate checkpointing is a future ADR, named below.

ADR-006's day-ninety-one negative is narrowed, not erased: "who did I see in #eng in March"
still dies at day 91 with the attention class, and coverage must carry that horizon exactly
as ADR-006's Risks demand. "Did U0XXX exist in March, and as what" no longer falls off the
cliff.

### The snapshots invert

`users.json` and `channels.json` keep their format and their flush path, demoted from
source of truth to rehydration caches for the full slack-go structs the projection
deliberately drops. This amends ADR-006's "Reference snapshots keep their current shape…
The ledger records deltas observed on the render path; it does not duplicate the
directory": the sweep's fetched directory now serves the snapshot write and the estate diff
from one fetch, and the ledger-plus-fold is the authority on existence over time.

The fold decides existence at load: a tombstoned user loads with `Deleted` forced true, a
gone channel is excluded from the live map and exposed through a fold-record read path, and
a fold-live entity missing from the snapshot hydrates as a skeleton repaired on demand. A
deleted or corrupt snapshot therefore self-heals — skeletons serve until the next flush
rematerializes the file — and no recovery runs through a human. ADR-006's rejection of
checkpoint files targeted checkpoints a compactor depends on; the snapshot is read to
decide nothing the fold owns.

### Agent-facing exposure

No new tool; ADR-003's eleven stand and issue #49 is unchanged. Three existing carriers:

- `list-users` gains `includeDeleted` (default false, preserving today's filter).
  Tombstoned users appear as dated facts:
  `"deactivatedBetween":["2026-08-18","2026-08-19"]`.
- `list-channels`: archived entries gain `archivedBetween`; absent-tombstoned channels
  appear under the same flag.
- Every payload's coverage gains
  `"estate":{"lastFullSweep":"2026-08-19T03:00:05Z","swept":true}`.

The resolver's unresolved outcome splits three ways, so "not in the estate" is
distinguishable from "never swept":

```json
{"resolved":false,"reason":"tombstoned",
 "was":{"id":"U0XXX","displayName":"Dana O.","handle":"dana.o"},
 "deactivatedBetween":["2026-05-10","2026-05-12"],
 "estate":{"lastFullSweep":"2026-08-19T03:00:05Z"}}
```
```json
{"resolved":false,"reason":"never_seen","estate":{"lastFullSweep":"2026-08-19T03:00:05Z"}}
```
```json
{"resolved":false,"reason":"unswept","estate":{"lastFullSweep":null},
 "hint":"no full sweep has completed; absence cannot be asserted"}
```

The rule, ADR-004-shaped: a tombstone surfaces as a dated fact; absence is asserted only
under a completed sweep; otherwise the response says it cannot know.

### Deferred, named

- As-of-T rendering in message bodies — ADR-004's seam consuming `FoldAsOf`, a later ADR.
- Watermark rekeying to `TeamID` and the multi-workspace layout — issue #14.
- Estate checkpointing, if boot replay cost ever proves the structural bound wrong.
- Any estate query tool.

## Consequences

### Positive

- The four estate questions — is this ID real, what is it, what was it called then, when
  did it disappear — are answerable from local state, dated, with declared coverage.
- Deactivations, joins, and archives become observable at all, closing the gap ADR-006's
  "attention, not history" section conceded.
- Full-record events make replay torn-line-safe and concurrent appends idempotent.
- One directory fetch serves the sweep, the snapshot, and the estate diff.
- The stale-directory recovery stops being "delete the cache file by hand."

### Negative

- The first sweep writes the whole directory as `first-seen` lines — roughly a megabyte per
  five thousand users, once, and the floor the file never shrinks below.
- Boot replay cost grows with workspace churn over years rather than a 90-day window —
  ADR-006's boot-cost negative, worsened. The gauge is logged; the cap is deliberately not
  set.
- A third persistence lifecycle — never-rewritten append-only — joins the snapshot and the
  compacted ledger.
- The sweep is a periodic full-directory API cost ADR-005 deliberately avoided, accepted
  here at 24-hour cadence for a purpose that rejection did not consider.

### Risks

- **Privacy, sharpened.** An indefinitely-retained record of who existed in a workspace and
  when. Issue #15's stated rationale for deprioritizing cache encryption — "cache is
  rebuilt on every boot anyway" — is invalidated by this ADR; #15 becomes a dependency of a
  durable estate, as ADR-006 already began arguing. The in-schema mitigation: the
  projection excludes email, phone, and status.
- **Two observers, one fold.** Traffic-sourced and sweep-sourced events must diff against
  the same fold or double-append. Full-record events make the failure redundant rather than
  corrupting, and redundancy is still noise to keep out.
- **Endpoint degradation writing durable falsehoods.** The mass-tombstone guard bounds the
  worst case; a degradation below the guard's threshold still writes false tombstones that
  a later complete sweep must revive. Revival events exist for exactly this.

## Related

- ADR-003 — the coverage-field contract the sweep event feeds; the eleven-tool surface this
  ADR holds.
- ADR-004 — the no-false-assertion rule that shapes `notBefore`, absence-only-under-
  completeness, and the three resolver outcomes.
- ADR-005 — the rejection reversed in scope; lazy revalidation upheld as the render-path
  freshness answer; its encounters remain attention-class.
- ADR-006 — amended: the retention section, the "attention, not history" section, and the
  reference-snapshot section. Upheld by name: append-on-change, the boot fold, torn-line
  tolerance, positions never pruned, no SQLite, no background compactor.
- Issue #14 — multi-workspace; the `TeamID` keying this ADR adopts and the watermark
  inconsistency it defers.
- Issue #15 — encryption at rest, reframed from optional to a dependency of durable estate
  state.
- Issue #49 — the tool-surface cut, unchanged by this ADR.

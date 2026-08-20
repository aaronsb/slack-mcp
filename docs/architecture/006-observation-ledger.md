# ADR-006: Observation Ledger and Folded Caches

## Status

Proposed

Amended by ADR-007: the retention rule, the "attention, not history" boundary, and the
reference-snapshot role now apply to the attention class only; estate observations get
their own file, an indefinite retention, and a sweep observer.

## Context

The server holds three persisted stores under three different models, and no contract governs
any of them.

**`cache.Store`** (`pkg/cache/store.go`) writes whole-file JSON through a temp file and an
atomic rename, with `MarkDirty` and `StartPeriodicFlush` driving writes. It backs `users.json`
and `channels.json`. It exposes `Age(filename)`, which nothing calls.

**`watermark.Store`** (`pkg/watermark/watermark.go`) writes whole-file JSON the same way, at
`0600`, keyed by workspace and scope. It carries `Prune(maxAge, now)`, which deletes fold
entries whose `LastSeenAt` has aged out.

**Neither** holds the two temporal things ADR-005 introduced: encounter history for `@`
ranking, and a per-entry `fetchedAt` for lazy revalidation.

Their freshness models diverge for no recorded reason. Channels load in two phases —
`loadMemberChannels` then `backgroundBackfill`, guarded by a one-shot `backfillDone`. Users are
fetched once ever, behind `if len(ap.users) == 0`. The watermark prunes by age. Three
strategies, none written down, one of them a bug.

### Every store overwrites, so no state has a history

- A display-name change replaces the old value with no record that it changed.
- A deactivation is invisible until something looks for it.
- A three-month-old message renders under the author's current name, because the previous one
  was never kept.
- ADR-003's own Negative section states the exposure: "New persistent state to write, migrate,
  and corrupt. The server currently holds only caches, which are disposable; a watermark is
  not." A corrupted fold has nothing to rebuild from.

### Constraints

Inherited from ADR-003, and binding here:

- **Single Go binary, no external state service.** Server-held state is a local file under XDG.
- **Fast startup is a product property.** Two-phase channel caching exists to protect it.
- **The human uses the official client in parallel.** Nothing may depend on being the only
  reader of Slack.

### Approaches considered and rejected

**Keep overwriting; add fields as questions arise.** ADR-005's per-entry `fetchedAt` answers
freshness and nothing else. Every later temporal question — when did this change, was this
person active last quarter — adds another ad-hoc field to a structure that cannot express
"before". Rejected as the general answer; `fetchedAt` survives as the degenerate case of one.

**Event-source everything, positions included.** A bounded log cannot safely hold a position:
pruning would silently reset an `ack`, and ADR-003 already names the failure — an agent that
acks without reading skips silently. Rejected.

**Background compaction.** A separate compactor is a second writer, and it requires a
checkpoint that can drift from the log it summarizes. Rejected.

**Size-bounded ring buffer.** Bounds cost precisely, at the price of a retention window that
varies with traffic — a busy week evicts a quiet quarter. Rejected as the primary bound;
retained as a backstop.

**SQLite.** A real answer to all of it, and a cgo and build-matrix dependency against a
static-binary constraint that cross-compiles to six targets. Rejected.

## Decision

### Three kinds of state, three treatments

| Kind | Examples | Store | Lifecycle |
|---|---|---|---|
| **Reference** | directory bulk from `users.list`, channel list | whole-file snapshot, as today | replaceable, disposable |
| **Observation** | profile deltas, encounters, conversation movement | append-only ledger | bounded by retention |
| **Position** | watermark `LastShownTS`, thread `LastReplySeen` | persisted fold, own file | **never pruned by the ledger** |

### The ledger

One file per workspace and scope, JSONL, append-only. One observation per line:

```json
{"t":"2026-08-19T14:02:11Z","kind":"profile","user":"U0AAA","display":"Dana Okafor","deleted":false}
{"t":"2026-08-19T14:02:11Z","kind":"encounter","user":"U0AAA","conv":"C0ENG"}
{"t":"2026-08-19T14:07:40Z","kind":"movement","conv":"C0ENG","latest":"1787001371.622709"}
```

Line-oriented so an append is one write with no read-modify-write, and so a torn trailing line
from an interrupted process is skipped on replay rather than corrupting the file.

### Append on change, not on observation

The rule that bounds everything else. A user seen five hundred times in a day produces one
event, not five hundred. An observation matching the fold updates the fold's `lastSeen` scalar
and writes nothing.

The ledger therefore contains only changes, which is what makes both a long horizon affordable
and the file directly readable as a change log.

### The fold is built once, at startup

Replay the ledger at boot to build the in-memory fold. Hold it for the process lifetime. Every
write mutates the fold and appends the event, so the fold is never stale relative to the
running process, and the ledger is never re-read during a session.

### Compaction is the same pass as the fold

The boot replay reads the whole ledger and writes back the retained tail through temp file and
rename, exactly as both existing stores write. One read, one write, at startup.

No background compactor, no second writer, and no checkpoint file that can drift from the log
it summarizes.

### Retention: ninety days, with a count backstop

Time is the primary bound, chosen so a quarter of history is visible — long enough to carry
renames, deactivations, role changes, and whether a person has been heard from this quarter.
With change-only appends, ninety days stays small.

A cap of fifty thousand events is a backstop against pathological growth. Whichever bound
binds first wins, and compaction logs which one did.

### Positions are never pruned

The watermark keeps its own file, its own `Prune`, and its existing semantics. It is a
checkpoint, not a summary of the ledger, and it must survive any retention horizon.

The general rule: **state whose loss changes behavior cannot live only in a bounded ledger.**

### Reference snapshots keep their current shape

`users.json` and `channels.json` stay whole-file, replaceable, and disposable. The ledger
records deltas observed on the render path; it does not duplicate the directory.

`Age(filename)` — already implemented, currently unused — becomes the staleness check for a
snapshot refetch.

### Historical resolution becomes possible

With profile observations retained, a name can be rendered as it stood at a message's
timestamp rather than as it stands now.

This ADR establishes the capability. Whether ADR-004's rendering seam uses it, and how a
historically-resolved name is distinguished from a current one, is deferred to ADR-004 rather
than decided here.

### The ledger records attention, not history

A rename that happens between two polls, while nothing was being rendered, leaves no event.
The ledger is bounded by what the agent looked at.

Any tool reporting from it says so. "Nothing changed" from the ledger means "nothing I saw
changed", and a result that conflates the two would be exactly the false assertion ADR-004
prohibits.

## Consequences

### Positive

- Three ad-hoc persistence strategies collapse to one contract, and the users cache stops being
  the odd one out by accident.
- ADR-003's stated corruption exposure is answered: a damaged fold rebuilds from the ledger.
- ADR-005's encounter history and `fetchedAt` both have a home, and `fetchedAt` becomes the
  timestamp of the latest observation rather than a bolted-on field.
- Change over time becomes answerable — renames, deactivations, and going quiet are events
  rather than absences.
- Startup cost stays one read and one write, with the fold in memory thereafter.

### Negative

- A new persistence primitive. `cache.Store` writes whole files; append-only line writes are
  a second mechanism to build and test.
- Boot cost grows with ledger size. Compaction bounds it; a workspace at the count cap pays
  fifty thousand line parses at every start.
- Two representations to keep consistent. A write path that appends without folding, or folds
  without appending, produces a divergence invisible until the next boot.
- Retention is a policy with no obviously right value. Ninety days is a judgment, and the first
  question it cannot answer will be about day ninety-one.

### Risks

- **Privacy.** The ledger is a durable local record of who the user talks to and when, which
  the current disposable caches are not. It lands under XDG at `0600` alongside the watermark.
  This sharpens issue #15 (encryption at rest) from a nice-to-have into a dependency.
- **Concurrent writers.** Two server instances sharing a workspace and scope would interleave
  appends. The watermark already carries this exposure and answers it with scope separation;
  the ledger inherits both the problem and the answer, and neither is currently enforced.
- **Torn writes.** An interrupted append leaves a partial trailing line. Replay skips it, so
  the loss is one event. Any framing that made a partial line unskippable would turn that into
  a corrupt store.
- **Retention masquerading as history.** A tool that reports "no change in 90 days" from a
  ledger pruned at 90 days is reporting its own horizon. Coverage fields (ADR-003) must carry
  the horizon whenever the ledger is the source.

## Related

- ADR-003 — the watermark this ADR classifies as a position and excludes from pruning; the
  corruption exposure it names; the coverage-field contract retention reporting must use.
- ADR-004 — the consumer of historical resolution, and the source of the rule that a response
  never asserts more than it knows.
- ADR-005 — encounter history and `fetchedAt`, both of which this ADR gives a home; its users
  cache becomes one fold among several.
- Issue #15 — encryption at rest, promoted from optional by the ledger's durability.

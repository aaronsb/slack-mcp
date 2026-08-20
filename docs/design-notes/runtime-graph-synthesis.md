# Runtime Graph Synthesis

The relationship graph this server queries does not exist as an artifact.
Every node and edge is synthesized at call time from two append-only event
logs — the estate ledger (lifecycle intervals, retained indefinitely) and
the attention ledger (activity strips, decaying at ninety days) — plus,
when a question exceeds what the logs hold, a compiled pass that writes
fresh observations into the attention log before the joins run. In the
established vocabulary: event sourcing with CQRS read models, where each
view is a derived projection computed on read, and the graph is the union
of those projections at one instant.

ADR-007 decided the estate log, ADR-006 the retention mechanics, ADR-008
the views and the two-executor split. This note records why the synthesis
property itself — never materializing the graph — is load-bearing, so a
future proposal to "just cache the graph" meets the argument it has to
beat.

## Four properties that fall out

### No staleness machinery

A materialized graph needs invalidation: when an edge expires, who
recomputes it, which source wins when they disagree. A synthesized graph
is exactly as current as its ledgers at every query, and the coverage
footer can state its horizon honestly because the horizon is an input to
the fold, never a cache age to estimate. The staleness problem is not
solved here; it is unconstructable.

### No second system of record

The edges are derivations. Message content stays on Slack; the durable
local state is two JSONL files of observations — entity facts and
`{user, conversation, day}` encounters, no text. Deleting those two files
deletes the graph. A stored graph database of workspace relationships
would itself be the surveillance artifact this project refuses to build;
under synthesis, the durable thing is small, bounded, auditable line by
line, and one encryption pass (issue #15) covers all of it.

### The instrument calibrates through use

Both executors feed the same log through the same dedup gate. Reading
enriches the graph as a side effect (the encounter observer rides fetches
that already happened); asking questions enriches it too (the compiled
executor records its search matches as encounters rather than answering
from them directly). Measured in the first live session of v2.0.0: one
channel read plus one compiled pass — a single search call, 260ms, 36
matches — turned five thin ego networks into a resolvable community
structure, and every later question that day started from that state
instead of from zero. Usage is not load the graph must survive; it is how
the graph gets better.

### Determinism makes the downstream cheap

Same ledgers, same graph. The views are unit-testable as fold math
against fixture logs, identical on every call, free of context-window
cost. The deferred visualization surface (issue #61) inherits this:
rendering a graph report is executing a view and drawing the result into
an embedded localhost page — a pure function of the folds, with no graph
store to operate, back up, or migrate.

## The boundary that keeps it honest

Synthesis is cheap because the folds are small: thousands of entities,
tens of thousands of encounters, all in memory after a boot replay that
tolerates torn tails and compacts the decaying log. The bet recorded in
ADR-007 — that change-only appends and lossy projections bound growth
structurally — is also the bet that keeps per-query synthesis viable. If
a fold ever grows past what a query can traverse comfortably, the answer
the corpus already prefers is a tighter projection or a per-class
retention rule, applied at the log. A materialized cache would trade the
four properties above for speed the folds do not yet need.

## Related

- ADR-006 — retention and compaction mechanics of the decaying log.
- ADR-007 — the estate ledger; the structural-growth bet.
- ADR-008 — the views, the two-executor split, the privacy asymmetry.
- ADR-009 — the tool surface these projections serve.
- Issue #15 — encryption at rest for both ledgers.
- Issue #61 — the visualization surface synthesis makes trivial.

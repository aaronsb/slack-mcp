# ADR-010: The Batch Executor

## Status

Proposed

Extends ADR-009's surface by one tool. Design only; implementation is the
next session's work.

## Context

An agent that already holds a plan executes it one tool call per turn:
inbox, then mentions, then three reads is five round-trips of model
latency and per-turn framing, and where the tools pace themselves
(compiled searches, channel walks) the latency is additive. The results
would have been the same tokens either way — the waste is turns, not
output. Browser automation surfaces solved this with a one-shot batch
call, and its shape is the right one: the queue lives in a single call's
payload, because on this transport every mutation of a server-side queue
would itself cost the turn it was meant to save.

## Decision

### One tool: `batch`

A ninth tool executing an ordered array of commands in one call:

```
batch commands=[
  {tool: 'inbox',    params: {view: 'new'}},
  {tool: 'inbox',    params: {view: 'mentions'}},
  {tool: 'estate',   params: {view: 'initiatives'}},
  {tool: 'messages', params: {target: '#ai-incubator', since: '1d'}},
]
```

Execution is sequential, in order. The response is one markdown document:
each item's rendered output in sequence, each section opening with that
item's echo line, separated by horizontal rules. An item that fails
renders its error in place and execution continues — one unreadable
channel must not cost the rest of the plan. Items are independent by
contract: an item cannot reference an earlier item's results, and a plan
with data dependencies stays multi-turn.

Bounds: at most 12 items per batch; `batch` may not contain `batch`.
Every item's own output laws apply unchanged — parameter echo, paged
caps, coverage honesty — so a batched capped list still names its exact
next call.

### The read-only boundary

`batch` executes `inbox`, `messages`, and `estate` — nothing else.
ADR-009 made the verb boundary the permission boundary; a batch that
could carry `say` would smuggle a Slack-visible write past per-tool
gating. `dismiss` was a candidate (its effect is a private watermark, no
Slack signal) and is excluded anyway: "the executor admits only reads" is
a law an agent can hold without a footnote, and dismissing costs one
ordinary call. In the assignment rule's vocabulary the batch is a fourth
kind: an **executor** — it encodes composition and never effect.

### Playbooks: the stateful queue, where state pays

A queue an agent edits across turns costs a turn per edit — but a
*saved, named* sequence outlives the turn and earns its persistence:

```
batch save='morning' commands=[...]   # store (replaces whole array)
batch run='morning'                   # execute the stored sequence
batch list=true                       # names, item counts, last run
batch delete='morning'
```

Editing is re-saving the whole array; item-level insert/remove verbs are
rejected because the array is small, the agent always holds it whole, and
five micro-verbs would spend schema on what one `save` already does.
Playbooks persist per workspace beside the watermark store
(`playbooks.json`, XDG, 0600), holding tool names and parameters only —
never results.

### The hint, fired by evidence

The server timestamps read-tool calls. When one lands within two seconds
of the previous — the signature of an agent executing a plan it already
holds — the response footer appends one line naming the batch
equivalent. Silent otherwise: the hint fires on the observed pattern, not
on every response.

## Consequences

### Positive

- A held plan costs one turn; paced items overlap their latency with
  nothing instead of with model round-trips.
- Playbooks give standing routines (a morning sweep, a weekly estate
  review) a one-call form agents can build once and reuse.
- The combined dump preserves every per-item honesty rule.

### Negative

- A tenth-minus-one tool: the surface grows to nine.
- Output tokens do not shrink — the saving is turns and latency, and the
  ADR says so rather than promising context reduction the math cannot
  deliver.
- Batching rewards pre-planning; an agent that should have reacted to
  item two's results before running item four gets no protection beyond
  the independence contract being stated.

### Risks

- Twelve paced items in one call can run long; the per-item rendering
  order doubles as a progress trace, and the item cap bounds the worst
  case.

## Related

- ADR-009 — the surface this extends; the effect/permission boundary the
  read-only rule preserves.
- ADR-008 — the coverage and echo laws every batched item keeps.
- Issue #61 — the viz launcher; a playbook is the natural producer of a
  report page's data.

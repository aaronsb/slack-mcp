# ADR-009: Tool Surface Recomposition

## Status

Proposed

Supersedes issue #49's 16→11 cut. Depends on ADR-008's view pattern;
extends ADR-003's surface decisions. Ships at v2.0.0 — breaking.

## Context

The tool surface grew by accretion to seventeen flat verbs, several of
which answer the same question with different addressing: three tools
fetch conversation content, two scan the attention surface, two list
directory entries beside the estate tool that subsumes them. Every agent
pays the description tokens for all seventeen on every session, and the
list reads as overlapping verbs rather than distinct capabilities.

Two forces sharpened during ADR-008's implementation. The view pattern
proved that one tool can carry many whole-question modes without becoming
a query language. And two output rules earned their place: **parameter
echo** (a tool renders the effective parameters it ran with, because an
agent cannot distinguish "my parameter worked" from "my parameter was
dropped and the default looked plausible" — a transport bug hid every
number parameter on the surface for months, invisible until a view echoed
its window) and **caps always page** (a cap bounds one response, never
reachability; every truncation names the exact next call).

## Decision

### The assignment rule

Every capability lives at exactly one of three depths, chosen by a rule,
not by history:

- **Verb** — its own tool — only if invoking it changes the world. The
  verb boundary is the effect boundary, which is also the permission
  boundary.
- **View on a noun** — a mode of a domain tool — if it answers a whole
  question about one durable domain, read-only.
- **Parameter** — if it merely addresses, narrows, or windows an existing
  call.

A capability migrates down the ladder as its effect weakens and its
domain sharpens. The corners of the design space are the rejected
alternatives: all-verbs is the accreted flat surface; all-nouns taxes the
agent with a taxonomy it must learn before asking anything; all-params is
the generic query surface ADR-008 already rejected.

### The surface: eight tools

```
inbox                        what needs me (read-only noun)
    view=new                     since my last dismiss    (was: poll)
    view=unreads                                          (was: check-unreads)
    view=mentions                                         (was: check-mentions)
messages                     conversation content (read-only noun)
    target=<handle|#chan|@person>                         (was: read)
        around=<ts>                                       (was: get-context)
        since=<window>                                    (was: catch-up)
    query=<slack search syntax>                           (was: search)
estate                       workspace shape + relationships (read-only noun)
    view=about|families|person|initiatives|convergence    (ADR-008)
    view=people                                           (was: list-users)
    view=channels                                         (was: list-channels)
say                          contribute content (write, Slack-visible)
    to= text=                                             (was: send-message)
    emoji= on=<handle>                                    (was: react)
dismiss                      advance the private watermark (write, invisible)
                                                          (was: ack)
mark-read                    fire read receipts (write, visible presence)
                                                          (name kept: the name is the warning)
auth                                                      (was: auth-setup)
download                                                  (was: download-file)
```

`check-timing` dissolves: no effect, no domain — its pacing guidance
becomes output lines on the nouns that already know the timing.

### Names carry semantics

The stealth boundary — this server's core privacy property — becomes
nomenclature. `dismiss` is private (a local watermark, zero Slack
effect); `mark-read` is public (the one write that fires read receipts),
and keeps its blunt name because the name is the warning. The `poll`/
`ack` protocol jargon dissolves into the inbox vocabulary every agent
already holds: check the inbox, dismiss what is handled.

### Every noun carries the house contract

Modes enumerated in the description; person parameters resolved through
the ladder (candidates with evidence on a miss, writes never auto-pick
below exact handle); parameter echo in the rendered output; every capped
list paged with the exact next call; coverage naming its executor and
horizon; observations, never judgments. The rendered markdown remains the
agent's entire world.

### Migration

A clean break at v2.0.0: no aliases, because deprecated names would cost
exactly the description tokens this decision reclaims. Setup and README
document the mapping table above. The internal architecture — providers,
folds, ledgers, feature handlers — does not change; the recomposition is
registration, dispatch, and rendering.

## Consequences

### Positive

- Seventeen descriptions become eight; the per-session token cost of the
  tool list roughly halves, and the list reads as question-shapes.
- The stealth split is legible from names alone.
- The assignment rule decides future additions: name the effect, the
  domain, or the scope, and the capability's place follows.

### Negative

- Breaking for every existing client configuration at once.
- `say` absorbing `react` spends permission granularity: a client can no
  longer allow reactions while denying messages.
- Three noun schemas are wider than the verbs they replace; the guard
  against mega-schema creep is the assignment rule itself — new modes
  must answer whole questions about the noun's domain, or they are
  parameters, or they belong elsewhere.

### Risks

- The `messages` noun absorbs four tools including search; if Slack's
  query grammar grows past "addressing by query", search may deserve its
  verb back. The rule provides the test: finding is effect-less and
  domain-shared, so it stays a mode until its competence diverges.

## Related

- ADR-003 — the original surface decisions this recomposes.
- ADR-008 — the view pattern, the resolution ladder, and the output
  honesty rules every noun inherits.
- Issue #49 — the 16→11 cut this supersedes.
- Issue #15 — encryption at rest, unaffected.

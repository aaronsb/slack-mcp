# Architecture Decisions

Seven ADRs, and one argument running through the last five.

## What this corpus argues

The server is consumed by an agent, and an agent has no second window. A human reading a
briefing has Slack open beside it: an unresolved `<@U0AAAAAAAAA>` gets hovered, an empty
message body gets opened in the real client, a claim that channels were marked read gets
checked against the sidebar. The agent has none of that. The tool output is its entire world.

So wherever the server elides, abbreviates, or decorates, the agent has three moves —
hallucinate, hedge, or drop — and the human becomes a lookup service for facts the server
already held.

Four failure modes, each with a decision against it:

| Failure | What the agent does | Decided in |
|---|---|---|
| A parameter the caller cannot answer | asks the human to paste coordinates | ADR-003 |
| A referent the payload does not define | hedges — "this may be you" | ADR-004, ADR-005 |
| Content silently dropped | reads absence as "nothing here" | ADR-004 |
| An effect asserted but not performed | reports the falsehood onward | ADR-004 |

The last is the worst. A gap an agent can see, it routes around; an incorrect assertion removes
the signal that anything needs checking.

### The autonomy is asymmetric

Every one of those failures was resolved by the agent turning to the human. None was a missing
permission — all were missing facts. An agent's autonomy comes from knowing, not from acting.

- **Reads: unbounded and complete.** Resolve every referent, extract every body, report every
  gap. No second call should be needed to understand what the first returned.
- **Writes: few, named, explicit.** Exactly two tools change anything in Slack; one changes read
  state; `ack` moves only the agent's private position.

Widening writes to compensate for narrow reads is what produces a passive briefing that clears
five channels.

### What the payloads must satisfy

ADR-003 governs the way in, ADR-004 the way out:

> A tool parameter must be answerable from what the caller already knows.
> Everything a response refers to must be resolvable from that response.

ADR-005 supplies the resolver both depend on. ADR-006 supplies the state the resolver reads.

## The decisions

### Surface — what the tools take and return

| ADR | Title | Status | In the code |
|---|---|---|---|
| [003](003-resolvable-tool-surface.md) | A Resolvable Tool Surface | Accepted | **Partial** — see arrears |
| [004](004-self-contained-payloads.md) | Self-Contained Payloads | Proposed | **Partial** — tag resolution on the render path shipped (#59); the renderMessage seam and unresolved field are #63 |
| [005](005-identity-resolution.md) | Identity Resolution | Proposed | **Partial** — the resolution ladder runs behind search from: (#56); rings, encounters, and the remaining person parameters are open |

### State — what persists and for how long

| ADR | Title | Status | In the code |
|---|---|---|---|
| [006](006-observation-ledger.md) | Observation Ledger and Folded Caches | Proposed, amended by 007 | **Partial** — its mechanisms shipped inside ADR-007's estate; the attention ledger is not started |
| [007](007-estate-ledger.md) | The Estate Ledger | Accepted | **Implemented** — #51–#57; running against a live workspace |
| [008](008-relationship-views.md) | Relationship Views | Accepted | Implemented — families/person/initiatives/convergence/about views, encounter observer, compiled executor (#62, #65, #66, stage 3 PR) |
| [009](009-tool-surface-recomposition.md) | Tool Surface Recomposition | Proposed | Not started — 17 tools → 8 by the verb/noun/parameter assignment rule; ships at v2.0.0, supersedes #49 |
| [010](010-batch-executor.md) | The Batch Executor | Proposed | Design only — one-shot read batches + saved playbooks; next session implements |

### Auth — how tokens are obtained

| ADR | Title | Status | In the code |
|---|---|---|---|
| [002](002-browser-token-extraction.md) | Browser-Automated Token Extraction | Draft | **Partial** — see arrears |

### Delivery — language, distribution, tool set

| ADR | Title | Status | In the code |
|---|---|---|---|
| [001](001-slack-mcp-fusion.md) | Slack MCP Fusion | Proposed | **Partial** — see arrears |

## Arrears

Where a decision and the code disagree. This section exists because ADR-003 was invisible
enough to be rediscovered from scratch and filed as issue #47.

**ADR-003 shipped its additive half only.** `poll`, `read`, and `ack` are registered;
`pkg/watermark/` implements the watermark including `Prune`. The five tools ADR-003 retired —
`catch-up`, `check-unreads`, `check-mentions`, `check-timing`, `get-context` — are all still
registered and undeprecated. Sixteen tools where the ADR decided eleven. Tracked in **#49**.

Consequences of that gap, all observed in one test-drive: an agent selecting by natural
phrasing lands on `catch-up`, which ADR-003 retired; it then reads `catch-up`'s "marking as
read" guidance, which sits directly above `// TODO: Actually mark as read` and describes an
effect that never occurs; and it cites the 177-channel figure from #24, which ADR-003's live
probe corrected to 30.

**ADR-002 shipped Tiers 2 and 3.** The Firefox extension flow and the manual flow are in
`pkg/setup/`, alongside keyring-based cookie extraction for Linux, macOS, and Windows. Tier 1 —
Chrome DevTools Protocol via `go-rod/rod` — is absent; neither `rod` nor `chromedp` is in
`go.mod`. Status `Draft` is accurate.

**ADR-001 specifies ten tools.** Sixteen are registered. `CLAUDE.md`'s tool table still lists
ADR-001's ten, so it never picked up `poll`, `read`, `ack`, `list-users`, `download-file`, or
`auth-setup` either. Both land on eleven via #49.

## Concerns with no ADR

Named so they are not rediscovered:

- **Endpoint degradation.** The product rests on undocumented internal endpoints. ADR-003 lists
  their drift as a risk and decides no policy — today a shape change produces an empty result
  rather than a loud one.
- **Token storage at rest.** ADR-002 covers extraction. Storage is undecided, and ADR-007's
  indefinitely-retained estate ledger sharpens it further than ADR-006 already had — issue #15.
- **Multi-workspace.** Issue #14. The watermark is already keyed by workspace and ADR-005's
  rings are workspace-scoped; the key shape is a live dependency in two ADRs and decided in
  neither.
- **Distribution and release.** Issues #20 and #25. `docs/release-runbook.md` records steps,
  not the decisions behind the npm-wrapper pattern, the cross-compile matrix, or OIDC.

## Conventions

Numbered sequentially, never renumbered — issues and commits cite ADR numbers. Format is
`# ADR-NNN: Title`, then `## Status`, `## Context`, `## Decision`, `## Consequences`, and
`## Related` where an ADR depends on another.

Keep them small enough to finish. ADR-003 carries seven decisions under one status, which is
how half of it shipped and the rest became arrears: a decision with no natural completion point
has no way to be marked done.

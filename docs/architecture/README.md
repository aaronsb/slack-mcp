# Architecture Decisions

Eleven ADRs, and one argument running through the last nine.

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
- **Writes: few, named, explicit.** One tool contributes content to Slack; one fires read
  receipts; `dismiss` moves only the agent's private position.

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
| [003](003-resolvable-tool-surface.md) | A Resolvable Tool Surface | Accepted | Implemented — `poll`/`read`/`ack` live on as `inbox`/`messages`/`dismiss`; the five tools it retired were removed by ADR-009 |
| [004](004-self-contained-payloads.md) | Self-Contained Payloads | Proposed | **Partial** — tag resolution on the render path (#59) and the renderMessage seam with its unresolved field (#73) shipped; unresolved tags are reported as data but still render raw in markdown |
| [005](005-identity-resolution.md) | Identity Resolution | Proposed | **Partial** — the resolution ladder runs behind search from: (#56); rings, encounters, and the remaining person parameters are open |

### State — what persists and for how long

| ADR | Title | Status | In the code |
|---|---|---|---|
| [006](006-observation-ledger.md) | Observation Ledger and Folded Caches | Proposed, amended by 007 | **Partial** — its mechanisms shipped inside ADR-007's estate; the attention ledger is not started |
| [007](007-estate-ledger.md) | The Estate Ledger | Accepted | **Implemented** — #51–#57; running against a live workspace |
| [008](008-relationship-views.md) | Relationship Views | Accepted | Implemented — families/person/initiatives/convergence/about views, encounter observer, compiled executor (#62, #65, #66, stage 3 PR) |
| [009](009-tool-surface-recomposition.md) | Tool Surface Recomposition | Accepted | Implemented — shipped at v2.0.0; eight tools by the verb/noun/parameter assignment rule, pinned by `TestV2SurfaceIsExactlyEightTools`; superseded #49 |
| [010](010-batch-executor.md) | The Batch Executor | Accepted | One-shot read batches + saved playbooks + the frequency hint |
| [011](011-time-flows-down-the-page.md) | Time Flows Down the Page | Accepted | Every rendered message list is oldest-first; fetches stay newest-first so caps keep the newest |

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

**ADR-003's arrears closed at v2.0.0.** Its five retired tools stayed registered for months
alongside their replacements (#49), and one test-drive showed the cost: an agent landed on
`catch-up`, read guidance describing a mark-as-read effect that never occurred, and cited a
channel count the ADR's live probe had already corrected. ADR-009 removed them with the
recomposition. The implementation files still carry the old names — `catchup.go`,
`get_context.go`, `check_unreads_simple.go` — but nothing registers them as tools.

**ADR-002 shipped Tiers 2 and 3.** The Firefox extension flow and the manual flow are in
`pkg/setup/`, alongside keyring-based cookie extraction for Linux, macOS, and Windows. Tier 1 —
Chrome DevTools Protocol via `go-rod/rod` — is absent; neither `rod` nor `chromedp` is in
`go.mod`. Status `Draft` is accurate.

**ADR-001 specifies ten tools.** Nine are registered — ADR-009's eight plus ADR-010's
`batch` — and none carries an ADR-001 name. `CLAUDE.md` and the README document the current
nine. ADR-001's tool list is superseded rather than in arrears; its status stays Proposed
because the rest of the decision (Go, session tokens, the fusion of two prior servers) is what
the code implements.

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
- **Distribution and release.** Issues #20 and #25 landed OIDC publishing and the release
  workflows. `docs/release-runbook.md` records steps and the workflow comments record the
  publisher identity, but no ADR holds the decisions behind the npm-wrapper pattern, the
  cross-compile matrix, or OIDC. Issue #89 records the registry-publish race.

## Conventions

Numbered sequentially, never renumbered — issues and commits cite ADR numbers. Format is
`# ADR-NNN: Title`, then `## Status`, `## Context`, `## Decision`, `## Consequences`, and
`## Related` where an ADR depends on another.

Keep them small enough to finish. ADR-003 carries seven decisions under one status, which is
how half of it shipped and the rest became arrears: a decision with no natural completion point
has no way to be marked done.

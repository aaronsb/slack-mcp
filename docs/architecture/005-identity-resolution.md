# ADR-005: Identity Resolution

## Status

Proposed

## Context

ADR-003 requires that parameters be answerable from what the caller knows, and lists
person-as-channel among the references the server must resolve. ADR-004 requires that a
rendered body name every person it refers to. Both depend on a resolver that does not exist
as a named component, and the code that stands in for one fails in both directions.

### Two operations are conflated

**Identifier to name** is a map lookup. `provider/api.go:35` holds
`users map[string]slack.User` keyed by ID. `GetUsersContext` paginates internally, so
`GetUsersOptionLimit(1000)` is a page size and the map holds the whole directory. Nothing on
the render path reads it.

**Name fragment to identifier** is a match. `list_users.go:69-71` lowercases `Name`,
`RealName`, and `Profile.DisplayName` and calls `strings.Contains` on each, over the full
directory, unranked. A fragment like `dana` returns every Dana, Daniel, and Danielle in
arbitrary order, and `formatUsers` at `format.go:339` then drops the ID that
`list_users.go:86` had placed in the data map. The one documented path from a name back to an
identifier terminates in a list of names.

The tool description at `list_users.go:20` promises matching against "display name, username,
or email prefix". Email is never compared. This is the defect family ADR-004 names: prose
asserting a capability the code does not have.

### The directory is fetched once, ever

```go
ap.loadUsersFromCache()

if len(ap.users) == 0 {
    // No cached users, fetch from API
    if err := ap.fetchAndCacheUsers(ctx); err != nil {
```

`api.go:201-210`. A non-empty cache is never refreshed — no TTL, no revalidation. The next
line starts `go ap.backgroundBackfill(ctx)` for channels; users have no equivalent. Anyone who
joins the workspace after the first run is invisible until the cache file is deleted by hand.

### The server does not know who it is

`AuthTest()` is called at `check_unreads.go:78`, `mentions_real.go:56`, and
`check_unreads_real.go:72`. Each call builds a self-mention pattern and discards the result.
The authed identity is never held, so no rendering path can mark a mention as the user's own.

### What this cost in use

A briefing agent (issue #44) read a message assigning a workstream to `<@U0AAAAAAAAA>` and
could not determine whether that was its own user. It could not resolve the identifier, and
the tool documented to resolve it returns no identifiers. The briefing ended in a hedge, and
the human answered a question the server held the answer to.

### Approaches considered and rejected

**One `resolve(query)` that handles both directions.** The two have different result types —
one answer versus a result set — different latency budgets, and different consequences when
wrong. A single entry point forces every caller to handle both shapes. Rejected.

**Fuzzy matching (edit distance, trigram) as the primary matcher.** Adds a threshold to tune
and produces confident wrong matches on short fragments, which is the failure mode this work
exists to remove. Rejected as the primary path; reconsider if substring plus recency shows
real misses in use.

**Periodic full-directory refresh.** Re-sweeps thousands of records to keep current the small
subset the agent will ever name. Rejected as the primary freshness mechanism.

**Require handles everywhere, never match names.** ADR-003's handle discipline works when the
referent came from a prior call. It cannot serve "DM Dana" for a Dana who has not appeared.
Rejected as a complete answer; retained where a handle exists.

**Match against a separate "known users" store.** The split is real but not a storage split —
one map already holds the directory. Building a second store duplicates data to express a
ranking difference. Rejected.

## Decision

### Two operations, named separately

| | `@` resolution | directory search |
|---|---|---|
| Intent | resolve to one person | show who matches |
| Result | a person, or bounded ambiguity | a result set |
| Network | never | permitted |
| Ranking | encounter recency | match quality |
| A miss means | not encountered — search | not in this workspace |
| Called from | any parameter naming a person | an explicit call |

### The `@` ladder

Precedence, evaluated in order, entirely against cached state:

1. **Exact handle** (`user.Name`) — resolve outright. Slack guarantees handle uniqueness, so
   this path never produces candidates.
2. **Exact display name or real name**, unique in the workspace — resolve.
3. **Prefix match** across handle, display name, and real name — return candidates ranked by
   encounter recency.
4. **No match** — return `resolved: false` with a reason and the search fallback named.

Step 4 is a result, not an empty list:

```json
{ "resolved": false, "reason": "not_encountered",
  "hint": "list-users query='dana' searches the full directory" }
```

### Self-contained means no network

`@` resolution issues no API call. It runs on the compose and render paths, where a network
round trip per name is not affordable, and its determinism is what makes it testable.

### Rings define "known"

One map serves both operations; the ranking universe is what differs.

| Ring | Membership | Size | Reached by |
|---|---|---|---|
| Encountered | appeared in traffic this agent has seen | dozens | `@` steps 1–3 |
| Affiliated | shares a channel, or has DM history | hundreds | `@` fallthrough |
| Directory | everyone else, plus recent joiners | thousands | search only |

Encounter history is recorded alongside ADR-003's watermark, which already tracks per
conversation what was surfaced. Participants are the addition.

### Directory search matches three keys, one of them exact

Display name and account name are substring matches producing a ranked result set. **Email is
unique, so it resolves or it does not, and never produces candidates** — `GetUserByEmail` is
one call and also reaches people absent from the cache entirely.

Email is an input key. Resolved output carries name and identifier; addresses are not rendered
into message bodies or briefings.

`list-users` returns the identifier its description already promises, accepts a batch
`resolve: ["U…", …]` form, and matches email as documented.

### Writes never auto-pick

A wrong resolution on a read path costs a round trip. A wrong resolution in `send-message`
mentions the wrong person in a message that has been sent.

Reads may take the top-ranked candidate. **Writes auto-resolve only from step 1 (exact
handle); every other outcome returns the candidate list.**

### Candidates carry why they matched

```
@dana
→ { ambiguous: true, candidates: [
      { id: "U0BBB", name: "Dana Okafor",    seen: "#all-hands, 2h ago" },
      { id: "U0CCC", name: "Dana Whitfield", seen: "#design, 3w ago" } ] }
```

ADR-003's candidate-list pattern, with the recency that produced the ranking. Two bare names
are not choosable; two names with recency almost always are.

### The users cache repairs from traffic

The directory is not swept to stay current. Every rendered message carries user identifiers,
and an identifier that misses the cache belongs to someone the agent is encountering now.

1. Miss on the render path — the body keeps the raw identifier and the response declares it in
   ADR-004's `unresolved` field.
2. That declaration is the repair queue. A single `users.info` resolves the identifier out of
   band and writes it back.
3. The next sight of that person resolves from cache.

The cache converges on the people who appear in the human's actual conversations, and
`unresolved` decays toward empty without a directory sweep. A slow background revalidation
covers profile drift for people already cached.

This ADR decides the users cache only. Cache lifecycle as a general contract — channels,
watermark, the XDG store — remains undecided.

### Session identity is resolved once and held

`AuthTest` runs once in the provider and its result is held, rather than fetched and discarded
at three call sites. ADR-004's `(you)` marking reads it.

### No new tool

Resolution is ADR-003's "Resolve it" disposition, which places it in the parameter rather than
in a new verb. `@dana` is valid wherever a person is named — `send-message`, `read`,
`search from:` — and ambiguity returns as a result. `list-users` remains the explicit
directory search.

The surface stays at ADR-003's eleven tools.

### Open probe

Slack's own composer performs live partial matching on `@` input, which implies a
server-side autocomplete endpoint. ADR-003's endpoint-findings table was produced by probing a
live workspace before deciding, and the same is owed here. If such an endpoint answers, it
supplies ring-3 matching with Slack's own ranking and the local cache becomes a fast path. If
it returns `unknown_method`, cache plus recency is the answer on evidence rather than
assumption.

## Consequences

### Positive

- "Is this me?" is answerable from cached state with no API call.
- A recent joiner is reachable by email or search rather than permanently invisible.
- `@` is deterministic and network-free, so its ladder is unit-testable against a fixture map.
- The wrong-person send is closed off structurally rather than by caution.
- `list-users` stops advertising an email match it does not perform.
- Cache freshness costs one `users.info` per newly-encountered person instead of a directory
  sweep.

### Negative

- Encounter history is new state on the watermark, which must be written, migrated, and
  bounded.
- The `@` ladder has four outcomes that every calling path must handle, against one today.
- Ring membership is invisible to the user. Why `@dana` resolved on one call and returned
  candidates on the next is explicable only through the `seen` field.

### Risks

- **Display-name collisions inside ring 1.** Two encountered people sharing a display name
  defeat recency ranking. The candidate list bounds the damage; step 1's handle path is the
  escape.
- **Stale profiles.** Repair-from-traffic fixes absence, not drift. Someone who changes their
  display name renders under the old one until background revalidation reaches them.
- **Encounter history as a tracking surface.** The watermark gains a record of who the agent
  has seen. It is local, under XDG, and subject to the same handling as the caches beside it.

## Related

- ADR-003 — the "Resolve it" disposition and the candidate-list result type this applies to
  people; the watermark this extends with participants.
- ADR-004 — the consumer. Rendered bodies call this resolver; its `unresolved` field is this
  ADR's repair queue.
- Issue #44 — the failure that prompted this.
- Issue #14 (multi-workspace) — encounter history and the users cache are both
  workspace-scoped, and their key shape is a dependency.

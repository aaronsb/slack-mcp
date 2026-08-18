# Contributing

## Before you push

```bash
make check
```

That runs formatting, `go vet`, the tests under `-race`, and a build — the same
target CI runs, so a green `make check` locally means a green CI.

## Working on tools

Tools live in `pkg/features/`. Each is a `Feature` with a schema and a handler,
registered in `pkg/server/semantic_server.go`.

Two rules from [ADR-003](docs/architecture/003-resolvable-tool-surface.md) that
are easy to break without noticing:

**A parameter must be answerable from what the caller already knows.** If a tool
needs a message timestamp, a cursor, or a channel ID, the caller cannot supply
it — the server resolves it, defaults it from stored state, or hands it back as
an opaque handle from a previous call. Required parameters are limited to things
a person would say out loud.

**Never claim a side effect you did not perform.** Guidance text is read by a
model as instruction. A tool that says "marked as read" had better have marked
something as read; a tool that offers `focus='threads'` had better honour it.
Both of those were real bugs here.

## Testing

`pkg/slacktest` is a fake Slack host serving the public API and the internal
endpoints. It needs no credentials:

```go
srv := slacktest.New(t)
srv.Handle("client.counts", func(*http.Request) any { ... })
ap := srv.Provider(t)
```

Fixture shapes come from real captured responses, including the quirks the
design depends on — `latest` present on read conversations, `last_read`
occasionally ahead of `latest`. If you change them, change them to match Slack,
not to make a test pass.

When a test measures how many requests something made, quiesce first — the
provider warms caches in background goroutines:

```go
ap.Provide()
srv.Quiesce(t)
srv.ResetCalls()
```

**Check your tests fail.** Break the thing on purpose and confirm the test
catches it. Several bugs here survived a full green suite because the test
exercised a neighbouring path; two of them were found by mutating the code and
watching nothing fail.

## Decisions

Anything that shapes the tool surface belongs in an ADR under
`docs/architecture/` before the code, not after. Read
[ADR-003](docs/architecture/003-resolvable-tool-surface.md) first — it is the
current design and most tool work is downstream of it.

## Pull requests

One increment per PR, with the reasoning in the commit message rather than only
in the diff. Say what you decided against as well as what you did; the next
person reading it usually wants to know why the obvious approach was not taken.

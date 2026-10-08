# ADR-016: Deferred and Withdrawn Contributions

## Status

Accepted

Amends ADR-009: the surface stays at its registered eleven tools. Extends
ADR-003's endpoint findings with the drafts API, and ADR-013's binding with
the send time. Decided on a live probe (2026-10-07, #101).

## Context

An agent cannot send later. "Post the release notes to #eng at 9 tomorrow"
needs either a process that stays alive until 9 or a person who remembers.
Slack already schedules messages on its own servers: the message is sent from
the person's account at the chosen time whether or not any client is running.

The public method, `chat.scheduleMessage`, rejects session tokens. The Slack
client's own path is the internal drafts API, and it accepts them. The probe
established its shape:

- `drafts.create` takes a rich_text block, a destination (`channel_id`, an
  optional `thread_ts`, and `broadcast`), and `date_scheduled` in Unix
  seconds. A `reply_broadcast` key is silently dropped.
- Slack refused a send time 60 seconds out about half the time, with its
  clock and ours agreeing to the second.
- `drafts.list` returns scheduled drafts and the person's unsent composer
  drafts together; a scheduled draft is one with `date_scheduled` set that is
  neither sent nor deleted. The list has no cursor and reports `has_more`.
- `drafts.delete` wants the draft's `last_updated_ts` with its fraction
  padded to seven digits, and refuses six with `draft_has_conflict`.
- A fired draft reads `is_sent` and leaves the active list. It arrives as a
  normal message from the person, with its rich_text intact.
- A file ID that was uploaded but never shared is refused (`file_not_found`).

Scheduling brings three capabilities that ADR-009's assignment rule has to
place: schedule a message, see what is scheduled, and cancel one. It also
raises a question of integrity: Slack lets the person edit a scheduled message
after the agent scheduled it.

## Decision

### Scheduling is a scope parameter on `say`

`say … at=<time>` has the same effect as `say` (content posted under the
person's name, visible in Slack) with the time moved. A parameter that
windows an existing call is the parameter rung of ADR-009's rule, so no verb
is added.

`at=` runs every step ADR-013 runs for an immediate send, in the same order,
before the draft is created: the strike lock, the local checks, the
destination's quarantine, the scanner, and the approval gate. What Slack sends
later is what the scanner read now.

The send time is part of what an approval binds to. An approval for a message
sent now does not let the same text through as a scheduled one, and an
approval for one time does not cover another.

Accepted forms: RFC 3339 with an offset or `Z`, an ISO date and time with no
offset, and Unix seconds. A time with no offset is read in the person's Slack
profile zone when that zone and the server's local zone have the same offset
at that instant. When they differ the time is refused, and the error names
both zones and asks for an offset; the server never guesses which one was
meant. The bounds are now + 2 minutes to now + 120 days, checked before any
Slack call.

`at=` is refused with `files` (Slack refuses unshared file IDs) and with
`emoji` (a reaction is not a message). `broadcast` is carried as the
destination's `broadcast` key.

### Seeing what is scheduled is a read mode on `messages`

`messages scheduled=true` lists pending scheduled messages, soonest first, and
`target=` narrows it to one conversation. A scheduled message is
conversation content with a destination and a future time, so it belongs to
the conversation-content noun. `inbox` is rejected as its home: every inbox
view is inbound and is cleared with `dismiss`, and a scheduled message is
neither.

The mode is read-only: it never marks anything read and never writes a draft.
ADR-010 admits it to `batch` without change.

### Cancelling is a parameter on `say`

`say cancel=<handle>` withdraws a pending scheduled message. Withdrawing your
own pending contribution belongs to the same effect as making it, as
`remove=true` does for a reaction. A separate verb would pay for itself only
if a client needed to allow cancelling while denying posting, and ADR-009
already gave up that granularity for reactions.

Cancelling runs the strike lock. It runs no scanner and no gate: it removes
content and sends none.

### The server touches only scheduled drafts

Listing and cancelling act only on drafts that are scheduled, pending, and not
deleted. The person's unsent composer drafts are never shown, read into a
result, or deleted.

A scheduled message is named by a handle of kind `s` carrying the
conversation and the draft. Draft IDs never reach the agent.

### An edit made in Slack is reported, never blocked

The person can edit or reschedule a scheduled message in Slack. That content
is the person's, and blocking it would turn the agent's tool into a check on
the person. The server records what it scheduled (the draft's last update, its
time, and a hash of its blocks; no message content) in the workspace's state
directory. The list compares each pending draft against that record and says
which of three it is: scheduled here and unchanged, scheduled here and edited
in Slack since (naming whether the time or the content changed), or scheduled
from Slack.

### Every scheduled time is absolute

A scheduled time renders as a date and time with its zone abbreviation and
UTC offset, in the profile zone, plus the distance from now. The echo carries
the caller's literal `at=` value beside the resolved time, so a zone mistake
is visible in the result that made it.

### A list without a cursor says when it is cut

When `drafts.list` reports `has_more`, the result says that Slack offers no
next page and more scheduled messages may exist. ADR-009's rule is that caps
always page; where the source cannot page, the output states the gap instead.

### Alternatives considered

- **`inbox view='scheduled'`.** Rejected: inbox is inbound and has dismiss
  grammar, and a scheduled message is outbound and cannot be dismissed.
- **A `schedule` verb.** Rejected: a tenth tool for an effect `say`
  already has.
- **An `unsay` or `cancel` verb.** Rejected for the permission reason above.
- **A local scheduler process.** Rejected: it dies with the host, and Slack
  already schedules on its own servers.
- **Block a send whose draft changed after scheduling.** Rejected: only the
  person's own account can edit the draft, and the content scanned at
  scheduling time was the agent's. A change afterwards is the person's.
- **Read a time with no offset as UTC, or as the server's zone alone.**
  Rejected: a remote host's zone can differ from the person's, and a
  silent wrong guess fires at the wrong hour with nobody watching.

## Consequences

### Positive

- An agent can send later with no process kept alive.
- The surface does not grow; `say` gains two parameters and `messages`
  one.
- A scheduled send passes the same outbound safety as an immediate one, and
  an approval names its time.
- The person's own drafts are invisible to the server.

### Negative

- The drafts API is undocumented and internal; ADR-003's endpoint-drift risk
  applies. A changed shape fails the call; it does not fall back to an
  immediate send.
- The scanner reads the content at scheduling time. Content the person edits
  into the draft later is never scanned.
- The edit record is local. A draft scheduled through this server from
  another machine lists as scheduled from Slack.
- The strike lock stops new sends; it does not reach drafts already
  scheduled, which Slack sends on its own. The lock also refuses
  `say cancel=`, so while it is engaged the operator cancels from Slack's
  Scheduled list.

### Risks

- `drafts.delete`'s seven-digit padding is a quirk, not a contract; if Slack
  changes it, cancelling fails visibly.
- Scheduling into a conversation where the person has an unsent composer
  draft fails with `attached_draft_exists`. The probe could not run that case
  without the person at the keyboard; the server reports the error and
  suggests a thread.
- Slack's real lead-time floor is near 60 seconds; the 2-minute bound sits
  above it, and a change on Slack's side surfaces as `time_in_past`.

## Related

- ADR-003: endpoint findings, the drafts rows
- ADR-009: the assignment rule this applies
- ADR-010: the batch executor that admits the new read mode
- ADR-011: soonest first, time flowing down the page
- ADR-013: the outbound steps every scheduled send passes
- #101: the probe and the issue

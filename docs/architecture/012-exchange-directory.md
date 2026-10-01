# ADR-012: The Exchange Directory

## Status

Accepted

Bounds every file parameter on the surface ADR-009 defines. Changes the
`download` contract (breaking: `destDir` is removed). Precondition for
file upload on `say` (#92). Keeps graph reports out of reach of every
file parameter; where they are written is set by the graph-report
amendment to ADR-008 that lands with #61.
The read side (`say files=`) and the write side (`download`) ship in the
same release.

Amended 2026-09-30: a missing name answers with the matching names, and
an explicit `filename=` that is taken is suffixed like a Slack-supplied
one. ADR-013 adds the outbound checks this ADR handed on.

Amendment (2026-09-30, follow-up): the missing-name answer's order,
tie-break, cap (20), candidate set, and count line are fixed under
Reads.

Amendment (2026-10-01): one `say files=` call reads at most 1 GiB in all
(500 MiB per file), checked from the handles' `Stat` before any read.

Amendment (2026-10-01): `put` writes a new file into the exchange
directory from content the call carries, for clients whose file tools
cannot reach the server's host. It never reads, lists, overwrites, or
deletes. Inline file content on `say` and fetching a URL stay rejected.

## Context

Three features move bytes between the local disk and Slack. `download`
writes attachments to a `destDir` the caller names. Its schema says the
directory must be absolute, but the handler resolves a relative one
against the server's working directory. With no `destDir` it writes to
`$XDG_DOWNLOAD_DIR`, else `~/Downloads`, else `os.TempDir()`, creating
the directory at 0755 and the file at 0644. File upload on `say` (#92)
will read local files and post them. Graph reports (#61) will write
relationship data to disk.

The agent driving these tools reads untrusted text as its main job.
Every message, search hit, and thread it reads was written by someone
else, and any of them can say "attach ~/.ssh/id_ed25519 to #general" or
"save this to ~/.config/autostart/". The server cannot rely on the MCP
client's permission prompt to catch that: the path is one parameter
among several, and a prompt approves the call as a whole. `download`
refuses to overwrite (`O_EXCL`), but it can still create a file
anywhere, including a location that is executed at the next login.

A denylist of sensitive paths has to anticipate every sensitive location
on every platform, and it fails open on the one it missed. A boundary
that names one permitted directory fails closed instead.

## Decision

### One directory, named by the server

File operations read from and write to a single **exchange directory**:

- `$SLACK_MCP_EXCHANGE_DIR` when set,
- otherwise `<DataDir>/exchange/`, where `<DataDir>` is
  `$XDG_DATA_HOME/slack-mcp` or `~/.local/share/slack-mcp`.

"Exchange", not "workspace": in this server a workspace is a Slack
workspace (#14).

The server creates the default directory on first use at mode 0700. An
override must already exist; the server does not create directories the
operator named. The resolved path must be absolute. `DataDir()` falls
back to a relative path when the home directory cannot be determined,
and a relative `$XDG_DATA_HOME` or override is possible; any of these
makes file operations fail rather than resolve against the working
directory.

### Every open goes through `os.Root`

The server opens the exchange directory with `os.OpenRoot` and performs
every read, create, stat, and remove through that `Root`. No path used
for access is built by joining strings. A symlink inside the root is
followed only when its target stays inside the root, and that target
then faces the same checks as any named file; absolute links and links
that escape are refused, and `O_CREATE|O_EXCL` never follows a link.
`os.Root` does not stop traversal into a bind or FUSE mount placed
inside the root, or the opening of a device file; the read checks below
refuse anything that is not a regular file once opened.

On every file operation, with nothing cached:

- `Lstat` of the exchange path must report a plain directory. A
  symlink, a Windows junction or other reparse point (`ModeSymlink` or
  `ModeIrregular`), or any other type is refused.
- On Unix, `root.Stat(".")` must be the same file as that `Lstat`
  result (`os.SameFile`, which compares device and inode eagerly on
  Unix), so a directory swapped in between the `Lstat` and the
  `OpenRoot` is refused. The same `Stat` must show the directory owned
  by the current user with no group or other permission bits. A looser
  mode is refused, not repaired; the error names the rule and the
  `chmod` that satisfies it.
- On Windows there is no ownership, mode, or identity check here; the
  default location sits inside the user profile.

A refused exchange directory makes every file operation fail with an
error naming the rule. The server still starts, since the read tools do
not depend on it.

### The override is a foot-gun guard, not the boundary

The override is operator configuration, taken only from the process
environment the MCP client sets. Injected content cannot set that
environment, and ADR-014 refuses to start when a `.env` file supplies
any key outside its allowlist, so the threat this ADR addresses cannot
move the directory through configuration. The override checks catch an
operator pointing it somewhere that makes bare names dangerous. They run on every file operation, like
the checks above.

They compare by file identity, not by string. Each path is resolved
with `filepath.EvalSymlinks`. "The override is inside X" is decided by
walking the override's resolved parents and comparing each to X; "the
override is an ancestor of X" is decided by walking X's resolved parents
and comparing each to the override. Comparisons use `os.SameFile` on
`Stat` results, which survives case-insensitive filesystems, symlinked
ancestors, and `..` spellings. On Windows `os.SameFile` reopens paths
lazily, so it compares paths as they are at comparison time. That is
acceptable here, where the inputs are the operator's own configuration
and nobody is racing them; it was not acceptable for per-read checks,
where an attacker controls the timing. The override is refused when it:

- is `$HOME`, or an ancestor of the data directory or the config
  directory;
- is the data directory or inside it, other than the default exchange
  path;
- is the config directory or inside it;
- is inside `$HOME` and its path below `$HOME` has a component that
  begins with a dot (`~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/...`);
- is the user's desktop, documents, or downloads directory
  (`$XDG_DESKTOP_DIR`, `$XDG_DOCUMENTS_DIR`, `$XDG_DOWNLOAD_DIR`, else
  `~/Desktop`, `~/Documents`, `~/Downloads`).

On Unix the ownership and mode rule also refuses most other shared or
well-known directories, which are rarely 0700. It also limits who can
use the directory: a client running under another UID or in a container
cannot reach a 0700 directory the server owns, and mounted volumes that
present 0755 or 0777 (WSL drvfs among them) are refused. Cloud-sync
mounts cannot be detected reliably; pointing the override at one is the
operator's choice. On Windows the list is the whole guard.

### File parameters take names, not paths

Every file parameter on the surface is a bare name resolved inside the
exchange directory. One rule applies on every platform, so a name that
works on one host works on all of them. A name is refused when it:

- is empty, `.`, or `..` (the exact component; `notes..txt` is fine);
- fails `filepath.IsLocal`, or is not a single path component (`/` and
  `\` are refused on every platform);
- contains `:` (Windows alternate data streams and drive-relative
  paths), a control character (U+0000–U+001F), or any of `<>"|?*`;
- ends in a dot or a space;
- has a stem, before the first dot and with trailing spaces removed,
  that is a Windows device name compared case-insensitively: `CON`,
  `PRN`, `AUX`, `NUL`, `CONIN$`, `CONOUT$`, `COM1`–`COM9`,
  `LPT1`–`LPT9`, or `COM`/`LPT` followed by `¹`, `²`, or `³`. This
  holds with any extension, although Windows 11 accepts `CON.txt`;
- is longer than 255 bytes of UTF-8.

`filepath.IsLocal` checks device names only on Windows, so the server
checks them itself.

### Reads

`say files=` validates and opens every named file before anything is
posted. A malformed name, a missing file, or a file that fails a check
fails the whole call with zero Slack calls; the message text is not
sent.

Each name is opened through the root, read-only, with `O_NONBLOCK` on
Unix so a FIFO swapped in after any earlier check cannot block the
open. Every check then uses the open handle, never an earlier path
lookup: `Stat` on the handle, and on Windows `GetFileInformationByHandle`
for the link count.

- It must be a regular file.
- It must have exactly one link (Unix `Stat_t.Nlink`; Windows
  `NumberOfLinks`). A file is refused while another name for the same
  inode exists; the error says to copy the file in instead. Once the
  other name is removed, or replaced by a rename, the remaining name has
  one link and reads normally. The rule catches a link left in place,
  not a link made and then cut loose; the defense against that is that
  making the link is a separate client-side action.
- Size and count limits are checked from that `Stat`, and the read is
  bounded by the limit, since the file can grow after the check.

A missing name answers with the files in the directory whose names
match the one requested. A stem is the name without its extension, the
extension as defined under Writes, so the stem of `.env` is `.env` and
no valid name has an empty stem. Names are compared under Unicode simple
case folding, the folding `strings.EqualFold` uses. A name matches when
it contains the requested name's stem, or when its own stem is contained
in the requested name. The call still makes zero Slack calls and posts
nothing; the agent retries with a listed name.

The candidates are the directory's entries that are regular files or
symlinks, as `ReadDir` reports them without following links, and whose
names pass the bare-name rule; a name the agent could not pass back is
never offered. Matches are sorted by a fold key, each rune replaced by
the least rune of its `unicode.SimpleFold` orbit, so names that
`strings.EqualFold` calls equal get the same key. Keys compare by byte
order; names with equal keys (`Report.pdf`, `report.pdf`) compare by the
byte order of the names themselves. The first 20 are listed, under a
count line:

```
No file named budget.xlsx is in the exchange directory. 3 names match:
No file named budget.xlsx is in the exchange directory. 47 names match; the first 20 by name:
No file named budget.xlsx is in the exchange directory, and no name matches it.
```

The first form is used when the list is complete, the second when it is
capped. Each missing name in a `say files=` call gets its own count line
and list, capped separately. A directory that cannot be listed gives the
plain miss and names the listing error.

The answer is a correction aid, but an agent can enumerate the
directory with it by varying the requested name: a one-letter stem
matches every name containing that letter. Each answer is still capped,
so a large directory takes many failed calls to list. That departs from
ADR-009's rule that a cap never bounds reachability, and is accepted for
a hint whose job is to fix a near miss; a page of it would need a next
call through `say`, a verb that posts to Slack.

A sandboxed client that cannot see the server's filesystem has no
direct way to list the exchange directory in v1, only the enumeration
above; `put` writes into it and does not list it. If one is needed, a read-only listing is a view or parameter on a
noun under ADR-009, not a verb, and is deferred.

### Writes

`download` and `put` create their files through the root with `O_EXCL`
at mode 0600.

A caller's `filename` is validated before any Slack call, and `put`'s
`name` before anything else. A malformed
one is refused, not sanitized: a name that fails the bare-name rule is
the shape of an attempt to reach outside the directory, and silently
repairing it would hide that.

Without `filename`, the server sanitizes Slack's `file.Name` into a
valid bare name: each refused character becomes `_`, trailing dots and
spaces are dropped, a device-name stem gets a leading `_`, an over-long
name is truncated at a rune boundary keeping its extension, and an empty
result falls back to the file ID.

When the name is taken, whether the caller gave it or Slack did, the
server tries a suffix before the extension, where the extension is what
`filepath.Ext` returns unless the only dot is the leading one:

| Name | Suffixed |
|---|---|
| `report.pdf` | `report (1).pdf` |
| `README` | `README (1)` |
| `.env` | `.env (1)` |
| `archive.tar.gz` | `archive.tar (1).gz` |

It tries the name and up to 99 suffixes, 100 attempts in all, then
fails naming the collision and suggesting a `filename=` (for `put`, a
`name=`) that is free. A
suffixed name that would exceed the length limit truncates the stem.

The output reports the name used and the absolute path. A suffixed name
is stated as a rename: `budget.xlsx existed; saved as budget (1).xlsx`.
The path is for display; the server never opens anything by it.

### `download` writes only to the exchange directory

The `destDir` parameter is removed; a call that passes it fails with an
error naming the removed parameter and the exchange directory. Files
and the exchange directory are created at 0600 and 0700, a deliberate
change from the current 0644 and 0755.

### Reports live beside it, never inside

Graph reports are written to `<DataDir>/reports/`. The exchange root is
a separate `os.Root`, the override may not be the data directory or any
directory inside it other than the default exchange path, and `os.Root`
refuses a symlink out to `reports/`. A hard link to a report is refused
while the report's own name exists. Sharing a report in Slack takes a
deliberate copy into the exchange directory.

### Getting files in and out is the client's job

Copying, moving, and deleting local files is what the agent's client
already provides, under its own file permissions and its own prompts.
A server tool for those would duplicate the client's capability under
the server's broader filesystem access, and would move the decision to
touch a file outside the exchange directory from the client's prompt to
a parameter on this server.

The exception is a client whose file tools cannot reach the server's
host: a code sandbox on another machine, or a server in the remote
deployment. For it the client's job cannot be done, so `put` writes a
new file from bytes the call carries (amendment 2026-10-01). It touches
nothing outside the exchange directory and nothing already in it.

ADR-009's verb test (a capability is its own tool only if invoking it
changes the world) does not decide this: copying and deleting files do
change the world. The ground is that the change belongs to the client.

## Amendment (2026-09-30): misses and collisions

Two failures that cost the agent a round trip without protecting
anything now answer in the same call. The Reads and Writes sections
above carry the amended rules.

### A missing name lists its matches

Before: the names closest to the one requested, by an unstated
distance. After: the names that match it by stem under the rule in
Reads, sorted by fold key, the first 20 listed under a count of all
matches. A
match rule the agent can predict is one it can act on; a distance
ranking was a second guess at what the agent meant. The rule also lets
the agent enumerate the directory, as Reads states.

### An explicit name that is taken is suffixed

Before: an explicit `filename=` that was taken failed, on the ground
that a changed name would hide the caller's mistake. After: it takes
the same `name (n).ext` suffix as a Slack-supplied name, bounded at the
same 100 attempts, and the output states the rename in the words of the
example above. The plain statement keeps the collision visible, which
was the point of refusing; the refusal added only a retry in which the
agent picked the suffix itself.

Malformed explicit names are still refused before any Slack call, for
the reason under Writes.

### Still no rename, move, or delete verb

Unchanged: renaming or deleting a file in the exchange directory is the
client's job, as above. The operator may revisit this.

## Amendment (2026-10-01): put

A client whose code sandbox runs on another machine generates a file
there, a chart image for instance. Its file tools cannot reach the
server's host, so the file never reaches the exchange directory and
`say files=` can never attach it. The server in the remote deployment
puts every client in the same position.

Before: getting a file into the exchange directory was the client's job
alone, and the only server tool that wrote there was `download`, which
writes what Slack serves. After: `put` writes a new file from content
the call carries.

### What `put` does

- `name` is checked by the bare-name rule before anything else. A
  malformed name is refused, not sanitized, for the reason under
  Writes, and nothing is created.
- It takes exactly one of `content`, UTF-8 text written as given, or
  `base64`, the standard alphabet with padding optional and line breaks
  ignored. A payload in the URL-safe alphabet is refused by name rather
  than guessed at, and invalid base64 is refused. Both, or neither, is
  refused.
- The decoded content is at most 5 MiB and not empty, checked before the
  directory is opened. The content travels through the model as tool
  call tokens, a third larger as base64; a larger file is the client's
  or the operator's to move.
- The file is created through the root with `O_CREATE|O_EXCL` at 0600,
  suffixed when the name is taken, with the rename stated as under
  Writes. A failed write or close removes the name and says nothing was
  kept; when the removal fails too, the result says the partial file is
  still there.
- The result gives the name used, the byte count, the display path, and
  the next call, `say to='<destination>' files=['<name>']`.
- It makes no Slack call. It still answers with setup guidance before
  credentials exist, as every tool but `auth` does: its only use is to
  feed `say`, which needs them.
- It is a verb under ADR-009, since it changes the world, and `batch`
  refuses it, admitting only the read nouns (ADR-010).

It is not restricted by deployment or transport the way `unlock` is: a
file written on the server's host is the point, not a hazard.

### What `put` does not do

- **Read.** It returns no bytes. A read would give a client that cannot
  see the server's filesystem a route to every file in the directory,
  downloads and the operator's own files included, under no client
  prompt.
- **List.** Listing stays deferred as under Reads, and belongs on a
  noun, not on a verb that writes.
- **Overwrite.** A taken name is always suffixed. Replacing a file would
  let injected text swap the bytes of a file the operator or a download
  placed, between the time it was checked and the time it is sent.
- **Delete.** Removing files stays the client's and the operator's job.
  A delete would let injected text destroy files no prompt covers.
- **Take a path.** `name` is a file parameter like any other, bound by
  the bare-name rule.
- **Fetch a URL.** Fetching would make the server an HTTP client for an
  address the agent read in untrusted text: server-side request forgery
  against loopback and the local network, the server's own SSE port and
  `unlock` page among them, and bytes that never passed through the
  agent's context.

### Inline content on `say` stays rejected

The operator rejected `files=[{name, base64}]` on `say`. Inline content
would make `say` a one-call conduit that carries arbitrary
agent-composed bytes straight to Slack, with no artifact at rest and no
second step. `put` leaves a named file on the operator's disk first, and
the send stays a separate `say` call that names the file and faces every
check ADR-013 runs.

### Threats

- Injected text can now cause a file to be written in the exchange
  directory. The write is bounded: the directory is 0700 and the
  user's; the call names a file and cannot name a place; `O_EXCL` means
  it never replaces anything; the file is 0600 with no execute bit; one
  call writes at most 5 MiB; and nothing in the server executes a file
  from the directory, and only `say` reads one.
- A planted file reaches Slack only through a `say` that names it, and
  that call runs the strike lock, the local checks, the destination's
  quarantine, the secret scanner, which reads each file's name and
  bytes, and the approval gate (ADR-013).
- `put` records no provenance. Gate case 2 looks attachments up by the
  SHA-256 of their bytes, so a `put` file whose bytes equal a recorded
  download matches that record and is gated as a move. Bytes that differ
  have no record and get the scanner only, as an operator-placed file
  does; case 1 still gates an external destination. Content the agent
  read in one conversation can therefore reach another as a `put` file
  without approval, but the agent could already send that content as
  `say text=`, which case 2 never covers, and ADR-013 accepts that a
  one-byte edit defeats case 2. `put` opens no route around case 2 that
  text did not already have.
- An injected `put` can take a name first, so a later `download` of the
  same name is saved suffixed. The download states the rename and
  reports the name used; an agent that attaches the name it expected
  instead sends the planted file, which still faces the checks above.
- Files accumulate faster: nothing removes what `put` writes, and
  repeated calls are bounded only by the call rate. The directory is the
  operator's to clean.

## Consequences

### Positive

- An injected "attach this credential" or "save this there" cannot be
  expressed through any tool parameter. Reaching a file outside the
  directory takes a separate client-side copy that the client's own
  permissions govern.
- One rule covers every current and future file feature, instead of a
  per-feature path policy.

### Negative

- `download` callers that pass `destDir` break, and files no longer land
  in `~/Downloads`. The release notes call it out.
- Uploading a file takes two steps when it lives elsewhere: copy it in
  (or, from a client that cannot reach the directory, `put` it), then
  `say files=`.
- A file hard-linked into the exchange directory is refused on read
  while its other name exists.
- Files accumulate. Nothing is deleted automatically; the directory is
  the operator's to clean.
- A sandboxed client that cannot see the server's filesystem can list
  the exchange directory only through failed `say files=` calls.
- When the server runs on a different host from the client (the
  experimental remote deployment, or SSE bound to a non-loopback host),
  the exchange directory is on the server's host and the agent's file
  tools are on the client's. The agent can write a file in with `put`
  (up to 5 MiB) but cannot take one out; the operator transfers
  downloads, and the paths `download` reports name the server's host,
  which the output states. Accepted for v1.

### Risks

- A future file parameter could accept a path. Tests assert the
  bare-name rule on every file parameter, and a planted `../`, an
  absolute path, a `:` name, and a device name must fail with zero Slack
  calls.
- The agent's own file tools can still copy a sensitive file into the
  exchange directory if the client permits it. The server cannot see
  that step; the defense is that it is a separate, client-visible
  action.
- Download-then-attach moves a file between conversations entirely
  through tool parameters: `download` from a private channel, then
  `say files=` to a public one. This ADR bounds where files live, not
  where they go. ADR-013 decides where they go: a secret scan on every
  `say`, quarantine of the destination on a match, and operator approval
  for a file that moves between conversations in `strict`, unless the
  destination is trusted for cross-conversation moves; in `soft` a move
  is a warning.
- Some filesystems report a link count of 1 for every file, which
  disables the hard-link rule on them.
- On macOS an ACL can grant other users access that the mode bits do
  not show, so a 0700 directory with an ACL entry passes the mode check.
- A bind mount or a directory hard link that root places onto the
  exchange path looks like a plain directory to these checks. Root is
  outside the threat model.
- On Windows the override guard is only the refusal list. It misses
  `%APPDATA%`, including the Startup folder, and folders OneDrive has
  redirected. OneDrive placeholder files surface as `ModeIrregular` and
  are refused, so that case fails closed.

### Reversibility

Re-adding a path parameter is a small code change that gives up the
security property. It cannot restore `~/Downloads` as the default
without moving where every adapted caller's files land, so it would be a
second breaking change.

## Alternatives Considered

- **Absolute paths with a denylist** (the first plan on #92): refuse the
  config and data directories and dot-directories under `$HOME`. Rejected
  because it fails open on every location it did not anticipate, and it
  leaves `download` able to create files anywhere.
- **Absolute paths within allowed roots** (`SLACK_MCP_UPLOAD_ROOTS`):
  configuration nobody sets, so the default decides, and a broad default
  such as `$HOME` minus dot-directories is the denylist again.
- **File content inline as base64 on `say`**: no filesystem boundary to
  defend, but multi-megabyte payloads cost tokens and hit message limits,
  and the agent still has to read the file with its own tools first.
  Rejected again in the 2026-10-01 amendment on the operator's ground
  that it makes `say` a one-call conduit for agent-composed bytes. Inline
  base64 is how `put` takes binary content, capped at 5 MiB.
- **A `manage_exchange` tool** (list, read, write, delete), as Confluence
  ADR-502 has: rejected for the reasons under "Getting files in and out
  is the client's job". `put` is its write half alone, create-only; list,
  read, and delete stay rejected.
- **`put` fetching a URL**: rejected in the 2026-10-01 amendment, for
  the reason stated there.
- **`os.Root` alone, without the directory checks**: `os.Root` bounds
  names inside the directory but not the directory itself. A symlinked
  or group-writable exchange directory, or one that is `~/.ssh`, passes
  `os.Root` untouched. Rejected as insufficient on its own.
- **Per-read lexical containment with `Lstat` and `os.SameFile`**:
  `Lstat` checks only the final component, and on Windows `os.SameFile`
  on an `Lstat` result reopens the path lazily, comparing the handle
  with the path as it is now rather than the snapshot an attacker could
  race. Rejected for `os.Root` plus checks on the open handle.
- **Dropping the override**: `$XDG_DATA_HOME` already relocates the
  exchange directory. It also relocates the caches, ledgers, and reports,
  so an operator who needs the exchange directory where a sandboxed
  client can reach it would have to expose all of that with it. Kept,
  with the guard above.

## Related

- ADR-009: the surface these parameters live on, its verb test, and its
  echo and paging laws.
- ADR-013: outbound safety. This ADR is its first layer: nothing outside
  the exchange directory can be named.
- ADR-008: as accepted, it defers graph reports to a localhost page
  embedded in the binary. The graph-report amendment to ADR-008 that
  lands with #61 writes them as files under `<DataDir>/reports/`; this
  ADR only fixes that they stay outside the exchange directory.
- Confluence Cloud ADR-502: the same staging-directory idea. It adds a
  `manage_workspace` tool, keeps base64 upload and download as
  alternatives (its workspace is optional), checks containment with
  `realpath`, and refuses a smaller set of override locations by path.
  This ADR makes the directory the only route, opens through `os.Root`,
  compares overrides by identity, checks ownership and mode on every
  use, and refuses hard links.
- Issues #92 (upload), #61 (graph reports), #14 (why not "workspace").

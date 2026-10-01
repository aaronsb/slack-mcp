# ADR-012: The Exchange Directory

## Status

Accepted

Bounds every file parameter on the surface ADR-009 defines. Changes the
`download` contract (breaking: `destDir` is removed). Precondition for
file upload on `say` (#92). Keeps graph reports (#61, placed by the
2026-09-30 amendment to ADR-008) out of reach of every file parameter.
The read side (`say files=`) and the write side (`download`) ship in the
same release.

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
every read, create, stat, and remove through that `Root`. No path is
built by joining strings, and no component of a name, symlink or not,
can resolve outside the root.

Before opening the root, on every file operation (nothing is cached):

- `Lstat` of the exchange path must report a plain directory. A
  symlink, a Windows junction or other reparse point (`ModeSymlink` or
  `ModeIrregular`), or any other type is refused.
- On Unix, a `Stat` through the opened root (`root.Stat(".")`) must
  show the directory owned by the current user with no group or other
  permission bits. A looser mode is refused, not repaired; the error
  names the rule and the `chmod` that satisfies it. Windows has no
  equivalent check here; the default location sits inside the user
  profile.

Swapping the directory between the `Lstat` and the `OpenRoot` requires
write access to its parent, which belongs to the same user whose file
tools could already place anything inside. The checks close the
misconfigured case, not that one.

A refused exchange directory makes every file operation fail with an
error naming the rule. The server still starts, since the read tools do
not depend on it.

### The override is a foot-gun guard, not the boundary

The override is operator configuration. Injected content cannot set an
environment variable, so the threat this ADR addresses cannot move the
directory. The override checks catch an operator pointing it somewhere
that makes bare names dangerous.

They compare by file identity, not by string: each path is resolved
with `filepath.EvalSymlinks`, and "is" and "is inside" are decided by
walking the resolved override's parents and comparing each to the
candidate with `os.SameFile` on `Stat` results. That survives
case-insensitive filesystems, symlinked ancestors, and `..` spellings.
The override is refused when it:

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

The per-use ownership and mode rule refuses most other shared or
well-known directories, which are rarely 0700. Cloud-sync mounts cannot
be detected reliably; pointing the override at one is the operator's
choice.

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

`say files=` opens each name through the root, read-only, with
`O_NONBLOCK` on Unix so a FIFO swapped in after any earlier check cannot
block the open. Every property is then taken from `Stat` on the open
handle, never from an earlier path lookup:

- it must be a regular file;
- it must have exactly one link (Unix `Stat_t.Nlink`; Windows
  `NumberOfLinks` from `GetFileInformationByHandle`), so a hard link to
  a file elsewhere on the volume cannot be uploaded by name. The error
  says to copy the file in instead;
- size and count limits are checked from that `Stat`, and the read is
  bounded by the limit, since the file can grow after the check.

A name that is not there answers with a correction hint: the names
closest to the one requested and the most recently modified, up to a
fixed cap, with the directory's total count. It is a hint attached to a
failed write, not a list. Paging it per ADR-009 would need a next call,
and the only call that reaches it is `say`, a verb that posts to Slack.

### Writes

`download` creates its file through the root with `O_EXCL` at mode
0600. The server sanitizes Slack's `file.Name` into a valid bare name:
each refused character becomes `_`, trailing dots and spaces are
dropped, a device-name stem gets a leading `_`, an over-long name is
truncated at a rune boundary keeping its extension, and an empty result
falls back to the file ID. A caller's `filename` override is refused,
not sanitized, and is validated before any Slack call. An explicit name
that came back changed would hide the caller's mistake.

When the name is taken, the server tries a suffix before the extension,
where the extension is what `filepath.Ext` returns unless the only dot
is the leading one:

| Name | Suffixed |
|---|---|
| `report.pdf` | `report (1).pdf` |
| `README` | `README (1)` |
| `.env` | `.env (1)` |
| `archive.tar.gz` | `archive.tar (1).gz` |

It tries the name and up to 99 suffixes, 100 attempts in all, then
fails naming the collision and suggesting `filename=`. A suffixed name
that would exceed the length limit truncates the stem. The output
reports the name used and its absolute path.

### `download` writes only to the exchange directory

The `destDir` parameter is removed; a call that passes it fails with an
error naming the removed parameter and the exchange directory. Files
and the exchange directory are created at 0600 and 0700, a deliberate
change from the current 0644 and 0755.

### Reports live beside it, never inside

Graph reports are written to `<DataDir>/reports/`. The exchange root is
a separate `os.Root`, the override may not be the data directory or any
directory inside it other than the default exchange path, `os.Root`
refuses a symlink out to `reports/`, and the link-count rule refuses a
hard link from it. Sharing a report in Slack takes a deliberate copy
into the exchange directory.

### Getting files in and out is the client's job

Copying, moving, and deleting local files is what the agent's client
already provides, under its own file permissions and its own prompts.
A server tool for those would duplicate the client's capability under
the server's broader filesystem access, and would move the decision to
touch a file outside the exchange directory from the client's prompt to
a parameter on this server. Listing is covered by the missing-name hint.
The surface stays at nine tools.

ADR-009's verb test (a capability is its own tool only if invoking it
changes the world) does not decide this: copying and deleting files do
change the world. The ground is that the change belongs to the client.

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
- Uploading a file takes two steps when it lives elsewhere: copy it in,
  then `say files=`.
- A file hard-linked into the exchange directory is refused on read.
- Files accumulate. Nothing is deleted automatically; the directory is
  the operator's to clean.
- A sandboxed client that cannot see the server's filesystem has only
  the missing-name hint to discover what is there.
- Under the experimental remote deployment, the exchange directory is on
  the server's host and the agent's file tools are on the client's host.
  The agent cannot put files in or take them out; the operator transfers
  them, and the paths `download` reports name the server's host, which
  the output states. Accepted for v1.

### Risks

- A future file parameter could accept a path. Tests assert the
  bare-name rule on every file parameter, and a planted `../`, an
  absolute path, a `:` name, and a device name must fail with zero Slack
  calls.
- The agent's own file tools can still copy a sensitive file into the
  exchange directory if the client permits it. The server cannot see
  that step; the defense is that it is a separate, client-visible
  action.
- Some filesystems report a link count of 1 for every file, which
  disables the hard-link rule on them.
- Windows has no ownership or mode check on the directory.

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
- **File content inline as base64**: no filesystem boundary to defend,
  but multi-megabyte payloads cost tokens and hit message limits, and the
  agent still has to read the file with its own tools first.
- **A `manage_exchange` tool** (list, read, write, delete), as Confluence
  ADR-502 has: rejected for the reasons under "Getting files in and out
  is the client's job".
- **`os.Root` alone, without the directory checks**: `os.Root` bounds
  names inside the directory but not the directory itself. A symlinked
  or group-writable exchange directory, or one that is `~/.ssh`, passes
  `os.Root` untouched. Rejected as insufficient on its own.
- **Lexical containment with `Lstat` and `os.SameFile`** (this ADR's
  first draft): `Lstat` checks only the final component, and on Windows
  `os.SameFile` on an `Lstat` result reopens the path lazily, comparing
  the handle with the path as it is now rather than the snapshot.
  Rejected for `os.Root` plus checks on the open handle.
- **Dropping the override**: `$XDG_DATA_HOME` already relocates the
  exchange directory. It also relocates the caches, ledgers, and reports,
  so an operator who needs the exchange directory where a sandboxed
  client can reach it would have to expose all of that with it. Kept,
  with the guard above.

## Related

- ADR-009: the surface these parameters live on, its verb test, and its
  echo and paging laws.
- ADR-008 and its 2026-09-30 amendment: ADR-008 deferred a localhost
  visualization page; the amendment (#61) makes graph reports static
  files under `<DataDir>/reports/`.
- Confluence Cloud ADR-502: the same staging-directory idea. It adds a
  `manage_workspace` tool, keeps base64 upload and download as
  alternatives (its workspace is optional), checks containment with
  `realpath`, and refuses a smaller set of override locations by path.
  This ADR makes the directory the only route, opens through `os.Root`,
  compares overrides by identity, checks ownership and mode on every
  use, and refuses hard links.
- Issues #92 (upload), #61 (graph reports), #14 (why not "workspace").

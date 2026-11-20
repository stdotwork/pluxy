# App tools

Native programs that pluxy's desktop app runs on demand, as curated first-party native tools, and
the libraries they share. This is a cargo workspace of its own (its own `Cargo.lock`), never linked
into the desktop app. The app starts a tool only for a call and stops it 30 seconds after its last
call ends; a stopped tool leaves no process or thread. What a launch starts is not the tool's: a
detached launch runs on, and a managed runtime runs until it is stopped, its package is disabled
or removed, or the app quits or restarts for an update. The contract the tools speak is the app
program contract v0 in [`../app/app-host/README.md`](../app/app-host/README.md), which stays the
reference: this folder implements it, it does not change it.

```bash
cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
cargo build --release --locked -p codex-sessions   # a small, stripped program (likewise agent-usage, usage-codex, usage-claude, usage-omp, usage-pi, usage-opencode)
cargo build --locked -p pluxy-transactions --example fixture-writer   # the native test's fixture
cargo build --release --locked -p claude-code   # likewise claude-desktop, codex-setup, execution, omp-setup, opencode, grok-setup, hermes-setup and pi-setup
cargo build --release --locked -p wsl           # pluxy/wsl, for Windows (bin/pluxy-wsl.exe)
```

Platforms: every tool but `wsl` runs on Linux, and every tool and library also builds and is
tested on Windows and macOS (`.github/workflows/next-app-tools-platforms.yml`, the whole
workspace); their manifests name programs for `darwin/amd64`, `darwin/arm64` and
`windows/amd64`. The app does not run native tools on those systems yet. `wsl` is a Windows tool
(`windows/amd64`, `windows/arm64`; on other systems it builds and answers every operation
`unsupported`); that workflow's WSL jobs run it, and `lib/program` and `lib/transactions` built
for Linux, against real WSL distributions. Each setup tool finds its client's files and programs
where that client puts them on each system, as its section says; on Windows the tools read
`LOCALAPPDATA` where a client keeps its files there, which the app is to pass them with `HOME`.

## `lib/files`

The file rules the tools share on Linux, macOS and Windows (`pluxy-files`), so no tool has
platform code of its own for them:

- Private files and folders are this user's alone (`create_private_dir`, `create_new_private`,
  `make_private`, `is_private`). On Linux and macOS: owned by this user (the effective user ID),
  0600 and 0700. On Windows: owned by this user, the token's default owner (its user, or for an
  elevated administrator the Administrators group) or SYSTEM; any other owner is refused, as the
  Unix check refuses another user's file. The access list must exist and be protected (take
  nothing from its folder's); every allow entry is for this user or SYSTEM only (inherit-only
  included); deny entries are ignored; any other entry kind is refused. A folder passes its list on
  to what is made in it, but what is made that way is not private until it is made so, as its list
  is not protected. A new private file or folder gets its list as it is made, never afterwards,
  and the default owner as anything the process makes does. In a folder whose inherited entries
  deny OWNER RIGHTS the right to change an access list (a way to stop people changing the lists
  of what they make), Windows refuses that, so making a private file or folder there fails with
  `PermissionDenied`. `make_private` changes a file through
  a handle to the file itself, not a link (on Linux and macOS with `fchmod`), and refuses anything
  but a file or folder, and a file of another owner (never taking it over); `make_private_open`
  does the same through a handle the caller holds, for what it has just made.
- On Windows, `access_list` reads a file's access list through its handle in the system's text
  form (SDDL: `D:P(…)` when protected, `D:AI(…)` with entries marked `ID` when it takes up its
  folder's), and `set_access_list` gives one back as it was written, protected or not (a NULL list,
  which would allow everyone, is refused). `rename_open` renames a file through its own handle into
  a folder open as another handle, replacing what is there, with POSIX semantics where the file
  system has them (the name is replaced even while another program has the old file open).
  `create_private_in` makes a new file relative to a folder's handle, private as it is made and
  shared with no other handle.
- On Windows, `others_may_write` says whether an account other than this user, SYSTEM and
  Administrators may change a path (its owner is another's, it has no access list, or an allow
  entry that applies to it gives another account a right to write, add, delete or change the
  list or owner, or is of a kind it does not read). `others_may_change` says why, for a file or
  folder open as a handle, with TrustedInstaller trusted too (it owns `C:\` and Windows' own
  folders), looking for any such change (`Change::Any`) or only for removing or renaming what a
  folder holds, or the folder, or changing its list or owner (`Change::Entries`): what the
  execution program's Windows terminal requires of its program and the folders above it.
- `windows_name_problem` says why Windows would read a path's name as another file (a stream such
  as `a.json:x`, a name ending in a dot or a blank, a device name such as `NUL` or `COM1.json`), a
  plain string check the same on every platform.
- `executable` says whether a path (a link followed) is a program someone may start by its path:
  on Linux and macOS a regular file with an execute bit, on Windows a regular file named `….exe` or
  `….com` that starts with `MZ` (a PE image), as `execution` requires. `sync_dir` flushes a folder, so a rename in it lasts (on Windows it does nothing, as in
  `lib/transactions`).
- `open_regular` opens a file for reading only if it is a regular file, checked on the open handle,
  without following a link: on Linux and macOS with `O_NOFOLLOW` and `O_NONBLOCK` (a FIFO is
  refused, never waited on); on Windows the link itself is opened and refused, as is anything that
  is not a file on a disk.
- `Id` tells files apart (device and inode; on Windows the volume serial number and the 128-bit
  file ID, or the 64-bit file index on a file system without 128-bit IDs, such as FAT32), and
  `mtime_ns` is a modification time in nanoseconds since the Unix epoch, the same on every system.
- On macOS and Windows, a `Lock` is a private file this process holds while it lives, made already
  held (`create_locked`) or taken when no process holds it (`take_lock`); dropping it releases and
  removes it. On macOS it is held with `O_EXLOCK`; on Windows it is open for no one else to share,
  and the system removes it when its handle closes, also when the process stops.

## `lib/program`

The program side of the contract, for any tool (`pluxy-program`):

| Item | What it does |
| --- | --- |
| `run(handler)` | Checks the host's environment (`Identity::from_env`), says hello, answers `describe` from the package's own `pluxy-extension.json` (`Description::from_package`) and serves calls on stdin and stdout until stdin ends. |
| `serve(input, output, identity, description, handler)` | `run` on any streams, for tests. |
| `Handler::call(&Call)` | One call of a declared operation: its output or an `Error`. |
| `Handler::reconcile(&Call, unknown, journals)` | Before `describe` is answered: the calls of unknown outcome and the open journals the host names. By default nothing. |
| `Call` | `id`, `op`, `input`, `chain`, `secrets`, `job`; `cancelled()` and `check()`; `progress(value)`; `call(package, op, input)` (an import, through the host; cancelled with its call); `vault(op, fields)` (backups and journals); `jobs(op, input)` (a consumer's `registrations`, `start` or `cancel`); `batch(value)` (a producer's next batch, once the job's window has room) and `acked()`; `spawn()` (asks the host to spawn the launch this call confirmed, naming nothing of it; cancelled with its call); `runtime(op, runtime)` (status, list, stop or restart of its package's managed runtimes). |
| `Job` | The job a producer's call runs: `id`, `kind`, `window`. |
| `Call::save_file(name, bytes)`, `SaveOutcome` | For a call of a `disclose` operation: hands the host `bytes` as `append` frames of at most `APPEND_BYTES` (783,000 bytes, base64 under the frame limit) and one `save` suggesting `name` (the host's save service, [`../app/app-host/README.md`](../app/app-host/README.md) "Saving files"). The person chooses the place in the host's dialog; the answer is `Saved { size }` or `Cancelled`, or the host's error (not a `disclose` call, a name holding a path, more than 64 MiB, a second save, no dialog). One save per call. |
| `Error` | The contract's error object: `invalid_request`, `unsupported`, `not_found`, `unavailable`, `internal`, `cancelled`, or any code. |
| `read_frame`, `write_frame` | The framing: a four-byte big-endian length and UTF-8 JSON; a program's frames are at most `MAX_FRAME` (1 MiB). |
| `MAX_BATCH` | The largest batch a producer may send, 48 KiB of JSON. |
| `environment` | `PLUXY_ENVIRONMENT`, which the host sets when it runs a package's Linux program in a WSL distribution: `wsl:<distribution>:<user>`, with `PLUXY_ENVIRONMENT_ACCOUNT`, `<user ID>:<home>` as the host's record of the user has them. `Identity::from_env` (so `run`) then checks that `WSL_DISTRO_NAME` is that distribution, that the effective user ID's name in `/etc/passwd` is that user, and that the ID and that entry's home are the ones named; otherwise the program stops before hello with `pluxy expected WSL <d>/<u>, this is <d'>/<u'>` or `pluxy expected <u> with user ID <n> and home <h>, this is user ID <n'> with home <h'>`, so a record of a distribution or user made again under the same name (or of a home since moved), or a routing mistake, runs nothing. A missing or unreadable account is refused too. Anywhere but Linux the variable is refused. |

Each call runs on a thread of its own that ends with the call; the thread that reads stdin
routes cancels, the answers to a call's own calls, vault and jobs frames, and a job's acks. An
answer too large for a frame fails its call with `internal` instead of breaking the pipe. A
consumer answers the call that started a job before the job's batches can reach it (the host
delivers them as calls of its deliver operation, one call of a package at a time), so it never
waits for its own job inside a call.

## `lib/usage`

Schemas 1 and 2 of the usage hub's ingest API and what a usage parser shares (`pluxy-usage`): a parser
implements `Parser` (its label, default roots, which files are its, how a file's bytes become
rows) and answers its produces operation with `produce`, which lists files (`scan`: no links, at
most 8 folders deep, newest first, at most 20,000), finds a named file again only under the roots
(`locate`), reads it and sends its rows in parts within the batch limit (`Sink`). A parser of a
format read whole (`parse`) gets a file of at most 16 MiB; a parser of a line-based format sets
`streamed` and gets the file's lines one at a time (`parse_lines`, `LineReader`) whatever its size,
a line over 16 MiB being skipped and counted: the reader holds one line, and what the parser keeps
of a file meanwhile is its own (usage-claude keeps one small record per message). A parser at schema 2 sends a record that more than one
file can hold as one keyed event (`Sink::event`). A file that holds many records, such as a
database of sessions, can list as several entries (`Parser::entries`, `Listing::Parts`), each a file
of the hub's with its own ID and revision, which the parser reads itself (`Parser::read_entry`,
`Sink::revise`). An entry listed by more than one file (the same ID under two roots) is listed once,
at its newest revision; one with an ID over 4,096 bytes is left out; a file whose entries cannot be
listed now (`Listing::Failed`) is listed as itself, so its read reports why. Either of the last two
makes the listing count as cut short, so the hub marks no file gone. The listing is sorted newest
first and cut at 20,000 entries after that. `fields` reads the fields usage records share, by
the old desktop app's rules: token counters under the names agents use, a model name and a time.
The schemas are in [`agent-usage/README.md`](agent-usage/README.md#schema-1).

## `lib/sqlite-ro`

Opens another program's SQLite database strictly read-only and creates no file beside it
(`pluxy-sqlite-ro`: `open_read_only(path)` gives a `Db`, which derefs to a `rusqlite::Connection`).
`codex-sessions` and `usage-opencode` read Codex's and OpenCode's databases with it. The rules:

- The database path must be a regular file: a link, a FIFO, a folder or a missing file is refused
  without being opened. A `-wal`, `-shm` or `-journal` beside it that exists must be a regular file
  too (SQLite opens a `-journal` to look for a hot one, and a FIFO there would hang it). The database
  and a `-wal` that is copied are opened without following a link or waiting on a FIFO, and checked
  to be regular files on the open handle.
- A database whose `-shm` exists (a process has it open) is opened in place with `mode=ro`: it uses
  that `-shm` as any SQLite reader does (it writes to that shared-memory file and takes SQLite's
  shared read locks) and never changes the database or its `-wal`.
- With no `-shm` and a `-wal`, the database is never opened in place, whatever its header says
  (SQLite deletes a `-wal` that holds bytes beside a database of 0 bytes, and creates a `-shm` beside
  one whose header is not WAL's). With the `-wal` empty it is opened `immutable`. With the `-wal`
  holding bytes it is copied, with its `-wal`, into a private folder (`lib/files`; `wal-<pid>-<n>`) under
  `$PLUXY_TOOL_DATA_DIR/tmp` (the system's temporary folder when that is not set), read there, and
  the folder is removed when the `Db` is dropped.
- With no `-shm` and no `-wal`, a database with a WAL header is opened `immutable`, and any other
  in place.
- The tool's own `tmp` must be a real folder, not a link; one that is not private is made private.
  Before a copy is made there, the copies left by processes that stopped (folders named exactly
  `wal-<pid>-<n>`, directories only) are removed. On Linux a copy is left over when `/proc` does
  not list its pid. On macOS and Windows a copy has a lock beside it, `wal-<pid>-<n>.lock`
  (`lib/files`), made already held before the folder and released after the folder is removed; a
  copy is left over when its lock is missing or no process holds it, whatever its pid (pids are
  reused), and it is removed while the remover holds its lock, so no copy can be made under that
  name meanwhile. A lock left with no folder goes too. Nothing is pruned in the system's temporary
  folder. A copy's name that is taken already is passed over for the next.
- On Windows the database path must be a full one (SQLite would read any other against its own
  current folder). It is given to SQLite in its long form (`\\?\C:\…`, `\\?\UNC\server\share\…`)
  with SQLite's `win32-longpath` file layer, so a path longer than 260 characters opens.
- An `Error` names no path, and carries SQLite's error code, not its message (which names the file).
  A tool that shows a path to its user adds it around the error itself.

A process that starts between the check for `-shm` and the open can make one read see a torn
state; a later read is right. A per-call cache of open databases is not in the library: only
`usage-opencode` needs one.

## `agent-usage`

`pluxy/agent-usage`, the usage hub: the one store of agents' token usage and its *Agent usage*
page. See [its README](agent-usage/README.md).

## `usage-fixture`

`pluxy/usage-fixture`, a usage parser of a simple test format for the hub's checks (the native
test packs it); never released. It reads `*.jsonl` files of records `{"id"?, "ts", "model",
"input", "output", ...}` under its roots, and lines that end a part or pause a read, so a check can
cancel a job between two parts of one file.

## `lib/transactions`

Link-preserving, journalled config writes for any writer tool (`pluxy-transactions`), a client of
the host's vault (backups and journals) through the contract's `vault` frames; it keeps no
journal store of its own. Linux, macOS and Windows (below for what differs on Windows).

| Item | What it does |
| --- | --- |
| `Selection` | What the person selected: `File(path)`, a config file that may be a link (as stow makes), or `Root { root, path }`, a file inside a root folder that may be a link (as chezmoi can make), `path` being slash-separated parts inside it (a backslash is refused, as it separates parts on Windows). |
| `Transactions::new(vault, data)` | The operations for one call: `vault` is the `Call` (or any `Vault`), `data` the tool's data folder (`data_dir()`, `PLUXY_TOOL_DATA_DIR`), where the state file `transactions.json` (private) keeps each set's targets and backup IDs, the originals kept for restore, and the journals left open. A data folder that does not exist yet reads as an empty state, and the first write of the state file makes it (private, with any missing folders above it: 0700 on Linux and macOS; on Windows the folders above it get what their folder passes down), so a preview or status leaves nothing behind; in WSL the host cannot make it before the program starts. A link there, or a folder another user owns, is refused (on Windows also one another user may write). |
| `preview(set, selections)` | Resolves and pins each selection: `{"set", "targets": [<pin>], "applied", "edited"}`. A pin names the selected path, its link's text, the file it resolves to, the identities (device and inode; on Windows the volume serial number and 64-bit file index) of the link, the root, the file's folder and the file, and the file's SHA-256. A link inside a selected root, a part that leaves it (`..`, `.`, empty) and anything but a regular file are refused. A read: it asks nothing, and it reports a file's existence and SHA-256 for any path, so a writer must not export it to other packages. |
| `apply(set, pins, image, merge, written)` | Writes each pinned target with what `image` makes of its bytes now, and records `set` as applied: its original images (with the person's edits since the last apply promoted into them by `merge`) and its applied images, as vault backups. A record none of whose selections still resolves to its target (a link pointed elsewhere) is replaced, its originals kept for restore. |
| `clear(set, pins, merge, written)` | Each target becomes what `merge` makes of its original, its applied image and its bytes now: the writer's untouched values go, the person's edits stay. A pin of the recorded target file itself (named directly) reaches a record made through a link that now points elsewhere. |
| `restore(set, pins, written)` | Puts the original bytes back (removing a file that did not exist), keeping the mode the file has; a file that is gone comes back with the original's mode. With no record for these targets, it puts back the originals kept for them: after a conflict, or after a new apply replaced a retargeted record. |
| `reconcile(journals)` | For `Handler::reconcile`: each open journal the host names, reconciled from what is on disk (below). |
| `status()` | The applied sets, the originals kept for restore and the latest recovery reports. |
| `Merge`, `Json`, `merge_value` | The three-way merge, and the one for JSON files (written with two-space indentation, keys in the file's order and numbers with their exact digits, through serde_json's `preserve_order` and `arbitrary_precision`; other formatting and comments are not kept, so a format that must keep them brings its own `Merge`). |
| `Target`, `Pin` | Resolving and pinning, and the checked reads and writes, for a writer that needs them directly. `Pin::from_json` refuses any field `Pin::to_json` does not write. |
| `pin_schema()` | The JSON Schema of a pin, in the host's schema subset: what a writer's write operations take as `targets` items (the fixture writer's manifest uses it exactly). |
| `backup_create(set, pins)` | Manual Backup: keeps the pinned files as they are now (an empty image and `exists: false` for one that does not exist) as one version of `set`, vault backups labelled `bk <set>`; then deletes the oldest versions over `KEEP_BACKUPS` (10): `{"backup", "deleted"}`. |
| `backup_list(set)` | The versions, newest first: `{"backups": [{"id", "created", "files": [{"selected", "path", "target", "exists", "size"}]}], "keep"}`. |
| `backup_diff(set, id, leaves)` | For each file of version `id`, as its selection resolves now: `exists`, `now_exists`, `same`, `readable` and `changes`, each changed setting's `path` (its keys) and what a restore does to it (`adds`, `changes`, `removes`), at most 200 and the rest counted in `more`. Never a value. A selection that resolves to another file is refused. |
| `backup_restore(set, pins, id, written)` | Puts version `id` back in its files (the pins must be of exactly its targets, in order), removing one it did not have, through a journal like every write. A file there keeps its permission bits, as with clear and restore; one made again gets the backed-up bits. No automatic backup is made; the set's record and the originals kept for restore stay. |
| `backup_delete(set, id)` | Deletes version `id` from the state file and the vault. |
| `backup_call(call, tx, set, leaves)`, `backup_operations(files)` | The backup browser's standard operations, which every writer exports with exactly these manifest entries (`files` targets per version) and serves with `backup_call`: `backup-list`, `backup-diff` (reads), `backup-create`, `backup-restore`, `backup-delete` (writes, each confirmed). The app's shared backup browser (`../app/src/backups.tsx`) calls them. |
| `Leaves`, `json_leaves` | A writer's reading of its file into settings for a diff (path and value, in file order; None when it cannot read it), and the one for JSON files: objects by key, arrays and other values as one setting each. |
| `toml` (feature `toml`), `yaml` (feature `yaml`) | The in-place editors of config files, shared by the setup tools, each behind its own cargo feature so a tool compiles only the parser it uses (both are off by default). `toml` (toml_edit, pinned) reads a file as a document that keeps its comments and formatting, sets a key where it stands (`set`, `table_mut`) and makes Clear's three-way merge into the file as it is now (`merge`, `leaves`); a tool gives it its own `parse` and the opening words of its refusals. `yaml` (saphyr and yaml-edit) reads a file as YAML 1.2 into a JSON value (`parse`), edits only the keys whose values differ through a lossless tree and reads the result again (`edit`), and is a `Merge` for Clear (`Yamls`); a tool gives it the program's name for the one refusal that names it. What a tool writes and refuses stays in the tool. |

How a write goes:

- The pins come back in the write's input, so the operation's native confirmation (which shows
  the exact input) names the selected path and the file it resolves to. The write resolves the
  selection again and refuses, asking for a new preview, if the link points elsewhere (or is
  another link, even with the same text, unless the filesystem gave it the old link's inode
  number, as ext4 may once the old one is gone), the file was replaced (as editors save, by rename) or
  modified since the preview. Only the target changes: a link stays the same link with the same
  text.
- Each file is written through a handle on its folder (cap-std), never by its path again: a new
  file beside it, written 0600, checked once more, given the old file's permission bits (setuid,
  setgid and sticky left out; 0600, or the original's, for a file that did not exist) and renamed
  over it, the folder flushed, and the file checked to be the one written. An apply writes a
  writer's values, a key among them, so the file it writes is owner-only: the group's and others'
  bits are taken away (an existing 0644 file becomes 0600); clear and restore keep the mode the
  file has. A rolled-back write, in the call or by recovery, puts back the mode the file had
  before it as well as its bytes: the file is left exactly as it was. The whole operation is checked again at its end. At most 8 files of at most
  256 KiB each.
- The written file is a new file: hard links to the old one keep the old bytes, and its owner,
  ACLs, extended attributes and security label are not copied.
- Before the first write, the old bytes go into vault backups and the operation into a vault
  journal (`journal.begin`, its `intent` naming each file's pin and SHA-256s before and after).
  A write that fails puts back the files already written, newest first, except one another
  program changed meanwhile, which stays as that program left it, its original kept for restore;
  the journal ends rolled back. If putting one back fails, the journal stays open for the next
  start, and its backups are kept until then.
- Once all are written, the state file records the set and the journal, then the journal is
  committed, and backups nothing names any more (no record, kept original or open journal) are
  deleted.
- After a crash, the next start's describe names the open journal. `reconcile` looks at each
  journalled target itself, whatever its selection resolves to now (a link made again counts as
  the same file), and removes the temporary files a write left beside it. It commits the journal
  if the state file names it or every file holds its new bytes (saving the set's record), and
  otherwise puts back, from the backups, each file that holds the new bytes, leaving any file
  another program changed since (or whose backup is gone), its original kept for restore, and
  ends it rolled back. It never writes the new bytes again, and repeating it changes nothing more.

In a WSL distribution (`PLUXY_ENVIRONMENT` set, see `lib/program`), a selection on a Windows drive is
refused (`This file is on a Windows drive; set it up from Windows local.`) when it resolves, before
anything is read, and so is a journalled target's folder or file at recovery. A path is on a Windows drive
when the mount holding it in `/proc/self/mountinfo` is of type `drvfs`, `9p` or `virtiofs`, or is
mounted directly in the folder WSL mounts drives in (the folder holding a one-letter drive mount,
such as `/mnt`), as `/mnt/wsl` is. Checked are the selected path's folder, the folder of the file
written and the file itself (a file can be a mount of its own), and at recovery the pinned folder
and file. Without metadata, a drive keeps no owner-only
permission bits, and Windows' own writer sets those files up.

Other programs do not take part in these checks: a write between the last check and the rename,
or after the final check, is not seen, and several files are journalled, not written atomically.
Saving replaces the file; an edit made in the same instant by another program may be lost. Say
so where the person confirms: the writers' write operations carry this sentence in their
descriptions, which their native confirmations show.
This is cooperative write correctness for a full-access tool, not a confinement.

On Windows:

- A file's permissions are its access list: records keep it as SDDL text where Linux and macOS keep
  the permission bits. The new file is private from the moment it is made (a protected list
  allowing only this user and SYSTEM, given as it is made, relative to the folder's handle) and
  open to no other handle until it is renamed and closed.
  Clear and restore give it the old file's list (protected or taking up its folder's, as it was);
  an apply leaves it private, the twin of owner-only; a file that did not exist stays private, or
  gets the list recorded for it.
- The rename goes through the new file's own handle into the folder's handle (`rename_open`), with
  POSIX semantics where the file system has them. A file another program holds open without
  letting it be deleted, or one marked read-only, is not replaced: the write fails and is rolled
  back. The folder is not flushed: Windows has no flush of a folder's entries through a handle
  opened to read it, and NTFS logs them itself.
- An existing file is written under the name it has on disk, whatever case (or short 8.3 name)
  selected it, and one file selected under two spellings is refused as named twice (as on macOS,
  whose file systems also ignore case by default).
- A name Windows would read as another file (a stream such as `a.json:x`, a name ending in a dot
  or a blank, a device name such as `NUL` or `COM1.json`) and a path in a device namespace
  (`\\.\…`, `\\?\GLOBALROOT\…`) are refused, selected or reached through a link.
- Links are symbolic links and junctions alike: a selected file or root may be either, and either
  inside a selected root is refused. A file's or folder's identity is the volume serial number and
  the 64-bit file index (NTFS gives a reused index a new sequence number, so a link made again is
  another link).
- A path inside a WSL distribution (`\\wsl.localhost\…` or `\\wsl$\…`, in any case, either
  slash, also after `\\?\UNC\`), selected or reached through a link, is refused: the package's
  Linux program serves those files, inside the distribution.

Manual backups and retention:

- A manual backup's bytes are only in the vault; the state file records each version's vault IDs,
  selections, targets, presence, sizes and permission bits, never the bytes or a hash of them, and
  pages get only the IDs, the paths and a diff of setting names. Values are compared inside the
  tool. A setting whose name is itself a secret would show as that name; the host's redaction of
  the vault's secrets applies to every answer as well.
- Retention counts only: 10 versions per set, the oldest deleted when another is saved (the
  operation's confirmation says so). Nothing is deleted by age. Retention and the backup
  operations never touch the library's own backups (`tx `), which stay while a set's record, an
  original kept after a conflict or a replaced record, or an open journal names them; `sweep`
  deletes only backups nothing names, manual ones whose delete failed among them. The vault's
  limit of 256 backups per package stays a backstop.

The example `fixture-writer` (`examples/fixture-writer`, package `pluxy/fixture-writer`) is the
library's test fixture, never a curated tool: it sets top-level values in JSON files with
`preview`, `apply`, `clear`, `restore` and `status`, and `crash_after` makes `apply` exit after
that many files. The desktop app's native test packs it (`next/app/native/transactions.ts`).

## `usage-codex`

`pluxy/usage-codex`, the usage hub's parser of Codex's session files. It reads the rollout files
(`*.jsonl`) under `~/.codex/sessions` and `~/.codex/archived_sessions` (from the `HOME` the app
gives; the hub's `roots` name others) and sends the hub token counts per UTC hour and model, and
nothing else: no prompt, message, folder or key leaves the parser. It never writes a Codex file.

A rollout's file ID is its file name, so a session Codex moves to the archive is the same file,
and one copied there is one session (the newer file is read). Each file is read whole when it
changes, by the old desktop app's rules:

- a `turn_context` line sets the model for the lines after it, and a model in an event's own
  usage object wins;
- an event's `info.total_token_usage` is a cumulative counter: the event counts what each field
  grew by since the last event in the file, never below zero (a counter that falls starts again from
  the lower value); an event with only `info.last_token_usage` or `payload.usage` counts as it is;
- input is net of cached input; the total is the event's `total_tokens`, else input + output +
  cached input; reasoning is part of output;
- every event is one sample, also one that adds nothing; an event with no counter yet (`info` null)
  is none;
- an event's hour is its line's `timestamp` (a line with no readable time is skipped; the old
  reader used the file's modification time, which a parser is not given). A date with no time is
  that day's start in UTC, where the old reader used local midnight.

The rollout is read line by line, whatever its size; a line over 16 MiB is skipped, as the old reader did.

## `usage-claude`

`pluxy/usage-claude`, the usage hub's parser of Claude Code's session transcripts, at schema 2. It
reads the transcripts (`*.jsonl`) under `~/.claude/projects` (from the `HOME` the app gives; the
hub's `roots` name others) and sends the hub token counts per message, and nothing else: no prompt,
reply, folder name or raw message, request or session ID leaves the parser. It never writes a
Claude Code file.

A transcript's file ID is its path below the root with the first folder, Claude Code's project
folder (named after the project's working folder), replaced by a hash of its name. A custom root
should name a `projects` folder (as `~/.claude/projects` is), so that the hashed folder is the
project's: under a root above it, the project folder's name would be kept in the file ID in the
hub's store (never in a report). Each file is
read whole, line by line, when it changes, by the old desktop app's rules:

- a line's usage is its `message.usage` (with no `message` object, its own `usage`), its model the
  message's `model`; `<synthetic>`, Claude Code's own placeholder, is no model;
- Claude Code writes one line per content block of a response, each repeating the response's
  `message.id` and usage, so the lines of one message ID are one message: the last one wins, at its
  own time;
- the lines of one message ID and request ID (or one message ID, when a line has no request ID)
  are one message; an empty message ID is none;
- a message is a keyed event: its key is a hash of its message ID and `requestId`, else of its
  message ID and `sessionId`, so a response that `/branch` or `--fork-session` copies into another
  transcript counts once; with neither ID it counts in its file only;
- a line with usage but no message ID counts as it is, as does each record of a line that is a
  list (or holds a `messages` list);
- the total is input + output + cache write + cache read unless the usage gives its own; every
  message is one sample; a line with no readable `timestamp` is skipped (the old reader used the
  file's modification time);
- the parser holds one small record per message of a file until the file is read: a file of more
  than 100,000 messages (more rows than the hub keeps of one file) fails, and keeps its old rows.

Two responses that share a message ID and have no request ID (a gateway that reuses one ID) are
one message within a file, as they were in the old app.
## `lib/pi-sessions`

What the Pi and Oh My Pi usage parsers share (`pluxy-pi-sessions`): the two tools write the same
session format, so one library reads it and each parser is a small program of its own. Their session
files are `*.jsonl` under `~/.<tool>/agent/sessions` and `~/.<tool>/sessions` (the hub's `roots` name
others), read-only. A file's ID is its path below the root it is under, with the first folder (the
tool names it after the session's working folder) replaced by a hash of its name, so an ID names no
folder of the person's; a session present under both roots is one file and the newer copy is read.
Rules, by the tools' own sources:

- an entry of type `message` has its usage in `message.usage`; an entry of type `usage` (Pi),
  `model_usage` (Oh My Pi), `compaction` or `branch_summary` (Pi) has it in its own `usage`. Nothing
  else is read: an Oh My Pi `task` result keeps its child's usage in `details.usage`, and the child has
  a file of its own;
- `input` is already net of cache reads and writes (the tools' providers take them out), `output`
  holds the reasoning tokens (`reasoning` in Pi, `reasoningTokens` in Oh My Pi), which are never added
  again; the total is the entry's `totalTokens`, else input + output + cache read + cache write;
- the model is the message's `responseModel` (the model that answered), else its `model`; another
  entry's `model`; none is the empty name;
- the hour is the entry's `timestamp` (an ISO time), else the message's timestamp (milliseconds); an
  entry with neither readable is skipped (the old reader used the file's modification time, which a
  parser is not given);
- every entry with a usage that holds a count is one sample, also one that adds nothing;
- an entry with an `id` and a `timestamp` is an event of [schema 2](agent-usage/README.md#schema-2),
  its key a hash of the two, counted once by the hub whichever files hold it. A session a tool forks
  or branches (Pi's and Oh My Pi's `forkFrom` and `createBranchedSession`) is a new file that starts
  with copies of the parent's entries, `id` and `timestamp` unchanged, so a copied entry is the same
  event in both files. The `id` alone is eight random hex digits, unique within one session only,
  which is why the time is part of the key. An entry without either is a plain row, counted in each
  file that has it, as at schema 1.

Only counters, a model name, a time and the hashed key are read: no prompt, message, title, folder or
raw entry ID leaves the parser.

## `usage-omp`

`pluxy/usage-omp`, the usage hub's parser of Oh My Pi's session files, from `~/.omp/agent/sessions` and
`~/.omp/sessions` (the `HOME` the app gives). It is `lib/pi-sessions` with Oh My Pi's name and folder.

## `usage-pi`

`pluxy/usage-pi`, the usage hub's parser of Pi's session files, from `~/.pi/agent/sessions` and
`~/.pi/sessions` (the `HOME` the app gives). It is `lib/pi-sessions` with Pi's name and folder.

## `usage-opencode`

`pluxy/usage-opencode`, the usage hub's parser of OpenCode's local store, at schema 2. It reads
OpenCode's database (`opencode.db`, or `opencode-<channel>.db` from a build of another channel) and
its older JSON message files (`storage/message/<session>/<message>.json`) under
`~/.local/share/opencode` and `~/.opencode` (from the `HOME` the app gives), and on Windows also
`opencode` in the roaming and local app data folders (`APPDATA`, `LOCALAPPDATA`, when the app gives
them); the hub's `roots` name others, such as `$XDG_DATA_HOME/opencode`. It sends the hub token
counts per message, and nothing else: no prompt, reply, title, folder name or raw session or message
ID leaves the parser. It never writes an OpenCode file.

- Each session in a database is a file of the hub's of its own, `<database path below the
  root>#<hash of the session ID>`, so the hub reads again only the sessions that changed, and a
  database of many sessions never passes the 100,000 rows the hub keeps of one file. A session's
  revision is its message rows' count and data length and their latest update time, read through
  SQLite, so a change still in the database's `-wal` counts. The same session in databases under
  two roots is read from the one where it is newest. A database that cannot be opened (a linked
  `-wal`, say) is listed as itself, its read fails with the reason, and none of its sessions is
  marked gone. A JSON message file's ID is its path below the root with the session folder and file
  name replaced by their hashes.
- The database's current table, `session_message`, is read (its `assistant` rows, and `compaction`
  rows that carry the compaction request's own tokens), and so is the `message` table of a database
  OpenCode made before it, which OpenCode's migration copies into `session_message` and leaves in
  place. A message is a keyed event, the key a hash of its message and session IDs, which OpenCode's
  migrations (JSON store to database, `message` to `session_message`) keep; a JSON message file's
  IDs are its file's and folder's names, where OpenCode's migration took them: a message stored in more
  than one of these ways counts once. A fork copies its parent's history under new message IDs
  (`<fork event>_<seq>`); a copy takes its original's key, so it counts once too. Forks from before
  `session_message` cannot be told from new messages and count again.
- OpenCode's output is the visible output, reasoning apart: the hub's output is output + reasoning,
  and the total is input + output + reasoning + cache read + cache write (OpenCode's own total; a
  provider total some older messages carry is not used, so every copy of a message counts the same).
  A message's time is its `time.created`. Title and other side requests OpenCode records only as
  session totals are not counted.
- The database is opened by [`lib/sqlite-ro`](#libsqlite-ro): read-only, nothing made beside it, a
  link, or a `-wal`, `-shm` or `-journal` that is not a regular file, not read.

## `codex-sessions`

`pluxy/codex-sessions` reads Codex's local sessions, read-only, and shows them on its page
(*Codex sessions*). It never writes a Codex file, never reads `config.toml` or `auth.json`, and
has no repair, delete or editor. Its one write outside its data folder is a file the person saves
from `export`, in the host's save dialog. `open` starts the person's own app on a rollout, after
the person confirms the launch (below); the reader itself spawns nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `list` | `read` | `offset` (default 0), `limit` (1 to 100, default 50) | The Codex home, the databases read, a page of sessions (ID, title, folder, provider, archived, update time, database), the total, whether there are more, the page's active and archived counts, and warnings |
| `detail` | `disclose`: each call asks the person | `id`, `offset`, `limit` (messages) | The session's title, folder, provider, model, archived, update time, rollout file and database; its statistics; a page of its messages, each cut at 8 KiB (`truncated`), at most 512 KiB in all, and `next_offset` |
| `export` | `disclose`: each call asks the person | `id` | Whether the person `saved` the session's rollout (and its `size`) or `cancelled` the save dialog; never the rollout's bytes |
| `open` | `read`; the launch it asks for is confirmed | `id` | `{"outcome": "opened"}` once the launch was started; the call's error (a refused or declined launch, a session with no rollout file, `pluxy/execution` not enabled); never a path or a byte of the file |
| `settings` | `write` | `codex_home`: a full path, or empty for the default | The Codex home now read, and whether it is custom |

`export` finds a session's rollout as `detail` does (same ID checks; a link to a file outside the
Codex home is refused) and saves its bytes exactly as they are on disk, with
[`Call::save_file`](lib/program/src/lib.rs): the host asks where in its native save dialog,
titled *Save a file from pluxy/codex-sessions*, which suggests `session-<id>-<YYYY-MM-DD>.jsonl`
(the ID without `local:`, the date in UTC), as the old app's *Export JSONL* did. The page shows
the outcome as text and never holds the bytes. Like the old app it saves only a rollout of at
most 10 MiB that is UTF-8 text (the host takes up to 64 MiB): the whole file is read to be sent,
so this also bounds the reader's memory. No error names the rollout's path or holds a byte of
it.

`open` is the old app's *Edit with local app*, as a launch the person confirms: the session's
rollout is opened by the desktop's own opener (`xdg-open`, else `gio open`), which starts whatever
app the desktop has for JSON Lines files. It reads no byte of the file. It finds the rollout as
`detail` does, and opens only a regular file named `rollout-*.jsonl` inside the Codex home's
`sessions/` or `archived_sessions/` (whichever holds it, never the Codex home itself: the host
refuses a folder that is the home folder or above it); a rollout reached through a link is opened
as the file the link resolves to. It then calls
[`pluxy/execution`](#execution) (an optional import of its `prepare` and `launch`): `prepare` with
`{"kind": "open", "file", "within"}`, the two resolved paths and nothing else, so the opener is
found only in the system's program folders, and then `launch` with exactly what `prepare` gave, plus
`"lifetime": "detached"`: no environment, standard input, secret or program. The call that starts
the launch is execution's, so the person's confirmation is the host's: it names this tool and
`pluxy/execution`, the file, its content type, the folder and the opener, is asked every time, and
offers no *Remember* (`../app/app-host/README.md`, "Launches and managed runtimes"). Without
`pluxy/execution` enabled, `open` fails with the host's `extension_unavailable` and the reader
works as before. A native-test build (feature `native-test`, the app's native test only) also
sends `search_path` with the folder named in `opener-folder` in the tool's data folder, for the
test's stand-in opener.

The Codex home is `~/.codex` (from the `HOME` the app gives), or the folder set with `settings`,
kept in the tool's data folder (`PLUXY_TOOL_DATA_DIR`). `list` and `detail` take no paths, so a
page showing session text cannot point the reader elsewhere.

What it reads, as the old desktop app's session manager did:

- SQLite databases `sqlite/*.db`, `*.sqlite` and `*.sqlite3` and the legacy `state_5.sqlite`, each
  kept only with a `threads`, `automation_runs` or `inbox_items` table, opened read-only. A
  database that cannot be read becomes one warning; the rest loads.
- `threads` (title or name, folder, provider, archived, update time, whichever columns exist) and
  `automation_runs` (an `archived` status is archived).
- Rollout files `sessions/**` and `archived_sessions/**` named `rollout-…<UUID>.jsonl`: for the
  list only their names (links are skipped); a rollout whose session a database has is dropped.
- `session_index.jsonl`: a rollout's title and update time, else the file's modification time.
  An index larger than 16 MiB becomes a warning and is not read.

Links to databases, rollouts and the index are skipped, and so is a `sqlite/`, `sessions/` or
`archived_sessions/` folder that is itself a link (a warning names it), and a database whose
`-wal` or `-shm` file is a link (a warning names it). The Codex home itself may be a link.
`detail` finds a thread whose database ID is stored with Codex's `local:` prefix by its ID with or
without the prefix.

Databases are opened by [`lib/sqlite-ro`](#libsqlite-ro), which adds no file beside one that no
Codex process has open (a WAL database whose `-wal` holds committed changes Codex stopped before
copying back is read from a private copy in the tool's data folder, removed after). A database it
refuses or cannot open, or whose copy fails, is one warning that names the database, and is
skipped; the library's own errors name no path, and the warning adds it. A Codex process that
starts at the same moment can make one read see a torn state; Refresh reads again.

Every string a reply takes from a database or a rollout is capped: titles, folders, providers,
models and paths at 4 KiB, IDs at 128 bytes, roles, timestamps and call IDs at 256 bytes, each
ending with "…" when cut. A `list` page holds at most 512 KiB of sessions: when longer ones would
not fit it holds fewer and says there are more, and the next page starts after the last one given.
A `detail` page's metadata counts toward its 512 KiB too.

Sessions with the same ID (without a `local:` prefix) are one: the newer wins and the other fills
its empty fields. The list is newest first, then by ID. A session's detail reads its rollout (the
database's `rollout_path` when it is inside the Codex home's session folders, else the file named
for it there): its `response_item` records become messages (user and assistant text, reasoning or
`[Encrypted reasoning block]`, tool calls as `name(arguments)` and their outputs), and the model is
the database's, else the last `turn_context`'s.

The page renders every session's text as text, never as HTML.

## `claude-code`

`pluxy/claude-code` sets up Claude Code to use pluxy's core and says how to start it, for the
desktop app's Claude Code page (`../app/README.md`, "Claude Code"). It writes one settings file
through `lib/transactions` (set `claude-code`): link-preserving, journalled, the original and
applied images in the host's vault, and its manual backups (the backup browser). It exports
nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: one selection (`file`, or `root` and `path`), by default `~/.claude/settings.json` in the app's `HOME` (refused, with no folder made, while `~/.claude` does not exist: Claude Code makes it when it first runs); `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and `settings`: `state` (`not-configured`, `configured`, `needs-update`, or `refused` with a `reason` for a file Claude Code would not take), `base_url`, `key_set`, each role's model and 1M switch, `startup_model`, `subagent_model`, `max_context_tokens`, `auto_compact_pct`, `disable_auto_compact`; never the key |
| `apply` | `write`; secret `key` (value) | One pin, `base_url`, `key`, `opus`, `sonnet`, `haiku`, `fable` (empty: follows Sonnet) with `<role>_1m`, `startup_model`, `subagent_model` (empty: removed), `max_context_tokens` (100000 to 1000000), `auto_compact_pct` (1 to 100), `disable_auto_compact` | The library's outcome |
| `clear` | `write` | One pin | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | One pin | The library's outcome: the original bytes back (Restore Backup) |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | — | `{"client": "Claude Code", "kind": "terminal", "program": "claude", "args": []}`, for the launcher |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations (`lib/transactions`), one file per version, diffed with `json_leaves` | The library's answers: IDs, paths, presence, sizes and setting names, never a value |

What `apply` writes, keeping everything else in the file (keys in its order, numbers with their
digits), with Claude Code's own names
([settings](https://code.claude.com/docs/en/settings), [environment variables](https://code.claude.com/docs/en/env-vars),
[model configuration](https://code.claude.com/docs/en/model-config), read 2026-11-14):

- in `env`: `ANTHROPIC_BASE_URL` (the core) and `ANTHROPIC_AUTH_TOKEN` (the client key, sent as a
  bearer token); `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU,FABLE}_MODEL`, each with `[1m]` when its 1M
  switch is on (a model typed with `[1m]` counts as the switch); the `/model` picker's
  `ANTHROPIC_DEFAULT_*_MODEL_NAME` and `_DESCRIPTION` ("Opus · <model>", "Auto-compact window: 1M",
  "1M enabled · …"), and `ANTHROPIC_CUSTOM_MODEL_OPTION` with its name and description for the
  Sonnet model, as the old app wrote them; `CLAUDE_CODE_SUBAGENT_MODEL`;
  `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1` (the picker lists the core's models);
  `CLAUDE_CODE_AUTO_MODE_SERVER=0` (auto mode uses Claude Code's own classifier requests instead
  of asking the server to review its actions, as the old app set it); `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and `CLAUDE_CODE_AUTO_COMPACT_WINDOW` (the
  window, which beats every other window setting), `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` and
  `DISABLE_AUTO_COMPACT` (`1` or `0`). `ANTHROPIC_API_KEY` is removed (it would win over the bearer
  token) and so is `ANTHROPIC_MODEL` (it would win over `model`);
- at the top level: `model` (the startup model, so `/model` can still change it; removed when
  empty), `autoCompactWindow` and `autoCompactEnabled`.

Clear's three-way merge brings back what Apply removed or replaced, unless the person changed it
since. `CLAUDE_CODE_EFFORT_LEVEL` and per-model `modelSettings` are left alone. `apply` and `clear`
refuse, before writing, a file Claude Code would not take as its own settings: not strict JSON (a
comment, a trailing comma), not an object, an `env` that is not an object of strings, or a
`$schema` other than `https://json.schemastore.org/claude-code-settings.json`. `preview` pins such
a file all the same and reports it as `refused`, so Restore Backup can be reached: it puts the
original back whatever the file holds now. A missing or empty file is an empty one. `preview`
reads the file twice (the pins, then the values); when another program wrote it in between, it
reads again once, then refuses, so the values shown are always those of the file pinned.

## `codex`

`pluxy/codex-setup` (crate `codex-setup`; `pluxy/codex` is the core's Codex extension) sets up
Codex to use pluxy's core and says how to start it, for the desktop app's Codex page
(`../app/README.md`, "Codex"). It writes two files through `lib/transactions` (set `codex`, in this
order): Codex's `config.toml` and the model catalog Codex reads from `model_catalog_json`,
`pluxy-model-catalog.json` beside it by default. Both are link-preserving, journalled and backed up
together; the original and applied images stay in the host's vault. It exports nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: two selections (`file`, or `root` and `path`), config.toml then the catalog, by default `~/.codex/config.toml` and `~/.codex/pluxy-model-catalog.json` in the app's `HOME` (the tool sees no `CODEX_HOME`: select that folder as a root); `base_url` (optional) | The library's preview and `settings`: `state` (`not-configured`, `configured`, `needs-update`, or `refused` with a `reason` when Codex would not take either file), `base_url`, `key_set`, `provider` (the `model_provider` now), `model`, `profile`, `catalog`, `remote_compaction`, `agents`, `approval_model` and each catalog model's editable settings (`system_prompt_chars`, never a prompt); never the key |
| `import` | `read` | `models`: the core's served models (`slug`, `upstream`, `display_name`, `non_codex`) | The editable settings of the entries they would get, and `missing`: those pluxy has no Codex template for. Nothing is written |
| `apply` | `write`; secret `key` (value) | Two pins, `base_url`, `key`, `model` (one of the catalog's), `approval_model` (empty: Codex default), `remote_compaction`, `agents` (`enabled`, `max_threads`, `model`, `effort`, `interrupt`), `models` (1 to 64: `slug`, for a new entry `upstream` and `non_codex`, `display_name`, `description`, `context_window`, `max_context_window`, `effective_context_window_percent`, `auto_compact_token_limit`, `reasoning`, `default_reasoning`, `input_modalities`, `visible`, `tool_search`, optional `system_prompt`) | The library's outcome |
| `clear` | `write` | Two pins | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | Two pins | The library's outcome: both originals back (a file that did not exist is removed) |
| `backup-*` | as the library's | as the library's, two pins | Manual backups of both files; config.toml is diffed by TOML setting path |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | `target`: `cli` (default), `app` or `service`; for `app`, `program`: the full path of the program | `codex` in a terminal with no arguments; Codex App as a direct launch of that program (Codex has no Linux app; refused without a full path); `codex app-server daemon restart` as a direct launch in `HOME` |

What `apply` writes in config.toml, edited in place with `toml_edit` (comments, formatting and
everything else stay), with Codex's own names (source `openai/codex` at `b1573f102`, read
2026-11-14: `codex-rs/config/src/config_toml.rs`, `codex-rs/model-provider-info`,
`codex-rs/core/config.schema.json`):

- at the top level: `model_provider = "pluxy"`, `model`, and `model_catalog_json` (the catalog's
  file name when it is in config.toml's selected folder, which Codex resolves relative paths
  against; else its full path);
- `[model_providers.pluxy]`: `name = "pluxy"`, `base_url` (the core's address with `/v1`),
  `wire_api = "responses"` (the only one Codex speaks) and `experimental_bearer_token` (the client
  key, as the old app wrote it); `env_key` and `requires_openai_auth` are removed from that table;
- **Remote compaction (experimental)**: `capabilities.remote_compaction = "v2"` in that table, or
  removed (Codex's default for a custom provider is local compaction);
- `[agents]` (`enabled`, `interrupt_message`, `max_concurrent_threads_per_session`,
  `default_subagent_model`, `default_subagent_reasoning_effort`; empty ones removed), only when the
  file has the table or the settings differ from Codex's defaults. Codex also reads `max_threads`
  as the thread limit: the preview shows it when the other is not set, and Apply removes it.

`auth.json` (the person's ChatGPT login) is never written. The catalog is Codex's `{"models": [...]}`.
A new entry is made from pluxy's Codex client template for its upstream name
(`../extensions/codex/client_models.json`, embedded at build time: the file the core serves Codex),
keeping only the fields Codex requires and those that shape how it drives the model, with
`model_messages.instructions_template` (Codex refuses a model without instructions); a full
template is about 88 KB and a file at most 256 KiB, so about ten models fit. A model served by
another provider gets the core's non-Codex values. Entries already in the file keep every field
the form does not edit; each gets the form's settings, its place as `priority` and the approval
model as `auto_review_model_override`. Clear's merge is TOML for config.toml (the current file
edited in place; a key coming back from the original goes at its table's end) and JSON for the
catalog, told apart by the file Apply wrote; a file holding only white space counts as none.
`apply` and `clear` refuse, before writing, a config.toml that is not TOML, or whose
`model_providers`, `model_providers.pluxy`, its `capabilities` or `agents` is not a table, or whose
pluxy provider asks for another wire API; and a catalog that is not a JSON object with a `models`
list of entries with a `slug`. `preview` pins both files all the same and reports them as
`refused`, so Restore Backup and the backups can be reached: Restore puts both originals back
whatever the files hold now. `preview` reads the files twice (the pins, then the values); when
another program wrote one in between, it reads again once, then refuses, so the values shown are
always those of the files pinned. Codex's files carry no version, and none is assumed.

## `claude-desktop`

`pluxy/claude-desktop` sets up Claude Desktop to use pluxy's core as its third-party inference
gateway and says how to start it, for the desktop app's Claude Desktop page (`../app/README.md`,
"Claude Desktop"). It writes three files through `lib/transactions` (set `claude-desktop`, in this
order), link-preserving and journalled like `claude-code`, with manual backups of all three. It
exports nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: three selections (`file`, or `root` and `path`), by default `claude_desktop_config.json`, `configLibrary/00000000-0000-4000-8000-000000831700.json` and `configLibrary/_meta.json` in Claude Desktop's third-party folder (below); `base_url` (optional): the core the state is checked against | The library's preview and `settings`: `state` (`not-configured`, `configured`, `needs-update`), `base_url`, `key_set`, `deployment_mode`, `applied_id`, `hybrid_pointer` and the model rows (`model`, `alias`, `one_m`); never the key. Files Claude Desktop would not take are pinned all the same, with `settings` `{"state": "refused", "reason"}`, so Restore Backup still reaches them. With no `targets` and nothing at `configLibrary` in the third-party folder, refused with "Claude Desktop's configuration library does not exist yet" (what `folders` creates) |
| `folders` | `write` | — | Creates `configLibrary` in the third-party folder when it does not exist, private (`lib/files`), with the folders above it that are missing (on Linux and macOS private too; on Windows they take their folder's list): `created`, `folder` |
| `apply` | `write`; secret `key` (value) | Three pins, `base_url`, `key`, `models`: 1 to 16 rows of `model` (what the core serves), `alias` (the name Claude Desktop uses; empty: the model's own) and `one_m` | The library's outcome |
| `clear` | `write` | Three pins | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | Three pins | The library's outcome: the original bytes back, files that did not exist removed (Restore Backup) |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | — | `{"client": "Claude Desktop", "kind": "direct", "program": …, "args": [], "cwd": <HOME>}`, for the launcher: `program` is `claude-desktop` on Linux and `/Applications/Claude.app` on macOS (or `~/Applications/Claude.app` when only that exists); on Windows `unsupported`, as Anthropic names no program or packaged-app ID to start |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations, three files per version, diffed with `json_leaves` | The library's answers |

Claude Desktop's third-party folder, where it keeps its data, is `~/.config/Claude-3p` on Linux
(the tool's environment has no `XDG_CONFIG_HOME`: a person who moved it selects that folder),
`~/Library/Application Support/Claude-3p` on macOS and `%LOCALAPPDATA%\Claude-3p` on Windows
(`LOCALAPPDATA` as the app passes it; with none, or one that is not a full path, the default
preview and `folders` refuse). With the MSIX installer on Windows 10 it can be
`%LOCALAPPDATA%\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Local\Claude-3p` instead, which the tool
takes when only that one exists (after an upgrade to Windows 11 the old one can stay behind).
Anthropic's [data storage](https://claude.com/docs/third-party/claude-desktop/data-storage) and
[configuration format](https://claude.com/docs/third-party/claude-desktop/configuration-format)
pages (read 2026-11-17) name these folders and `configLibrary` in them; the older Windows folder,
`%APPDATA%\Claude-3p` (before 1.5186.0), is not read, as Claude Desktop moves it at its next start.
They do not name `claude_desktop_config.json`; Claude Desktop is one app on all three systems (its
[installation](https://claude.com/docs/third-party/claude-desktop/installation) page), and the tool
takes that file beside `configLibrary` on macOS and Windows as its Linux package keeps it. This is
not checked on a real macOS or Windows install yet.

The files and keys are Claude Desktop's own, as Anthropic's third-party documentation
([configuration](https://claude.com/docs/third-party/claude-desktop/configuration),
[where it reads it](https://claude.com/docs/third-party/claude-desktop/configuration-format),
[gateway](https://claude.com/docs/third-party/claude-desktop/gateway),
[Linux](https://code.claude.com/docs/en/desktop-linux), read 2026-11-14) and the code of its Linux
package (`claude-desktop` 5.11492.5, the official apt repository) name them. What `apply` writes,
keeping everything else in each file (keys in their order, numbers with their digits):

- `claude_desktop_config.json`: `deploymentMode` `3p`, the sign-in choice Claude Desktop's own
  chooser saves there. Claude Desktop reads it only from this third-party folder, so the standard
  app's `~/.config/Claude/claude_desktop_config.json` (its MCP servers) is not touched;
- the gateway profile: `inferenceProvider` `gateway`, `inferenceGatewayBaseUrl` (the core; Claude
  Desktop calls `/v1/messages` below it), `inferenceGatewayApiKey` (the client key),
  `inferenceGatewayAuthScheme` `bearer`, `inferenceCredentialKind` `static`,
  `modelDiscoveryEnabled` and `bootstrapEnabled` `false`, `disableDeploymentModeChooser` `true`
  and `inferenceModels`; `selfHostedUrl` (a session host, which would take inference elsewhere) is
  removed. Each row becomes `{"name": <alias, else model>, "labelOverride": <model, when an alias
  differs>, "anthropicFamilyTier", "isFamilyDefault", "supports1m", "prefer1m"}`: the first is
  Claude Desktop's default model, the tier comes from the name (the first of each tier is its
  default) and both 1M keys are set when the row's 1M switch is on;
- `_meta.json`: `appliedId` names pluxy's profile (the old desktop app's ID, so its entry is
  updated, not duplicated), `entries` gains `{"id", "name": "pluxy"}` and `hybridPointer`, which
  would win over `appliedId`, is removed; the person's other configurations stay.

Claude Desktop drops a gateway model whose name does not look like an Anthropic model (it must
hold `claude`, `anthropic` or a family name such as `sonnet`, and no other family's, such as `gpt`
or `gemini`), so `apply` refuses such a row: a non-Claude model needs an alias, and the core must
serve the model under it (an account's or extension's `model_alias`). This package does not change
the core; the window's Claude Desktop page can add the alias through the management API, and the
package's Clear and Restore Backup leave it. Clear's three-way merge brings back what Apply removed or replaced unless the
person changed it since, with two rules of Claude Desktop's own: it seals the key in place with
the device's keystore at its next launch, which is not the person's edit, so Clear still removes
the key; and pluxy's entry leaves `_meta.json` unless the original had it. Every operation but
`restore` refuses, before writing, a file Claude Desktop would not take: not strict JSON, not an
object, a profile whose `inferenceModels` is not a list, or an index whose `entries` is not a list
of configurations or whose `appliedId` is not a string. A missing file, or one of only white space,
is an empty one, in Clear's merge too; a missing folder refuses the whole operation, before any
file is written. The merge tells the three files apart by the whole set of values Apply wrote into
each. A profile with `selfHostedUrl` set does not read back as configured. Apply writes each file
owner-only (the library's rule: a key is in the profile, and the files sit beside it). None of these files has a
version, and no minimum Claude Desktop version is assumed.

## `opencode`

`pluxy/opencode` sets up OpenCode to use pluxy's core and says how to start its CLI or desktop
app, for the desktop app's OpenCode page (`../app/README.md`, "OpenCode"). It writes one config
file through `lib/transactions` (set `opencode`): link-preserving, journalled, the original and
applied images in the host's vault, and its manual backups (the backup browser). It exports
nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: one selection (`file`, or `root` and `path`), by default OpenCode's global config in the app's `HOME` (below); `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and `config`: `state` (`not-configured`, `configured`, `needs-update`, or `refused` with `reason` for a file OpenCode would not take, which is pinned all the same), `base_url` (the provider's), `key_set`, `model` (the default, when it is one of pluxy's), `default_model` (the file's `model` as `provider/model`), `models` (`id`, `name`, `attachment`), `old_provider` (the old desktop app's `cpa-gui` provider is there), `v2_provider` (OpenCode 2's `providers.pluxy` is there); never the key. The file is read again once if it changed between the pin and the read, then refused |
| `apply` | `write`; secret `key` (value) | One pin, `base_url`, `key`, `models` (1 to 256: `id`, `name` (empty: the ID), `attachment`), `model` (one of them) | The library's outcome |
| `clear` | `write` | One pin | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | One pin | The library's outcome: the original bytes back (Restore Backup) |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | `target`: `cli` (the default) or `app` | `{"client": "OpenCode", "kind": "terminal", "program": <~/.opencode/bin/opencode (opencode.exe on Windows) when it is a program there, else "opencode">, "args": []}`, or `{"client": "OpenCode Desktop", "kind": "direct", "program": …, "args": [], "cwd": <HOME>}`, for the launcher: on Linux `/opt/OpenCode/ai.opencode` (or `/opt/OpenCode/ai.opencode.desktop`, OpenCode 1's, when only that exists), on macOS `/Applications/OpenCode.app` (or `~/Applications/OpenCode.app` when only that exists), on Windows `%LOCALAPPDATA%\Programs\ai.opencode.desktop\OpenCode.exe` (refused with no `LOCALAPPDATA`) |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations (`lib/transactions`), one file per version, diffed by setting name as `json_leaves` reads JSON | The library's answers: IDs, paths, presence, sizes and setting names, never a value |

OpenCode reads its global config from `~/.config/opencode/` (`$XDG_CONFIG_HOME/opencode`, or
`$OPENCODE_CONFIG_DIR`), on every system (`%USERPROFILE%\.config\opencode` on Windows: its
`packages/util/src/global-roots.ts`, and `xdg-basedir` in OpenCode 1, take the home folder's
`.config` wherever `XDG_CONFIG_HOME` is not set): `opencode.json` and then `opencode.jsonc`, so the latter's values win
(OpenCode 1.x also reads an older `config.json` first). The default file is the one OpenCode itself
writes: `opencode.jsonc` if it exists, else `opencode.json` if it exists, else a new
`opencode.jsonc`. The tool's environment holds no `XDG_CONFIG_HOME` or `OPENCODE_CONFIG*`, so a
person who uses them selects that file or folder; project configs and `OPENCODE_CONFIG_CONTENT`
still win over the global file, as in OpenCode. Read at source (anomalyco/opencode, tags v1.19.5
and v2.3.0, 2026-11-14): `packages/opencode/src/config/config.ts` (1.x) and
`packages/core/src/config.ts`, `config/discovery.ts`, `config/normalize.ts` (2.x).

What `apply` writes, in the form both OpenCode 1.x reads and 2.x migrates when it loads it (2.x's
own form, `providers`, is unknown to 1.x):

- `provider.pluxy`: `npm` `@ai-sdk/openai-compatible`, `name` `pluxy`, `options.baseURL` (the
  core's address with `/v1`) and `options.apiKey` (the client key), its other members kept;
  `models` holds exactly the listed models, each with `name` and, with attachments,
  `"attachment": true` and `modalities` input text, image and pdf, else input text only (output
  text), an existing entry's other members kept;
- `model`: `pluxy/<model>` (replacing a `{providerID, model}` default, which Clear brings back);
- `$schema`: `https://opencode.ai/config.json`, first, only when the file names none.

The file is edited in place through a JSONC syntax tree (jsonc-parser): comments, formatting,
trailing commas and every other setting stay where they are, and new members follow the file's
own style. Clear's three-way merge brings back what Apply replaced unless the person changed it
since, and edits only what differs. An old desktop app's `cpa-gui` provider is left alone. A file
with OpenCode 2's own `providers.pluxy` is never reported configured and `apply` refuses it, since
OpenCode would use that entry instead; the person removes it. `configured` also needs `npm`
`@ai-sdk/openai-compatible`, the base URL, the key and the default among pluxy's models. Every
operation but `restore` refuses, before writing, a file OpenCode would not take as its own config
(the preview reports it `refused`): not JSON or JSONC by OpenCode's parser rules (comments and
trailing commas are fine; single quotes, bare keys, hexadecimal numbers or missing commas are not),
a leading byte order mark, a key named twice in any object (arrays included), not an object, a
`$schema` other than `https://opencode.ai/config.json`, a `provider`, `provider.pluxy`, its
`options` or `models` (or a model entry) that is not an object, or a `model` that is neither
`provider/model` text nor `{providerID, model}`.
A missing or empty file is an empty one. Restore Backup puts the original back whatever the file
holds now.

## `omp-setup`

`pluxy/omp-setup` sets up Oh My Pi to use pluxy's core and says how to start it, for the desktop
app's Oh My Pi page (`../app/README.md`, "Oh My Pi"). It writes Oh My Pi's two files through
`lib/transactions` (set `omp`, targets in this order: the models file, then the settings file):
link-preserving, journalled, the original and applied images in the host's vault, and its manual
backups (the backup browser). It exports nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: two selections (`file`, or `root` and `path`); or `folder`: an agent folder (it may be a link), whose two files are used; by default the two files in `~/.omp/agent` in the app's `HOME` (below); `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and `config`: `state` (`not-configured`, `configured`, `needs-update`), `base_url` (the provider's), `key_set`, `model` (the default role's, when it is pluxy's), `default_role` (the settings file's `modelRoles.default`), `legacy` (the old desktop app's `easy-cliproxyapi` provider is there); or, for files Oh My Pi would not take, `state` `refused` and `reason`; never the key |
| `apply` | `write`; secret `key` (value) | Two pins, `base_url`, `key`, `model` (one model ID) | The library's outcome |
| `clear` | `write` | Two pins | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | Two pins | The library's outcome: the original bytes back (Restore Backup) |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | — | `{"client": "Oh My Pi", "kind": "terminal", "program": <~/.bun/bin/omp, else ~/.local/bin/omp (on Windows ~/.bun/bin/omp.exe, else omp.exe in %LOCALAPPDATA%\omp, where omp.sh/install.ps1 puts it), when it is a program there, else "omp">, "args": []}`, for the launcher |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations (`lib/transactions`), both files per version, diffed by setting name (each file's YAML read as its value) | The library's answers: IDs, paths, presence, sizes and setting names, never a value |

Oh My Pi keeps its files in its agent folder, `~/.omp/agent` on every system (`os.homedir()`, so
`%USERPROFILE%\.omp\agent` on Windows: `packages/utils/src/dirs.ts` at v18.21.0) (`PI_CODING_AGENT_DIR` moves it;
`PI_CONFIG_DIR` renames `.omp`; a profile, `OMP_PROFILE` or `--profile`, uses
`~/.omp/profiles/<name>/agent`): `models.yml` (or an existing `models.yaml`) holds custom
providers and models, `config.yml` (or an existing `config.yaml`) the settings, both YAML. The
tool's environment holds none of those variables, so a person who uses them selects that folder or
those files. Read at source (can1357/oh-my-pi at af8e4c142b2b, release v18.20.0, 2026-11-14):
`packages/utils/src/dirs.ts`, `packages/coding-agent/src/config/config-file.ts`,
`models-config-schema-bundle.ts`, `settings.ts` and `docs/models.md`, `docs/settings.md`.

What `apply` writes:

- in the models file, `providers.pluxy`: `baseUrl` (the core's address with `/v1`), `apiKey` (the
  client key), `api` `openai-responses` and `discovery.type` `openai-models-list` (Oh My Pi lists
  the core's models from its `/v1/models`); its other members, such as the person's own `models`,
  `modelOverrides` or `headers`, and every other provider stay;
- in the settings file, `modelRoles.default`: `pluxy/<model>`; the other roles and settings stay.

Both files are edited in place through a lossless YAML tree (yaml-edit): comments, key order,
quoting and indentation stay, and only keys whose values change are touched. Each file is read as
YAML 1.2 (saphyr), as Oh My Pi's parser reads it, and every edited file is read again and must hold
exactly the intended value, else nothing is written. Clear's three-way merge brings back what Apply
replaced unless the person changed it since, and edits only what differs; a file Apply created that
holds nothing else is removed. An old desktop app's `easy-cliproxyapi` provider is left alone. `apply`
and `clear` refuse, before writing, files Oh My Pi would not take; `preview` pins them all the same
and reports them `refused` with the reason, so Restore Backup and the backups still reach them: not
UTF-8, not YAML (a repeated key too), more than one document, a top level that is not a mapping, a
tag, a key that is not text or a number that is not finite, `providers`, `providers.pluxy` or its
`discovery` not a mapping, `modelRoles` not a mapping, or `modelRoles.default` neither text nor a
list of text. A missing or empty file, or one whose document is only `null`, is an empty one (apply
gives it a mapping); a `null` mapping is an absent one, as Oh My Pi reads it. A list of default
selectors (fallbacks in order) is the person's default, never pluxy's: apply replaces it with
pluxy's model and Clear puts the list back. A preview reads the files once more if one changed
between its pin and its read, and refuses if it changed again. Neither file has a version field, so
none is checked. With neither YAML name in the folder, a legacy `models.json` or `settings.json`
there refuses them too: Oh My Pi moves it into a
new YAML file when it next starts, which a file made here would stop (settings Oh My Pi still holds
in `agent.db` from an old version are not seen). The key may not start with `!` (Oh My Pi runs such
a value as a command) or hold spaces; Oh My Pi first reads `apiKey` as an environment variable's
name, so a key equal to a set variable's name would be replaced. The model is one ID: no spaces or
commas (a role's fallbacks), not starting with `@` (a role) or `*`. Restore Backup puts the
originals back whatever the files hold now.

## `grok-setup`

`pluxy/grok-setup` sets up Grok Build (xAI's coding CLI, `grok`) to use pluxy's core and says how to
start it, for the desktop app's Grok Build page (`../app/README.md`, "Grok Build"). It writes one
file, Grok Build's user config, through `lib/transactions` (set `grok`): link-preserving,
journalled, the original and applied images in the host's vault, and its manual backups. It exports
nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: one selection (`file`, or `root` and `path`), by default `~/.grok/config.toml` in the app's `HOME`; `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and `settings`: `state` (`not-configured`, `configured`, `needs-update`), `base_url`, `key_set`, `default` (`models.default` as the file has it), `model` (the default's model when it is pluxy's entry), `models` (pluxy's entries: `model`, else the one the entry's name says, `name`, `context_window`), `legacy` (how many `cpa-gui/` entries an older app left); for a file Grok Build would not take, `{state: "refused", reason}`, the file still pinned; never the key |
| `apply` | `write`; secret `key` (value) | One pin, `base_url`, `key` (refused when it holds a `$`: Grok Build expands `$NAME` in its strings), `model` (the default: one of `models`), `models`: 1 to 256 of `{model, name, context_window}` (`name` empty: the model) | The library's outcome |
| `clear` | `write` | One pin | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | One pin | The library's outcome: the original bytes back (Restore Backup) |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | — | `{"client": "Grok Build", "kind": "terminal", "program": …, "args": []}`, for the launcher: `program` is `~/.grok/bin/grok` (`grok.exe` on Windows, from `install.ps1`) when that is a program (where Grok Build's install script puts it, often not on a desktop session's PATH), else `grok` |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations (`lib/transactions`), one file per version, diffed by TOML setting path | The library's answers: IDs, paths, presence, sizes and setting names, never a value |

What `apply` writes, editing the file in place with `toml_edit` (comments, formatting, order and
everything else kept), with Grok Build's own keys (its configuration reference,
`docs/user-guide/26-config-reference.md` in xai-org/grok-build, and Grok Build 1.0.77 run offline
on a temporary home, read 2026-11-14):

- `models.default` = `pluxy/<model>`;
- one table `[model."pluxy/<model>"]` per listed model, setting `model`, `base_url` (the core's
  address with `/v1`), `name` (shown in Grok Build's model picker), `api_key` (the client key; a
  model's own key wins over a signed-in session and `XAI_API_KEY`, so the person's login stays), `api_backend =
  "chat_completions"` (the OpenAI Chat Completions API, as the old app wrote) and `context_window`
  (it drives auto-compaction; the page offers 200000, the old app's default). Other keys the person
  added to such a table stay; pluxy's tables for models no longer listed are removed. Entries of
  the person's own and an older app's `cpa-gui/` entries are left as they are.

Clear's three-way merge (TOML, values compared whatever their formatting) brings back the person's
default model, unless they changed it since, and removes the untouched entries; a value the person
changed in a pluxy entry stays. A file Apply created and nothing else changed is removed. Apply
leaves the file owner-only (0600: it holds the key); Clear and Restore Backup keep the mode it has.
The state is `configured` only when every pluxy entry is whole (a `model`, `api_backend =
"chat_completions"`, a key and the default's `base_url`); an entry missing any of them reads as
`needs-update`, so the page offers Repair Connection. A preview reads the file again once if it
changed between the pin and the read, and refuses if it changed again. Apply and Clear refuse,
before writing, a file Grok Build would not take or Apply cannot edit (a preview still pins it and
reports it refused, so Restore Backup reaches it): not UTF-8 or not TOML (Grok Build itself refuses such a file), `models` or `model` not a
table, `models.default` not a string, or a `model."pluxy/…"` entry not a table. Grok Build has no
version or schema field, and no version bound is assumed. A missing or empty file is an empty one.
The tool's environment has no `GROK_HOME`: a person who moved Grok Build's folder selects the file
or folder. The folder is `~/.grok` on every system (`%USERPROFILE%\.grok` on Windows: xAI's
config reference and `xai-dirs`, grok-build main at 2bdd1d6a6369, read 2026-11-17). A project's `.grok/config.toml` cannot hold models (Grok Build reads only MCP, plugin
and permission settings there), so only the user config is written.

## `hermes-setup`

`pluxy/hermes-setup` sets up Hermes Agent to use pluxy's core and says how to start it, for the
desktop app's Hermes Agent page (`../app/README.md`, "Hermes Agent"). It writes Hermes Agent's
`config.yaml` through `lib/transactions` (set `hermes`, one target): link-preserving, journalled,
the original and applied images in the host's vault, and its manual backups (the backup browser).
It exports nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: one selection (`file`, or `root` and `path`); or `folder`: a Hermes home folder (what `HERMES_HOME` names; it may be a link), whose `config.yaml` is used; by default `config.yaml` in the home Hermes uses (below); `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and `config`: `state` (`not-configured`, `configured`, `needs-update`), `base_url` (the provider's `api`), `key_set`, `provider` (`model.provider`), `model` (`model.default`, when the provider is pluxy's), `legacy` (the old desktop app's `cpa-gui` provider is there); or, for a file Hermes would not take, `state` `refused` and `reason`; never the key |
| `apply` | `write`; secret `key` (value) | One pin, `base_url`, `key`, `model` (one model ID) | The library's outcome |
| `clear` | `write` | One pin | The library's outcome: the values Apply wrote go, edits made since stay |
| `restore` | `write` | One pin | The library's outcome: the original bytes back (Restore Backup) |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | — | `{"client": "Hermes Agent", "kind": "terminal", "program": <~/.local/bin/hermes (on Windows hermes.exe in the bin folder of Hermes' home, where install.ps1 puts it) when it is a program there, else "hermes">, "args": []}`, for the launcher |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations (`lib/transactions`), one file per version, diffed by setting name (the file's YAML read as its value) | The library's answers: IDs, paths, presence, sizes and setting names, never a value |

Hermes Agent keeps its settings in `config.yaml` in its home folder: `HERMES_HOME`, else
`~/.hermes` on Linux and macOS and `%LOCALAPPDATA%\hermes` on Windows (`AppData\Local\hermes` in
the home folder when `LOCALAPPDATA` is not set or is blank, as Hermes reads it, or is not a full
path: `_platform_default_hermes_home` in
`hermes_constants.py`, main at c664ffdbccd9, read 2026-11-17); with no `--profile` flag, a profile
named in that folder's `active_profile` (other than `default`) makes the home its
`profiles/<name>`. The tool's environment has no `HERMES_HOME`, so by default it uses that
folder's `config.yaml`, or the active profile's
`config.yaml` (a name that is not a profile ID, or a profile folder that is not there, refuses: the
person selects the folder); a person who sets `HERMES_HOME` selects that folder. Hermes's own checks
that a profile folder is a live profile are not repeated. Read at source (NousResearch/hermes-agent
at tag v0.23.1, 3b4215785882, 2026-11-09): `hermes_constants.py`, `hermes_yaml.py`,
`hermes_cli/config.py`, `config_providers.py`, `config_version_stamp.py`, `runtime_provider_custom.py`,
`providers.py`, `model_setup_flows_custom.py`, `main.py` (profiles), `setup-hermes.sh` and
`website/docs/user-guide/configuring-models.md`.

What `apply` writes, as `hermes model` does for a provider of the `providers:` map:

- `providers.pluxy`: `api` (the core's address with `/v1`), `api_key` (the client key),
  `transport` `chat_completions` and `default_model` (the model). Keys there that Hermes would read
  before these, or in their place, are removed: `api_mode`, `key_env`, `api_key_env`, `key_cmd`,
  the address as `base_url` or `url`, `model`, the camelCase spellings `baseUrl`, `apiKey`,
  `apiMode`, `keyEnv`, `apiKeyEnv` and `defaultModel`, and `enabled` when it is not `true`; a
  preview shows such a file as needing an update. Its other members, such as the person's per-model `models` metadata or
  `extra_headers`, and every other provider stay. Hermes lists the core's models from its
  `/v1/models` itself, so no `models` map is written.
- `model`: `provider` `custom:pluxy` (Hermes's name for that entry), `default` (the model) and
  `api_mode` `chat_completions`; an earlier endpoint's `base_url`, `api_key`, `key_env` and
  `api_key_env` there are removed (`key_env` would win over the entry's key). A `model:` written as
  plain text, the default model, becomes a mapping, as Hermes itself does. Everything else stays.

The file is edited in place through a lossless YAML tree (yaml-edit; the library's YAML
editor, shared with `omp-setup`): comments, key order, quoting and indentation stay, and only keys whose values change
are touched. It is read as YAML 1.2 (saphyr) and every edited file is read again and must hold
exactly the intended value, else nothing is written. Hermes reads it with YAML 1.1 rules (ruamel),
which take some plain words and numbers differently, so the key and the model pluxy writes are
refused when a YAML 1.1 reader takes them for anything but text (a boolean such as `yes`, `on` or
`n` in YAML 1.1's spellings, a null, `=`, an integer in decimal, binary, octal, hex or sexagesimal
form, a float, `.inf`, `.nan` or a timestamp; a hex key or a UUID is text), when they hold `${`
(Hermes expands it from the environment) or spaces. Clear's three-way merge brings back what Apply replaced unless the
person changed it since, and edits only what differs; a file Apply created that holds nothing else
is removed. An old desktop app's `custom_providers` entry named `cpa-gui` (or a `providers.cpa-gui`)
is left alone. `apply` and `clear` refuse, before writing, a file Hermes would not take, or would
not take pluxy's values in; `preview` pins it all the same and reports it `refused` with the reason,
so Restore Backup and the backups still reach it: not UTF-8, not YAML (a repeated key too), more than one document, a
top level that is not a mapping, a tag, `providers` or `providers.pluxy` not a mapping, `model` not
a mapping or text, `model.provider` or `model.default` not text. A missing or empty file, or one
whose document is only `null`, is an empty one (apply gives it a mapping); a `null`
`providers.pluxy` is an absent one. A preview reads the file once more if it changed between its pin
and its read, and refuses if it changed again. `_config_version` is not checked: Hermes reads every version's `providers:` and treats a
file without one as current. Restore Backup puts the original back whatever the file holds now.

## `pi-setup`

`pluxy/pi-setup` sets up Pi to use pluxy's core, installs, updates and uninstalls the CLIProxyAPI
provider plugin Pi needs for it, and says how to start Pi, for the desktop app's Pi page
(`../app/README.md`, "Pi"). It writes Pi's files through `lib/transactions` (set `pi`, targets in
this order, all in one agent folder: `cliproxyapi.json`, `settings.json`,
`cliproxyapi-models.json`): link-preserving, journalled, the original and applied images in the
host's vault, and its manual backups (the backup browser). It exports nothing.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: the three files' selections (`file`, which may be a link, as stow makes; or `root` and `path`), in one folder; or `folder`: an agent folder (it may be a link, as chezmoi can make; a link inside it is refused, so linked files are selected as `targets`); by default the three files in `~/.pi/agent` in the app's `HOME` (below), which must exist (Pi makes it when it first runs); `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and `pi`: `state` (`not-configured`: the plugin is not listed; `configured`; `needs-update`), `base_url` (cliproxyapi.json's), `key_set`, `provider` (the plugin's provider ID), `default_provider`, `model` (Pi's default model), `registered` (settings.json lists pluxy's plugin), `legacy` (it lists an older app's npm copy), `adapter`: `installed`, `version`, `bundled`, `update`, `foreign` (a folder of that name pluxy did not install); never the key. Files Pi would not take are pinned all the same, so Restore Backup and the backups reach them: `state` is `refused`, `reason` says why, and `adapter` is still there. A file another program writes between the preview's two reads is read again once, else the preview is refused |
| `apply` | `write`; secret `key` (value) | Three pins, `base_url`, `key`, `model` (one model ID) | The library's outcome and `adapter`: `installed` or `kept` |
| `update` | `write` | `folder`: the agent folder | `{"outcome": "updated", "version"}` |
| `uninstall` | `write` | Three pins | The library's outcome and `adapter_removed` |
| `clear` | `write` | Three pins | The library's outcome (the values Apply wrote go, edits made since stay) and `adapter_removed` |
| `restore` | `write` | Three pins | The library's outcome (the original bytes back: Restore Backup) and `adapter_removed` |
| `status` | `read` | — | The library's status |
| `recipe` | `read` | — | `{"client": "Pi", "kind": "terminal", "program": <~/.pi/agent/bin/pi, else ~/.local/bin/pi, when an executable file is there, else "pi">, "args": []}`, for the launcher. On Windows always `pi` from the PATH: `pi.dev/install.ps1` makes `pi.cmd` launchers, not npm's shims, so starting a Pi installed that way is refused at launch (`execution` runs a batch file only as an npm, pnpm or Yarn shim), while an npm install of Pi starts; setting Pi up works either way |
| `backup-list`, `backup-diff`, `backup-create`, `backup-restore`, `backup-delete` | `read`, `read`, `write`, `write`, `write` | The library's standard backup operations (`lib/transactions`), the three files per version, diffed by setting name | The library's answers: IDs, paths, presence, sizes and setting names, never a value |

Pi keeps its user settings in its agent folder, `~/.pi/agent` on every system (`getAgentDir` in
`packages/coding-agent/src/config.ts` at earendil-works/pi v1.5.2 joins `os.homedir()`, so
`%USERPROFILE%\.pi\agent` on Windows) (`PI_CODING_AGENT_DIR` moves it; the
tool's environment does not have it, so a person who uses it selects that folder): `settings.json`
holds Pi's settings, among them `packages` (npm, git or local Pi packages), `defaultProvider` and
`defaultModel`; Pi reads it as strict JSON. Pi talks to pluxy's core through the CLIProxyAPI
provider plugin, which reads `cliproxyapi.json` in the same folder (`baseUrl`, `apiKey`, and
optional `providerId`, `providerName`, `fast` and `pause`; `CLIPROXYAPI_*` variables in Pi's
environment win over it), registers the provider `cliproxyapi`, lists the core's Codex catalog
(`/v1/models?client_version=pi`), sends requests to `/backend-api/codex/responses` and caches the
list in `cliproxyapi-models.json`. Read at source: earendil-works/pi at 17d6ca759a07 (Pi 1.5.2,
2026-11-14): `packages/coding-agent/src/config.ts`, `core/settings-manager.ts`,
`core/package-manager.ts`, `core/extensions/loader.ts` and `docs/settings.md`, `docs/packages.md`,
`docs/quickstart.md`; the plugin, npm `@router-for-me/pi-cliproxyapi-provider` 1.4.25.

**The plugin.** The old desktop app ran `pi install npm:@router-for-me/pi-cliproxyapi-provider`,
which fetches whatever the npm registry serves. This tool never runs Pi or npm and fetches
nothing: it carries a reviewed copy of that plugin (`pi-setup/adapter/`, MIT, its `LICENSE` kept)
in its signed program, and writes it into the agent folder as a local Pi package,
`pluxy-cliproxyapi-provider/`, with a marker file `.pluxy-adapter.json` (this package and the
copy's version, `1.4.25-pluxy.1`). Pi loads a local package from its folder and never installs or
updates it. **Update** replaces pluxy's copy with the one the installed tool carries (a newer
plugin comes with a new pluxy/pi-setup); the page offers it when the installed copy's version
differs. A folder of that name without pluxy's marker is someone else's: Apply and Update refuse,
and nothing removes it. The review read the plugin's network, process, file and code-loading calls
(it starts no processes; it fetches the core's model list and `https://models.dev/api.json` for
prices) and found one hole, patched in the copy: the plugin wrote a patched copy of a Pi module to
a fixed name in the shared temporary folder and imported it, so another user of the machine who
made that folder first could run code as the Pi user; the copy keeps it in its own folder's
`.cache` (mode 0700) instead. On Windows, where Node ignores the mode, the copy's files and
`.cache` take the agent folder's access list (in the person's profile, not a shared folder); the
tool writes the copy's files there with their folder's list, and on Linux and macOS 0644 in 0755
folders. The plugin declares Pi `>=0.82.0` (`@earendil-works/pi-coding-agent`);
the tool cannot run `pi --version`, so it does not check.

What `apply` writes:

- the plugin, when pluxy's copy is not in the agent folder;
- in `cliproxyapi.json`, `baseUrl` (the core's address, without a final `/` or `/v1`) and `apiKey`
  (the client key, which the plugin sends as a bearer token); the other keys stay;
- in `settings.json`, the local package `./pluxy-cliproxyapi-provider` in `packages` (the relative
  form Pi itself writes; an entry the person narrowed stays as it is), without an older app's
  `npm:@router-for-me/pi-cliproxyapi-provider` entry (both would register the same provider and
  commands; its npm files stay), `defaultProvider` (cliproxyapi.json's `providerId`, else
  `cliproxyapi`) and `defaultModel`; the other packages and settings stay;
- `cliproxyapi-models.json` is removed, so the plugin lists the core's models afresh.

Files are written back as JSON with two-space indentation; key order and numbers' digits stay.
Clear's three-way merge brings back what Apply replaced unless the person changed it since; on top,
pluxy's plugin never stays listed, an entry Apply took out of `packages` comes back, and a model
cache Apply removed is removed again when there was none before Apply (else the plugin's newer one
stays). **Uninstall** takes only the plugin's entry out of `settings.json` (through `apply`, so
Clear and Restore Backup still work afterward) and removes its folder; the connection settings stay,
as in the old app. Clear, Restore Backup and Uninstall remove pluxy's plugin folder once
`settings.json` no longer lists it. `preview` and `apply` refuse, before writing, a file Pi or the
plugin would not take: `settings.json` or `cliproxyapi.json` not strict JSON (a byte order mark
too) or not an object, `packages` not a list of package sources (a string, or an object with a
string `source`), `defaultProvider`, `defaultModel`, `baseUrl`, `apiKey` or `providerId` not text.
Clear and Uninstall refuse such files too; Restore Backup still puts the originals back. A missing file is an empty one; the cache is not checked (Apply removes it). Apply writes the files owner-only (the library takes away the group's and others' bits), since `cliproxyapi.json` holds the key. Neither file has a
version field, so none is checked. The key may not hold spaces; the model is one ID without spaces.

## `execution`

`pluxy/execution` prepares the launches of pluxy's desktop app: an agent in a terminal, a program
run directly, or a file opened in the person's own app. The app's Launcher page, its tray's
Start… menu and the client pages' Start CLI and Start App use it, and so does the Codex session
reader to open a session's file. It never starts a process itself and never holds a secret's
value: the host confirms each launch natively, spawns exactly what was confirmed when the program asks, and resolves secrets from
its vault at the spawn (`../app/app-host/README.md`, "Launches and managed runtimes").

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `terminals` | `read` | `search_path`: the launch environment's PATH entries | `terminals`: Automatic first, then each terminal found, with its label and path |
| `prepare` | `read` | `kind` (`terminal`, `direct` or `open`). For `terminal` and `direct`: `program` (a name looked up on `search_path`, or a full path), `override` (a full path used instead), `args`, `cwd`, `terminal` (default Automatic), `search_path`. For `open`: `file`, `within` (the folder it must be inside), and optionally `search_path` | A launch's `executable`, `argv` and `cwd`; for a terminal the `program` it runs; for an open launch the `file` and `within` folder, resolved |
| `launch` | `launch` | What `prepare` gave, with `lifetime` (`detached`, the default, or `managed`: for a direct launch only, see below), `environment`, `stdin` and the secret field `key` (a reference only) | The host's answer to its spawn request: `lifetime`, and a managed runtime's `runtime` and `pid` |
| `runtime` | `read` | `op` (`status`, `list`), `runtime` | The host's answer |
| `control` | `write` | `op` (`stop`, `restart`), `runtime` | The host's answer |

It exports `prepare` and `launch`, under export contract 1: another package prepares and starts
launches, but never looks at, stops or restarts its runtimes, and the app asks before each
`control` call. Terminals, in Automatic's order (the old
desktop app's list): `x-terminal-emulator` (System terminal), `gnome-terminal` (`--`), `konsole`,
`xfce4-terminal` (`-x`), `mate-terminal` (`-x`), `kitty`, `alacritty`, `ghostty` and `xterm`, each
running the program after `-e` unless noted. A terminal launch runs `<terminal> <flag> <program>
<args>`; a direct one `<program> <args>`. The program must be an executable file, the working folder
an existing absolute folder. The terminal finds the program by that path as it starts, after the
app's checks, so the app refuses a terminal launch whose program another user could change or
redirect by then: one below a folder others or a group may write (a sticky folder such as `/tmp`
aside, and the person's own private group, as Homebrew on Linux and a umask of 002 make them: a
group no other user in `/etc/passwd` and `/etc/group` has, users and groups from other sources
such as LDAP being unseen),
reached through a link such a user could change, through `/proc`, or that such a group may write
(`../app/app-host/README.md`, "Launches and managed runtimes"). `launch` refuses, before the host
is asked, a launch of a terminal of the list above whose argv starts with its exec flag but that
does not name as its `program` the argument right after it, one that names a `program` whose
argv does not start with that flag, and on Linux and macOS a launch that names a `program` but
is no terminal's: the app checks `program` as the file the terminal runs. A terminal of the list
started without its exec flag and naming no `program` (its own window, or its own options) is a
direct launch of it.

A launch's `lifetime` is `detached` or `managed`, and **`managed` applies to direct launches only**.
The host keeps and stops a managed runtime through the process it started, and for a terminal
launch that is the terminal's own launcher, which many terminals end at once after handing the
work to a server process (gnome-terminal's client hands off to `gnome-terminal-server`; konsole
and kitty may in single-instance mode). Stop and status would track a process that is gone, not
the agent. The host therefore refuses a `launch` that names a terminal's `program` with
`lifetime: "managed"` (`invalid_request`, before the confirmation), whoever asks; a terminal
launch is detached. To have the app stop an agent, launch it directly
(`../app/app-host/README.md`, "Detached and managed").

An **open** launch opens a file in the app the desktop opens its type with, as the old desktop
app's "Edit with local app" did: it runs `xdg-open <file>`, or `gio open <file>` when there is no
`xdg-open`, in the file's folder. The opener is taken only from `/usr/local/bin`, `/usr/bin`,
`/bin` and `/run/current-system/sw/bin`: an open launch takes no `search_path` (only a
native-test build of the program does, for the native test's stand-in opener). The caller names
the file and the folder it may open files in (`within`), for the Codex session reader its
sessions folder: both are resolved, links followed, and the file must be a regular file with no
execute bit inside that folder, which must not be `/`, the home folder or a folder above it. That
folder and every folder between it and the file must be the user's own and writable by neither
group nor others (`prepare` refuses the call otherwise, so the caller learns before the person
is asked). The resolved file is the opener's argument. The app checks all of this again, and more
(the file's content type, who owns and may write the file, the launch's exact shape), shows the
file, its type and the folder in its confirmation and binds them
(`../app/app-host/README.md`, "Launches and managed runtimes"). Which app the opener then starts
is the desktop's choice for the file's type: the confirmation names the opener, not that app.

A call from another package (a tool's import, or a page's) may prepare only an open launch: a
terminal or direct preparation answers whether programs and folders exist, so only the app's own
window asks for those.

### On Windows and macOS

There the execution program is also the terminal and the opener of the launches it prepares: the
app runs it, as the confirmed launch, with arguments that name a launch mode, and it runs that
mode instead of serving operations (as a tool it gets no arguments). A launch's `lifetime` and the
host's checks are as on Linux.

**Windows.** The terminals are `powershell` (PowerShell, Automatic's first), `cmd` (Command
Prompt) and `console` (Console window). A terminal launch runs `<execution> terminal --with <kind>
-- <program> <args>`, which the app starts in a new console: Windows shows it in the person's
default terminal app (Windows Terminal, or the console host). The execution program runs the
program there in the launch's folder and environment, leaving Ctrl+C to it, says how it ended,
and then waits for Enter (`console`) or stays at a `cmd.exe /D` or `powershell.exe -NoLogo
-NoProfile` prompt, both from the System32 folder Windows reports (never the launch's
`SystemRoot`). There is no Windows Terminal kind:
`wt.exe` splits its command line its own way, so the arguments confirmed would not be the ones
it runs. In the native test's build (feature `native-test`) nobody is at the window, so the
`console` window does not wait for Enter, and the shell after the program runs one fixed command
that writes its working folder to `pluxy-after-exit` in that folder, then exits; a normal build
does neither.

Windows cannot run an open file, so the execution program runs the program by its path, and
first holds it: it opens the program and every folder above it so that none of them can be
renamed, removed or replaced, nor the program written, until the program has started. With all of
them held it checks that the path names exactly what it holds (the program's final path is the
path named, so no link, junction or short name is on the way, and it is the same file by volume
serial number and file ID), that the program is a PE image (`.exe` or `.com` starting with `MZ`,
and no `.com.exe` beside a `.com`), and, through the held handles, that no account but the user,
SYSTEM, Administrators and TrustedInstaller may change the program or its folder (another
account's file there would load as one of the program's libraries), nor remove or rename what a
folder above holds (owner and access list, `pluxy_files::others_may_change`; adding to a folder
above is allowed, as every account may in `C:\`). So a program another account swapped in after
the app checked it, or could swap in before it starts, never runs. Otherwise the window says
`pluxy: will not run "<program>": "<path>" …`, naming the path and why, and the program does not
start. The user's own processes, and administrators', can still swap the program before it is
held, as on Linux and macOS.

A program is a PE image (`.exe` or `.com` starting with `MZ`), looked up as `<name>.com`,
`.exe`, `.bat`, `.cmd` in each search path folder in turn. A batch file runs only as an npm, pnpm
or Yarn command shim the execution program can read: it becomes what the shim runs, Node
(`node.exe` beside the shim, else on the search path) with the shim's script and arguments, or
the shim's own program; any other batch file is refused, since `cmd.exe` would read the
arguments its own way. For the same reason `cmd.exe`, `wt.exe`, `conhost.exe` and the other
programs that read their command line their own way start only with no arguments. The program (for a
shim, what it runs) and the working folder are judged and confirmed by their final paths, links
and short names resolved, so a link to `cmd.exe` is `cmd.exe`. Paths are shown
and confirmed in their plain form (`C:\…`, not `\\?\C:\…`), and a path whose plain form would
name another file (a name ending in a dot or a blank, a device name such as `NUL`) is refused.

A direct launch may name a packaged app as `shell:AppsFolder\<AppUserModelID>` (such as the Codex
app), with no arguments: `<execution> app -- <AppUserModelID>` starts it through the shell. An
open launch runs `<execution> open -- <file>`, which opens the file with the app Windows has for
its extension (`ShellExecuteExW`, verb `open`, the extension as its class, no dialog). The file
must be `.txt`, `.json`, `.jsonl`, `.ndjson`, `.md` or `.log`, on a local drive, with no
alternate data stream and a path shorter than 260 UTF-16 units, and some app must open its type:
`prepare` checks that (the shell would report success and open nothing). Its folders must be real
folders (no link, junction or other reparse point) writable by no one but the user, SYSTEM and
Administrators.

**macOS.** The terminal is Terminal (iTerm2 and Ghostty are not offered yet). A terminal launch
runs `<execution> terminal --with terminal --app <Terminal.app> -- <program> <args>`. It makes a
private folder in the per-user temporary folder with a Unix socket and a `.command` file, has
Terminal open the file (`/usr/bin/open -a`), and hands the program, arguments, folder and
environment over the socket to the first connection from this user; the file only runs
`<execution> attach <socket>`, so nothing of the launch is written to a file or read by a shell.
The program itself goes over the socket too, as the file the app confirmed and checked: the app
gives it to the execution program as descriptor 3, and a terminal launch without it is refused.
That refusal is silent today: the app does not read the terminal mode's exit code or error
output, so an app that gives no descriptor (one older than this execution program) sees its
terminal launches start and no window open. A terminal launch must name as its `program` the
argument right after `--`.
In the window, that file runs, by the path it has then, once no other user but a member of
`admin` can change it or a folder above it, each on a volume that keeps ownership (not one with
Finder's "Ignore ownership on this volume"), and that path still names it (the rule the app holds
a direct launch's program to); otherwise the window says which path another user could change,
and nothing runs. A file put at the program's path since the app checked it never runs. The
program runs with the launch's environment and the window's own `TERM…`, `LC_TERMINAL…` and
`PATH`. When macOS holds `open` back for 20 seconds (a quarantine or a
permission prompt), the launch fails as blocked; when no window takes it within 60 seconds, as
expired. A launch whose hand-over would be over 1 MiB (the program, arguments, folder and
environment) is refused before Terminal opens.

A direct launch may name an application bundle (`….app`) in `/Applications`, `~/Applications` or
`/System/Applications`, with no arguments: it runs `/usr/bin/open -a <bundle>`. An open launch
runs `/usr/bin/open <file>`, which opens the file in the app LaunchServices has for its type. The
types are Windows' and Linux's, the file must carry no attribute that can choose another app
(`com.apple.FinderInfo`, `com.apple.ResourceFork`, `com.apple.LaunchServices.OpenWith`), and
LaunchServices must have an app for it.

On both, an app launch carries nothing but the app (no environment, standard input, secret or
managed lifetime), and a terminal launch takes no standard input: the execution program refuses
either before the host is asked.

## `wsl`

`pluxy/wsl` (program `bin/pluxy-wsl.exe`, Windows only: on any other system every operation answers
`unsupported`) tells pluxy's desktop app which WSL distributions and users there are, checks one,
and prepares launches inside it. The app uses it to list and check the distributions and users a
client page offers and to prepare a launch there; it then runs the client package's own Linux
program in the distribution through `wsl.exe` itself ([Tools in
WSL](../app/app-host/README.md#tools-in-wsl-windows)). This tool only reads. It writes nothing
anywhere and never starts a launch. It runs only Windows' own `wsl.exe`, from System32 as
`GetSystemDirectoryW` reports it: with no window, empty standard input, an environment of
`SystemRoot`, `WINDIR` and `WSL_UTF8=1` only, its output read while it runs and capped, and a
deadline after which it is ended through its own handle (10 seconds to list, 60 for a run inside a
distribution, which may have to start). The call's cancel ends it too. WSL 1 sometimes refuses a
launch with `0xd0000225` before anything runs; such a run is tried again, up to three times, two
seconds apart, within the deadline. Inside a distribution it runs one fixed `sh -c` script per
operation, with no login shell and no rc file; names reach a script only as `wsl.exe`'s `-d` and
`-u` values or as the script's arguments.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `distros` | `read` | none | `distros`: each distribution's `name`, `default` (true for WSL's default) and `version` (1, 2 or null); `installed`: false when WSL is missing or not enabled, or no distribution pluxy can use is installed (no `wsl.exe` in System32; a listing that names none, as the `wsl.exe` on GitHub's windows-latest answers with no distribution; or `wsl.exe --list` failing with WSL's error code `WSL_E_DEFAULT_DISTRO_NOT_FOUND` or `WSL_E_WSL_OPTIONAL_COMPONENT_REQUIRED`; an old inbox `wsl.exe` of Windows 10 that prints no code answers `unavailable` instead). From `wsl.exe --list --quiet`, which starts no distribution, and the registry (`HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss`), read only for the default mark and the version (the WSL 2 bit, `0x8`, of each distribution's `Flags`, as `wsl.exe --list --verbose` reports it; its `Version` value is the registration's format, not the WSL version) |
| `users` | `read` | `distro` | `users`: root (last) and each person's account (user ID 1000 to 59999, a login shell, an absolute home), each with `name`, `uid`, `home`, `default`, `root`; `default`: the user the distribution starts as; `arch` (`amd64` or `arm64`); `wsl` (the version). Starts the distribution and reads `getent passwd` (else `/etc/passwd`) as its default user |
| `check` | `read` | `distro`, `user` | As that user: `distro`, `user`, `uid`, `home`, `arch`, `wsl`; `drives`, each Windows drive mounted (`letter`, `path`, and `exec`: false on a `noexec` mount); `automount`, the folder WSL mounts drives in (that of drive C, with a trailing `/`; null with none). Refused when it ran as another user or in another distribution |
| `prepare` | `read` | `distro`, `user`, `kind` (`terminal` or `direct`), `program` (a name found on the user's PATH as `wsl.exe --exec` gives it) or `override` (a full Linux path), `args`, `cwd` (a Linux folder; default the user's home) | `launch`, exactly `pluxy/execution`'s `launch` input: `executable` (System32's `wsl.exe`), `argv` (`-d <distro> -u <user> --cd <folder> --exec <program> <args>`), `cwd` (the person's home folder on Windows, from `HOME`) and `program`; and `kind` beside it, which the host applies before it calls execution (`terminal`: a console window of its own, no standard input, no managed lifetime) |

Names are checked before `wsl.exe` runs: a distribution name as the old app accepted one (not
empty, `.` or `..`; none of `\ / : * ? " < > |`, no control character, no trailing dot or space), at
most 64 characters and not starting with `-`; Docker Desktop's `docker-desktop` and
`docker-desktop-data` are hidden and refused; a user name is a POSIX portable name
(`[a-z_][a-z0-9_-]*[$]?`, at most 32 bytes). A missing distribution or user is `not_found` (WSL 1
reports a missing user as a connection error, so its message says the user was not found or could
not be started). `prepare` finds the program and checks the folder inside the distribution as the
user; a program that only a shell profile puts on the PATH (such as one nvm installs) is not found
by name, and is given by its full path. The program it names is not pinned: it is found again when
the launch starts.

Tests: unit tests on every system (decoding UTF-16 and UTF-8 `wsl.exe` output, hidden and refused
names, the users and mounts read, the `prepare` script run by `/bin/sh` on Linux and macOS);
`tests/real_wsl.rs`, run by the platforms workflow's WSL job with `--ignored` against Windows' own
`wsl.exe` and real distributions under WSL 2 and WSL 1, including the fixture writer's Linux program
run in a distribution as the host runs one, its hello refused for another user or distribution.
No test stands in for `wsl.exe`.

# App tools

App tools are native programs that pluxy's desktop app runs on demand, and the libraries they
share. This folder is a cargo workspace of its own (its own `Cargo.lock`); the desktop app never
links it. The app starts a tool for a call and stops it 30 seconds after its last call ends, and a
stopped tool leaves no process or thread. What a launch started is not the tool's: a detached
launch runs on, and a managed runtime runs until it is stopped, its package is disabled or removed,
or the app quits or restarts for an update.

The contract between the app and a tool (frames, states, jobs, launches, saving files, the vault)
is in [`../app/app-host/README.md`](../app/app-host/README.md) ("App program contract v0"). This
folder implements it and does not change it. This page is the reference for what each tool and
library does.

## Tools and libraries

A tool is a package with a `pluxy-extension.json` manifest and one program; the manifests name
programs for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` and `windows/amd64`
(`wsl`: `windows/amd64` and `windows/arm64` only). "Page" is the desktop app's page that uses the
tool.

| Folder | Package (crate) | What it does | Page |
| --- | --- | --- | --- |
| [`claude-code`](#claude-code) | `pluxy/claude-code` | Points Claude Code at pluxy: edits its settings file | Claude Code |
| [`codex`](#codex) | `pluxy/codex-setup` (`codex-setup`) | Points Codex at pluxy: edits `config.toml` and a model catalog | Codex |
| [`claude-desktop`](#claude-desktop) | `pluxy/claude-desktop` | Points Claude Desktop at pluxy as its third-party gateway: edits three files | Claude Desktop |
| [`opencode`](#opencode) | `pluxy/opencode` | Points OpenCode at pluxy: edits its global config | OpenCode |
| [`omp-setup`](#omp-setup) | `pluxy/omp-setup` | Points Oh My Pi at pluxy: edits its models and settings files | Oh My Pi |
| [`grok-setup`](#grok-setup) | `pluxy/grok-setup` | Points Grok Build at pluxy: edits its user config | Grok Build |
| [`hermes-setup`](#hermes-setup) | `pluxy/hermes-setup` | Points Hermes Agent at pluxy: edits `config.yaml` | Hermes Agent |
| [`pi-setup`](#pi-setup) | `pluxy/pi-setup` | Points Pi at pluxy: edits three files and manages Pi's provider plugin | Pi |
| [`execution`](#execution) | `pluxy/execution` | Prepares launches: an agent in a terminal, a program run directly, a file opened in the person's own app | Launcher, the tray's Start… menu, the tool pages' Start section |
| [`codex-sessions`](#codex-sessions) | `pluxy/codex-sessions` | Reads Codex's local sessions, read-only | Codex sessions |
| [`agent-usage`](agent-usage/README.md) | `pluxy/agent-usage` | The usage hub: one store of agents' token usage | Agent usage |
| [`usage-codex`](#usage-parsers), [`usage-claude`](#usage-parsers), [`usage-opencode`](#usage-parsers), [`usage-omp`](#usage-parsers), [`usage-pi`](#usage-parsers) | `pluxy/usage-codex`, `pluxy/usage-claude`, `pluxy/usage-opencode`, `pluxy/usage-omp`, `pluxy/usage-pi` | Usage parsers: read one agent's session files and send the hub token counts | none (their rows show on Agent usage) |
| `usage-fixture` | `pluxy/usage-fixture` | A parser of a test format for the hub's checks; never released | none |
| [`wsl`](#wsl) | `pluxy/wsl` (program `pluxy-wsl`) | Lists WSL distributions and users, checks one, prepares launches inside it (Windows only) | The client pages' WSL choice |
| [`lib/files`](#libfiles) | `pluxy-files` | Private files and folders, locks and path checks on Linux, macOS and Windows | |
| [`lib/program`](#libprogram) | `pluxy-program` | The program side of the contract | |
| [`lib/transactions`](#libtransactions) | `pluxy-transactions` | Link-preserving, journalled config writes and manual backups | |
| [`lib/usage`](#libusage) | `pluxy-usage` | The usage hub's ingest API and what a parser shares | |
| [`lib/sqlite-ro`](#libsqlite-ro) | `pluxy-sqlite-ro` | Opens another program's SQLite database read-only | |
| [`lib/pi-sessions`](#libpi-sessions) | `pluxy-pi-sessions` | What the Pi and Oh My Pi parsers share | |

## Build and test

Run from this folder (`next/app-tools/`):

```bash
cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
cargo build --release --locked -p <crate>       # one tool, a small stripped program; <crate> is the crate name in the table above (for `pluxy/wsl`, `wsl`)
cargo build --locked -p pluxy-transactions --example fixture-writer   # the native test's fixture
```

The `wsl` crate builds the program `pluxy-wsl` (`bin/pluxy-wsl.exe` on Windows). `make tools` in `next/` runs the
same checks, and `cargo build --release` uses the workspace profile (size-optimised, stripped,
`panic = "abort"`).

## Platforms

- **Linux:** `next-app.yml` builds and tests the workspace and runs every tool but `wsl` in the
  app's native test. No job runs the `linux/arm64` programs.
- **Windows and macOS:** `next-app-tools-platforms.yml` runs clippy and the unit tests of the whole
  workspace on `windows-latest` and `macos-latest`. `next-app-platforms.yml` runs the app's native
  test shards there (`platform`, `package`, `tools`, `open`, `update`); the `tools` and `open` shards
  build and start the `execution` program and test tools. `next-app-tools-pack.yml` builds every
  tool but `usage-fixture` on a runner of each platform (`linux/amd64`, `linux/arm64`, `darwin/amd64`,
  `darwin/arm64`, `windows/amd64`) and packs the archives. The desktop app itself supports Linux only for now (`../app/README.md`, "Platforms"),
  so the tools run there in test builds.
- **WSL:** `wsl` is a Windows tool; on any other system every operation answers `unsupported`. The
  WSL jobs of `next-app-tools-platforms.yml` run it, `lib/program`, `lib/transactions`, `claude-code`
  and the app's program host and spawn service against real WSL 2 and WSL 1 distributions. Only
  `claude-code` declares `"environments": ["wsl"]`.
- **Where a client keeps its files** is per tool below. On Windows the tools read `LOCALAPPDATA`
  where a client keeps files there; the app passes `LOCALAPPDATA` and `APPDATA` with `HOME`.
- The usage parsers and the other tools read from the `HOME` the app gives them.

The docs of `docs/ci.md` list the workflows in full.

## Libraries

### `lib/files`

`pluxy-files`: the file rules every tool shares, so no tool has platform code of its own for them.

| Item | What it does |
| --- | --- |
| `create_private_dir`, `create_new_private`, `make_private`, `make_private_open`, `is_private` | Private files and folders belong to this user alone. Linux and macOS: owned by the effective user, mode 0600 (files) or 0700 (folders). Windows: owned by this user, the token's default owner or SYSTEM; the access list must exist, be protected, and allow only this user or SYSTEM; deny entries are ignored. `make_private` works through a handle to the file itself (not a link), refuses anything but a file or folder, and never takes over another owner's file. A folder whose inherited entries deny OWNER RIGHTS the right to change an access list makes `create_*` fail with `PermissionDenied` on Windows. |
| `access_list`, `set_access_list`, `rename_open`, `create_private_in` (Windows) | Read and set an access list as SDDL text through a handle (a NULL list is refused); rename a file through its own handle into a folder handle, with POSIX semantics where the file system has them; create a private file relative to a folder handle. |
| `others_may_write`, `others_may_change` (Windows) | Whether an account other than this user, SYSTEM and Administrators (TrustedInstaller too, for `others_may_change`) may change a path. `Change::Any` looks for any change, `Change::Entries` only for removing or renaming what a folder holds, or the folder, or changing its list or owner. `execution` uses `others_may_change` for its terminal's program and the folders above it, and `others_may_write` for an open launch's folders. |
| `windows_name_problem` | Whether Windows would read a path's name as another file (a stream such as `a.json:x`, a trailing dot or blank, a device name such as `NUL` or `COM1.json`). A string check, the same on every platform. |
| `executable` | Whether a path (a link followed) is a program someone may start by its path: Linux and macOS, a regular file with an execute bit; Windows, a regular file named `….exe` or `….com` that starts with `MZ`. |
| `open_regular` | Opens a file for reading only when it is a regular file, checked on the open handle, without following a link (`O_NOFOLLOW` and `O_NONBLOCK` on Linux and macOS, so a FIFO is refused; on Windows the link itself is opened and refused). |
| `Id`, `mtime_ns` | File identity (device and inode; on Windows the volume serial number and the 128-bit file ID, or the 64-bit file index on file systems without one, such as FAT32) and modification time in nanoseconds since the Unix epoch. |
| `sync_dir` | Flushes a folder so a rename in it lasts; does nothing on Windows. |
| `Lock`, `create_locked`, `take_lock` (macOS, Windows) | A private file this process holds while it lives, made already held or taken when no process holds it; dropping it releases and removes it (`O_EXLOCK` on macOS; on Windows it is open to no one else and the system removes it when the handle closes). |

### `lib/program`

`pluxy-program`: the program side of the contract, for any tool.

| Item | What it does |
| --- | --- |
| `run(handler)` | Checks the host's environment (`Identity::from_env`), says hello, answers `describe` from the package's own `pluxy-extension.json`, and serves calls on stdin and stdout until stdin ends. |
| `serve(input, output, identity, description, handler)` | `run` on any streams, for tests. |
| `Handler::call(&Call)` | One call of a declared operation: its output or an `Error`. |
| `Handler::reconcile(&Call, unknown, journals)` | Before `describe` is answered: the calls of unknown outcome and the open journals the host names. Does nothing by default. |
| `Call` | Fields `id`, `op`, `input`, `chain`, `secrets`, `job`; methods `cancelled()`, `check()`, `progress(value)`, `call(package, op, input)` (an import, through the host), `vault(op, fields)` (backups and journals), `jobs(op, input)` (a consumer's `registrations`, `start`, `cancel`), `batch(value)` and `acked()` (a producer's batches), `spawn()` (asks the host to spawn the launch this call confirmed), `runtime(op, runtime)` (status, list, stop or restart of the package's managed runtimes). |
| `Call::save_file(name, bytes)`, `SaveOutcome` | For a `disclose` call: sends `bytes` as `append` frames of at most `APPEND_BYTES` (783,000) and one `save` suggesting `name`. The person picks the place in the host's dialog. Answer: `Saved { size }` or `Cancelled`, or the host's error (not a `disclose` call, a name holding a path, over 64 MiB, a second save, no dialog). One save per call. |
| `Error` | The contract's error object: `invalid_request`, `unsupported`, `not_found`, `unavailable`, `internal`, `cancelled`, or any code. |
| `read_frame`, `write_frame`, `MAX_FRAME`, `MAX_BATCH` | A four-byte big-endian length then UTF-8 JSON; a program's frames are at most `MAX_FRAME` (1 MiB); a producer's batch at most `MAX_BATCH` (48 KiB of JSON). |
| `environment` | When the host runs a package's Linux program in a WSL distribution it sets `PLUXY_ENVIRONMENT` (`wsl:<distribution>:<user>`) and `PLUXY_ENVIRONMENT_ACCOUNT` (`<user ID>:<home>`). `Identity::from_env` then checks `WSL_DISTRO_NAME`, the effective user's name in `/etc/passwd`, and the user ID and home, and stops before hello on a mismatch. The variable is refused anywhere but Linux. |

Each call runs on a thread of its own. The thread that reads stdin routes cancels and the answers
to a call's own calls, vault and jobs frames and a job's acks. An answer too large for a frame fails
its call with `internal`. A consumer answers the call that started a job before the job's batches
reach it, so it never waits for its own job inside a call.

### `lib/transactions`

`pluxy-transactions`: link-preserving, journalled config writes for any writer tool. It is a client
of the host's vault (backups and journals) through the contract's `vault` frames and keeps no
journal store of its own.

| Item | What it does |
| --- | --- |
| `Selection` | What the person selected: `File(path)` (may be a link, as stow makes), or `Root { root, path }` (a file inside a root folder, which may be a link, as chezmoi can make; `path` is slash-separated parts, a backslash is refused). |
| `Transactions::new(vault, data)` | The operations for one call. `data` is the tool's data folder (`PLUXY_TOOL_DATA_DIR`), where the private `transactions.json` keeps each set's targets and backup IDs, the originals kept for restore and the open journals. A missing data folder reads as empty state; the first write makes it. A link there, or a folder another user owns, is refused. |
| `preview(set, selections)` | Resolves and pins each selection: `{"set", "targets": [pin], "applied", "edited"}`. A pin holds the selected path, the link's text, the resolved file, the identities of link, root, folder and file, and the file's SHA-256. Refused: a link inside a selected root, a part that leaves it (`..`, `.`, empty), anything but a regular file. It reports any path's existence and SHA-256, so a writer must not export it. |
| `apply(set, pins, image, merge, written)` | Writes each pinned target with what `image` makes of its bytes now and records `set` as applied: the original images (with the person's edits since the last apply merged in) and the applied images, as vault backups. |
| `clear(set, pins, merge, written)` | Each target becomes what `merge` makes of its original, its applied image and its bytes now: the writer's untouched values go, the person's edits stay. |
| `restore(set, pins, written)` | Puts the original bytes back (removing a file that did not exist), keeping the file's mode. |
| `reconcile(journals)` | For `Handler::reconcile`: settles each open journal from what is on disk. |
| `status()` | The applied sets, the originals kept for restore and the latest recovery reports. |
| `Merge`, `Json`, `merge_value` | The three-way merge, and the JSON one (two-space indentation, keys in the file's order, exact number digits; comments and other formatting are not kept). |
| `Target`, `Pin`, `pin_schema()` | Resolving, pinning and checked reads and writes; `pin_schema()` is the JSON Schema of a pin, for a write operation's `targets` items. `Pin::from_json` refuses any field `Pin::to_json` does not write. |
| `Folder::pinned(pin)`, `Folder::empty(root, name, marker)`, `put_folder`, `remove_folder` | Writes a folder a tool owns inside a selected root, known by a marker file (pi-setup's plugin folder). |
| `backup_create`, `backup_list`, `backup_diff`, `backup_restore`, `backup_delete`; `backup_call`, `backup_operations(files)` | Manual backups, below. Every writer exports exactly these manifest entries and serves them with `backup_call`; the app's backup browser (`../app/src/backups.tsx`) calls them. |
| `Leaves`, `json_leaves` | A writer's reading of a file into settings (path and value) for a diff. |
| `toml`, `yaml` (cargo features, off by default) | In-place editors for setup tools, so a tool compiles only the parser it uses. `toml` (toml_edit) keeps comments and formatting, sets a key where it stands and merges for Clear. `yaml` (saphyr and yaml-edit) reads YAML 1.2 into a JSON value, edits only keys whose values differ through a lossless tree, re-reads the result, and merges for Clear. |
| `hook`, `Step` (feature `test-hooks`) | Tests only: a hook at each step of a folder write. |

**How a write goes.**

- The pins come back in the write's input, so the operation's native confirmation names the
  selected path and the file it resolves to. The write resolves the selection again and refuses
  (asking for a new preview) if the link points elsewhere, the file was replaced, or its bytes
  changed since the preview. Only the target changes: a link stays the same link.
- Each file is written through a handle on its folder (cap-std), not its path: a new file beside it,
  mode 0600, checked again, given the old file's permission bits (setuid, setgid and sticky left
  out), renamed over it, the folder flushed, then checked to be the file written. `apply` leaves
  the file owner-only; `clear` and `restore` keep the mode the file has. A file that did not exist
  gets 0600 (or the recorded one). At most 8 files of at most 256 KiB each.
- The written file is a new file: hard links keep the old bytes, and the owner, ACLs, extended
  attributes and security label are not copied.
- Before the first write, the old bytes go into vault backups and the operation into a vault
  journal (`journal.begin`; its `intent` names each file's pin and the SHA-256s before and after).
  If a write fails, the files already written are put back, newest first, except a file another
  program changed meanwhile (it stays as that program left it, its original kept for restore). If
  putting one back fails, the journal stays open for the next start.
- Once all are written, the state file records the set and the journal, the journal is committed,
  and backups nothing names any more are deleted.
- After a crash, the next start's `describe` names the open journal. `reconcile` looks at each
  journalled target, removes the temporary files a write left beside it, commits the journal if the
  state file names it or every file holds its new bytes, and otherwise puts back each file that holds
  the new bytes from its backup. It never writes new bytes again and is safe to repeat.
- The writes are cooperative: another program's write between the last check and the rename is not
  seen, and several files are journalled, not written atomically. The writers' write operations say
  so in their descriptions, which their native confirmations show.

**How a folder write goes** (`put_folder`, `remove_folder`).

- Every step goes through the handle on the root folder opened with the `Folder`, so a link pointed
  elsewhere meanwhile changes nothing. The journal's `intent.folder` names the root's resolved path
  and identity, the folder and marker names, the pinned copy's identities and SHA-256, and the two
  hidden names used beside the folder (`.<name>.pluxy-<hex>.old` and `.new`).
- `put_folder` makes the new copy's empty folder under the `.new` name before the journal begins,
  writes the copy there, moves the old copy aside to `.old` and checks that what moved is the copy
  pinned (folder identity, marker identity, marker SHA-256; if not it goes back and the write is
  refused), renames the new copy in, checks it, checks the old copy again, removes it and commits
  the journal. It returns the copy put, pinned. `remove_folder` moves the folder aside, checks it the
  same way, then removes it.
- A hidden copy is removed or put back only while it is the folder of the recorded identity.
- What an operation cannot settle is named in the error and the journal stays open for the next
  start; a failed cleanup never ends the journal.
- `reconcile` commits a put whose new copy is under the name (and removes the old copy while it is
  still the pinned one), commits a removal whose folder aside has no marker, and otherwise rolls
  back. What it cannot settle is left in place and named in the report's `conflicts`; the journal
  ends anyway, so the tool still starts.
- Limits: on Linux and macOS a folder is renamed by name in the held root with its identity checked
  just before; on Windows it is renamed through its own handle (`rename_open`) but cap-std removes by
  the held folder's path. A folder put under a name between the last check and a rename or removal is
  not seen.

**Windows.**

- Permissions are access lists: records keep SDDL text where Linux and macOS keep mode bits. The new
  file is private from the moment it is made (protected list for this user and SYSTEM, relative to
  the folder's handle) and open to no other handle until renamed and closed. `clear` and `restore`
  give it the old file's list.
- The rename goes through `rename_open`. A file another program holds open without sharing delete, or
  one marked read-only, is not replaced: the write fails and is rolled back. The folder is not
  flushed.
- An existing file is written under the name it has on disk, whatever case or short 8.3 name
  selected it; one file selected under two spellings is refused (as on macOS, whose file systems also
  ignore case by default). Names Windows reads as another file (see `windows_name_problem`) and
  device-namespace paths (`\\.\…`, `\\?\GLOBALROOT\…`) are refused.
- Symbolic links and junctions are both links. A path inside a WSL distribution (`\\wsl.localhost\…`,
  `\\wsl$\…`) is refused: the package's Linux program serves those files.

**In a WSL distribution** (`PLUXY_ENVIRONMENT` set), a selection on a Windows drive is refused
(`This file is on a Windows drive; set it up from Windows local.`), and so is a journalled target at
recovery. A path is on a Windows drive when its mount in `/proc/self/mountinfo` is of type `drvfs`,
`9p` or `virtiofs`, or is mounted directly in the folder WSL mounts drives in (the folder holding a
one-letter drive mount, such as `/mnt`).

**Manual backups.**

| Operation | What it does |
| --- | --- |
| `backup_create(set, pins)` | Keeps the pinned files as they are now as one version of `set` (vault backups labelled `bk <set>`); then deletes the oldest versions over `KEEP_BACKUPS` (10). Returns `{"backup", "deleted"}`. |
| `backup_list(set)` | Versions, newest first: `{"backups": [{"id", "created", "files": [{"selected", "path", "target", "exists", "size"}]}], "keep"}`. |
| `backup_diff(set, id, leaves)` | Per file: `exists`, `now_exists`, `same`, `readable` and `changes`, each changed setting's `path` and what a restore does to it (`adds`, `changes`, `removes`), at most 200 with the rest counted in `more`. Never a value. |
| `backup_restore(set, pins, id, written)` | Puts version `id` back (the pins must be of exactly its targets, in order), through a journal like every write. No automatic backup is made. |
| `backup_delete(set, id)` | Deletes version `id` from the state file and the vault. |

A manual backup's bytes are only in the vault; the state file records vault IDs, selections,
targets, presence, sizes and permission bits, never bytes or a hash of them. Retention counts only:
10 versions per set. The library's own backups (`tx `) stay while a set's record, an original kept
after a conflict or a replaced record, or an open journal names them. The vault's limit of 256
backups per package is a backstop.

The example `fixture-writer` (`examples/fixture-writer`, package `pluxy/fixture-writer`) is the
library's test fixture, never a curated tool: it sets top-level values in JSON files with
`preview`, `apply`, `clear`, `restore` and `status`, and `crash_after` makes `apply` exit after that
many files. The app's native test packs it (`next/app/native/transactions.ts`).

### `lib/usage`

`pluxy-usage`: schemas 1 and 2 of the usage hub's ingest API (see
[`agent-usage/README.md`](agent-usage/README.md#schema-1)) and what a parser shares. A parser
implements `Parser` (label, default roots, which files are its, how a file's bytes become rows) and
answers its produces operation with `produce`:

- `scan` lists files: no links, at most 8 folders deep, newest first, at most 20,000.
- `locate` finds a named file again, only under the roots.
- A whole-file parser (`parse`) gets a file of at most 16 MiB. A line-based parser sets `streamed`
  and gets lines one at a time (`parse_lines`, `LineReader`) whatever the file's size; a line over
  16 MiB is skipped and counted.
- `Sink` sends rows in parts within the batch limit. At schema 2, `Sink::event` sends a record that
  more than one file can hold as one keyed event.
- A file that holds many records (a database of sessions) can list as several entries
  (`Parser::entries`, `Listing::Parts`), each a file of the hub's with its own ID and revision, which
  the parser reads itself (`Parser::read_entry`, `Sink::revise`). An entry listed under two roots is
  listed once, at its newest revision; one with an ID over 4,096 bytes is left out; a file whose
  entries cannot be listed (`Listing::Failed`) is listed as itself, so its read reports why. Either
  of the last two makes the listing count as cut short, so the hub marks no file gone. The listing
  is sorted newest first and cut at 20,000 entries.
- `fields` reads the fields usage records share: token counters under the names agents use, a model
  name and a time.

### `lib/sqlite-ro`

`pluxy-sqlite-ro`: `open_read_only(path)` gives a `Db` (derefs to a `rusqlite::Connection`) that
opens another program's SQLite database strictly read-only and creates no file beside it.
`codex-sessions` and `usage-opencode` use it.

- The database path must be a regular file: a link, FIFO, folder or missing file is refused without
  being opened. A `-wal`, `-shm` or `-journal` beside it must be a regular file too.
- A database with a `-shm` (a process has it open) is opened in place with `mode=ro`.
- With no `-shm` and a `-wal`: an empty `-wal` opens `immutable`; a `-wal` with bytes is copied, with
  the database, into a private folder `wal-<pid>-<n>` under `$PLUXY_TOOL_DATA_DIR/tmp` (the system's
  temporary folder when that is unset), read there, and removed when the `Db` drops.
- With no `-shm` and no `-wal`: a database with a WAL header opens `immutable`, any other in place.
- The tool's `tmp` must be a real folder, not a link; one that is not private is made private. Before
  a copy is made, copies left by stopped processes are removed: on Linux a copy is left over when
  `/proc` does not list its pid; on macOS and Windows each copy has a lock beside it
  (`wal-<pid>-<n>.lock`, `lib/files`) and is left over when no process holds it. Nothing is pruned in
  the system's temporary folder.
- On Windows the database path must be a full path, given to SQLite in its long form (`\\?\C:\…`)
  with the `win32-longpath` file layer.
- An `Error` names no path and carries SQLite's error code, not its message; a tool that shows a path
  adds it itself.

A process that starts between the `-shm` check and the open can make one read see a torn state; a
later read is right.

### `lib/pi-sessions`

`pluxy-pi-sessions`: what the Pi and Oh My Pi usage parsers share. Both tools write the same session
format: `*.jsonl` files under `~/.<tool>/agent/sessions` and `~/.<tool>/sessions`, read-only. A
file's ID is its path below the root with the first folder (named after the session's working
folder) replaced by a hash, so an ID names no folder; a session under both roots is one file and the
newer copy is read.

- Usage is in `message.usage` of an entry of type `message`, and in the entry's own `usage` for type
  `usage` (Pi), `model_usage` (Oh My Pi), `compaction` or `branch_summary` (Pi). Nothing else is read.
- `input` is already net of cache reads and writes; `output` includes reasoning tokens (never added
  again). The total is the entry's `totalTokens`, else input + output + cache read + cache write.
- The model is the message's `responseModel`, else its `model`; another entry's `model`; none is the
  empty name.
- The hour is the entry's `timestamp` (ISO), else the message's timestamp (an RFC 3339 string, a `YYYY-MM-DD` date, or a number of seconds or milliseconds); an entry
  with neither is skipped. Every entry whose usage holds a count is one sample.
- An entry with an `id` and a `timestamp` is a schema 2 event whose key is a hash of both, counted
  once by the hub whichever files hold it (a forked or branched session copies its parent's entries).
  An entry missing either is a plain row.

Only counters, a model name, a time and the hashed key leave the parser.

## Setup tools

`claude-code`, `codex`, `claude-desktop`, `opencode`, `omp-setup`, `grok-setup`, `hermes-setup` and
`pi-setup` set a coding client up to use pluxy's core and say how to start it. Each writes its
client's files through `lib/transactions` (one set per tool: link-preserving, journalled, original
and applied images in the host's vault, manual backups through the backup browser) and exports
nothing.

Operations every setup tool has:

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `preview` | `read` | `targets`: the selections (`file`, or `root` and `path`), by default the client's files in the app's `HOME`; `base_url` (optional): the core the state is checked against | The library's preview (pins, `applied`, `edited`) and a tool block (`settings`, `config` or `pi`) with `state`: `not-configured`, `configured`, `needs-update`, or `refused` with a `reason`; never the key |
| `apply` | `write`; secret `key` (value) | The pins, `base_url`, `key`, and the tool's own fields | The library's outcome |
| `clear` | `write` | The pins | The library's outcome: the values `apply` wrote go, edits made since stay |
| `restore` | `write` | The pins | The library's outcome: the original bytes back, files that did not exist removed |
| `status` | `read` | none | The library's status |
| `recipe` | `read` | tool-specific | How the launcher starts the client: `{"client", "kind": "terminal" or "direct", "program", "args"}` (direct: `cwd` too, for some) |
| `backup-list`, `backup-diff` | `read` | the library's | The library's answers: IDs, paths, presence, sizes and setting names, never a value |
| `backup-create`, `backup-restore`, `backup-delete` | `write` | the library's | The library's answers; each is confirmed |

Rules every setup tool follows:

- `preview` and the manual backups take no secret. The client key reaches `apply` only as the
  `key` secret field and is written into the client's file; it is never shown in an output.
- `apply` and `clear` refuse, before writing, a file the client would not take (the tool's
  list is below). `preview` pins such a file all the same and reports `state` `refused` with the
  `reason`, so Restore Backup (`restore`) still puts the original back whatever the file holds now.
- `preview` reads the files twice (the pins, then the values); if another program wrote one in
  between, it reads again once, then refuses, so the values shown are those of the files pinned.
- A missing or empty file counts as an empty one. The files carry no version, and none is checked.
- Clear's three-way merge brings back what `apply` removed or replaced, unless the person changed it
  since. A file `apply` created that holds nothing else is removed.
- The tool's environment has no `XDG_CONFIG_HOME` or client-specific variables
  (`CODEX_HOME`, `OPENCODE_CONFIG*`, `PI_CODING_AGENT_DIR`, `HERMES_HOME`, `GROK_HOME` and so on):
  a person who moved the client's folder selects that folder or file.
- A client's address is the core's address with `/v1` unless a tool says otherwise.

### `claude-code`

Page: Claude Code. Set `claude-code`, one file: `~/.claude/settings.json`. `preview` refuses, and
makes no folder, while `~/.claude` does not exist (Claude Code makes it on first run).

`preview`'s `settings`: `state` (`refused` for a file Claude Code would not take), `base_url`,
`key_set`, each role's model and 1M switch, `startup_model`, `subagent_model`, `max_context_tokens`,
`auto_compact_pct`, `disable_auto_compact`.

`apply` input: one pin, `base_url`, `key`, `opus`, `sonnet`, `haiku`, `fable` (empty: follows Sonnet)
with `<role>_1m`, `startup_model`, `subagent_model` (empty: removed), `max_context_tokens` (100000 to
1000000), `auto_compact_pct` (1 to 100), `disable_auto_compact`.

`apply` writes, keeping everything else (keys in order, numbers with their digits):

- in `env`: `ANTHROPIC_BASE_URL` (the core) and `ANTHROPIC_AUTH_TOKEN` (the client key, sent as a
  bearer token); `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU,FABLE}_MODEL`, each with `[1m]` when its 1M
  switch is on; the `/model` picker's `ANTHROPIC_DEFAULT_*_MODEL_NAME` and `_DESCRIPTION`, and
  `ANTHROPIC_CUSTOM_MODEL_OPTION` with its name and description for the Sonnet model;
  `CLAUDE_CODE_SUBAGENT_MODEL`; `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`;
  `CLAUDE_CODE_AUTO_MODE_SERVER=0`; `CLAUDE_CODE_MAX_CONTEXT_TOKENS`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW`,
  `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` and `DISABLE_AUTO_COMPACT` (`1` or `0`). It removes
  `ANTHROPIC_API_KEY` and `ANTHROPIC_MODEL`, which would win over the values above;
- at the top level: `model` (the startup model; removed when empty), `autoCompactWindow` and
  `autoCompactEnabled`.

Refused: not strict JSON (a
comment, a trailing comma), not an object, an `env` that is not an object of strings, or a `$schema`
other than `https://json.schemastore.org/claude-code-settings.json`.

`recipe`: `{"client": "Claude Code", "kind": "terminal", "program": "claude", "args": []}`.

### `codex`

Page: Codex. Package `pluxy/codex-setup` (crate `codex-setup`; `pluxy/codex` is the core's Codex
extension). Set `codex`, two files in this order: `~/.codex/config.toml` and the model catalog
`~/.codex/pluxy-model-catalog.json` (Codex reads it from `model_catalog_json`).

Extra operations and inputs:

| Operation | Notes |
| --- | --- |
| `preview` | Two selections. `settings`: `state`, `base_url`, `key_set`, `provider` (the current `model_provider`), `model`, `profile`, `catalog`, `remote_compaction`, `agents`, `approval_model`, and each catalog model's editable settings (`system_prompt_chars`, never the prompt). |
| `import` (`read`) | Input `models`: the core's served models (`slug`, `upstream`, `display_name`, `non_codex`). Returns the editable settings of the entries they would get and `missing` (models pluxy has no Codex template for). Writes nothing. |
| `apply` | Two pins, `base_url`, `key`, `model` (one of the catalog's), `approval_model` (empty: Codex default), `remote_compaction`, `agents` (`enabled`, `max_threads`, `model`, `effort`, `interrupt`), `models` (1 to 64: `slug`, for a new entry `upstream` and `non_codex`, `display_name`, `description`, `context_window`, `max_context_window`, `effective_context_window_percent`, `auto_compact_token_limit`, `reasoning`, `default_reasoning`, `input_modalities`, `visible`, `tool_search`, optional `system_prompt`). |
| `recipe` | `target`: `cli` (default; `codex` in a terminal, no arguments), `app` (direct launch of `program`, a full path starting with `/`; Codex has no Linux app, so refused without one) or `service` (`codex app-server daemon restart`, a direct launch in `HOME`). |

`apply` writes in `config.toml`, edited in place with `toml_edit` (comments and formatting stay):

- top level: `model_provider = "pluxy"`, `model`, `model_catalog_json` (the catalog's file name when
  it is in config.toml's folder, else its full path);
- `[model_providers.pluxy]`: `name = "pluxy"`, `base_url` (the core's address with `/v1`),
  `wire_api = "responses"`, `experimental_bearer_token` (the client key); it removes `env_key` and
  `requires_openai_auth` there;
- `capabilities.remote_compaction = "v2"` in that table, or removed (the Remote compaction option);
- `[agents]` (`enabled`, `interrupt_message`, `max_concurrent_threads_per_session`,
  `default_subagent_model`, `default_subagent_reasoning_effort`; empty ones removed), only when the
  file has the table or the settings differ from Codex's defaults. Codex also reads `max_threads`;
  `apply` removes it.

`auth.json` is never written. The catalog is Codex's `{"models": [...]}`. A new entry is made from
pluxy's Codex client template for its upstream name (`../extensions/codex/client_models.json`,
embedded at build time), keeping the fields Codex requires and those that shape how it drives the
model, with `model_messages.instructions_template`. A full template is about 88 KB and a file is at
most 256 KiB, so about ten models fit. A model served by another provider gets the core's non-Codex
values. Existing entries keep every field the form does not edit; each gets the form's settings, its
place as `priority`, and the approval model as `auto_review_model_override`. Clear's merge is TOML
for config.toml and JSON for the catalog.

Refused: a config.toml that is not TOML, or whose `model_providers`, `model_providers.pluxy`, its
`capabilities` or `agents` is not a table, or whose pluxy provider asks for another wire API; a
catalog that is not a JSON object with a `models` list of entries with a `slug`.

### `claude-desktop`

Page: Claude Desktop. Set `claude-desktop`, three files in this order, in Claude Desktop's
third-party folder: `claude_desktop_config.json`,
`configLibrary/00000000-0000-4000-8000-000000831700.json` and `configLibrary/_meta.json`.

The third-party folder is `~/.config/Claude-3p` on Linux, `~/Library/Application Support/Claude-3p`
on macOS and `%LOCALAPPDATA%\Claude-3p` on Windows (with no `LOCALAPPDATA`, or one that is not a full
path, the default preview and `folders` refuse). With the MSIX installer on Windows 10 it can be
`%LOCALAPPDATA%\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Local\Claude-3p`, which the tool takes when
only that one exists. `%APPDATA%\Claude-3p` is not read. The macOS and Windows paths and the file
names beside `configLibrary` follow Anthropic's documentation and its Linux package; they are not
checked against a real macOS or Windows install.

| Operation | Notes |
| --- | --- |
| `preview` | Three selections. `settings`: `state` (`not-configured`, `configured`, `needs-update`, or `refused` with a `reason`), `base_url`, `key_set`, `deployment_mode`, `applied_id`, `hybrid_pointer`, the model rows (`model`, `alias`, `one_m`). With no `targets` and no `configLibrary` in the folder, refused ("Claude Desktop's configuration library does not exist yet"). |
| `folders` (`write`) | Creates `configLibrary` in the third-party folder when missing, private, with missing folders above it (on Windows they take their folder's list). Returns `created`, `folder`. |
| `apply` | Three pins, `base_url`, `key`, `models`: 1 to 16 rows of `model` (what the core serves), `alias` (the name Claude Desktop uses; empty: the model's own), `one_m`. |
| `recipe` | `{"client": "Claude Desktop", "kind": "direct", "program", "args": [], "cwd": <HOME>}`; `program` is `claude-desktop` on Linux and `/Applications/Claude.app` on macOS (or `~/Applications/Claude.app` when only that exists); `unsupported` on Windows. |

`apply` writes, keeping everything else:

- `claude_desktop_config.json`: `deploymentMode` `3p`. The standard app's
  `~/.config/Claude/claude_desktop_config.json` is not touched.
- the gateway profile: `inferenceProvider` `gateway`, `inferenceGatewayBaseUrl` (the core),
  `inferenceGatewayApiKey` (the client key), `inferenceGatewayAuthScheme` `bearer`,
  `inferenceCredentialKind` `static`, `modelDiscoveryEnabled` and `bootstrapEnabled` `false`,
  `disableDeploymentModeChooser` `true` and `inferenceModels`; it removes `selfHostedUrl`. Each row
  becomes `{"name": <alias, else model>, "labelOverride": <model, when an alias differs>,
  "anthropicFamilyTier", "isFamilyDefault", "supports1m", "prefer1m"}`. The two tier keys appear
  only when the name is `claude-<family>`, starts with `claude-<family>-` or holds `-<family>-`, and
  `isFamilyDefault` is true for the first row of each tier; the two 1M keys appear only when the
  row's 1M switch is on.
- `_meta.json`: `appliedId` names pluxy's profile, `entries` gains `{"id", "name": "pluxy"}`, and
  `hybridPointer` is removed; the person's other configurations stay.

Claude Desktop drops a gateway model whose name does not look like an Anthropic model (it must hold
`claude`, `anthropic` or a family name such as `sonnet`, and no other family's such as `gpt` or
`gemini`), so `apply` refuses such a row: a non-Claude model needs an alias, and the core must serve
the model under it (an account's or extension's `model_alias`). This tool does not change the core;
the page can add the alias through the management API.

Clear's merge has two rules of Claude Desktop's own: it seals the key with the device keystore at its
next launch, which is not the person's edit, so Clear still removes the key; and pluxy's entry leaves
`_meta.json` unless the original had it. A profile with `selfHostedUrl` set does not read back as
configured. `apply` and `clear` refuse, before writing, a file Claude Desktop would not
take (`preview` reports it `refused`): not strict JSON, not an object, a profile whose `inferenceModels` is not a list, or an index
whose `entries` is not a list of configurations or whose `appliedId` is not a string. A missing
folder refuses the whole operation before any file is written.

### `opencode`

Page: OpenCode. Set `opencode`, one file: OpenCode's global config in `~/.config/opencode/` in the app's
`HOME` on every system. The default file is the one OpenCode itself
writes: `opencode.jsonc` if it exists, else `opencode.json` if it exists, else a new
`opencode.jsonc`. Project configs and `OPENCODE_CONFIG_CONTENT` still win over the global file.

| Operation | Notes |
| --- | --- |
| `preview` | `config`: `state`, `base_url` (the provider's), `key_set`, `model` (the default, when it is one of pluxy's), `default_model` (the file's `model` as `provider/model`), `models` (`id`, `name`, `attachment`), `old_provider` (an `cpa-gui` provider is there), `v2_provider` (OpenCode 2's `providers.pluxy` is there). |
| `apply` | One pin, `base_url`, `key`, `models` (1 to 256: `id`, `name` (empty: the ID), `attachment`), `model` (one of them). |
| `recipe` | `target`: `cli` (default; `{"client": "OpenCode", "kind": "terminal"}`, `program` is `~/.opencode/bin/opencode` (`opencode.exe` on Windows) when it is a program there, else `opencode`) or `app` (`{"client": "OpenCode Desktop", "kind": "direct", "cwd": <HOME>}`: Linux `/opt/OpenCode/ai.opencode` or `/opt/OpenCode/ai.opencode.desktop`, macOS `/Applications/OpenCode.app` or `~/Applications/OpenCode.app`, Windows `%LOCALAPPDATA%\Programs\ai.opencode.desktop\OpenCode.exe`, refused with no `LOCALAPPDATA`). |

`apply` writes in the form OpenCode 1.x reads and 2.x migrates:

- `provider.pluxy`: `npm` `@ai-sdk/openai-compatible`, `name` `pluxy`, `options.baseURL` (the core's
  address with `/v1`) and `options.apiKey`; `models` holds exactly the listed models, each with
  `name` and, with attachments, `"attachment": true` and `modalities` input text, image and pdf,
  else text only;
- `model`: `pluxy/<model>` (replacing a `{providerID, model}` default, which Clear brings back);
- `$schema`: `https://opencode.ai/config.json`, first, only when the file names none.

The file is edited in place through a JSONC syntax tree (jsonc-parser): comments, formatting,
trailing commas and every other setting stay. An `cpa-gui` provider is left alone. A file with
OpenCode 2's own `providers.pluxy` is never reported configured and `apply` refuses it (the person
removes it). `configured` also needs `npm`, the base URL, the key and a default among pluxy's models.

Refused: not JSON or JSONC by OpenCode's parser rules (single quotes, bare keys, hexadecimal numbers
and missing commas are errors; comments and trailing commas are fine), a leading byte order mark, a
key named twice in any object, not an object, a `$schema` other than
`https://opencode.ai/config.json`, a `provider`, `provider.pluxy`, its `options` or `models` (or a
model entry) that is not an object, or a `model` that is neither `provider/model` text nor
`{providerID, model}`.

### `omp-setup`

Page: Oh My Pi. Set `omp`, two files in this order, in `~/.omp/agent` on every system: the models file
(`models.yml`, or an existing `models.yaml`; custom providers and models) and the settings file
(`config.yml`, or an existing `config.yaml`). Both are YAML.

| Operation | Notes |
| --- | --- |
| `preview` | `targets`: two selections, or `folder`: an agent folder (may be a link) whose two files are used. `config`: `state`, `base_url`, `key_set`, `model` (the default role's, when pluxy's), `default_role` (`modelRoles.default`), `legacy` (an `easy-cliproxyapi` provider is there). |
| `apply` | Two pins, `base_url`, `key`, `model` (one model ID: no spaces or commas, not starting with `@` or `*`). The key may not start with `!` (Oh My Pi runs such a value as a command) or hold spaces. |
| `recipe` | `{"client": "Oh My Pi", "kind": "terminal", "program"}`: `~/.bun/bin/omp`, else `~/.local/bin/omp` (Windows: `~/.bun/bin/omp.exe`, else `omp.exe` in `%LOCALAPPDATA%\omp`) when it is a program there, else `omp`. |

`apply` writes:

- in the models file, `providers.pluxy`: `baseUrl` (the core's address with `/v1`), `apiKey`, `api`
  `openai-responses`, `discovery.type` `openai-models-list`; its other members and every other
  provider stay;
- in the settings file, `modelRoles.default`: `pluxy/<model>`; other roles and settings stay.

Both files are edited in place through a lossless YAML tree: comments, key order, quoting and
indentation stay, and each edited file is re-read and must hold exactly the intended value, else
nothing is written. A list of default selectors (fallbacks) is replaced by pluxy's model and Clear
puts the list back. A `null` mapping is an absent one. An `easy-cliproxyapi` provider is left alone.
With neither YAML name in the folder, a legacy `models.json` or `settings.json` there refuses the
files: Oh My Pi moves it into a new YAML file at its next start.

Refused: not UTF-8, not YAML (a repeated key too), more than one document, a top level that is not
a mapping, a tag or other value the settings do not use, a key that is neither text, an integer nor a boolean, a number that is not finite, `providers`,
`providers.pluxy` or its `discovery` not a mapping, `modelRoles` not a mapping, or
`modelRoles.default` neither text nor a list of text.

### `grok-setup`

Page: Grok Build. Set `grok`, one file: `~/.grok/config.toml` on every system. A project's
`.grok/config.toml` cannot hold models, so only the user config is written.

| Operation | Notes |
| --- | --- |
| `preview` | `settings`: `state`, `base_url`, `key_set`, `default` (`models.default` as the file has it), `model` (the default's model when it is pluxy's entry), `models` (pluxy's entries: `model`, `name`, `context_window`), `legacy` (the count of `cpa-gui/` entries). |
| `apply` | One pin, `base_url`, `key` (refused when it holds `$`: Grok Build expands `$NAME`), `model` (one of `models`), `models`: 1 to 256 of `{model, name, context_window}` (`name` empty: the model). |
| `recipe` | `{"client": "Grok Build", "kind": "terminal", "program"}`: `~/.grok/bin/grok` (`grok.exe` on Windows) when it is a program there, else `grok`. |

`apply` edits in place with `toml_edit` and writes:

- `models.default` = `pluxy/<model>`;
- one table `[model."pluxy/<model>"]` per listed model: `model`, `base_url` (the core's address with
  `/v1`), `name`, `api_key` (the client key; a model's own key wins over a signed-in session and
  `XAI_API_KEY`), `api_backend = "chat_completions"` and `context_window` (drives auto-compaction;
  the page offers 200000). Other keys the person added to such a table stay; pluxy's tables for
  models no longer listed are removed. The person's own entries and `cpa-gui/` entries stay.

`apply` leaves the file owner-only (0600). The state is `configured` only when every pluxy entry is
whole (a `model`, `api_backend = "chat_completions"`, a key and the default's `base_url`); otherwise
`needs-update`, so the page offers Repair Connection.

Refused: not UTF-8 or not TOML, `models` or `model` not a table, `models.default` not a string, or a
`model."pluxy/…"` entry not a table.

### `hermes-setup`

Page: Hermes Agent. Set `hermes`, one file: `config.yaml` in Hermes's home: `~/.hermes` on Linux and
macOS and `%LOCALAPPDATA%\hermes` on Windows (`AppData\Local\hermes` under the home folder when
`LOCALAPPDATA` is unset, blank or not a full path). When the home's `active_profile` names a profile
other than `default`, the file is that profile's `profiles/<name>/config.yaml`; a name that is not a
profile ID, or a profile folder that is not there, refuses (the person selects the folder).

| Operation | Notes |
| --- | --- |
| `preview` | `targets`: one selection, or `folder`: a Hermes home (may be a link). `config`: `state`, `base_url` (the provider's `api`), `key_set`, `provider` (`model.provider`), `model` (`model.default`, when the provider is pluxy's), `legacy` (a `cpa-gui` provider is there). |
| `apply` | One pin, `base_url`, `key`, `model` (one model ID). |
| `recipe` | `{"client": "Hermes Agent", "kind": "terminal", "program"}`: `~/.local/bin/hermes` (Windows: `hermes.exe` in the `bin` folder of Hermes's home) when it is a program there, else `hermes`. |

`apply` writes, as `hermes model` does for a provider of the `providers:` map:

- `providers.pluxy`: `api` (the core's address with `/v1`), `api_key`, `transport`
  `chat_completions`, `default_model`. It removes keys Hermes would read before these or in their
  place (`api_mode`, `key_env`, `api_key_env`, `key_cmd`, `base_url`, `url`, `model`, the camelCase
  spellings `baseUrl`, `apiKey`, `apiMode`, `keyEnv`, `apiKeyEnv`, `defaultModel`, and `enabled` when
  it is not `true`). Other members (per-model `models` metadata, `extra_headers`) and every other
  provider stay. No `models` map is written: Hermes lists the core's models itself.
- `model`: `provider` `custom:pluxy`, `default` (the model), `api_mode` `chat_completions`; an earlier
  endpoint's `base_url`, `api_key`, `key_env` and `api_key_env` there are removed. A plain-text
  `model:` becomes a mapping.

The file is edited in place through a lossless YAML tree and re-read after each edit. Hermes reads
YAML 1.1, so the key and the model are refused when a YAML 1.1 reader would take them for anything but
text (a boolean such as `yes` or `on`, a null, an integer in any base, a float, `.inf`, `.nan`, a
timestamp), or when they hold `${` or spaces. A `custom_providers` entry or a `providers.cpa-gui` is
left alone. `_config_version` is not checked.

Refused: not UTF-8, not YAML (a repeated key too), more than one document, a top level that is not a
mapping, a tag or other value the settings do not use, a key that is neither text, an integer nor a boolean, `providers` or `providers.pluxy` not a mapping, `model` not a mapping or text,
`model.provider` or `model.default` not text.

### `pi-setup`

Page: Pi. Set `pi`, three files in this order, all in Pi's agent folder `~/.pi/agent` on every system:
`cliproxyapi.json`, `settings.json` and `cliproxyapi-models.json`. The folder must exist (Pi makes it
on first run). Pi reaches pluxy through the CLIProxyAPI provider plugin, which reads
`cliproxyapi.json` (`baseUrl`, `apiKey`, optional `providerId`, `providerName`, `fast`, `pause`;
`CLIPROXYAPI_*` variables in Pi's environment win over every key but `pause`), registers the provider `cliproxyapi`, lists the
core's Codex catalog (`/v1/models?client_version=pi`), sends requests to the
core's `/backend-api/` Codex routes and caches the list in `cliproxyapi-models.json`.

| Operation | Notes |
| --- | --- |
| `preview` | `targets`: the three selections in one folder, or `folder`: an agent folder (a link inside it is refused, so linked files are selected as `targets`). `pi`: `state` (`not-configured`: the plugin is not listed), `base_url`, `key_set`, `provider`, `default_provider`, `model`, `registered` (settings.json lists pluxy's plugin), `legacy` (it lists an older npm copy), `adapter`: `installed`, `version`, `bundled`, `update`, `foreign` (a folder of that name pluxy did not install), `pin` (the pin of pluxy's copy, from its marker `.pluxy-adapter.json`; null when pluxy's copy is not there). |
| `apply` | Three pins, `base_url`, `key`, `model` (one model ID; the key may not hold spaces). Output also holds `adapter`: `installed` or `kept`. |
| `update` (`write`) | Three pins, `plugin`: the preview's `adapter.pin`. Returns `{"outcome": "updated", "version"}`. |
| `uninstall` (`write`) | Three pins, `plugin`: the preview's `adapter.pin` (null when no copy was found). Returns the library's outcome and `adapter_removed`. |
| `clear`, `restore` | Three pins; also return `adapter_removed`. |
| `recipe` | `{"client": "Pi", "kind": "terminal", "program"}`: `~/.pi/agent/bin/pi`, else `~/.local/bin/pi`, when an executable file is there, else `pi`. On Windows always `pi` from the PATH; a Pi installed by `pi.dev/install.ps1` makes `pi.cmd` launchers, which `execution` refuses at launch (it runs a batch file only as an npm, pnpm or Yarn shim). |

**The plugin.** The tool never runs Pi or npm and fetches nothing. It carries a reviewed copy of the
CLIProxyAPI provider plugin (`pi-setup/adapter/`, MIT, its `LICENSE` kept) in its signed program and
writes it into the agent folder as a local Pi package, `pluxy-cliproxyapi-provider/`, with a marker
`.pluxy-adapter.json` (this package and the copy's version). The copy keeps a patched module in its
own `.cache` folder (mode 0700) instead of the shared temporary folder.
The plugin declares Pi `>=0.82.0`; the tool cannot run `pi --version`, so it does not check.

- **Update** replaces pluxy's copy with the one the tool carries; the page offers it when the
  installed copy's version differs. It refuses, as changed since the preview, a pinned file or agent
  folder that resolves elsewhere now, or a copy that is not the one pinned (its folder replaced, its
  marker changed, another version put there). It writes through `put_folder`/`remove_folder`.
- A folder of that name without pluxy's marker is someone else's: `apply` and `update` refuse and
  nothing removes it.
- **Uninstall** takes the plugin's entry out of `settings.json` (through `apply`, so Clear and Restore
  still work) and removes its folder (the pinned copy only); the connection settings stay.
- `clear`, `restore` and `uninstall` remove pluxy's plugin folder once `settings.json` no longer
  lists it. `clear` and `restore` take no plugin pin: they pin pluxy's copy as they find it in the
  agent folder their file transaction pinned. When every file is a link to another folder (as stow
  makes), the transaction pins no agent folder and they remove none.
- When `apply`'s file write fails after the plugin was installed, `apply` removes that copy by its
  identity.

`apply` writes:

- the plugin, when pluxy's copy is not in the agent folder;
- in `cliproxyapi.json`: `baseUrl` (the core's address, without a final `/` or `/v1`) and `apiKey` (the
  client key, sent as a bearer token); other keys stay;
- in `settings.json`: `./pluxy-cliproxyapi-provider` in `packages` (an entry the person narrowed
  stays), without an `npm:@router-for-me/pi-cliproxyapi-provider` entry (both would register the same
  provider; its npm files stay), `defaultProvider` (`cliproxyapi.json`'s `providerId`, else
  `cliproxyapi`) and `defaultModel`; other packages and settings stay;
- `cliproxyapi-models.json` is removed, so the plugin lists the core's models afresh.

Files are written back as JSON with two-space indentation; key order and numbers stay. `apply` leaves
them owner-only, since `cliproxyapi.json` holds the key. Clear also takes pluxy's plugin out of
`packages`, puts back an entry `apply` took out, and removes a model cache `apply` removed only when
none existed before.

Refused (`preview` and `apply`; `clear` and `uninstall` too, `restore` never): `settings.json` or
`cliproxyapi.json` not strict JSON (a byte order mark too) or not an object, `packages` not a list of
package sources (a string, or an object with a string `source`), `defaultProvider`, `defaultModel`,
`baseUrl`, `apiKey` or `providerId` not text.

## `execution`

`pluxy/execution` prepares the launches of the desktop app: an agent in a terminal, a program run
directly, or a file opened in the person's own app. The Launcher page, the tray's Start… menu, the
tool pages' Start section and `codex-sessions` (to open a session's file) use it. As a tool it never starts a
process itself and never holds a secret's value: the host confirms each launch natively, spawns
exactly what was confirmed when the program asks, and resolves secrets from its vault at the spawn
(`../app/app-host/README.md`, "Launches and managed runtimes").

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `terminals` | `read` | `search_path`: the launch environment's PATH entries | `terminals`: Automatic first, then each terminal found, with label and path |
| `prepare` | `read` | `kind` (`terminal`, `direct` or `open`). `terminal`, `direct`: `program`, `cwd` and `search_path` are required (`program` is a name looked up on `search_path`, or a full path), `override` (a full path used instead), `args`, `cwd`, `terminal` (default Automatic), `search_path`. `open`: `file`, `within` (the folder it must be inside); a `native-test` build also takes `search_path` | A launch's `executable`, `argv`, `cwd`; for a terminal the `program` it runs; for an open launch the `file` and `within` folder, resolved |
| `launch` | `launch` | What `prepare` gave, with `lifetime` (`detached`, the default, or `managed`), `environment`, `stdin` and the secret field `key` (a reference only) | The host's answer to its spawn request: `lifetime`, and a managed runtime's `runtime` and `pid` |
| `runtime` | `read` | `op` (`status`, `list`), `runtime` | The host's answer |
| `control` | `write` | `op` (`stop`, `restart`), `runtime` | The host's answer |

It exports `prepare` and `launch` under export contract 1: another package can prepare and start
launches but never looks at, stops or restarts its runtimes, and the app asks before each `control`
call. A call from another package may prepare only an `open` launch: a terminal or direct preparation
answers whether programs and folders exist, so only the app's own window asks for those.

**Linux.**

- Terminals, in Automatic's order: `x-terminal-emulator` (System terminal), `gnome-terminal` (`--`),
  `konsole`, `xfce4-terminal` (`-x`), `mate-terminal` (`-x`), `kitty`, `alacritty`, `ghostty` and
  `xterm`; each runs the program after `-e` unless noted. A terminal launch runs `<terminal> <flag>
  <program> <args>`; a direct one `<program> <args>`. The program must be an executable file and the
  working folder an existing absolute folder.
- The terminal finds the program by its path after the app's checks, so the app refuses a terminal
  launch whose program another user could change or redirect by then: one below a folder that others
  or a group may write (a sticky folder such as `/tmp` aside; the person's own private group is not
  counted), reached through a link such a user could change, through `/proc`, or that such a group
  may write. Groups and users from sources other than `/etc/passwd` and `/etc/group` (LDAP) are not
  seen.
- `launch` refuses, before the host is asked: a launch of a listed terminal whose argv starts with
  its exec flag but whose `program` is not the argument right after it; a `program` whose argv does
  not start with that flag; and on Linux and macOS a `program` for a launch that is no terminal's. A
  listed terminal started without its exec flag and naming no `program` is a direct launch of it.
- `lifetime` `managed` applies to direct launches only: the host keeps and stops a managed runtime
  through the process it started, and a terminal launcher often ends at once after handing work to a
  server process. The host refuses (`invalid_request`, before the confirmation) a launch naming a
  terminal's `program` with `lifetime: "managed"`.
- An **open** launch runs `xdg-open <file>`, or `gio open <file>` when there is no `xdg-open`, in the
  file's folder. The opener is taken only from `/usr/local/bin`, `/usr/bin`, `/bin` and
  `/run/current-system/sw/bin`, so an open launch takes no `search_path` (only a `native-test` build
  does, for the test's stand-in opener). The file must be a regular file with no execute bit inside
  `within`, which must not be `/`, the home folder or a folder above it; both are resolved, links
  followed. `within` and every folder between it and the file must be the user's own and writable by
  neither group nor others, or `prepare` refuses. The app checks all this again and more (content
  type, owner, the launch's exact shape), shows the file, its type and the folder in its
  confirmation and binds them. Which app opens the file is the desktop's choice.

**Windows and macOS.** The execution program is also the terminal and the opener of the launches it
prepares: the app runs it, as the confirmed launch, with arguments that name a launch mode, and it
runs that mode instead of serving operations. `lifetime` and the host's checks are as on Linux.

- **Windows.** Terminals: `powershell` (Automatic's first), `cmd` and `console`. A terminal launch runs
  `<execution> terminal --with <kind> -- <program> <args>` in a new console, which Windows shows in the
  person's default terminal app. It runs the program in the launch's folder and environment, says how
  it ended, then waits for Enter (`console`) or stays at a `cmd.exe /D` or `powershell.exe -NoLogo
  -NoProfile` prompt, both from the System32 folder Windows reports. There is no Windows Terminal kind
  (`wt.exe` splits its command line its own way). A program is a PE image (`.exe` or `.com` starting
  with `MZ`), looked up as `<name>.com`, `.exe`, `.bat`, `.cmd` in each search path folder in turn.
  A batch file runs only as an npm, pnpm or Yarn command shim it can read (it becomes what the shim runs);
  any other batch file is refused, and `cmd.exe`, `wt.exe`, `conhost.exe` and other programs that read
  their command line their own way start only with no arguments. Before running the program it opens
  the program and every folder above it so none can be renamed, removed or replaced until it has
  started, checks that the path names exactly what it holds (same file by volume serial number and
  file ID), that it is a PE image, and that no account but the user, SYSTEM, Administrators and
  TrustedInstaller may change the program or its folder or remove or rename what a folder above holds
  (`pluxy_files::others_may_change`); otherwise the window says `pluxy: will not run "<program>": …`
  and nothing starts. Paths are shown and confirmed in their plain form (`C:\…`). A direct launch may
  name a packaged app as `shell:AppsFolder\<AppUserModelID>` with no arguments
  (`<execution> app -- <AppUserModelID>`). An open launch runs `<execution> open -- <file>` through
  `ShellExecuteExW` (verb `open`, no dialog): the file must be `.txt`, `.json`, `.jsonl`, `.ndjson`,
  `.md` or `.log`, on a local drive, with no alternate data stream and a path under 260 UTF-16 units,
  some app must open its type, and its folders must be real folders writable by no one but the user,
  SYSTEM and Administrators.
- **macOS.** The terminal is Terminal (iTerm2 and Ghostty are not offered). A terminal launch runs
  `<execution> terminal --with terminal --app <Terminal.app> -- <program> <args>`: it makes a private
  folder with a Unix socket and a `.command` file, has Terminal open the file (`/usr/bin/open -a`), and
  hands the program, arguments, folder and environment over the socket to the first connection from
  this user, so nothing of the launch is written to a file or read by a shell. The program goes over
  the socket as descriptor 3, the file the app confirmed and checked; a terminal launch without it is
  refused, silently (the app does not read the terminal mode's exit code). In the window the file runs
  once no user but a member of `admin` can change it or a folder above it, each on a volume that keeps
  ownership. `open` held back for 20 seconds fails as blocked; no window within 60 seconds, as expired;
  a hand-over over 1 MiB is refused before Terminal opens. A direct launch may name an application
  bundle (`….app`) in `/Applications`, `~/Applications` or `/System/Applications`, with no arguments
  (`/usr/bin/open -a <bundle>`). An open launch runs `/usr/bin/open <file>`; the types are the extensions
  `.txt`, `.json`, `.jsonl`, `.ndjson`, `.md` and `.log`, the file must carry none of `com.apple.FinderInfo`, `com.apple.ResourceFork` and
  `com.apple.LaunchServices.OpenWith`, and LaunchServices must have an app for it.
- On both, an app launch carries nothing but the app (no environment, standard input, secret or
  managed lifetime), and a terminal launch takes no standard input; `execution` refuses either before
  the host is asked. In a `native-test` build the `console` window does not wait for Enter and the
  `cmd` and `powershell` shells write their working folder to `pluxy-after-exit` there and exit.

## `codex-sessions`

`pluxy/codex-sessions` reads Codex's local sessions and shows them on its page (Codex sessions).
It never writes a Codex file, never reads `config.toml` or `auth.json`, and has no repair, delete or
editor. Its one write outside its data folder is a file the person saves from `export`. It imports
`pluxy/execution`'s `prepare` and `launch`, optionally (export contract 1).

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `list` | `read` | `offset` (default 0), `limit` (1 to 100, default 50) | The Codex home, the databases read, a page of sessions (ID, title, folder, provider, archived, update time, database), the total, whether there are more, the page's active and archived counts, and warnings |
| `detail` | `disclose` | `id`, `offset`, `limit` (messages) | The session's title, folder, provider, model, archived, update time, rollout file and database; statistics; a page of messages, each cut at 8 KiB (`truncated`), at most 512 KiB in all, and `next_offset` |
| `export` | `disclose` | `id` | Whether the person `saved` the session's rollout (and its `size`) or `cancelled`; never the rollout's bytes |
| `open` | `read`; the launch it asks for is confirmed | `id` | `{"outcome": "opened"}` once the launch started; otherwise the call's error (a refused or declined launch, a session with no rollout file, `pluxy/execution` not enabled); never a path or a byte of the file |
| `settings` | `write` | `codex_home`: a full path, or empty for the default | The Codex home now read, and whether it is custom |

- The Codex home is `~/.codex` (from the app's `HOME`), or the folder set with `settings`, kept in
  the tool's data folder. `list` and `detail` take no paths.
- `export` finds the rollout as `detail` does (a link to a file outside the Codex home is refused) and
  saves its bytes as they are on disk with `Call::save_file`, suggesting
  `session-<id>-<YYYY-MM-DD>.jsonl` (ID without `local:`, date in UTC). It saves only a rollout of at
  most 10 MiB that is UTF-8 text; the whole file is read to be sent.
- `open` finds the rollout as `detail` does and opens only a regular file named `rollout-*.jsonl`
  inside the Codex home's `sessions/` or `archived_sessions/` (a link is opened as the file it
  resolves to). It calls `pluxy/execution`: `prepare` with `{"kind": "open", "file", "within"}`, then
  `launch` with exactly what `prepare` gave plus `"lifetime": "detached"`. The host's confirmation
  names this tool and `pluxy/execution`, the file, its content type, the folder and the opener, is
  asked every time and offers no *Remember*. Without `pluxy/execution` enabled, `open` fails with the
  host's `extension_unavailable`. A `native-test` build also sends `search_path` with the folder named
  in `opener-folder` in the data folder.
- `export` and `open` errors name no path and hold no byte of the rollout; a `detail` read error can name the rollout's path. The page renders every session's text as
  text, never as HTML.

What it reads:

- SQLite databases `sqlite/*.db`, `*.sqlite` and `*.sqlite3` and the legacy `state_5.sqlite`, each
  kept only with a `threads`, `automation_runs` or `inbox_items` table, opened with `lib/sqlite-ro`.
  A database that cannot be read becomes one warning naming it; the rest loads.
- `threads` (title or name, folder, provider, archived, update time, whichever columns exist) and
  `automation_runs` (an `archived` status is archived).
- Rollout files `sessions/**` and `archived_sessions/**` named `rollout-…<UUID>.jsonl`; for the list
  only their names; a rollout whose session a database has is dropped.
- `session_index.jsonl`: a rollout's title and update time, else the file's modification time. An
  index over 16 MiB becomes a warning and is not read.
- Links to databases, rollouts and the index are skipped, and so is a `sqlite/`, `sessions/` or
  `archived_sessions/` folder that is a link, and a database whose `-wal` or `-shm` is a link (a
  warning names it). The Codex home itself may be a link.
- Sessions with the same ID (without a `local:` prefix) are one: the newer wins and the other fills its
  empty fields; the list is newest first, then by ID. `detail` reads the rollout (the database's
  `rollout_path` when inside the Codex home's session folders, else the file named for it there): its
  `response_item` records become messages (text of any role, reasoning or
  `[Encrypted reasoning block]`, tool calls as `name(arguments)` and their outputs); the model is the
  database's, else the last `turn_context`'s.
- Every string from a database or a rollout is capped (titles, folders, providers, models, paths at
  4 KiB; IDs at 128 bytes; roles, timestamps and call IDs at 256 bytes, ending with "…"). A `list` page
  holds at most 512 KiB; longer pages hold fewer and say there are more.

## Usage parsers

A usage parser is a tool that reads one agent's session files and sends the usage hub
([`agent-usage`](agent-usage/README.md)) token counts per UTC hour and model (schema 1) or per
message (schema 2), and nothing else: no prompt, reply, title, folder name or raw ID leaves it. A
parser never writes an agent's file. Each is a small program on `lib/usage`; the hub's `roots` name
other roots than the defaults, which come from the app's `HOME`.

| Package | Schema | Default roots | Files |
| --- | --- | --- | --- |
| `pluxy/usage-codex` | 1 | `~/.codex/sessions`, `~/.codex/archived_sessions` | rollout `*.jsonl` |
| `pluxy/usage-claude` | 2 | `~/.claude/projects` | transcript `*.jsonl` |
| `pluxy/usage-opencode` | 2 | `~/.local/share/opencode`, `~/.opencode`; on Windows also `opencode` in `APPDATA` and `LOCALAPPDATA` | `opencode.db` (or `opencode-<channel>.db`) and `storage/message/<session>/<message>.json` |
| `pluxy/usage-omp` | 2 | `~/.omp/agent/sessions`, `~/.omp/sessions` | session `*.jsonl` (`lib/pi-sessions`) |
| `pluxy/usage-pi` | 2 | `~/.pi/agent/sessions`, `~/.pi/sessions` | session `*.jsonl` (`lib/pi-sessions`) |

`usage-fixture` (`pluxy/usage-fixture`, never released) reads `*.jsonl` files of records
`{"id"?, "ts", "model", "input", "output", ...}` under its roots, and lines that end a part or pause a
read, so a check can cancel a job between two parts of one file. It has a `relay` operation as well as
`scan`.

**`usage-codex`.** A rollout's file ID is its file name, so a session Codex moves to the archive is the
same file (the newer copy is read). Each file is read line by line when it changes:

- a `turn_context` line sets the model for the lines after it; a model in an event's own usage
  object wins;
- an event's `info.total_token_usage` is a cumulative counter: the event counts what each field grew
  by since the last event in the file, never below zero (a counter that falls starts again from the
  lower value); an event with only `info.last_token_usage` or `payload.usage` counts as it is;
- input is net of cached input; the total is the event's `total_tokens`, else input + output +
  cache write + cache read; reasoning is part of output;
- every event is one sample, also one that adds nothing; an event with `info` null counts only when it has `payload.usage`;
- an event's hour is its line's `timestamp` (or another of the usual time keys: `time`, `created_at`, `createdAt`, `date`, also inside `message`, `payload` or `data`); a line with no readable time is skipped; a date with no
  time is that day's start in UTC.

**`usage-claude`.** A transcript's file ID is its path below the root with the first folder (Claude
Code's project folder) replaced by a hash. A custom root should name a `projects` folder, so the
hashed folder is the project's. Each file is read line by line when it changes:

- a line's usage is its `message.usage` (with no `message` object, its own `usage`), its model the
  message's `model`; `<synthetic>` is no model;
- Claude Code writes one line per content block, each repeating the response's `message.id` and
  usage, so the lines of one message ID (and request ID, when present) are one message: the last
  wins, at its own time; an empty message ID is none;
- a message is a keyed event: its key is a hash of its message ID and `requestId`, else of its
  message ID and `sessionId`, so a response that `/branch` or `--fork-session` copies into another
  transcript counts once; with neither ID it counts in its file only;
- a line with usage but no message ID counts as it is, as does each record of a line that is a list (or
  holds a `messages` list);
- the total is input + output + cache write + cache read unless the usage gives its own; a line with
  no readable `timestamp` is skipped;
- the parser keeps one small record per message until the file is read: a file of more than 100,000
  messages fails and keeps its old rows.

**`usage-opencode`.**

- Each session in a database is a file of the hub's, `<database path below the root>#<hash of the
  session ID>`, so the hub reads again only the sessions that changed. A session's revision is its
  message rows' count and data length and their latest update time, read through SQLite, so a change
  still in the `-wal` counts. The same session under two roots is read from the one where it is newest.
  A database that cannot be opened is listed as itself, its read fails with the reason, and none of its
  sessions is marked gone. A JSON message file's ID is its path below the root with the session folder
  and file name replaced by hashes.
- It reads the `session_message` table (`assistant` rows, and `compaction` rows with the compaction
  request's own tokens) and the older `message` table. A message is a keyed event whose key is a hash
  of its message and session IDs, which OpenCode's migrations keep, so a message stored in more than
  one of these ways counts once. A fork copies its parent's history under new message IDs
  (`<fork event>_<seq>`); a copy takes its original's key. Forks from before `session_message` count
  again.
- The hub's output is output + reasoning; the total is input + output + reasoning + cache read + cache
  write. A message's time is its `time.created` (else the row's creation time). Title and other side requests recorded only as
  session totals are not counted.
- The database is opened by `lib/sqlite-ro`.

## `wsl`

`pluxy/wsl` (program `bin/pluxy-wsl.exe`, Windows only: every operation answers `unsupported` anywhere
else) tells the app which WSL distributions and users exist, checks one, and prepares launches inside
it. The app then runs the client package's own Linux program in the distribution through `wsl.exe`
itself (`../app/app-host/README.md`, "Tools in WSL (Windows)"). The tool only reads: it writes
nothing and never starts a launch.

It runs only Windows' own `wsl.exe`, from System32 as `GetSystemDirectoryW` reports it: no window,
empty standard input, an environment of `SystemRoot`, `WINDIR` and `WSL_UTF8=1` only, output capped,
and a deadline after which it is ended through its handle (10 seconds to list, 60 for a run inside a
distribution). A call's cancel ends it too. WSL 1 sometimes refuses a launch with `0xd0000225`; such
a run is tried again, two seconds apart, up to three attempts in all, within the deadline. Inside a distribution
it runs one fixed `sh -c` script per operation, with no login shell and no rc file; names reach a
script only as `wsl.exe`'s `-d` and `-u` values or as the script's arguments.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `distros` | `read` | none | `distros`: each distribution's `name`, `default` and `version` (1, 2 or null); `installed`: false when WSL is missing or not enabled, or no usable distribution exists (no `wsl.exe` in System32, a listing that names none, or `wsl.exe --list` failing with `WSL_E_DEFAULT_DISTRO_NOT_FOUND` or `WSL_E_WSL_OPTIONAL_COMPONENT_REQUIRED`; an old inbox `wsl.exe` that prints no code answers `unavailable`). From `wsl.exe --list --quiet`, which starts no distribution, and the registry (`HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss`) for the default mark and the version (bit `0x8` of each distribution's `Flags`) |
| `users` | `read` | `distro` | `users`: root (last) and each person's account (user ID 1000 to 59999, a login shell, an absolute home), each with `name`, `uid`, `home`, `default`, `root`; `default`; `arch` (`amd64` or `arm64`); `wsl` (the version). Starts the distribution and reads `getent passwd` (else `/etc/passwd`) |
| `check` | `read` | `distro`, `user` | As that user: `distro`, `user`, `uid`, `home`, `arch`, `wsl`; `drives`, each Windows drive mounted (`letter`, `path`, `exec`: false on a `noexec` mount); `automount`, the folder WSL mounts drives in (null with none). Refused when it ran as another user or in another distribution |
| `prepare` | `read` | `distro`, `user`, `kind` (`terminal` or `direct`), `program` (a name found on the user's PATH as `wsl.exe --exec` gives it) or `override` (a full Linux path), `args`, `cwd` (a Linux folder; default the user's home) | `launch`, exactly `pluxy/execution`'s `launch` input: `executable` (System32's `wsl.exe`), `argv` (`-d <distro> -u <user> --cd <folder> --exec <program> <args>`), `cwd` (the person's Windows home, from `HOME`) and `program`; and `kind`, which the host applies before it calls execution (`terminal`: a console window of its own, no standard input, no managed lifetime) |

Names are checked before `wsl.exe` runs: a distribution name is not empty, `.` or `..`, holds none of
`\ / : * ? " < > |` or a control character, has no trailing dot or space, is at most 64 characters and
does not start with `-`; `docker-desktop` and `docker-desktop-data` are hidden and refused; a user
name is a POSIX portable name (`[a-z_][a-z0-9_-]*[$]?`, at most 32 bytes). A missing distribution or
user is `not_found` (WSL 1 reports a missing user as a connection error, so the message says the user
was not found or could not be started). `prepare` finds the program and checks the folder inside the
distribution as the user; a program that only a shell profile puts on the PATH (such as one nvm
installs) is not found by name and is given by its full path. The program is not pinned: it is found
again when the launch starts.

Tests: unit tests on every system, and `tests/real_wsl.rs` (run by the WSL jobs with `--ignored`
against Windows' own `wsl.exe` and real distributions under WSL 2 and WSL 1).

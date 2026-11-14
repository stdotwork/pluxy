# App tools

Native programs that pluxy's desktop app runs on demand, as curated first-party native tools, and
the libraries they share. This is a cargo workspace of its own (its own `Cargo.lock`), never linked
into the desktop app. The app runs a tool only while something calls it and stops it when idle; a
tool keeps no process or thread after its calls end. The contract the tools speak is the app
program contract v0 in [`../app/app-host/README.md`](../app/app-host/README.md), which stays the
reference: this folder implements it, it does not change it.

```bash
cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
cargo build --release --locked -p codex-sessions   # a small, stripped program (likewise agent-usage, usage-codex)
cargo build --locked -p pluxy-transactions --example fixture-writer   # the native test's fixture
```

## `lib/program`

The program side of the contract, for any tool (`pluxy-program`):

| Item | What it does |
| --- | --- |
| `run(handler)` | Checks the host's environment (`Identity::from_env`), says hello, answers `describe` from the package's own `pluxy-extension.json` (`Description::from_package`) and serves calls on stdin and stdout until stdin ends. |
| `serve(input, output, identity, description, handler)` | `run` on any streams, for tests. |
| `Handler::call(&Call)` | One call of a declared operation: its output or an `Error`. |
| `Handler::reconcile(&Call, unknown, journals)` | Before `describe` is answered: the calls of unknown outcome and the open journals the host names. By default nothing. |
| `Call` | `id`, `op`, `input`, `chain`, `secrets`, `job`; `cancelled()` and `check()`; `progress(value)`; `call(package, op, input)` (an import, through the host; cancelled with its call); `vault(op, fields)` (backups and journals); `jobs(op, input)` (a consumer's `registrations`, `start` or `cancel`); `batch(value)` (a producer's next batch, once the job's window has room) and `acked()`. |
| `Job` | The job a producer's call runs: `id`, `kind`, `window`. |
| `Error` | The contract's error object: `invalid_request`, `unsupported`, `not_found`, `unavailable`, `internal`, `cancelled`, or any code. |
| `read_frame`, `write_frame` | The framing: a four-byte big-endian length and UTF-8 JSON; a program's frames are at most `MAX_FRAME` (1 MiB). |
| `MAX_BATCH` | The largest batch a producer may send, 48 KiB of JSON. |

Each call runs on a thread of its own that ends with the call; the thread that reads stdin
routes cancels, the answers to a call's own calls, vault and jobs frames, and a job's acks. An
answer too large for a frame fails its call with `internal` instead of breaking the pipe. A
consumer answers the call that started a job before the job's batches can reach it (the host
delivers them as calls of its deliver operation, one call of a package at a time), so it never
waits for its own job inside a call.

## `lib/usage`

Schema 1 of the usage hub's ingest API and what a usage parser shares (`pluxy-usage`): a parser
implements `Parser` (its label, default roots, which files are its, how a file's bytes become
rows) and answers its produces operation with `produce`, which lists files (`scan`: no links, at
most 8 folders deep, newest first, at most 20,000), finds a named file again only under the roots
(`locate`), reads it and sends its rows in parts within the batch limit (`Sink`). A parser of a
format read whole (`parse`) gets a file of at most 16 MiB; a parser of a line-based format sets
`streamed` and gets the file's lines one at a time (`parse_lines`, `LineReader`) whatever its size,
a line over 16 MiB being skipped and counted. The schema is in [`agent-usage/README.md`](agent-usage/README.md#schema-1).

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
journal store of its own. Linux.

| Item | What it does |
| --- | --- |
| `Selection` | What the person selected: `File(path)`, a config file that may be a link (as stow makes), or `Root { root, path }`, a file inside a root folder that may be a link (as chezmoi can make), `path` being slash-separated parts inside it. |
| `Transactions::new(vault, data)` | The operations for one call: `vault` is the `Call` (or any `Vault`), `data` the tool's data folder (`data_dir()`, `PLUXY_TOOL_DATA_DIR`), where the state file `transactions.json` (0600) keeps each set's targets and backup IDs, the originals kept for restore, and the journals left open. |
| `preview(set, selections)` | Resolves and pins each selection: `{"set", "targets": [<pin>], "applied", "edited"}`. A pin names the selected path, its link's text, the file it resolves to, the identities (device and inode) of the link, the root, the file's folder and the file, and the file's SHA-256. A link inside a selected root, a part that leaves it (`..`, `.`, empty) and anything but a regular file are refused. A read: it asks nothing, and it reports a file's existence and SHA-256 for any path, so a writer must not export it to other packages. |
| `apply(set, pins, image, merge, written)` | Writes each pinned target with what `image` makes of its bytes now, and records `set` as applied: its original images (with the person's edits since the last apply promoted into them by `merge`) and its applied images, as vault backups. A record none of whose selections still resolves to its target (a link pointed elsewhere) is replaced, its originals kept for restore. |
| `clear(set, pins, merge, written)` | Each target becomes what `merge` makes of its original, its applied image and its bytes now: the writer's untouched values go, the person's edits stay. A pin of the recorded target file itself (named directly) reaches a record made through a link that now points elsewhere. |
| `restore(set, pins, written)` | Puts the original bytes back with the original's mode (removing a file that did not exist). With no record for these targets, it puts back the originals kept for them: after a conflict, or after a new apply replaced a retargeted record. |
| `reconcile(journals)` | For `Handler::reconcile`: each open journal the host names, reconciled from what is on disk (below). |
| `status()` | The applied sets, the originals kept for restore and the latest recovery reports. |
| `Merge`, `Json`, `merge_value` | The three-way merge, and the one for JSON files (written with two-space indentation, keys in the file's order and numbers with their exact digits, through serde_json's `preserve_order` and `arbitrary_precision`; other formatting and comments are not kept, so a format that must keep them brings its own `Merge`). |
| `Target`, `Pin` | Resolving and pinning, and the checked reads and writes, for a writer that needs them directly. `Pin::from_json` refuses any field `Pin::to_json` does not write. |
| `pin_schema()` | The JSON Schema of a pin, in the host's schema subset: what a writer's write operations take as `targets` items (the fixture writer's manifest uses it exactly). |

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
  over it, the folder flushed, and the file checked to be the one written. The whole operation is
  checked again at its end. At most 8 files of at most 256 KiB each.
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

Other programs do not take part in these checks: a write between the last check and the rename,
or after the final check, is not seen, and several files are journalled, not written atomically.
Saving replaces the file; an edit made in the same instant by another program may be lost. Say
so where the person confirms: the writers' write operations carry this sentence in their
descriptions, which their native confirmations show.
This is cooperative write correctness for a full-access tool, not a confinement.

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

## `codex-sessions`

`pluxy/codex-sessions` reads Codex's local sessions, read-only, and shows them on its page
(*Codex sessions*). It never writes a Codex file, never reads `config.toml` or `auth.json`, and
has no repair, delete, export or editor.

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `list` | `read` | `offset` (default 0), `limit` (1 to 100, default 50) | The Codex home, the databases read, a page of sessions (ID, title, folder, provider, archived, update time, database), the total, whether there are more, the page's active and archived counts, and warnings |
| `detail` | `disclose`: each call asks the person | `id`, `offset`, `limit` (messages) | The session's title, folder, provider, model, archived, update time, rollout file and database; its statistics; a page of its messages, each cut at 8 KiB (`truncated`), at most 512 KiB in all, and `next_offset` |
| `settings` | `write` | `codex_home`: a full path, or empty for the default | The Codex home now read, and whether it is custom |

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

Links to databases, rollouts and the index are skipped (the Codex home itself may be a link).

The reader adds no file beside a database that no Codex process has open.
In WAL mode a reader needs SQLite's shared-memory file (`-shm`), which SQLite would create, so
when it is missing: a database with no `-wal`, or an empty one, is opened as immutable; one whose
`-wal` still holds committed changes (Codex stopped before copying them back) is copied with its
`-wal` into a private folder in the tool's data folder (`tmp`), and the copy is read and then
removed. If the copy fails, that database is a warning, named, and skipped. A Codex process that
starts at the same moment, between the check for `-shm` and the open, can make one read see a
torn state; Refresh reads again. While Codex has a database open, the reader uses its `-shm` as
any SQLite reader does: it writes to that shared-memory file and takes SQLite's shared read locks,
and never changes the database or its `-wal`.

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

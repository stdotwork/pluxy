# App tools

Native programs that pluxy's desktop app runs on demand, as curated first-party native tools, and
the libraries they share. This is a cargo workspace of its own (its own `Cargo.lock`), never linked
into the desktop app. The app runs a tool only while something calls it and stops it when idle; a
tool keeps no process or thread after its calls end. The contract the tools speak is the app
program contract v0 in [`../app/app-host/README.md`](../app/app-host/README.md), which stays the
reference: this folder implements it, it does not change it.

```bash
cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
cargo build --release --locked -p codex-sessions   # a small, stripped program
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
| `Call` | `id`, `op`, `input`, `chain`, `secrets`; `cancelled()` and `check()`; `progress(value)`; `call(package, op, input)` (an import, through the host; cancelled with its call); `vault(op, fields)` (backups and journals). |
| `Error` | The contract's error object: `invalid_request`, `unsupported`, `not_found`, `unavailable`, `internal`, `cancelled`, or any code. |
| `read_frame`, `write_frame` | The framing: a four-byte big-endian length and UTF-8 JSON; a program's frames are at most `MAX_FRAME` (1 MiB). |

Each call runs on a thread of its own that ends with the call; the thread that reads stdin
routes cancels and the answers to a call's own calls and vault frames. An answer too large for a
frame fails its call with `internal` instead of breaking the pipe. Jobs come with the host's jobs
contract.

## `lib/transactions`

Link-preserving, journalled config writes for any writer tool (`pluxy-transactions`), a client of
the host's vault (backups and journals) through the contract's `vault` frames; it keeps no
journal store of its own. Linux.

| Item | What it does |
| --- | --- |
| `Selection` | What the person selected: `File(path)`, a config file that may be a link (as stow makes), or `Root { root, path }`, a file inside a root folder that may be a link (as chezmoi can make), `path` being slash-separated parts inside it. |
| `Transactions::new(vault, data)` | The operations for one call: `vault` is the `Call` (or any `Vault`), `data` the tool's data folder (`data_dir()`, `PLUXY_TOOL_DATA_DIR`), where the state file `transactions.json` (0600) keeps each set's targets and backup IDs. |
| `preview(set, selections)` | Resolves and pins each selection: `{"set", "targets": [<pin>], "applied", "edited"}`. A pin names the selected path, its link's text, the file it resolves to, the identities (device and inode) of the link, the root, the file's folder and the file, and the file's SHA-256. A link inside a selected root, a part that leaves it (`..`, `.`, empty) and anything but a regular file are refused. A read: it asks nothing. |
| `apply(set, pins, image, merge, written)` | Writes each pinned target with what `image` makes of its bytes now, and records `set` as applied: its original images (with the person's edits since the last apply promoted into them by `merge`) and its applied images, as vault backups. |
| `clear(set, pins, merge, written)` | Each target becomes what `merge` makes of its original, its applied image and its bytes now: the writer's untouched values go, the person's edits stay. |
| `restore(set, pins, written)` | Puts the original bytes back (removing a file that did not exist). |
| `reconcile(journals)` | For `Handler::reconcile`: each open journal the host names, reconciled from what is on disk (below). |
| `status()` | The applied sets and the latest recovery reports. |
| `Merge`, `Json`, `merge_value` | The three-way merge, and the one for JSON files (written with two-space indentation, keys sorted, no comments: a format that must keep order or comments brings its own `Merge`). |
| `Target`, `Pin` | Resolving and pinning, and the checked reads and writes, for a writer that needs them directly. |

How a write goes:

- The pins come back in the write's input, so the operation's native confirmation (which shows
  the exact input) names the selected path and the file it resolves to. The write resolves the
  selection again and refuses, asking for a new preview, if the link points elsewhere (or is
  another link, even with the same text), the file was replaced (as editors save, by rename) or
  modified since the preview. Only the target changes: a link stays the same link with the same
  text.
- Each file is written through a handle on its folder (cap-std), never by its path again: a new
  file beside it (the old file's mode, or 0600 for a new file), checked once more, then renamed
  over it, the folder flushed, and the file checked to be the one written. The whole operation is
  checked again at its end. At most 8 files of at most 256 KiB each.
- Before the first write, the old bytes go into vault backups and the operation into a vault
  journal (`journal.begin`, its `intent` naming each file's pin and SHA-256s before and after).
  A write that fails puts back the files already written, newest first, except one another
  program changed meanwhile, which stays as that program left it; the journal ends rolled back.
  If putting one back fails, the journal stays open for the next start.
- Once all are written, the state file records the set and the journal, then the journal is
  committed, and backups no set names any more are deleted.
- After a crash, the next start's describe names the open journal: `reconcile` commits it if the
  state file names it or every file holds its new bytes (saving the set's record), and otherwise
  puts back, from the backups, each file that holds the new bytes, leaving any file another
  program changed since, and ends it rolled back. It never writes the new bytes again, and
  repeating it changes nothing more.

Other programs do not take part in these checks: a write between the last check and the rename,
or after the final check, is not seen, and several files are journalled, not written atomically.
This is cooperative write correctness for a full-access tool, not a confinement.

The example `fixture-writer` (`examples/fixture-writer`, package `pluxy/fixture-writer`) is the
library's test fixture, never a curated tool: it sets top-level values in JSON files with
`preview`, `apply`, `clear`, `restore` and `status`, and `crash_after` makes `apply` exit after
that many files. The desktop app's native test packs it (`next/app/native/transactions.ts`).

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
  kept only with a `threads`, `automation_runs` or `inbox_items` table, opened read-only (a WAL
  database with no `-shm` beside it, so no Codex process has it open, is opened as immutable, so
  reading it creates no file). A database that cannot be read becomes one warning; the rest loads.
- `threads` (title or name, folder, provider, archived, update time, whichever columns exist) and
  `automation_runs` (an `archived` status is archived).
- Rollout files `sessions/**` and `archived_sessions/**` named `rollout-…<UUID>.jsonl`: for the
  list only their names (links are skipped); a rollout whose session a database has is dropped.
- `session_index.jsonl`: a rollout's title and update time, else the file's modification time.

Sessions with the same ID (without a `local:` prefix) are one: the newer wins and the other fills
its empty fields. The list is newest first, then by ID. A session's detail reads its rollout (the
database's `rollout_path` when it is inside the Codex home's session folders, else the file named
for it there): its `response_item` records become messages (user and assistant text, reasoning or
`[Encrypted reasoning block]`, tool calls as `name(arguments)` and their outputs), and the model is
the database's, else the last `turn_context`'s.

The page renders every session's text as text, never as HTML.

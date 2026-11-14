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

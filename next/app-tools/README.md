# App tools

Native programs that pluxy's desktop app runs on demand, as curated first-party native tools, and
the libraries they share. This is a cargo workspace of its own (its own `Cargo.lock`), never linked
into the desktop app. The app runs a tool only while something calls it and stops it when idle; a
tool keeps no process or thread after its calls end. The contract the tools speak is the app
program contract v0 in [`../app/app-host/README.md`](../app/app-host/README.md), which stays the
reference: this folder implements it, it does not change it.

```bash
cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
cargo build --release --locked -p codex-sessions   # a small, stripped program (likewise agent-usage, usage-codex, usage-omp)
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

## `lib/pi-sessions`

What the Pi and Oh My Pi usage parsers share (`pluxy-pi-sessions`): the two tools write the same
session format, so one library reads it and each parser is a small program of its own. Their session
files are `*.jsonl` under `~/.<tool>/agent/sessions` and `~/.<tool>/sessions` (the hub's `roots` name
others), read-only. A file's ID is its path below the root it is under, so a session present under
both roots is one file and the newer copy is read. Rules, by the tools' own sources:

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
- every entry with a usage that holds a count is one sample, also one that adds nothing.

A session a tool forks is a new file that starts with copies of the parent's entries, so the entries
they share are counted in each file, as the old desktop app counted them: schema 1 keeps rows per file.
Only counters, a model name and a time are read: no prompt, message, title, folder or key leaves the
parser.

## `usage-omp`

`pluxy/usage-omp`, the usage hub's parser of Oh My Pi's session files, from `~/.omp/agent/sessions` and
`~/.omp/sessions` (the `HOME` the app gives). It is `lib/pi-sessions` with Oh My Pi's name and folder.

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

Links to databases, rollouts and the index are skipped, and so is a `sqlite/`, `sessions/` or
`archived_sessions/` folder that is itself a link (a warning names it), and a database whose
`-wal` or `-shm` file is a link (a warning names it). The Codex home itself may be a link.
`detail` finds a thread whose database ID is stored with Codex's `local:` prefix by its ID with or
without the prefix.

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

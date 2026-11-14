# Agent usage (`pluxy/agent-usage`)

The usage hub: the one store of agents' token usage on this computer and its *Agent usage* page.
It is an optional native tool. It reads no agent's files itself: usage parsers, each a package of
its own, read them and send the hub their rows through the host's [jobs](../../app/app-host/README.md#jobs).
The hub keeps the rows in its own SQLite store and answers the page.

This page is the reference for the hub's operations, its store and **schemas 1 and 2**, the
versioned ingest API parsers are built on.

## Operations

| Operation | Effect | Input | Output |
| --- | --- | --- | --- |
| `sources` | `read` | `{}` | `{"sources": [{"id", "label", "status", "roots", "files", "rows", "last_scan"}], "interval_minutes", "last_sync"}`: each parser enabled now (`status` `active`) and each one no longer enabled whose history is kept (`archived`) |
| `sync` | `read` | `{"now"?: true}` | `{"running", "started", "last_sync", "interval_minutes", "sources": [{"id", "label", "phase", "listed", "changed", "read", "failed", "staged", "truncated", "error"}]}`: the sync under way, or the last one |
| `report` | `read` | `{"range", "granularity"?, "sources"?, "offsets", "now"}` | What the page shows (see [Reports](#reports)) |
| `storage` | `read` | `{}` | `{"bytes", "rows", "files", "last_sync"}` |
| `settings` | `read` | `{"interval_minutes": 0, 5, 15, 30 or 60}` | `{"interval_minutes"}` |
| `roots` | `write`: each call asks the person | `{"source", "roots": [full paths]}`: at most 16, at most 16 KiB as JSON; an empty list for the parser's defaults | `{"source", "roots"}` |
| `clear` | `write`: each call asks the person | `{}` | `{"cleared": true}`; refused while a sync runs |
| `ingest` | the deliver operation | A job's delivery | `{}` |

The hub consumes kind `pluxy/agent-usage/usage` at schemas 1 and 2, delivered to `ingest`. Only the host
calls `ingest`: the window, pages and other tools are refused it. The hub exports nothing, so no
other tool may call any of its operations; its own page calls them all.

## Syncs

The page drives a sync. On opening, and once a minute while open, it calls `sync`. The hub starts
a sync when the page asks (`now`, the *Sync now* button), when it never synced, or when the
interval (`settings`, default 5 minutes, 0 for off) has passed since the last one. While a sync
runs, the page calls `sync` about once a second; each call starts what is due and returns at once.
Nothing syncs while the page is closed.

A sync, for each parser registered now:

1. starts a `files` job: the parser lists its files with their size and modification time;
2. once listed, starts `read` jobs naming the files whose size or modification time is not the one
   stored, at most 64 a job (fewer when the job's input would pass 60 KiB as JSON, under the host's
   64 KiB) and four jobs at once;
3. stores each file's rows when its last part arrives, replacing that file's rows and revision in
   one transaction. A file's parts that have arrived when its job ends early (cancelled, failed,
   the parser disabled) are dropped: the file keeps its old rows and is read again by the next
   sync. A file a parser cannot read keeps its old rows, and so does a file with a row whose hour
   is before 2000-01-01 or more than a day after the hub's time, or with more than 100,000 rows
   (distinct hours and models) before its last part: that file counts as failed, with the reason,
   and its job goes on.

A sync always ends. A job the hub hears nothing from for 60 seconds (the host waits at most 10 for
a delivery, and delivers a job's end once, best-effort) is cancelled and counted as ended, with the
reason shown on its source: a `files` job's source fails, and a `read` job's unfinished files keep
their old rows. A source whose file list cannot be stored when its `files` job ends fails too.

A file no longer listed is marked gone; its rows stay. A sync lives in the hub's memory: if the hub
stops during one, the next sync starts over and reads only the files not yet stored.

A parser that is disabled or removed is not scheduled; its rows stay, and it shows as archived
(*Uninstalled · history kept*). Disabling, updating or removing a parser during a sync cancels its
job, so the file it was reading keeps its old rows.

## Reports

`report` takes the time `range` (`today`, `24h`, `7d`, `30d` or `all`), the `granularity`
(`hour`, `day`, `week` from Monday, or `month`; by default hours for `today` and `24h`, weeks for
`all` over more than 92 days, otherwise days; `all` never has hours), the source IDs to include
(none: every source), the viewer's UTC offsets over time (`[[unix seconds, minutes east of UTC]]`,
ascending, the first holding from the beginning) and the time it asks at (`now`, unix seconds).
Days, weeks, months and hours are the viewer's local ones; `24h` is the 24 UTC hours to the end of
the current one, so it is 24 real hours on a day the clocks change. A range that would have more
than 800 buckets gets the next coarser granularity, and the answer's `granularity` says which; `all`
starts no earlier than 2000.

Rows are kept per UTC hour, so in a zone whose offset is not a whole hour (such as +05:30) each
hour's tokens fall in the local hour, and day, that the UTC hour starts in: at a day's edge they can
be 30 minutes off. A later schema could keep quarter hours.

| Field | What it holds |
| --- | --- |
| `buckets` | Each bucket's key: `YYYY-MM-DDTHH`, `YYYY-MM-DD` (a week's Monday) or `YYYY-MM` |
| `totals`, `series` | The range's tokens, and each bucket's: `input`, `output`, `cache_read`, `cache_write`, `reasoning`, `total`, `samples` |
| `previous` | The tokens of the period just before, as long; `null` for `all` |
| `active_days` | Days with tokens in the range |
| `by_source`, `by_model` | `{"keys", "values"}`: the six keys with the most tokens, then `__other__` for the rest, and each bucket's totals for them |
| `models` | Each model with tokens, most first; past the first 200, the rest as one `__other__` |
| `sources` | Every source: `id`, `label`, `status`, and its `total` and `active_days` in the range, whatever the source filter |
| `calendar` | `{"start", "totals"}`: each day's total, from the Monday on or before a year ago (or the first stored day's Monday, if earlier) to today |
| `hours` | Seven rows (Monday first) of 24 hours' totals over the last 30 days |
| `sync` | As `sync` gives it |

The calendar and hours follow the source filter but not the range.

## The store

`usage.db` in the hub's data folder (`PLUXY_TOOL_DATA_DIR`), which the app keeps when the hub is
updated or removed; the hub makes it readable by the person's account only. It holds, per source and file, the file's size and modification time and its
rows per UTC hour and model; per source, its keyed events ([schema 2](#schema-2)) and which files
hold each; each source's label and custom roots; the interval and the last sync time. A store an
earlier hub made (format 1) is moved to format 2 when the hub opens it, its rows kept; a hub of
that earlier version cannot read a format 2 store, and offers Clear Agent history. Rows are kept until the person clears them (`clear`), which deletes every row and file
revision, so the next sync reads every file again. When the store cannot be read (it is damaged), every
operation says so, and `clear` deletes it and starts an empty one. To delete the store itself,
delete the hub's data folder while the app is not running.

## Schema 1

The versioned ingest API of kind `pluxy/agent-usage/usage`. A parser is a native tool that
declares, in its manifest,

```json
"produces": [{"kind": "pluxy/agent-usage/usage", "schema": 1, "operation": "scan"}]
```

with a `read` operation (any ID) that runs only as a job. The hub starts two kinds of job; the
library `pluxy-usage` ([`../lib/usage`](../lib/usage)) implements both for a parser.

**`files`**: input `{"mode": "files", "roots"?: [full paths]}`. Without `roots`, the parser reads
its own default roots, found from `HOME`.

- Batches: `{"files": [{"file", "size", "mtime_ns"}]}`, newest first.
- Output: `{"label", "roots", "count", "truncated"}`.

**`read`**: input `{"mode": "read", "roots"?, "files": [file IDs]}`, at most 2,000 IDs.

- Batches: each file's parts in order, `{"file", "size", "mtime_ns", "part", "last", "rows"}`, or
  `{"file", "error"}` for a file it cannot read.
- Output: `{"read", "failed"}`.

The fields:

| Field | Meaning |
| --- | --- |
| `file` | The parser's stable ID for a file, at most 4,096 bytes: by default its full path; a parser whose files move (such as Codex's archived sessions) uses another stable key, such as the file name |
| `size`, `mtime_ns` | The file's size in bytes and modification time in nanoseconds, as read: the file's revision |
| `label` | The source's display name, at most 64 characters; the page shows it with the parser's package ID |
| `part`, `last` | A file's parts are numbered from 0; `last` is true on its final part, which may hold no rows |
| `rows` | `[{"hour", "model", "input", "output", "cache_read", "cache_write", "reasoning", "total", "samples"}]`: `hour` is the UTC hour's start in unix seconds, `model` at most 256 characters, every count a whole number from 0 to 2^53 − 1 |
| `error` | Why the file could not be read, at most 512 characters |

Rules:

- The hub replaces a file's rows as a whole, so a parser sends every row of a file each time it
  reads it, and counts a record repeated within one file (a message ID seen again, a cumulative
  counter) once. It keeps no state: the hub keeps every revision.
- `total` is the source's own total; when the source gives none, it is input + output + cache read
  + cache write. Reasoning is part of output and never added to the total. `samples` counts the
  records summed into a row.
- The hub stores a model name trimmed, in lower case and without any path before its last `/`.
- Every batch and the output are at most 48 KiB, as the host requires. A source lists at most
  20,000 files, folders at most 8 deep below a root, without following links; a parser that reads
  a file whole (as the library `pluxy-usage` has it) refuses one larger than 16 MiB, and one that
  reads lines skips a line over 16 MiB.
- A row's `hour` is from 2000-01-01 to a day after the hub's time, and a file has at most 100,000
  rows (distinct hours and models); otherwise the file fails and the job goes on.
- The host names a batch's source; a parser cannot. A batch the hub refuses (a file the job does
  not read, a part out of order, a row's field out of bounds) fails the job.

A change to these fields or rules that a hub would misread is a new schema: parsers then declare
it, and the hub accepts the old and the new while parsers move.

## Schema 2

Schema 1 with one more field: a row may carry a `key`, which makes it an **event**, one record
counted once by its source whichever files hold it, such as a Claude Code response that a branched
session copies into a second transcript. A parser that sends keys declares schema 2:

```json
"produces": [{"kind": "pluxy/agent-usage/usage", "schema": 2, "operation": "scan"}]
```

| Field | Meaning |
| --- | --- |
| `key` | In a row, optional: the event's stable key within its source, 1 to 128 bytes. A parser builds it from the record's own IDs (a hash of them, so no raw ID leaves it); the same record in another file has the same key |

Rules, beside schema 1's:

- A keyed row is one event: the parser sends each key of a file once, its `samples` normally 1,
  and does not sum two events into one row. Rows without a key are as in schema 1.
- When a file's last part arrives, the hub replaces its references with the keys this read sent,
  in the same transaction as its rows and revision, and records the schema it was read at. A
  file read at another schema than its parser declares now is read again at the next sync, even
  unchanged, so a parser that moves to schema 2 (or back) never has a record counted both as a
  row and as an event. An event is stored once per source and key,
  with the values of the file read last that has it. An event no file references any more (every
  file that held it was read again without it) is deleted; a file that is gone from its folder
  keeps its references, as it keeps its rows.
- Reports and `storage` count each event once, beside every file's rows.
- A key from a parser that declares schema 1 is refused, failing the job (a refused batch).

## The page

*Agent usage*: the time range and granularity, source chips, the Total tokens and Cache hit rate
cards, the Trend (by source or by model) and Token mix charts, the Activity calendar and hours, the
Models, Agent sources and Details tables, the sync status, *Sync now*, the sync interval, each
source's folders, the store's size and *Clear Agent history*. Each source keeps one colour, by its place among the
sources' IDs, in its chip, the Trend and the Agent sources table (with a bar of its share), so
sources compare across ranges and filters; a bar's tooltip gives its total and each part's. It is plain HTML, ES modules and SVG
in [`pages/`](pages), with the theme's `--px-*` tokens; `pages/view.js` works out every text and
number it shows, and `next/app/tests/agent-usage-page.test.ts` tests it. The page sets text and
attributes only, never HTML.

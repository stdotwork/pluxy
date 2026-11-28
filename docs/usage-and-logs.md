# Usage statistics and logs

pluxy keeps four kinds of record of what it does. This page says what each holds, where it is kept,
how to read it and how to turn it on or off.

| Record | What it holds | On by default | Read it |
| --- | --- | --- | --- |
| [Usage record](#usage-statistics) | One entry per request: model, account, status, time, token counts. No prompts or replies. | Yes | **Activity** page; `pluxy usage` |
| [Log](#log-files) | pluxy's own messages and one access line per client request. | Standard error only; a file when turned on | **Settings**, **Logs**; `pluxy logs` |
| [Request logs](#request-logs) | A request, what was sent upstream and the reply, in full. | Only for failed requests | **Settings**, **Logs**, **Failed requests**; `pluxy logs request` |
| [Profiling pages](#profiling-pages) | Memory and CPU profiles of the running core. | No; needs a special build | A browser or `go tool pprof` |

The usage record and the logs are kept in the folders `usage` and `logs` beside the config file,
readable only by the user that runs pluxy. The settings below go in the config file, and take effect
when core reloads it, which it does when the file is saved (see [Config](config.md)). The **Logging**
section of the panel's **Config** page (its **Settings** tab) changes the file logging, log size,
error log, request logging and usage on/off settings; the other keys are set in the config file.

## Usage statistics

### What is recorded

Core records one event for each request that reached routing, including each turn of a Responses
WebSocket. It does not record:

- a request refused for its client key, or whose body does not parse;
- a token count request (`count_tokens`, `:countTokens`), which generates nothing;
- a WebSocket warmup (`generate: false`) that core answers itself.

An event has these fields. `seq`, `timestamp`, `endpoint`, `stream`, `model`, `failed`, `latency_ms` and `tokens` are always present; any other field is left out when it has no value.

| Field | Meaning |
| --- | --- |
| `seq` | The event's number, from 1, in the order recorded. |
| `id` | The request's trace ID: the `X-CPA-TRACE-ID` header of the reply. |
| `timestamp` | When the request arrived. |
| `endpoint`, `stream` | The client API path, such as `/v1/chat/completions`, and whether the reply streamed. |
| `model` | The model the client asked for, without a thinking suffix. |
| `upstream_model` | The account's own name for it, when it differs (an alias or prefix was resolved). |
| `served_model` | The model the upstream's reply says served it, when it names one. |
| `provider`, `account` | The extension and the account the last attempt went to. |
| `chain_step` | The step of the model's fallback chain the last attempt was for, from 1. Left out for a model with no chain. |
| `why` | Why routing chose the last attempt's account over the others that serve the model (at that step of a fallback chain): `pinned` (the request named it in `X-Pluxy-Account`), `session` (it served the session's last request), `only` (no other account serves the model), `first` (it is alone at the highest `priority`), `only-free` (it shares the highest `priority` with others, and none of them could take the request then: tried already, busy or blocked), `backup` (no account of a higher `priority` could take it), or the strategy's name (`round-robin`, `weighted`, `fill-first`, `least-active`, `soonest-reset`) when it chose among several at the highest `priority`. Left out for a request that reached no account, and in older events that do not carry it. |
| `attempts`, `tried` | How many accounts the request went to, and what each attempt ended with (at most 16 entries). `tried` is left out for one attempt that succeeded. |
| `left_out` | For a failed request, the accounts that serve the model and were not tried, and why (at most 8 entries). Recorded whatever `routing.explain` says. |
| `key_id`, `label` | The ID of the client key the request used, as `pluxy key list` shows it, never the key itself, and the key's name at that time. |
| `client_ip` | The address the request came from: the connection's peer, or the client a trusted proxy names (`trusted_proxies`). |
| `failed`, `error_code`, `status` | Whether the request did not end with a complete reply, the error's code, and the HTTP status of the reply. A reply that fails after it began streaming keeps its status 200 with `failed` true. A client that goes away is recorded as `cancelled`, with no status if nothing reached it. |
| `latency_ms` | From arrival to the last event. |
| `tokens` | What the upstream reported: `input_tokens` (cached ones included), `output_tokens` (reasoning included), `reasoning_tokens`, `cached_tokens`, `cache_creation_tokens` and `total_tokens` (input plus output). A count the upstream did not report is left out, not 0. |

A WebSocket turn is recorded before its last message is written, so a client that has that message
finds the turn in the record. For the same reason a client that goes away while that message is
written leaves the turn recorded as finished, with the tokens the upstream reported.

### Where it is kept

- **In memory**, the recent requests: the last `max_events` requests, none older than `max_age_ms`.
  The panel's **Requests** tab and `pluxy usage events` read this.
- **On disk**, every event, in `usage/YYYY-MM-DD.jsonl` beside the config file: one file for each
  UTC day, one JSON event per line. Events are queued (4,096 at most) and written without waiting
  for the disk. When the queue is full the event is not written to disk and `dropped` counts it; an
  event the disk refuses is counted in `write_errors` and logged. Both still count in the memory
  record, so slow storage never holds up a request. Each line has `"v": 1` besides the event's
  fields, and the files are mode `0600`. Core does not flush each line to the disk (no `fsync`). At
  start, core refills the memory record from today's and yesterday's files, skipping a last line that
  was cut short.
- **Summaries**, `usage/summary/YYYY-MM-DD.json`, mode `0600`: when a range covers a closed UTC day,
  core builds that day's summary from its day file (on demand, then kept in memory), so a long range
  never rereads old files. A summary holds, for each hour, model, provider, account, client key and
  whether the request failed, the number of requests, the sum of the latencies and all six token
  counts. A summary is rebuilt when its day file has grown. The `usage/summary` folder grows by one
  small file for each closed day a range has covered.
- `usage/seq.json` keeps the highest event number, so numbering continues after old files are
  deleted. Core writes it at start, before it deletes a day file, and at shutdown. At start `seq`
  continues from the highest `seq` in the files it refilled from, or from the newest earlier day file
  that has an event when those hold none, or from `seq.json` when that is higher; lines without a
  `seq` are numbered in order. `state/key-id.key`, mode `0600`, keeps the key that makes `key_id`
  values, so IDs stay the same across restarts.

```json
{"usage": {"enabled": true, "max_events": 10000, "max_age_ms": 86400000, "keep_days": 90}}
```

| Key | Meaning | Default |
| --- | --- | --- |
| `usage.enabled` | Whether core records requests. Off, nothing new is recorded and what was recorded stays readable. | on |
| `usage.max_events` | Requests kept in memory. At most 200,000. | 10,000 |
| `usage.max_age_ms` | How long a request stays in memory. At most 30 days. | 24 hours |
| `usage.keep_days` | UTC days of day files kept. Older day files are deleted at start and daily. `0` keeps all. Summaries are never deleted. | 90 |

Lowering `max_events` or `max_age_ms` drops the oldest requests from memory at the next reload.
A read of the record also drops from memory the requests older than `max_age_ms`; nothing else is
removed by a read, and the day files are not touched.

In the panel, the **Logging** section of the **Config** page has the switch **Keep a record of
requests**.

### Read the usage

**Panel or desktop app.** **Activity** has two tabs:

- **Usage** adds up the requests over a **Range**: the last 24 hours, or 7 or 30 days counted in calendar days from local midnight (today included). It shows a chart over time
  and a table by **Provider**, **Model**, **Account** or **Key**, with requests, failed requests,
  tokens and average time. **See these requests** opens the **Requests** tab filtered to a row.
- **Requests** lists the requests core still keeps in memory, newest first, filtered by **Which
  requests** (**All** or **Failed**), provider, account, model or key. **Save as spreadsheet** writes
  the matching requests, the newest 10,000 at most, to a CSV file with the columns Time, Model,
  Provider, Account, Client key, Failed, Status, Latency, Tokens and Trace ID. Hiding names in the
  panel hides them on screen only; the file has them.

**Command line.**

```bash
pluxy usage --since 7d --by account        # totals, and a table by account
pluxy usage events --before 0 --failed true --limit 20   # the newest 20 failed requests
pluxy usage export --since 30d > usage.csv               # the panel's spreadsheet
```

| Command | What it shows |
| --- | --- |
| `pluxy usage` | The requests, failures, input tokens and output tokens over the range, then a table by `--by` (default `model`). |
| `pluxy usage events` | The recorded events: oldest first, or newest first with `--before`. |
| `pluxy usage export` | The events as CSV on standard output, newest first, as the panel's **Save as spreadsheet** writes them. It reads every page of events, and says on standard error when older events are no longer available. It has no `--json` form: use `pluxy usage events --json`. |

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--since <time>` | all three | From an RFC 3339 time, or a duration back from now such as `24h`, `7d` or `30d`. |
| `--until <time>` | all three | Up to an RFC 3339 time. |
| `--by <kind>` | `usage` | Breakdown: `model`, `account`, `key` or `provider`. |
| `--model`, `--account`, `--provider` | all three | Only this model, account or provider (extension). |
| `--client-key <id>` | all three | Only this client key ID. |
| `--failed <true or false>` | `usage events`, `usage export` | Only failed requests (`true`) or only successful ones (`false`). |
| `--after <seq>` | `usage events` | Oldest first: only events with a `seq` above this. Cannot be combined with `--before`. |
| `--before <seq>` | `usage events` | Newest first: only events with a `seq` below this. `0`, or no value, is the latest page. |
| `--limit <n>` | `usage events` | At most this many events. Default 100, at most 1,000. |

The commands take the flags every client command takes (`--json`, `--config`, `--url`, `--key`; see
[CLI](cli.md)). When recording is off, they say so.

**Management API.** `GET /v0/management/usage` returns the totals, the breakdowns and a time series
(from 1-second to 24-hour steps). `GET /v0/management/usage/events` pages through events; its reply
says with `lost` when events were pushed out before a program that polls with `after` read them.
A closed day is counted by whole UTC hours. In a series, such an hour is counted once, in the step
that holds its start (the first step when the hour starts before the series does), as it is in the
totals. The series is oldest first and ends at `until`, or at the step that holds the present when
there is no `until`.
Both take the filters `since`, `until`, `model`, `provider`, `account`, `key` (or `key_id`) and
`failed`. A filter that does not parse is a 400. See [Management API](management-api.md).

## Log files

Core writes its log to standard error. With `logs.to_file` on, it also writes it to `main.log` in
the logs folder, the folder `logs` beside the config file. Each line has its time (local, to the
millisecond) and level:

```text
[2026-11-09 12:00:00.123] [info ] config revision 2 active (management API: settings)
[2026-11-09 12:00:01.456] [warn ] warning: 429 | 0.812s | 127.0.0.1 | POST /v1/chat/completions | 20261109120001-5b1c0d7e9a3f4c26 | work-codex
```

| Level | A line is this level when |
| --- | --- |
| `info` | It is none of the below. |
| `warn` | It starts with `warning: `. The access line of a client request that ends with status 400 or more is a warning. The line that gives the failure's reason is not: it is `info`. |
| `error` | It starts with `error: `: something you must fix, such as a config change that was not applied, an extension that crashed or was stopped after crashes, an account a provider closed or that needs a new sign-in, or a failed write of core's own state. |

There are no debug lines. Lines an extension's program writes go to standard error only, each starting with its package ID in square brackets; they are not in `main.log`.

**Failure lines.** When routing ends a request with an error, the line that gives the failure's
reason names what routing did, after the error's message:

```text
POST /v1/responses from 127.0.0.1 [trace 20261109120001-5b1c0d7e9a3f4c26 account work-codex]: <message> model=gpt-5 tried=[work-codex(pluxy/codex): quota] left_out=[spare: exhausted until 2026-11-09T17:00:00Z; old-key: banned]
```

`tried` lists each account the request went to with its provider (and the model sent upstream when
that is not the one asked for), and how the attempt ended; `left_out` lists each account that serves
the model and was not tried, with its state and, for a block, when it ends. Both name accounts by their
config IDs, not their public IDs, and name the first 8 entries, then `; +N more`. The line holds codes,
states and times, never an upstream's words, and it is written whatever `routing.explain` says.

**Access lines.** Core writes one line when each client API request ends: after a streamed reply
ends, and when a Responses WebSocket closes. It writes none for `/healthz`, the management API, the
panel or a path no route serves.

```text
200 | 1.234s | 127.0.0.1 | POST /v1/chat/completions | 20261109120000-3f9a0c2e5b7d4e18 | openai-main
```

The fields are the reply's status, the time the request took, the client's address, the method and
the path (never the query string, where some clients send their key, and no header or body), the
trace ID, and the account the last attempt went to. `-` stands for a field the request has none of,
such as the account of a request refused before routing. A status of 400 or more is a `warn` line.

**Rotation and size.** `main.log` is renamed `main-<time>.log` (a UTC time) before a write would take it past 10 MiB, and
a new one begins. `logs.max_total_size_mb` caps the logs folder: when its `.log` files (`main.log`, its
rotated files, request logs and error logs) take more, the oldest are deleted first. `main.log`
counts toward the cap but is never deleted. The cap is applied when a file is rotated, when a request log is written and when the
setting changes.

```json
{"logs": {"to_file": true, "max_total_size_mb": 100}}
```

| Key | Meaning | Default |
| --- | --- | --- |
| `logs.to_file` | Also write the log to `main.log`. | off |
| `logs.max_total_size_mb` | Megabytes the logs folder's `.log` files may take. `0` is no cap. | 0 |

**Privacy of the folder.** Core makes the logs folder `0700` and each log file `0600` on Linux and
macOS (an existing folder keeps its mode), and on Windows gives the folder and each file an access
list that lets in only core's user and SYSTEM. On Windows core checks the folder each time it opens
`main.log` or writes a request log; a folder that is a link or a file, or that another account owns,
is refused: core still starts, writes no log files and says why on standard error. A `main.log` that
is not a regular file, or that another account owns, is refused on every system.

**Read the log.**

- Panel or desktop app: **Settings**, **Logs**, **pluxy's log**. It follows new lines, can wrap, search
  and filter by **All lines**, **Warnings** or **Errors**, and has **Download** and **Clear log**. When
  file logging is off it says so and offers **Turn on file logging**.
- Command line: `pluxy logs` prints the newest lines (`--limit`, default 1,000, at most 10,000).
  `--after <unix seconds>` prints the lines after a time, so a script can read on from the last
  line's time (the `latest-timestamp` of the `--json` reply). `pluxy logs clear` empties `main.log`
  and deletes its rotated files; request logs and error logs stay.
- Both read `main.log` and its rotated files, and answer an error while logging to file is off.
- Reading on from `latest-timestamp` misses no line: core gives each write a time of its own, later
  than the write before it, even when the clock goes back. Lines written together (a message that
  spans lines) share that time, so a page can hold a few more lines than `--limit`. How `after` and
  `limit` work is in [Management API](management-api.md#logs).

## Request logs

A request log is a file with one request and its reply, in full, for finding out why a request
failed. It holds what the client sent, including prompts and replies. Treat the logs folder as
private.

| Setting | What is written |
| --- | --- |
| `logs.request_log` off (the default) | Only requests that fail, as `error-<name>.log`: a reply status of 400 or more, a streamed reply that fails after it began, or a handler that panicked. A client that goes away is not a failure. |
| `logs.request_log` on | Every request to a client API endpoint that takes a POST (generation, token counts and so on) and each Responses WebSocket turn, once it passed the client key check, as `<name>.log`. Failed ones are among them, with no separate error log. The model list endpoints get an access line only. |

```json
{"logs": {"request_log": true, "error_logs_max_files": 10}}
```

| Key | Meaning | Default |
| --- | --- | --- |
| `logs.request_log` | Write every request. Keep it off unless you are investigating something: it writes the prompts. | off |
| `logs.error_logs_max_files` | How many error logs to keep, the newest. `0` keeps all. | 10 |

The panel has **Log every request** and **Failed requests to keep** in the **Logging** section of
the **Config** page.

**File names.** A log is named after the request's path, its time and its trace ID, such as
`v1-chat-completions-2026-11-09T120000-20261109120000-3f9a0c2e5b7d4e18.log`. An error log has
`error-` in front.

**What a log holds,** in this order:

1. `=== REQUEST INFO ===`: the URL, method, time, the client's address, the trace ID and the account
   the last attempt went to.
2. `=== HEADERS ===` and `=== REQUEST BODY ===`.
3. For each request the extension sent upstream, `=== UPSTREAM REQUEST ===` (account, time, URL,
   method, headers, body) and `=== UPSTREAM RESPONSE ===` (time, status, headers and body, or the
   error that kept a response from coming). A refused attempt and the retry on another account show
   as two pairs. A request that made more than two upstream requests keeps the first and the last,
   with a line saying how many were left out. These sections need an extension that supports
   [upstream capture](../next/protocol/README.md#upstream-capture) (extension protocol 0.13), and
   are written only while `logs.request_log` is on, so an error log written while it is off has none.
   The line that says a body was not read to its end needs protocol 0.17; with an older extension a
   body just ends where the read stopped.
4. `=== RESPONSE ===`: the status, headers and body of the reply to the client.

A Responses WebSocket turn is written as one request: the client's `response.create` message as the
body, then `=== WEBSOCKET MESSAGES ===` with the messages sent back, one per line. A turn that
answers with an error counts as failed. A message core refuses before it starts a turn (an unknown
message type, no model, an `input` that is not a list) is answered on the socket and is not written
to any request log. When a turn opens a WebSocket to the upstream, its upstream sections hold that
handshake (the request, the status and the headers); the messages on that socket are not written.

**Size limits.** A body that is too long keeps its start and its end, with a line saying how many
bytes were left out between them:

| Part | Kept |
| --- | --- |
| A reply over 1 MiB (request log on) | The first 256 KiB and the last 768 KiB. |
| A reply, while only error logs are written | The first 32 KiB and the last 96 KiB. |
| An upstream body | The first 32 KiB and the last 96 KiB. |

The client's request body is kept whole, up to the 24 MiB core accepts.

**Next to a cut.** Core removes a secret only where it is whole in the text, so next to each cut it
also leaves out the bytes that could be the two parts of a secret the cut split. On each side of the
cut:

- A window of 12 bytes for each byte of the longest spelling (the secret itself, or an escaped or
  encoded form of it) of a configured secret that holds a character other than a letter, a digit or one
  of `-_.~+/=%\`. A secret made only of those characters, such as an API key or a token, needs no
  window: the run of those characters at the edge goes, however long.
- A part of a secret that the window's edge still cuts through goes too: the edge moves to where a
  secret that the redaction finds whole there starts (or ends). After 4 moves, or when the redaction
  gives up on the text around the edge, the whole side goes.
- A percent escape nested 6 times takes 13 bytes for a byte, more than the window holds, so such a
  part of a secret can stay.

So the log shows less than the limits above when a secret with a space or other such character is
configured: a 20-byte secret with a space in it takes at least 240 bytes on each side of a cut. The
number of bytes left out is in the line that says how many were not logged.

A body that the extension did not read to its end ends with `[... the body was not read past here
...]`, and the same rule applies at that end, except that the window reaches back no further than the
last line break, so a stream that stopped after an event keeps every whole event. A body with no line
break near its end (JSON on one line, binary frames) loses the whole window. A secret that spans lines
can keep a line shorter than 8 bytes where the read stopped: always a line of 1 or 2 bytes, and a
line of 3 to 7 bytes when the body spells it with JSON or percent escapes or ends it with a carriage
return and line feed. Avoid configuring a secret that has such short lines. An extension marks a body
this way whenever it closes the body before its end, also for a reply that was complete when the
extension stopped at the last event of the stream.

**Secrets.** Core removes these from every request log, replacing them with a placeholder:

- the values of headers and query parameters whose names contain `authorization`, `api-key`,
  `apikey`, `api_key`, `token`, `secret`, `cookie` or `password`, or are exactly `key` (an
  authorization header keeps its scheme, as in `Bearer [redacted]`);
- anywhere in the file, every client key, the management key, every account's credential, the
  secrets in account settings (those of 8 bytes or more), the values of accounts' secret headers,
  and the user name and password of `proxy_url` and each account's proxy, also in their escaped and
  encoded forms (a proxy user name or password shorter than 8 bytes is removed only as part of the
  whole `user:password`);
- in the upstream sections, also the values of headers and query parameters whose names contain
  `auth`, `key`, `token`, `secret`, `cookie`, `session`, `password`, `passphrase` or `signature`, and the user name and password of a URL.

A secret made of parts has each part of 8 bytes or more removed on its own too, since an upstream may
echo only one: the token behind a public prefix (`devin-session-token$<jwt>`, `dca:<token>`), the
token of a `Bearer` value (or the decoded text of a `Basic` one), each value of a cookie list, and
each line of a secret that spans lines (a PEM private key). A prefix, a scheme or a cookie name is
never removed as a part.

Removal is by the secret's value, so text that is not a secret is not touched, except in a failed
request, where a word of the model's text that shares 4 characters (8 for a long token) with a
secret can be lost. The prompts and replies themselves are not removed.

**Read a request log.**

- Panel or desktop app: **Settings**, **Logs**, **Failed requests** lists the error logs, newest
  first, and opens one beside the list. Type a trace ID into **Trace ID** and choose **Open** to open
  the log of any request, plain or error. A log over 2 MiB cannot be shown in the panel.
- Command line: `pluxy logs errors` lists the error logs, `pluxy logs errors <name>` prints one,
  and `pluxy logs request <trace id>` prints the newest log of the request with that trace ID. The
  trace ID is the `X-CPA-TRACE-ID` header of the reply, and the `id` of the usage event. Core
  reads an error log only by a name of the form `error-<...>.log` (400 for another name, 404 when
  there is no such file) and finds a request log only by an ID of a trace ID's shape (see
  [Management API](management-api.md#logs)). A Responses
  WebSocket turn is the exception: its log has a trace ID of its own, and its usage event has no `id`. When there
  is no log and `logs.request_log` is off, the command says that only failed requests are logged.

## Profiling pages

Core can serve Go's profiling pages under `/debug/pprof/`: heap, goroutine and CPU profiles of the
running core, for finding what takes memory or time. They are off by default, and only a build made
with the `pprof` build tag has them:

```bash
go build -tags pprof -o pluxy ./cmd/pluxy
```

Turn them on in the config file:

```json
{"pprof": {"enable": true, "addr": "127.0.0.1:8316"}}
```

| Key | Meaning | Default |
| --- | --- | --- |
| `pprof.enable` | Serve the pages. | off |
| `pprof.addr` | The address of the pages' own listener. It must be a loopback address (`127.0.0.0/8`, `::1` or `localhost`); any other address makes the config fail to load. | `127.0.0.1:8316` |

The pages are on a listener of their own at `addr`, not on the client or management address. Core logs
the address when the listener starts. A reload that changes `enable` or `addr` starts, moves or
stops the listener; no restart is needed. A default build with the setting on logs one line saying
it has no profiling pages, and serves nothing (the line repeats at each config reload). An address core cannot bind refuses the reload.

```bash
go tool pprof http://127.0.0.1:8316/debug/pprof/heap     # a heap profile
```

The index at `/debug/pprof/` lists every profile. A 30-second CPU profile is
`/debug/pprof/profile?seconds=30`.

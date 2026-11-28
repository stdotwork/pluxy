# Proxy overhead and footprint

How much time and memory pluxy adds between a client and its upstream, how that is measured, and
the budgets CI holds it to, with CLIProxyAPI measured the same way for comparison. The numbers
below were measured at commit `1babf6deb`; rerun the benchmark to get them for another commit.

## Rerunning it

From `next/`, with Go installed and no account:

```bash
go run ./cmd/pluxy-bench                 # every scenario, 5 runs, report on stdout (about 15 minutes)
go run ./cmd/pluxy-bench -runs 1         # one run (about 3 minutes)
go run ./cmd/pluxy-bench -check          # what CI runs: the budget check (about 80 seconds)
go run ./cmd/pluxy-bench -json out.json  # also write every number, per run, as JSON
go run ./cmd/pluxy-bench -profile DIR    # CPU and heap profiles of the selected scenarios
go run ./cmd/pluxy-bench -requests 500 -profile DIR -scenarios stream-translate  # more samples to read
```

`go run ./cmd/pluxy-bench -h` lists the other flags: the mock's timings, the request body size, a
subset of scenarios, `-requests` (the request count of the scenarios that send a fixed number: all
but `agents` and `throughput`; refused with `-check`), and `-proxy-url` to measure another proxy
that is already running against the same mock. The memory and CPU figures read `/proc`, so they are
Linux only; elsewhere the report says they were not measured. On Linux, `-check` fails its memory
gates when the footprint was not sampled (`-proxy-url` without `-proxy-pid`, say), and its chunk
delay gates when no chunk was read, rather than passing on nothing.

Run it on a machine with nothing else busy. The report's header gives the commit, the CPU, and
the load average before and after, so a reader can tell when a machine was not idle.

## What it measures

The benchmark builds `cmd/pluxy` and the `openai-compatible` extension and runs them as they run
for users: core starts the extension as a program of its own and talks to it over a local socket,
so that hop is part of every number. Their upstream is a fake one inside the benchmark (the mock)
that speaks Anthropic Messages and OpenAI Chat Completions on `127.0.0.1`.

Each scenario runs twice with the same settings: once straight to the mock (direct) and once
through pluxy (proxy). What pluxy adds is the proxy number minus the direct one. The two sides
alternate which goes first, run after run.

- **Time to first token (TTFT):** from the client starting its request to the first text it
  reads.
- **Chunk delay:** each streamed text chunk carries the time the mock sent it. The client runs in
  the same process, so the time it reads the chunk minus that send time is the chunk's delay,
  measured on one clock.
- **Request path:** from the client starting its request to the mock having read the whole
  request body.
- **Fidelity:** every chunk marker arrives, in order, and the text read back equals the text the
  mock sent. A chunk lost, duplicated or reordered, or text changed, counts as a failure. Two
  chunks merged into one event are not seen: the client counts markers, not events.
- **Memory and CPU:** resident memory (RSS) of core and of each extension process, idle and
  sampled every 250 ms under load, and CPU time per request.
- **Start time and binary sizes.**

The request is about 100 KB of Anthropic Messages, shaped like a coding agent's: a system prompt,
20 tool definitions and earlier turns. `large-body` sends 800 KB, the size of a long session.

| Scenario | Client to upstream | At once | Mock timing |
| --- | --- | --- | --- |
| `stream-pass` | Messages to Messages, streamed | 1 | first token at 100 ms, 50 chunks 10 ms apart |
| `stream-translate` | Messages to Chat Completions, streamed | 1 | the same |
| `nonstream-pass` | Messages to Messages | 1 | reply after 100 ms |
| `nonstream-translate` | Messages to Chat Completions | 1 | reply after 100 ms |
| `agents` | Messages to Messages, streamed, for 20 s | 80 | first token at 300 ms, 100 chunks 30 ms apart |
| `throughput` | Messages to Messages, for 10 s | 16 | reply at once |
| `large-body` | Messages to Messages, streamed, 800 KB body | 1 | first token at 100 ms, 10 chunks 10 ms apart |

`agents` is the load of one heavy user running many coding agents: long streams, many open at
once, few new requests a second. 80 streams at once is above the most a heavy user's own proxy
logged over two days (73 at once; 12 or fewer 99% of the time).

## Baseline

`main` at `1babf6deb`, Go 1.26.10, on an 8 vCPU, 16 GB Linux machine (AMD EPYC, a Namespace
devbox). Median of 5 runs, with the lowest and highest run in brackets. Added = proxy minus
direct.

The machine is shared with other work, so it was not idle. Each run started only after the machine
had been under 25% busy for 15 seconds. The runs were still not equally quiet: the share of
processor time in use (the median of each run's samples, which include the benchmark's own work)
was 3 to 20% in the five pluxy runs and 3 to 38% in four of the five CLIProxyAPI runs. The fifth,
the first CLIProxyAPI run, was at 90%; its `agents` figures are the highest of CLIProxyAPI's five
(added total p50 28.6 ms against 4.7 to 9.1 ms in the others). Every range below includes it.

| Scenario | Added TTFT p50 | Added total p50 | Added total p99 | Chunk delay p99 (proxy) |
| --- | --- | --- | --- | --- |
| `stream-pass` | 2.2 ms (2.1 to 2.7) | 2.0 ms (1.7 to 2.2) | 6.8 ms (4.0 to 8.6) | 0.44 ms (0.36 to 2.86) |
| `stream-translate` | 3.0 ms (2.4 to 4.1) | 2.8 ms (1.9 to 3.7) | 5.4 ms (3.9 to 9.4) | 0.52 ms (0.43 to 2.82) |
| `nonstream-pass` | | 2.0 ms (1.8 to 3.3) | 3.8 ms (1.5 to 5.6) | |
| `nonstream-translate` | | 2.3 ms (1.7 to 2.6) | 3.5 ms (2.5 to 5.7) | |
| `agents` (80 at once) | 1.9 ms (1.3 to 2.5) | 2.0 ms (1.4 to 2.4) | 33 ms (24 to 55) | 0.46 ms (0.41 to 3.67) |
| `large-body` (800 KB) | 6.3 ms (3.8 to 10.5) | 6.6 ms (3.3 to 9.6) | 8.5 ms (6.8 to 16.8) | 0.48 ms (0.31 to 0.56) |

Straight to the mock, chunk delay p99 is 0.11 to 0.18 ms and the request path 0.2 to 0.3 ms
(0.9 ms at 800 KB); through pluxy the request path is 1.6 to 2.5 ms (7.2 ms at 800 KB). Chunk delay
p99 reaches 2.8 to 3.7 ms in the worst run of the streamed scenarios other than `large-body`; the
medians are 0.44 to 0.52 ms.

| Footprint | Value |
| --- | --- |
| Resident memory, idle, before any request | 50.8 MB in all (48.3 to 51.2) |
| Resident memory, idle, 10 s after the load | 75.1 MB in all (73.0 to 81.4) |
| Peak resident memory, `agents` (80 streams) | 117 MB in all (117 to 121), about 0.84 MB per open stream (0.82 to 0.89) |
| CPU per request, 100 KB body | 11 ms streamed, 15 ms streamed and translated, 4.0 ms not streamed (translated or not), one at a time; 1.7 ms at 16 at once |
| CPU per request, 800 KB body | 17 ms (11.5 to 20.5) |
| Throughput, `throughput` (16 at once, no upstream delay) | 2,848 requests/s (2,505 to 3,379); direct: 16,231 |
| Start, to listening | 40 ms (31 to 70) |
| Start, to the first request answered | 43 ms (33 to 72) |
| Binary sizes as released (`-trimpath -ldflags=-s -w`) | `pluxy` 12.3 MiB, `openai-compatible` 8.2 MiB |

On GitHub's ubuntu-latest runners (4 vCPU), `-check` measured an added TTFT of about 1.2 ms in
`stream-pass` and 1.8 ms in `stream-translate`, 50 MB idle and about 0.8 MB per open stream
(median of 3 runs: 45285206647 on `main` at `3fce95ab1`, 45292441059 and 45302488915).

## Compared with CLIProxyAPI

The same benchmark, on the same machine right after the baseline, against CLIProxyAPI v8.0.78's
released Linux binary (the proxy pluxy replaces), started with `-proxy-url` and pointed at the
same mock: a Claude API-key provider for the Messages scenarios and an OpenAI-compatibility
provider for the translated ones, with its request log, file logging and usage statistics off.
pluxy ran with its defaults. Median of 5 rounds, each round one run of every scenario per side,
with the side that goes first alternating; ranges in brackets, lowest to highest.

| | pluxy | CLIProxyAPI | Reading |
| --- | --- | --- | --- |
| Added TTFT p50, `stream-pass` | 2.2 ms (2.1 to 2.7) | 4.3 ms (3.9 to 4.8) | pluxy lower |
| Added TTFT p50, `stream-translate` | 3.0 ms (2.4 to 4.1) | 3.1 ms (2.5 to 4.5) | no measurable difference |
| Added total p50, `nonstream-pass` | 2.0 ms (1.8 to 3.3) | 3.1 ms (2.8 to 5.7) | no measurable difference |
| Added TTFT p50, `large-body` (800 KB) | 6.3 ms (3.8 to 10.5) | 29.3 ms (22.3 to 33.6) | pluxy lower |
| Added TTFT p50, `agents` (80 at once) | 1.9 ms (1.3 to 2.5) | 5.0 ms (3.9 to 14.6) | pluxy lower |
| Chunk delay p99, `agents` | 0.46 ms (0.41 to 3.67) | 1.31 ms (0.42 to 7.58) | no measurable difference |
| Added total p99, `agents` | 33 ms (24 to 55) | 27 ms (12 to 48) | no measurable difference; pluxy's median is higher |
| Upstream connections opened, `agents` (560 requests) | 133 (128 to 150) | 328 (315 to 336) | direct: 80 |
| Throughput, `throughput` | 2,848 requests/s (2,505 to 3,379) | 1,769 requests/s (1,646 to 1,843) | pluxy higher |
| CPU per request, `throughput` | 1.7 ms (1.5 to 1.8) | 2.9 ms (2.8 to 3.0) | pluxy lower |
| CPU per request, `agents` | 11.8 ms (9.8 to 13.4) | 12.6 ms (9.9 to 13.8) | no measurable difference |
| Resident memory, idle, before any request | 50.8 MB (48.3 to 51.2) | 58.6 MB (57.6 to 59.1) | pluxy lower |
| Resident memory, idle, 10 s after the load | 75.1 MB (73.0 to 81.4) | 93.5 MB (92.2 to 97.4) | pluxy lower |
| Peak resident memory, `agents` | 117 MB (117 to 121) | 213 MB (195 to 215) | pluxy lower |
| Binary size as released | 12.3 MiB core and 8.2 MiB extension | 74.9 MiB | |

CLIProxyAPI is one process; pluxy's requests also cross from core to the extension and back. In no
scenario is pluxy's added time measurably above CLIProxyAPI's. Where the ranges of the five rounds
overlap, the table reports no measurable difference rather than reading the medians. Where they do
not overlap, pluxy adds less time: first token in `stream-pass`, `agents` and `large-body` (800 KB:
about a fifth of CLIProxyAPI's added time).

The two translated scenarios were also run as 12 paired rounds, pluxy and CLIProxyAPI one after
the other in each round (the machine was 2 to 29% busy), which separates a difference of a
fraction of a millisecond better than five rounds. Difference = pluxy minus CLIProxyAPI, median of
the 12 rounds; the sign test is on how many rounds pluxy was higher:

| | Difference | pluxy higher in | Sign test |
| --- | --- | --- | --- |
| `stream-translate`, added TTFT p50 | +0.09 ms | 7 of 12 rounds | p = 0.77 |
| `stream-translate`, added total p50 | -0.31 ms | 4 of 12 | p = 0.39 |
| `stream-translate`, added total p99 | -2.48 ms | 3 of 12 | p = 0.15 |
| `stream-translate`, chunk delay p99 | 0.00 ms | 6 of 12 | p = 1.00 |
| `nonstream-translate`, added total p50 | -0.68 ms | 4 of 12 | p = 0.39 |
| `nonstream-translate`, added total p99 | -0.69 ms | 5 of 12 | p = 0.77 |
| `stream-translate`, CPU per request | -13.2 ms | 0 of 12 | p < 0.001 |

So there is no measurable difference in the time the two add to a translated request, and pluxy
uses less CPU for a streamed translated one (14.5 ms against 27.8 ms). All 12 rounds had 0 errors
and 0 fidelity failures, as did the 5 above.

What is not better, or not clear:

- **`agents` total p99.** pluxy's median is higher (33 ms against 27 ms) and the ranges overlap (24
  to 55 against 12 to 48), so the five rounds do not show which is higher. The CLIProxyAPI round
  that ran at 90% busy holds its highest value (48 ms); without it CLIProxyAPI's range is 12 to 30
  ms (median 23 ms), which still overlaps pluxy's. This is the one row of the table above where
  pluxy's median is the higher one.
- **Translated TTFT** is level, not lower: 3.0 against 3.1 ms in the five rounds, +0.09 ms in the
  paired rounds.

pluxy uses less memory: 13% less idle before any request, 20% less 10 s after the load and 45%
less at the peak under `agents` (medians).

## Where the added time goes

Every request crosses two processes, core and the extension, so the work that grows with the size
of a request is kept to one pass on each side:

- **The request is parsed once on each side and sent to the extension once** (the next section).
  Idle upstream connections are kept for concurrent requests.
- **A translated request's Chat body is written in one pass.** The extension's body writer
  (`next/format/openaichat/bodywriter.go`) writes the messages straight into one buffer, sized
  once, and escapes each text once; a 100 KB request was measured allocating about 129 KB.
- **Large call frames are written and read in one pass.** A call's frame is about 100 KB, nearly all
  of it the client's body. Core writes the body into the frame without walking it through the
  general JSON encoder, and the extension reads the frame with the repository's own JSON scanner
  (`next/internal/jsonscan`).
- **Reply frames are read without the general decoder.** Core reads a reply frame that arrived
  whole with one read, and the fields it needs (each event's `type` among them) are read with the
  scanner. Every chunk is forwarded as soon as it arrives, with no batching.

CPU profiles of core and the extension at `1babf6deb` show what limits it now (`-profile`, one
profile of each, on the same machine). Under `stream-translate` (2,000 requests of 10 chunks, the
mock answering within 1 ms, so the mock's own delays are out of the way) core used about 1.7 ms of
CPU a request and the extension 2.1 ms. Shares of each process's CPU time:

| | Core | Extension |
| --- | --- | --- |
| System calls and thread wake-ups | 18% and 7% | 13% and 8% |
| Garbage-collector marking | 16% | 17% |
| Parsing the client's request | 13% | 15% |
| Reading the reply frames (core) or the upstream's stream (extension) | 9% | 7% |
| Sending each chunk to the client | 10% | |
| Writing the call frame, or the reply frames | 6% | 4% |
| Reading the call frame | | 6% |
| Building the Chat body | | 9% |

Shares overlap: a system call made while sending a chunk is counted in both rows. Core and the
extension each parse the body once, and no frame step costs more than that parse. Under `agents` (80
streams, 23 s) each process used about 0.11 of a core, and most of it is per chunk: system calls 24%
of core and 25% of the extension, thread wake-ups 12% and 15%, and in core reading each reply frame
(32%) and sending each chunk to the client (15%). The parse is 6% of core and 4% of the extension
there. What is left is the work of forwarding every chunk as it arrives, in two processes.

### The request goes to the extension once

A generate call can carry the client's request in two forms: parsed (`items`, `tools`, `origin`)
and as the client's own body (`native.body`). Sending both means core encodes the request twice and
the extension decodes it twice, though an extension that sends the client's body upstream unchanged
never reads the parsed form.

So an extension can say it parses the body itself, and with which version of core's parsers (the
SDK does, with its own release's `format.ParserVersion`), and core then leaves the parsed form out
whenever the body is still the one it parsed and the extension's parser version is core's. The
extension parses the body with the same parsers core uses, so its handler gets the same request it
would get with the parsed form; an extension from another release whose parsers differ gets the
full request. A test keeps what the parsers make of each corpus and format-test request
in a golden file and fails, naming some of them, until the version is raised when a recorded
request's parse changes; a request added to the corpus or the format tests need not be recorded
to pass.
The tradeoff: core does not encode the parsed form for such a call, but the extension parses the
body in place of decoding it, also when it only passes the body through, since a handler's
request is a plain value that cannot be filled only when it is read. Core still sends the full
request when the body changed after parsing (a thinking suffix taken off its model), when the
body is not valid UTF-8, and to an extension that does not say it parses, or parses with another
version. The protocol's README
describes the rule (`next/protocol/README.md`, "Native-only calls").

## The budget check

`go run ./cmd/pluxy-bench -check` runs `stream-pass`, `stream-translate`, `nonstream-pass` and
`agents` (8 s) three times and fails when a median passes its budget. The budgets are constants in
[`next/cmd/pluxy-bench/budget.go`](../../next/cmd/pluxy-bench/budget.go), each with the CI runs it
came from. CI runs the check in `next-ci.yml`'s `bench` job.

Time budgets are ratios of the proxy to the direct path in the same run, so the mock's own
timing and the client's costs cancel out and the check needs no absolute times. The machine's
speed does not cancel out: a slower runner adds more on the proxy side. So each budget leaves
room for three times the overhead measured on CI's runner (for `stream-pass`, 1.04 against measured
ratios of 1.012, 1.012 and 1.018, that is a 4 ms budget against 1.2 to 1.8 ms overhead in three CI
runs): a change that adds about 3 ms or more to a request fails, the spread between runners does
not. Chunk delay is bounded as a fraction of the mock's chunk interval, since the direct chunk
delay is too close to zero to divide by. A budget is also kept above the widest value any of the
three runs measured: by 0.01 for a ratio, by 20% for a fraction or a size. Memory budgets are in MB
and KB per open stream: 100 MB and 2,200 KB, which are below three times the CI values (about
50 MB, and 0.8 MB a stream). A budget is lowered when a fix lowers its value, never raised. The
time budgets are 1.04 (`stream-pass` and `agents` p99), 1.06
(`stream-translate` and `nonstream-pass`) and 0.12 and 0.09 of the interval for chunk delay. Binary
sizes, start time and throughput are reported, not checked: new features grow them on purpose.

## What it does not cover

- **TLS.** The mock speaks plain HTTP: core starts extensions with an environment of its own, so
  a test certificate cannot reach them without a change to core. The cost of TLS handshakes to a
  real upstream, and how often an extension opens a new connection to it, are not in the times.
  The report counts upstream connections opened:
  under `agents`, the extension opened 133 (128 to 150) for 560 requests (direct: 80), so about one
  request in four opens a new connection. Over HTTPS to an upstream that speaks HTTP/2 those would
  share one connection; to an HTTP/1.1 upstream each would add a TLS handshake.
- **Other extensions and formats.** Only `openai-compatible`, with Messages clients. The other
  providers have their own request handling; Responses, Gemini and WebSocket clients are not
  measured.
- **The network.** Everything runs on one machine over loopback.
- **Real models.** The mock's timings are fixed; a real model's first token takes far longer than
  100 ms, so what pluxy adds is a smaller share of what a user waits.

## How other gateways measure

The method follows LiteLLM's AIGatewayBench, which subtracts a direct-to-mock run from a
through-the-gateway run at the same load
([ai-gateway-bench](https://github.com/BerriAI/ai-gateway-bench)), and Bifrost's mock upstream,
which schedules each chunk by deadline so the mock's own delays don't add up
([bifrost-benchmarking](https://github.com/maximhq/bifrost-benchmarking)). The benchmark does not
use their tools: `next/` takes no dependencies beyond its two, and general load tools such as
vegeta, k6 or hey can't time individual SSE chunks or read each process's memory.

Their published figures, measured on their machines with their loads and not comparable number
for number with the table above: LiteLLM's write-up reports 0.9 ms p50 overhead and 357 MiB peak
RSS for its Rust gateway, 1.2 ms and 2,809 MiB for Bifrost, at 50 requests a second on 4-core
pods ([LiteLLM, "Better benchmarks"](https://docs.litellm.ai/blog/better-benchmarks)).

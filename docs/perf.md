# Proxy overhead and footprint

How much time and memory pluxy adds between a client and its upstream, how that is measured, and
the budgets CI holds it to. The numbers below are a baseline on `main` at `35ad0d646`; rerun the
benchmark to get them for another commit.

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

`go run ./cmd/pluxy-bench -h` lists the other flags: the mock's timings, the request body size,
a subset of scenarios, `-requests` (the request count of the scenarios that send a fixed number:
all but `agents` and `throughput`; refused with `-check`), and `-proxy-url` to measure another proxy that is already running against
the same mock. The memory and CPU figures read `/proc`, so they are Linux only; elsewhere the
report says they were not measured. On Linux, `-check` fails its memory gates when the footprint
was not sampled (`-proxy-url` without `-proxy-pid`, say), and its chunk delay gates when no chunk
was read, rather than passing on nothing.

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

`main` at `35ad0d646`, Go 1.26.10, on an idle 8 vCPU, 16 GB Linux machine (AMD EPYC, a Namespace
devbox; load average 1.1 before, 3.1 after). Median of 5 runs, with the lowest and highest run
in brackets. Added = proxy minus direct.

| Scenario | Added TTFT p50 | Added total p50 | Added total p99 | Chunk delay p99 (proxy) |
| --- | --- | --- | --- | --- |
| `stream-pass` | 12.9 ms (12.5 to 13.0) | 12.7 ms (12.3 to 12.9) | 17.8 ms (14.5 to 24.4) | 0.53 ms (0.50 to 0.53) |
| `stream-translate` | 15.7 ms (15.4 to 16.2) | 15.2 ms (14.8 to 15.9) | 18.5 ms (17.0 to 22.6) | 0.55 ms (0.51 to 0.59) |
| `nonstream-pass` | | 13.0 ms (12.6 to 13.1) | 15.3 ms (13.5 to 16.9) | |
| `nonstream-translate` | | 15.5 ms (15.2 to 15.7) | 18.4 ms (17.3 to 21.9) | |
| `agents` (80 at once) | 12.7 ms (12.2 to 15.9) | 14.6 ms (14.0 to 21.2) | 103 ms (68 to 120) | 1.7 ms (1.5 to 2.5) |
| `large-body` (800 KB) | 83.8 ms (83.0 to 86.3) | 83.5 ms (82.3 to 86.1) | 93.4 ms (89.8 to 111) | 0.64 ms (0.61 to 0.68) |

Straight to the mock, chunk delay p99 is 0.12 to 0.17 ms and the request path 0.3 ms (1.3 ms at
800 KB); through pluxy the request path is 12 to 15 ms (84 ms at 800 KB).

| Footprint | Value |
| --- | --- |
| Resident memory, idle, before any request | core 32.5 MB, extension 9.8 MB, 42.3 MB in all |
| Resident memory, idle, 10 s after the load | core 43.2 MB, extension 21.2 MB, 64.4 MB in all |
| Peak resident memory, `agents` (80 streams) | 166 MB in all (142 to 173), about 1.5 MB per open stream |
| CPU per request, 100 KB body | 33 ms streamed, 19 ms not streamed, one at a time; 5.3 ms at 16 at once |
| CPU per request, 800 KB body | 117 ms (112 to 123) |
| Throughput, `throughput` (16 at once, no upstream delay) | 1,154 requests/s (1,122 to 1,163); direct: 16,035 |
| Start, to listening | 64 ms |
| Start, to the first request answered | 68 ms |
| Binary sizes as released (`-trimpath -ldflags=-s -w`) | `pluxy` 10.5 MiB, `openai-compatible` 6.8 MiB |

On GitHub's ubuntu-latest runners (4 vCPU), `-check` measured an added TTFT of 3.5 ms in
`stream-pass` and 4.0 ms in `stream-translate`, 45 MB idle and about 0.7 MB per open stream
(median of 3 runs: 44852547274 on `main` at `d9d1ee2d9`, 44850768612 and 44848492287).

## Compared with CLIProxyAPI

The same benchmark, on the same machine right after the baseline, against CLIProxyAPI v8.0.78's
released Linux binary (the proxy pluxy replaces), started with `-proxy-url` and pointed at the
same mock: a Claude API-key provider for the Messages scenarios and an OpenAI-compatibility
provider for the translated ones, with its request log, file logging and usage statistics off.
pluxy ran with its defaults. Median of 5 runs.

| | pluxy | CLIProxyAPI |
| --- | --- | --- |
| Added TTFT p50, `stream-pass` | 12.9 ms | 6.5 ms |
| Added TTFT p50, `stream-translate` | 15.7 ms | 4.6 ms |
| Added total p50, `nonstream-pass` | 13.0 ms | 6.5 ms |
| Added TTFT p50, `large-body` (800 KB) | 83.8 ms | 36.0 ms |
| Chunk delay p99, `agents` (80 at once) | 1.7 ms | 0.84 ms |
| Added total p99, `agents` | 103 ms | 73 ms |
| Upstream connections opened, `agents` | 371 | 331 |
| Throughput, `throughput` | 1,154 requests/s | 1,826 requests/s |
| CPU per request, `throughput` | 5.3 ms | 3.0 ms |
| Resident memory, idle | 42 MB | 60 MB |
| Peak resident memory, `agents` | 166 MB | 216 MB |
| Binary size as released | 10.5 MiB core and 6.8 MiB extension | 74.9 MiB |

CLIProxyAPI is one process; pluxy's requests also cross from core to the extension and back.
pluxy adds about twice the time per request and uses about a quarter less memory.

## Where the added time goes

CPU profiles of core under `throughput` (`-profile`) put 54% of core's CPU time in parsing the
Messages request (`anthropicmessages.ParseRequest`): it decodes each level of the JSON into
`json.RawMessage` values and decodes those again, so the body is validated and copied once per
level. Another 12% is a second full decode of the body to look for `prompt_cache_key`
(`sessionKey` in `internal/server/route.go`), and 13% is encoding the request again as JSON to
send it to the extension. Core allocates about 1.5 MB per 100 KB request. The added time grows
with the body: about 13 ms at 100 KB and 84 ms at 800 KB, one request at a time.

### The request goes to the extension once

A generate call used to carry the client's request twice: parsed (`items`, `tools`, `origin`)
and as the client's own body (`native.body`). Core encoded both into the call's JSON and the
extension decoded both, though an extension that sends the client's body upstream unchanged never
reads the parsed form. Later profiles put the largest remaining cost of a request in that
encoding and decoding, on both sides of the socket.

Since protocol 0.27 an extension can say it parses the body itself, and with which version of
core's parsers (the SDK does, with its own release's `format.ParserVersion`), and core then
leaves the parsed form out whenever the body is still the one it parsed and the extension's
parser version is core's. The extension parses the body with the same parsers core uses, so its
handler gets the same request as before; an extension from another release whose parsers differ
gets the full request. A test keeps what the parsers make of each corpus and format-test request
in a golden file and fails, naming some of them, until the version is raised when a recorded
request's parse changes; a request added to the corpus or the format tests need not be recorded
to pass.
The tradeoff: core no longer encodes the parsed form for such a call, but the extension parses the
body in place of decoding it, also when it only passes the body through, since a handler's
request is a plain value that cannot be filled only when it is read. Core still sends the full
request when the body changed after parsing (a thinking suffix taken off its model), when the
body is not valid UTF-8, and to an extension that does not say it parses, or parses with another
version. The protocol's README
describes the rule (`next/protocol/README.md`, "Native-only calls").

## The budget check

`go run ./cmd/pluxy-bench -check` runs `stream-pass`, `stream-translate`, `nonstream-pass` and
`agents` (8 s) three times and fails when a median passes its budget. The budgets are constants in
[`next/cmd/pluxy-bench/budget.go`](../next/cmd/pluxy-bench/budget.go), each with the baseline it
came from. CI runs the check in `next-ci.yml`'s `bench` job.

Time budgets are ratios of the proxy to the direct path in the same run, so the mock's own
timing and the client's costs cancel out and the check needs no absolute times. The machine's
speed does not cancel out: a slower runner adds more on the proxy side. So each budget leaves
room for two to three times today's overhead on CI's runner (for `stream-pass`, 1.10 against
measured ratios of 1.030, 1.034 and 1.047, that is a 10 ms budget against 3.0 to 4.7 ms overhead
in three CI runs): a change that adds about 6 ms or more to a request fails, the spread between
runners does not. Chunk delay is bounded as a fraction of the mock's chunk interval, since the
direct chunk delay is too close to zero to divide by. Memory budgets are in MB and KB per open
stream.
Binary sizes, start time and throughput are reported, not checked: new features grow them on
purpose.

## What it does not cover

- **TLS.** The mock speaks plain HTTP: core starts extensions with an environment of its own, so
  a test certificate cannot reach them without a change to core. The cost of TLS handshakes to a
  real upstream, and how often an extension opens a new connection to it, are not in the times.
  The report counts upstream connections opened: under `agents`, the extension opened 371 for
  about 490 requests (direct: 80), so most streams open a new connection. Over HTTPS to an
  upstream that speaks HTTP/2 those would share one connection; to an HTTP/1.1 upstream each
  would add a TLS handshake.
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

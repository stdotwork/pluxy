# Differential test corpus

The corpus checks the new core against today's core (`core/`) at the HTTP seam. Each case sends
one client request to a core whose provider points at a fake upstream server. The fake upstream
records every request it gets and answers with scripted replies. The runner then compares what
the two cores did: the reply the client got and the requests that reached the upstream.

The new core must match the old one except where a case lists an **exception**. Each exception
gives a reason, so every difference is on the record.

## Running it

From `next/`:

```bash
go test -count=1 -v -run TestCorpus ./corpus
```

This builds the new core and compares it with the old core's replies recorded in the case files.
It does not build or run the old core, so it is fast enough for every CI run. The report is
printed at the end (with `-v`).

| Variable | Effect |
| --- | --- |
| `PLUXY_CORPUS_OLD_CORE=1` | Also build `core/` (with cgo) and run it live. Fails if the live old core no longer matches a recording. |
| `PLUXY_CORPUS_OLD_CORE_BIN=path` | Run this old-core binary live instead of building one. |
| `PLUXY_CORPUS_RECORD=1` | Run the old core live and write its observations into the case files (`recorded_old_core`). |
| `PLUXY_CORPUS_REPORT=path` | Also write the report to a file. |
| `PLUXY_CORPUS_CASE=regexp` | Run only cases whose `format/provider/name` matches. |
| `PLUXY_CORPUS_DUMP_NEW=dir` | Write each new-core observation to a file in `dir`, to read the values an exception's `want` needs. |
| `PLUXY_CORPUS_LOGS=1` | With `-v`, log each core's output. |

CI runs the fast form in `next-ci.yml` and the live form in `next-corpus.yml` (on changes to
`next/` or `core/`).

## How a core is run

Both cores run as separate programs on random local ports, each with a fresh temporary folder
holding its config, its auth folder and its `HOME`. Their environment is cleared except for
`PATH`, and outbound proxies are set to a closed local port, so the old core's background
version checks cannot reach the internet. The fake upstream is on loopback, which is never
proxied.

The generated config is the same for every case:

- one provider account pointed at the fake upstream, with the API key `sk-corpus-upstream`
  (not a secret); a case with `accounts` above 1 gets more accounts, keyed
  `sk-corpus-upstream-2`, `sk-corpus-upstream-3` and so on, all pointed at the same upstream;
- the client key `corpus-client-key`;
- routing that never waits: the old core has no extra retry rounds (`request-retry: 0`), no
  waits (`max-retry-interval: 0`) and no cooldowns; the new core runs its default routing,
  which also never waits. Both still move a request on to another account within one round;
- for the old core: management and the bundled panel off, response compression off, the
  embedded model catalog (`-local-model`), and the request's model listed for the account (the
  old core serves only listed models; `account_models` in a case overrides this for both cores).

The client sends no `Accept-Encoding`, so replies are observed as the core sent them.

Providers with their own folder of cases:

| Provider | New core | Today's core |
| --- | --- | --- |
| `openai-compatible` | OpenAI-compatible extension, `wire_api` chat | an `openai-compatibility` entry |
| `openai-responses`, `anthropic`, `gemini` | OpenAI-compatible extension, `wire_api` responses, messages or gemini | a Codex API key without the Codex identity, a Claude API key, a Gemini API key |
| `codex` | Codex extension, API key | a Codex API key (Codex identity on) |
| `codex-oauth` | Codex extension, a ChatGPT subscription's OAuth credential | a Codex auth file |

Today's core sends a ChatGPT subscription's requests to `https://chatgpt.com/backend-api/codex`,
which no setting moves. For `codex-oauth` the runner therefore gives the old core an HTTP proxy
(the auth file's `proxy_url`) that accepts `CONNECT` only to `chatgpt.com:443`, ends the TLS
connection itself with a certificate from a test authority made for the run (the old core
trusts it through `SSL_CERT_FILE`), and hands the requests to the fake upstream. A `CONNECT` to
any other host is refused and fails the case, so nothing reaches the internet: this is how the
corpus noticed that today's core renews a subscription's token at `auth.openai.com` on a 401,
which the corpus does not fake (no `codex-oauth` case answers 401). The fake tokens are
unsigned JWTs that expire in 2100, so neither core renews them, and the old core is given time
to load its auth file before the case runs. The subscription cases use `gpt-6.1-sol`, because
today's core serves a subscription only the models in its catalog.

## Case files

One file per case: `cases/<client format>/<provider>/<name>.json`. `cases/corpus.json` holds
settings for every case.

```json
{
  "description": "What behaviour this pins down.",
  "source": ["core/... test or code the behaviour comes from"],
  "client": {
    "path": "/v1/chat/completions",
    "body": {"model": "gpt-test", "messages": [{"role": "user", "content": "Say hello"}], "stream": true}
  },
  "upstream": [
    {
      "sse": [
        {"data": {"id": "chatcmpl-corpus", "object": "chat.completion.chunk", "choices": [...]}},
        {"data": "[DONE]"}
      ]
    }
  ],
  "exceptions": [
    {"path": "client.headers.Retry-After", "kind": "gap", "reason": "Why the new core differs here."}
  ],
  "recorded_old_core": {"client": {...}, "upstream": [...]}
}
```

`client`: `method` (default `POST`), `path`, `headers`, `auth` (`client-key` by default,
`none`, or a literal token), and `body` (JSON) or `body_text` (literal text, for bodies that
are not JSON). The runner adds `Content-Type: application/json` and `User-Agent: pluxy-corpus/0`
unless the case sets them.

`upstream`: one reply per upstream request, in order. Each has `status` (default 200),
`headers`, and one of `body` (JSON), `body_text` (literal) or `sse` (stream events). An SSE event
has `data` (a JSON value, or a string written as is, such as `"[DONE]"`), optional `event` and
`comment`, or `raw` for bytes written exactly (malformed streams). `"abort": true` drops the
connection after the scripted body. A request beyond the script gets a 500, which shows up in
the comparison.

`account_models`: models the account lists in both cores (see above).

`accounts`: how many provider accounts both cores have (default 1). The old core's first pick
between equal accounts changes from run to run, so multi-account cases mark
`upstream[*].headers.Authorization` volatile; which account the new core picks is pinned by
the e2e tests.

`volatile`: paths whose values change between runs (timestamps, trace IDs). They are replaced by
`"(volatile)"` before any comparison.

`exceptions`: known differences. Each has:

- `path`: an observation path. Without `want` or `absent` it covers only the difference at that
  exact path, and only between plain values (not objects or arrays). Replacing or dropping an
  object or array needs `want` or `absent`.
- `kind`: `intended` when the new core differs on purpose (a decision is on record), or `gap` when
  it does not match yet and a later slice or the owner has to settle it.
- `reason`: why. Required.
- `want` (optional): the value the new core must give at `path`, in full, so an exception pins the
  new behaviour as well as excusing the old one. With `want` the exception also covers the
  differences below `path`, since nothing under it can be anything else. For a reply the new core
  replaces (a different status, an error body instead of data) pin `status` and `body` each.
- `absent` (optional, instead of `want`): the new core must give no value at `path`.

An exception that covers no difference fails the case, so the list cannot go stale. Global
exceptions in `cases/corpus.json` apply to every case and fail only if they cover nothing in the
whole corpus. A global `want` or `absent` is checked in the cases where the exception covers a
difference.

The test run also corrupts each value of each case's new-core observation in turn and fails if the
case still passes, so an exception cannot hide a difference it was not written for.

`recorded_old_core` is written by `PLUXY_CORPUS_RECORD=1`; do not edit it by hand.

## Observations and paths

An observation is JSON:

```json
{
  "client": {"status": 200, "headers": {...}, "body": ..., "events": [...], "body_error": "..."},
  "upstream": [{"method": "POST", "path": "/v1/chat/completions", "headers": {...}, "body": ...}]
}
```

- `body` is parsed JSON when the body is JSON, otherwise text. A streamed reply
  (`text/event-stream`) is a list of `events`, each with `data` (parsed when it is JSON) and
  `event`, `id` or `comment` when present. `body_error` is set when the body broke off.
- Headers are compared by value, except `Connection`, `Content-Length`, `Date`, `Host`,
  `Keep-Alive` and `Transfer-Encoding`, which describe the connection rather than the behaviour.
- Object key order and whitespace are not compared; array order is.
- Timing is not compared (whether a stream arrives incrementally is the e2e tests' job).

A path joins keys with `.` and puts array indexes in brackets:
`client.events[2].data.choices[0].delta`, `upstream[0].headers.User-Agent`. In exceptions and
`volatile`, `*` matches any key and `[*]` any index. Header names match case-insensitively.

## Adding cases

1. Write the case file without `recorded_old_core`. Take the behaviour from today's core: its
   tests, code or an issue, and name it in `source`.
2. Record it: `PLUXY_CORPUS_RECORD=1 PLUXY_CORPUS_CASE=<name> go test -count=1 -run TestCorpus ./corpus`.
   Recording also lays out the file in the corpus style (one stream event per line).
3. Run the corpus. For each difference the report lists, fix the new core or add an exception
   with its reason.

A new provider needs an entry in `Providers` (`cores.go`): how each core's config points that
provider at the fake upstream (for the old core, its `api-keys` block, or auth files with
`OldCoreAuthFiles`, and `Intercept` for a fixed upstream host). A new extension also goes in
`NewCoreExtensions`.

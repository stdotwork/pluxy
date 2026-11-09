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
- for the old core: the bundled panel off, management off unless the case calls a management API
  (see [Management API cases](#management-api-cases)), response compression off, the embedded
  model catalog (`-local-model`), and the request's model listed for the account (the old core
  serves only listed models; `account_models` in a case overrides this for both cores). A provider
  set up from the old core's `api-keys` block lists the same model for the new core's account,
  since both cores serve an account without a list only its provider's catalog.

The client sends no `Accept-Encoding`, so replies are observed as the core sent them.

Providers with their own folder of cases:

| Provider | New core | Today's core |
| --- | --- | --- |
| `openai-compatible` | OpenAI-compatible extension, `wire_api` chat | an `openai-compatibility` entry |
| `openai-responses`, `anthropic`, `gemini` | OpenAI-compatible extension, `wire_api` responses, messages or gemini | a Codex API key without the Codex identity, a Claude API key, a Gemini API key |
| `codex` | Codex extension, API key | a Codex API key (Codex identity on) |
| `codex-oauth` | Codex extension, a ChatGPT subscription's OAuth credential | a Codex auth file |
| `gemini-api-key` | Gemini extension, API key | a Gemini API key |
| `vertex-api-key` | Gemini extension, Vertex settings and an express mode key | a `vertex` API-key entry |
| `vertex` | Gemini extension, a Vertex service account | a Vertex service account auth file |
| `kimi` | Kimi extension, a Kimi Code OAuth credential | a Kimi auth file |
| `meta` | Meta extension, a Muse Code login's OAuth credential (the minted key and its device token) | a Meta auth file with the key and `base_url` |
| `meta-api-key` | Meta extension, an API key | a `meta` group with `base-url` and an API key |
| `antigravity` | Antigravity extension, an OAuth credential with its project | an Antigravity auth file with `base_url`, the project and saved model hints; its `fetchAvailableModels` calls are answered aside |
| `devin` | Devin extension, a session token as an API key credential | a Devin auth file with the token and `base_url`; its `GetUserStatus` calls are answered aside. Models are named `devin/<model>`, as today's core lists them; Gemini clients write the slash as `%2F` in the path, with `account_models` naming the model |
| `xai` | xAI extension, an API key | an `xai` group with `base-url` and an API key |
| `xai-cli` | xAI extension, a Grok login's OAuth credential with `base_url` | an xAI auth file with `auth_kind` `oauth`, an expiry far ahead and `base_url` |
| `codex-ws` | Codex extension, API key, `"websockets": true` in the account's settings | a Codex API key with `websockets: true` |
| `claude-oauth` | Claude extension, a subscription's OAuth credential with `base_url` | a Claude auth file with an access token expiring far ahead. Today's core sends a subscription's requests to `api.anthropic.com` whatever the file says, so only the management cases (below) reach the fake upstream with this provider |

Today's core sends a ChatGPT subscription's requests to `https://chatgpt.com/backend-api/codex`,
which no setting moves. For `codex-oauth` the runner therefore gives the old core an HTTP proxy
(the auth file's `proxy_url`) that accepts `CONNECT` only to `chatgpt.com:443`, ends the TLS
connection itself with a certificate from a test authority made for the run (the old core
trusts it through `SSL_CERT_FILE`), and hands the requests to the fake upstream. A plain
request for the fake upstream's own address (the old core's management `api-call` sends a quota
request there through the auth file's proxy) is passed to it. A `CONNECT` to any other host is
refused and fails the case, so nothing reaches the internet: this is how the
corpus noticed that today's core renews a subscription's token at `auth.openai.com` on a 401,
which the corpus does not fake (no `codex-oauth` case answers 401). The fake tokens are
unsigned JWTs that expire in 2100, so neither core renews them, and the old core is given time
to load its auth file before the case runs. The subscription cases use `gpt-6.1-sol`, because
today's core serves a subscription only the models in its catalog.

A Vertex service account works the same way: today's core always calls
`https://aiplatform.googleapis.com` and trades the account's signed JWT for a token at
`https://oauth2.googleapis.com/token`, so both hosts go through the proxy, and each `vertex`
case's first upstream reply is the token endpoint's. The new core's account points `base_url`
and the key's `token_uri` at the fake upstream. The key is an RSA key made for the run; the JWT
body is volatile. These cases use `gemini-2.5-pro`, a model in today's catalog.

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
`management-key`, `none`, or a literal token), and `body` (JSON) or `body_text` (literal text,
for bodies that are not JSON). `then` lists more requests of the same form, sent one after
another once the reply before has ended, for behaviour that shows across requests (which account
each one goes to, a cooldown a failure leaves); their replies are the observation's `then`, and
the global exceptions and volatile paths for `client` apply to each of them. The runner adds
`Content-Type: application/json` and `User-Agent: pluxy-corpus/0` unless the case sets them.

`upstream`: one reply per upstream request, in order. Each has `status` (default 200),
`headers`, and one of `body` (JSON), `body_text` (literal), `sse` (stream events) or `connect`
(Connect frames of protobuf messages, below). An SSE event
has `data` (a JSON value, or a string written as is, such as `"[DONE]"`), optional `event` and
`comment`, or `raw` for bytes written exactly (malformed streams). `"abort": true` drops the
connection after the scripted body. A request beyond the script gets a 500, which shows up in
the comparison.

A client may instead hold a WebSocket: `client.websocket` is a script, and the request becomes a
`GET` with an upgrade (no body). Each step is one of `send` (a JSON value, sent as one text
message), `send_text` (literal text), `read` (read messages until one whose `type` is the given
one, or `"turn"` for any message that ends a turn: `response.completed`, `response.done`,
`response.incomplete`, `response.failed` or `error`), `await_drop` (wait until the fake upstream's
WebSocket `upstream[n]`, `n` counting from 0, has dropped its connection and the core has closed its
side; a core that never does is recorded as `timeout`), `close` (a close code) or `drop` (end the
connection without a close message). After the last step the client closes normally. A read
that waits more than 10 seconds stops the script and is recorded as `timeout`.

An upstream reply may be `websocket`: the fake upstream accepts the handshake and plays a script.
Each step is one of `receive` (wait for the next message from the core), `send` (a JSON value as
one text message), `close` (`code` and `reason`) or `drop` (end the connection without a close
message, then wait for the core to close its side; a core that does not within 10 seconds is
recorded as `timeout`). A client that continues after a drop uses `await_drop`: the upstream drops
right after its last message, and the client would otherwise race the drop. Every message the core
sends is recorded, whenever it arrives; after the last step the fake upstream reads on until the
core closes the socket. A `hold` reply (HTTP) keeps the response open after its body until the core
ends the request, like an upstream still generating.

`old_client` and `old_client_more`: see [Management API cases](#management-api-cases).

`account_models`: models the account lists in both cores (see above).

`new_core_config`: settings added to the new core's config, by top-level key, for a behaviour
today's core has by default and the new core only when configured (CORS, for example). Today's
core runs with its usual config.

`accounts`: how many provider accounts both cores have (default 1). The old core orders equal
accounts by an ID hashed from the upstream URL, which changes from run to run, so multi-account
cases mark `upstream[*].headers.Authorization` volatile, or set `label_accounts`: each account's
key in the upstream requests' headers becomes `account-1`, `account-2` and so on, numbered in
the order the accounts were first used, which compares the pattern of turns.

`old_core` and `new_core` add settings to each core's generated config: `config` merges at the
top level, `account` into every account, and `accounts` its n-th entry into the n-th account
(for the old core the n-th key of its `api-keys` block), for settings such as a weight. A
`null` removes a value, such as `{"account": {"models": null}}` for an account without a models
list.

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

- A WebSocket client's observation has the handshake reply's `status` and `headers`, the
  `messages` it read (each parsed when it is JSON) and, when the core closed the socket, `close`
  with its `code` (1006 when the connection ended without a close message) and `reason`. A
  WebSocket the fake upstream accepted has, in place of `body`, `websocket`: the `messages` the
  core sent and how the core closed it (`close`, or `"open"` if it was still open 5 seconds after
  the client's script ended). `Sec-WebSocket-Key` and `Sec-WebSocket-Accept` change on every
  run and are not compared.
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

## Management API cases

The two cores manage accounts through different APIs, so a case about one of their account calls
sends each core its own request. Both cores run with the management API on and the corpus
management key `corpus-management-key` (not a secret) when a request has `"auth":
"management-key"`; the new core's `management_key` and the old core's `management.secret-key`
are set to it.

- `client` goes to the new core. `old_client`, when set, goes to the old core instead, in the
  same schema. `old_client_more` lists further requests the old core gets after it, in order, one
  reply each.
- In the old core's requests, `{{upstream}}` stands for the fake upstream's base URL, and
  `{{auth_index}}` for the `auth_index` the old core gives its one auth file (a hash of the file's
  path, so it changes every run; the runner reads it from the old core's `GET
  /v0/management/auth-files`). The old core's `POST /v0/management/api-call` relay needs both: it
  calls an absolute URL and puts the account's token where a header or the body says `$TOKEN$`.
- The old core's reply to `old_client` is the case's `client`; the replies to `old_client_more`
  are `client_more`, which the new core's observation does not have.

`account-quota/<provider>/usage.json` compares the new core's `GET
/v0/management/accounts/{id}/quota` with the quota requests today's panel makes through the
relay (`extensions/panel/src/features/quota/`). Today's core fetches no quota on its own; the old
core's `old_client` replays the panel's requests for the provider, in the order the new core makes
its own, with the panel's URLs moved to the fake upstream. What is compared is the upstream
requests: method, path, headers, body and the token. Only these are compared, not the panel's
reading of the replies, which is TypeScript in the panel. The client's replies differ by design (the
relay's wrapper against the new quota shape), and the new quota is pinned in the case. Each
provider's client identity headers differ by design as well: the quota fetchers use the provider's
own client identity, not the panel's pinned one. The Meta extension honours a `mint_url` setting,
where its quota call goes, only when core runs with `PLUXY_TEST_LOGIN_HOSTS=1`; the runner sets it
for a case that calls the management API, and the Meta account's `mint_url` points at the fake
upstream. Devin has no quota case: its new call and the panel's differ in encoding (protobuf against
JSON), so every part of them would be an exception, and today's core makes its own protobuf
`GetUserStatus` call only as a background refresh, which the runner cannot trigger and answers
aside.

`account-models/<provider>/catalog-list.json` compares the new core's `GET
/v0/management/accounts/{id}/models` with the old core's `GET
/v0/management/auth-files/models?name=<auth file>` for the same login. Model ids, display names and
owners must be equal and in the same order; the fields only one list has are exceptions, each
pinned or covered by a `*` path.

## Adding cases

1. Write the case file without `recorded_old_core`. Take the behaviour from today's core: its
   tests, code or an issue, and name it in `source`.
2. Record it: `PLUXY_CORPUS_RECORD=1 PLUXY_CORPUS_CASE=<name> go test -count=1 -run TestCorpus ./corpus`.
   Recording also lays out the file in the corpus style (one stream event per line).
3. Run the corpus. For each difference the report lists, fix the new core or add an exception
   with its reason.

A provider whose upstream speaks Connect with protobuf messages (Devin's API server) scripts its
replies as `connect` frames: each is `{"message": {...}}`, a data frame, or `{"end": {...}}`, the
end frame with its JSON trailer (`{}` for a clean end). A message is written in JSON keyed by field
number: a string is a text field, a number a varint, an object a nested message, `{"double": x}`
a 64-bit float, `{"fixed32": n}` a 32-bit fixed value, `{"bytes": "base64"}` bytes that are not
text, and an array a repeated field. The provider's `Proto` schema (`cores.go`) names its request
message's nested and repeated fields, so a recorded protobuf request is decoded into the same JSON
form and compared like any body (`connect.go`).

A provider whose core calls the upstream on its own, outside the case (today's core probes an
Antigravity login's models when it loads it and again later), sets `Aside` in its entry: the fake
upstream answers those calls with a fixed reply and neither records them nor uses up a scripted
reply.

A new provider needs an entry in `Providers` (`cores.go`): how each core's config points that
provider at the fake upstream (for the old core, its `api-keys` block, or auth files with
`OldCoreAuthFiles`, and `Intercept` for a fixed upstream host). A new extension also goes in
`NewCoreExtensions`.

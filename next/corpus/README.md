# Differential test corpus

The corpus checks pluxy against CLIProxyAPI at the HTTP level. Each case sends one client request
to pluxy, whose provider points at a fake upstream server. The fake upstream records every request
it gets and answers with scripted replies. The runner then compares what pluxy did (the reply the
client got and the requests that reached the upstream) with what CLIProxyAPI did for the same case,
which the case file holds as `recorded_old_core`. Why the corpus exists and the bar it sets are in
[docs/corpus.md](../../docs/corpus.md).

pluxy must match the recording except where a case lists an **exception**. Each exception gives a
reason, so every difference is on the record.

The recordings come from live runs of the copy of CLIProxyAPI that this repository held until
pluxy replaced it, in `core/` (the last commit that changed it is `d62c5bbf1`). That copy is no
longer in the repository and nothing here builds or runs it.
Code comments, this file's field names and the cases' `source` fields call it "the old core" or
"today's core", and its files `core/...`: those paths are files of CLIProxyAPI's source, which you
cannot open here. The recordings are the reference and are never re-recorded.

## Running it

From `next/`:

```bash
go test -count=1 -v -run TestCorpus ./corpus
```

This builds pluxy and compares it with CLIProxyAPI's replies recorded in the case files.
The report is printed at the end (with `-v`). It is fast enough for every CI run, and CI runs it in
`next-ci.yml`.

| Variable | Effect |
| --- | --- |
| `PLUXY_CORPUS_REPORT=path` | Also write the report to a file. |
| `PLUXY_CORPUS_CASE=regexp` | Run only cases whose `format/provider/name` matches. |
| `PLUXY_CORPUS_DUMP_NEW=dir` | Write each of pluxy's observations to a file in `dir`, to read the values an exception's `want` needs. |
| `PLUXY_CORPUS_LOGS=1` | With `-v`, log the core's output. |

### The recorded replies are the reference

The corpus has no mode that runs CLIProxyAPI, and nothing in it writes `recorded_old_core`. A case
file's recording is never edited by hand and never regenerated. A case with no `recorded_old_core`
fails. When pluxy is meant to differ from a recording, the case lists an exception with its
reason; see [Adding cases](#adding-cases) for a new case.

## How pluxy is run

pluxy runs as a separate program on a random local port, with a fresh temporary folder
holding its config and its `HOME`. Its environment is cleared except for `PATH` and `TZ=UTC` (the
zone the recordings were made in), and outbound proxies are set to a closed local port, so its
background requests cannot reach the internet. The fake upstream is on loopback, which is never
proxied.

The generated config is the same for every case:

- one provider account pointed at the fake upstream, with the API key `sk-Cq7Vx2Np9Lw4Zr8`
  (not a secret); a case with `accounts` above 1 gets more accounts, keyed
  `sk-Cq7Vx2Np9Lw4Zr8-2`, `sk-Cq7Vx2Np9Lw4Zr8-3` and so on, all pointed at the same upstream.
  The fake account secrets hold no words: pluxy removes from an error every run of text
  that shares 4 characters with one of the account's secrets, so a word in a fake key would
  vanish from the error messages the cores are compared on;
- the client key `corpus-client-key`;
- routing that never waits: pluxy runs its default routing, which never waits, and still
  moves a request on to another account within one round;
- a management key the case never sends, unless the case calls the management API (see
  [Management API cases](#management-api-cases)): core always has one;
- the models the account lists: the request's model (`account_models` in a case overrides this).
  A provider marked `CatalogModels` in `cores.go`, one CLIProxyAPI took from auth files, lists
  none, so its extension serves its own catalog.

The client sends no `Accept-Encoding`, so replies are observed as the core sent them.

CLIProxyAPI's replies were recorded on the same setup. It ran with the same account and keys, no
extra retry rounds (`request-retry: 0`), no waits (`max-retry-interval: 0`) and no cooldowns, the
bundled panel off, management off unless the case called a management API, response compression
off, its embedded model catalog (`-local-model`), and the request's model listed for the account
(it served only listed models). It took a provider from its `api-keys` block or from auth files,
as the table below says.

Providers with their own folder of cases, with what CLIProxyAPI ran when each was recorded:

| Provider | pluxy | CLIProxyAPI (as recorded) |
| --- | --- | --- |
| `openai-compatible` | OpenAI-compatible extension, `wire_api` chat | an `openai-compatibility` entry |
| `openai-responses`, `anthropic`, `gemini` | OpenAI-compatible extension, `wire_api` responses, messages or gemini | a Codex API key without the Codex identity, a Claude API key, a Gemini API key |
| `codex` | Codex extension, API key | a Codex API key (Codex identity on) |
| `codex-oauth` | Codex extension, a ChatGPT subscription's OAuth credential | a Codex auth file |
| `codex-siwc` | Codex extension, the OAuth credential a Sign in with ChatGPT login produces (`siwc` in its data) with `base_url` | a Codex auth file with a nested `siwc` token set and `use_siwc`. CLIProxyAPI sent a non-Codex client's requests to `https://api.openai.com/v1` whatever the file said, so it ran through an intercepting proxy (below) |
| `gemini-api-key` | Gemini extension, API key | a Gemini API key |
| `interactions` | Gemini extension, API key, `"interactions": true` in the account's settings | an `interactions` group with `base-url` and an API key |
| `vertex-api-key` | Gemini extension, Vertex settings and an express mode key | a `vertex` API-key entry |
| `vertex` | Gemini extension, a Vertex service account | a Vertex service account auth file |
| `kimi` | Kimi extension, a Kimi Code OAuth credential | a Kimi auth file |
| `meta` | Meta extension, a Muse Code login's OAuth credential (the minted key and its device token) | a Meta auth file with the key and `base_url` |
| `meta-api-key` | Meta extension, an API key | a `meta` group with `base-url` and an API key |
| `devin` | Devin extension, a session token as an API key credential | a Devin auth file with the token and `base_url`; its `GetUserStatus` calls were answered aside, and pluxy makes none. Models are named `devin/<model>`, as CLIProxyAPI lists them; Gemini clients write the slash as `%2F` in the path, with `account_models` naming the model |
| `xai` | xAI extension, an API key | an `xai` group with `base-url` and an API key |
| `xai-cli` | xAI extension, a Grok login's OAuth credential with `base_url` | an xAI auth file with `auth_kind` `oauth`, an expiry far ahead and `base_url` |
| `codex-ws` | Codex extension, API key, `"websockets": true` in the account's settings | a Codex API key with `websockets: true` |
| `claude-oauth` | Claude extension, a subscription's OAuth credential with `base_url` | a Claude auth file with an access token expiring far ahead. CLIProxyAPI sent a subscription's requests to `api.anthropic.com` whatever the file said, so only the management cases (below) reached the fake upstream with this provider |

CLIProxyAPI sent a ChatGPT subscription's requests to `https://chatgpt.com/backend-api/codex`,
which no setting moved. For `codex-oauth` it was therefore run behind an HTTP proxy
(the auth file's `proxy_url`) that accepted `CONNECT` only to `chatgpt.com:443`, ended the TLS
connection itself with a certificate from a test authority made for the run, and handed the
requests to the fake upstream; a `CONNECT` to any other host was refused and failed the case, so
nothing reached the internet. CLIProxyAPI renewed a
subscription's token at `auth.openai.com` on a 401, which the corpus does not fake (no
`codex-oauth` case answers 401). The fake tokens are unsigned JWTs that expire in 2100, so neither
pluxy nor CLIProxyAPI renews them. The subscription cases use `gpt-6.1-sol`, because CLIProxyAPI served a
subscription only the models in its catalog.

A Vertex service account was recorded the same way: CLIProxyAPI always called
`https://aiplatform.googleapis.com` and traded the account's signed JWT for a token at
`https://oauth2.googleapis.com/token`, so both hosts went through the proxy, and each `vertex`
case's first upstream reply is the token endpoint's. pluxy's account points `base_url`
and the key's `token_uri` at the fake upstream, and pluxy runs with
`PLUXY_TEST_LOGIN_HOSTS=1`, without which the Gemini extension takes only Google's token
endpoint. The key is an RSA key made for the run; the JWT
body is volatile. These cases use `gemini-2.5-pro`, a model in CLIProxyAPI's catalog.

## Case files

One file per case: `cases/<client format>/<provider>/<name>.json`. `cases/corpus.json` holds
settings for every case.

```json
{
  "description": "What behaviour this pins down.",
  "source": ["core/... CLIProxyAPI test or code the behaviour comes from"],
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
    {"path": "client.headers.Retry-After", "kind": "gap", "reason": "Why pluxy differs here."}
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

`old_client` and `old_client_more`: what CLIProxyAPI was sent when its reply was recorded; see
[Management API cases](#management-api-cases). Nothing runs them now.

`account_models`: models the account lists in pluxy (CLIProxyAPI listed them too when its
reply was recorded; see above).

`new_core_config`: settings added to pluxy's config, by top-level key, for a behaviour
CLIProxyAPI had by default and pluxy has only when configured (CORS, for example). CLIProxyAPI's
replies were recorded with its usual config.

`accounts`: how many provider accounts pluxy has (default 1; CLIProxyAPI had as many when
its reply was recorded). CLIProxyAPI ordered equal accounts by an ID hashed from the upstream
URL, which changed from run to run, so multi-account cases mark
`upstream[*].headers.Authorization` volatile, or set `label_accounts`: each account's key in the
upstream requests' headers becomes `account-1`, `account-2` and so on, numbered in the order the
accounts were first used, which compares the pattern of turns.

`new_core` adds settings to pluxy's generated config: `config` merges at the top level,
`account` into every account, and `accounts` its n-th entry into the n-th account, for settings
such as a weight. A `null` removes a value, such as `{"account": {"models": null}}` for an account
without a models list. `old_core` holds the same kind of settings for CLIProxyAPI's config when
its reply was recorded; nothing runs it now.

`volatile`: paths whose values change between runs (timestamps, trace IDs). They are replaced by
`"(volatile)"` before any comparison.

`exceptions`: known differences. Each has:

- `path`: an observation path. Without `want` or `absent` it covers only the difference at that
  exact path, and only between plain values (not objects or arrays). Replacing or dropping an
  object or array needs `want` or `absent`.
- `kind`: `intended` when pluxy differs on purpose (a decision is on record), or `gap` when
  it does not match yet and the difference is still to be settled.
- `reason`: why. Required.
- `want` (optional): the value pluxy must give at `path`, in full, so an exception pins pluxy's
  behaviour as well as excusing the difference. With `want` the exception also covers the
  differences below `path`, since nothing under it can be anything else. For a reply pluxy
  replaces (a different status, an error body instead of data) pin `status` and `body` each.
- `absent` (optional, instead of `want`): pluxy must give no value at `path`.

An exception that covers no difference fails the case, so the list cannot go stale. Global
exceptions in `cases/corpus.json` apply to every case and fail only if they cover nothing in the
whole corpus. A global `want` or `absent` is checked in the cases where the exception covers a
difference.

The test run also corrupts each value of each case's observation of pluxy in turn and fails if the
case still passes, so an exception cannot hide a difference it was not written for.

`recorded_old_core` is CLIProxyAPI's reply. Do not edit it by hand or regenerate it.

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

pluxy and CLIProxyAPI managed accounts through different APIs, so a case about one of their account
calls sent each its own request. pluxy runs with the management API on and the corpus
management key `corpus-management-key` (not a secret) when a request has `"auth":
"management-key"`; its `management_key` is set to it. CLIProxyAPI ran with its
`management.secret-key` set to the same key.

- `client` goes to pluxy. `old_client`, when set, is what CLIProxyAPI was sent instead,
  in the same schema, and `old_client_more` lists further requests it was sent after that, in
  order, one reply each. Nothing sends them now; they stay in the case files as the record of how
  the reply was recorded.
- In CLIProxyAPI's requests (and in any request's `body_text`), `{{upstream}}` stands for the fake upstream's base URL, and
  `{{auth_index}}` stood for the `auth_index` CLIProxyAPI gave its one auth file (a hash of the
  file's path). CLIProxyAPI's `POST /v0/management/api-call` relay needed both: it calls an
  absolute URL and puts the account's token where a header or the body says `$TOKEN$`. Only
  `{{upstream}}` is expanded now, in a `body_text` sent to pluxy.
- CLIProxyAPI's reply to `old_client` is the case's recorded `client`; the replies to
  `old_client_more` are the recorded `client_more`, which pluxy's observation does not have.

`account-quota/<provider>/usage.json` compares pluxy's `GET
/v0/management/accounts/{id}/quota` with the quota requests CLIProxyAPI's Management Center made
through the relay (`extensions/panel/src/features/quota/` in this repository at commit `d0f7af2ff`,
the copy of the Management Center it held; the cases' `source` fields write it `d0f7af2ff:<path>`).
CLIProxyAPI fetched no quota on its own; its `old_client` replayed the panel's requests for the
provider, in the order pluxy makes its own, with the panel's URLs moved to the fake upstream. What is compared is the upstream
requests: method, path, headers, body and the token. Only these are compared, not the panel's
reading of the replies, which is TypeScript in the panel. The client's replies differ by design (the
relay's wrapper against pluxy's quota shape), and pluxy's quota is pinned in the case. Each
provider's client identity headers differ by design as well: the quota fetchers use the provider's
own client identity, not the panel's pinned one. The Meta extension honours a `mint_url` setting,
where its quota call goes, only when core runs with `PLUXY_TEST_LOGIN_HOSTS=1`; the runner sets it
for a case that calls the management API, and the Meta account's `mint_url` points at the fake
upstream. Devin has no quota case: pluxy's call and the panel's differ in encoding (protobuf against
JSON), so every part of them would be an exception, and CLIProxyAPI made its own protobuf
`GetUserStatus` call only as a background refresh, which the runner could not trigger and answered
aside.

`account-models/<provider>/catalog-list.json` compares pluxy's `GET
/v0/management/accounts/{id}/models` with CLIProxyAPI's `GET
/v0/management/auth-files/models?name=<auth file>` for the same login. Model ids, display names and
owners must be equal and in the same order; the fields only one list has are exceptions, each
pinned or covered by a `*` path.

`vertex-import/vertex/*.json` compares pluxy's `POST /v0/management/vertex/import` with
CLIProxyAPI's route of the same path. Both were sent the same request, so there is no `old_client`: a
multipart body written as `body_text` with a fixed boundary in the `Content-Type` header. A request's
`body_text` may hold two placeholders: `{{upstream}}`, the fake upstream's base
URL (the key's `token_uri` is `{{upstream}}/token`), and `{{private_key}}`, the RSA key the runner
makes for the run (`corpusRSA`), as the inside of a JSON string, so no case holds a key. CLIProxyAPI
saved the key without contacting Google; pluxy fetches a token at the key's
`token_uri`, so a success case scripts the token reply as its first upstream reply and pluxy's
request for it is an exception, as is pluxy's reply (the account in place of the saved
file's path, which is volatile) and pluxy's management error shape. Cases where the key is
refused by the token endpoint, a login is repeated or the account is in the config have no
counterpart in CLIProxyAPI and are in `e2e/verteximport_test.go`.

## Adding cases

No case can be recorded from CLIProxyAPI any more. A new case needs a `recorded_old_core` that
already exists:

1. Write the case file. Take the behaviour from CLIProxyAPI (its tests, code or an issue) and
   name it in `source`.
2. Give it CLIProxyAPI's reply from an existing recording: the `recorded_old_core`
   of an existing case whose request and scripted upstream replies are the same as the new
   case's. Copy it unchanged, keep the corpus layout (one stream event per line) and say in
   `description` which case it comes from.
3. Run the corpus. For each difference the report lists, fix pluxy or add an exception
   with its reason. Where pluxy is meant to differ, `kind` is `intended` and `want` (or
   `absent`) pins pluxy's value, as described above.

A behaviour CLIProxyAPI never had has no reply to compare with, so it is not a corpus case: pin it
in a Go test beside the code, as `e2e/verteximport_test.go` does for the cases it lists.

A provider whose upstream speaks Connect with protobuf messages (Devin's API server) scripts its
replies as `connect` frames: each is `{"message": {...}}`, a data frame, or `{"end": {...}}`, the
end frame with its JSON trailer (`{}` for a clean end). A message is written in JSON keyed by field
number: a string is a text field, a number a varint, an object a nested message, `{"double": x}`
a 64-bit float, `{"fixed32": n}` a 32-bit fixed value, `{"bytes": "base64"}` bytes that are not
text, and an array a repeated field. The provider's `Proto` schema (`cores.go`) names its request
message's nested and repeated fields, so a recorded protobuf request is decoded into the same JSON
form and compared like any body (`connect.go`).

A provider whose core calls the upstream on its own, outside the case (for example, fetching an
account's model list when loading it), sets `Aside` in its entry: the fake
upstream answers those calls with a fixed reply and neither records them nor uses up a scripted
reply.

A new provider needs an entry in `Providers` (`cores.go`): how pluxy's config points that
provider at the fake upstream, and `CatalogModels` when its accounts list no models. A new
extension also goes in `NewCoreExtensions`.

### Codex client catalog

The cases in `codex-models/codex/` send `GET /v1/models?client_version=...` to pluxy with
the same account models CLIProxyAPI had when its reply was recorded. The runner preserves the
query string.
The recorded replies cover the current and legacy reasoning levels. An unknown model is omitted
by pluxy's catalog, while CLIProxyAPI synthesized it from a default template; that intended
exception pins an empty catalog to avoid claiming capabilities for an unknown model. Catalog
requests bypass routing and usage recording, so no account trace header is added.

# next: the new pluxy core

A new core for pluxy, built from scratch beside today's `core/`. Core receives client requests and
hands them to **provider extensions**: separate programs, one per kind of upstream, that core
starts and talks to over a local socket. Today it serves OpenAI Chat Completions, OpenAI
Responses, Anthropic Messages and Gemini GenerateContent (streamed and not, over HTTP) through the
OpenAI-compatible API-key provider, which talks to an upstream that speaks any of them. A request
in the upstream's own format passes through unchanged; a Responses, Messages or Gemini request to a
Chat upstream, a Messages or Gemini request to a Responses upstream, and a Gemini request to a
Messages upstream are translated, and what the translation could not carry is logged. The
[Codex provider](#codex) serves OpenAI's Codex models from an API key, a ChatGPT subscription or a Sign in with ChatGPT (SIWC) sign-in.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/pluxy` | The core program. |
| `protocol/` | The extension protocol: [README](protocol/README.md), [tutorial](protocol/TUTORIAL.md), wire types and framing. |
| `llm/` | The internal model of a request and its streamed reply, shared by core and extensions. |
| `sdk/` | Go SDK for writing extensions. |
| `conformance/` | The conformance kit extensions run in their tests. |
| `format/` | Client and upstream formats as libraries extensions build in: `sse`, `openaichat`, `openairesponses`, `anthropicmessages`, `geminigenerate`. |
| `extensions/openai-compatible` | Provider extension for OpenAI-compatible APIs with an API key. |
| `extensions/codex` | Provider extension for OpenAI's Codex models: an OpenAI API key, a ChatGPT subscription (sign-in in a browser or with a device code) or a Sign in with ChatGPT (SIWC) sign-in, with a fake OpenAI login server and Codex endpoint for tests (`codextest`). |
| `extensions/claude` | Provider extension for Anthropic's Claude, with an API key or a Claude subscription login; `claudetest` is a fake Anthropic for its tests. |
| `extensions/gemini` | Provider extension for Google's Gemini: the Gemini API with an API key, and Vertex AI with a service account or an express mode key; `geminitest` is a fake Google for its tests. |
| `extensions/kimi` | Provider extension for Kimi Code, Moonshot AI's coding subscription, signed in with a device code; `kimitest` is a fake Kimi for its tests. |
| `extensions/xai` | Provider extension for xAI's Grok models: an API key, or a Grok account signed in as the Grok CLI signs in; `xaitest` is a fake xAI and xAI sign-in for its tests. |
| `extensions/meta` | Provider extension for Meta's Muse models: a Muse Code subscription signed in with a device code, or a Meta API key; `metatest` is a fake Meta for its tests. |
| `extensions/antigravity` | Provider extension for Google Antigravity: Gemini and Claude models through a Google account signed in to Antigravity; `agtest` is a fake Google login and Cloud Code for its tests. |
| `extensions/devin` | Provider extension for Devin: the models a Devin account gets, signed in as Devin's CLI signs in or with a session token, over Devin's protobuf API; `devintest` is a fake Devin for its tests. |
| `examples/scripted` | The tutorial's example extension. |
| `examples/oauth` | An example provider that signs in by device code, browser (PKCE) or API key and renews its tokens, with a fake OAuth provider for tests (`oauthtest`). |
| `internal/host`, `internal/server` | Core's extension host and HTTP API (client formats and the management API). |
| `internal/cred` | Core's credential store and renewal of accounts added by login. |
| `internal/packages` | Registries, release archives and the record of installed packages. |
| `internal/panel` | Serves the web panel at `/panel/`, embedded in core's binary. |
| `internal/pages` | Serves extension pages on a site of their own ([extension pages](ui/extension-pages/README.md)). |
| `internal/logfile` | Core's [log files](#log-files): `main.log`, its rotation, the folder's size cap, reading lines back. |
| `internal/usage` | The [usage record](#usage-statistics): one event per request, kept in memory within limits. |
| `internal/extconfig` | Extensions' settings: the schema from the manifest, checking values, and the store core keeps them in. |
| `ui/` | The UI kit the panel and the desktop app share, the panel's browser build, and the [theme contract](ui/themes/README.md) ([README](ui/README.md)). |
| `app/` | The desktop app: the kit's second build in a Tauri window, with a tray icon and three ways to reach core ([README](app/README.md)). |
| `e2e/` | Black-box tests that drive the built core over HTTP. |
| `corpus/` | Differential test corpus: cases replayed against this core and today's `core/`, with listed exceptions ([README](corpus/README.md)). |

## Run it

```bash
go generate ./internal/panel   # builds the web panel core embeds; needs Bun (skip it for a core without the panel)
go build -o pluxy ./cmd/pluxy
go build -o extensions/openai-compatible/openai-compatible ./extensions/openai-compatible
go build -o extensions/claude/claude ./extensions/claude
```

`config.json`:

```json
{
  "listen": "127.0.0.1:8400",
  "client_keys": ["choose-a-client-key"],
  "management_key": "choose-a-management-key",
  "extension": {
    "manifest": "extensions/openai-compatible/pluxy-extension.json",
    "accounts": [
      {
        "id": "openai",
        "settings": {"base_url": "https://api.openai.com/v1"},
        "credential": {"kind": "api_key", "api_key": "sk-..."}
      }
    ]
  }
}
```

```bash
./pluxy -config config.json
```

With a `management_key` set, the web panel is at `http://127.0.0.1:8400/panel/`: core's status,
accounts, extensions and themes, the pages extensions bring and their settings. Sign in with the
management key. Extension pages are served from a listener of their own, on another site than
the panel (`extension_pages` in the config); see [extension pages](ui/extension-pages/README.md).

An account may list `models` it serves; without a list it serves the models its extension lists
for it, as today's core serves an account the models of its provider's catalog, and any model
when the extension has no list for it (the OpenAI-compatible extension has none). Accounts can rename,
exclude and prefix models, and clients list the models on `GET /v1/models`: see
[Model lists and model names](#model-lists-and-model-names). Core runs the
extension the config names and every [installed package](#packages) that is enabled, and spreads
requests over all their accounts: see [Accounts and routing](#accounts-and-routing).

An account's `settings` may set `"wire_api": "responses"` for an upstream that speaks OpenAI
Responses (`POST {base_url}/responses`), or `"wire_api": "messages"` for one that speaks Anthropic
Messages (`POST {base_url}/messages`, with the key in `x-api-key` and the client's
`anthropic-version` and `anthropic-beta` headers), or `"wire_api": "gemini"` for one that speaks
Gemini GenerateContent (`base_url` ending in `/v1beta`; `POST {base_url}/models/{model}:generateContent`
or `:streamGenerateContent?alt=sse`, with the key in `x-goog-api-key`); the default is `"chat"`. A
Responses account serves Responses, Messages and Gemini clients; a Messages account serves Messages
and Gemini clients; a Gemini account serves only Gemini clients for now.

A Chat or Responses account sends a reasoning effort the upstream may not take as the nearest one
it does, as today's core does: `"reasoning_levels"` in its `settings` lists the efforts the
upstream accepts (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`), by default `low`,
`medium` and `high`, so `xhigh` and `max` go as `high`, `minimal` and `none` as `low`, and `auto`
as `medium`. Today's core refuses `minimal` with a 400 instead.

Gemini clients call `POST /v1beta/models/{model}:generateContent` or `:streamGenerateContent`
(a stream is server-sent events, with or without `alt=sse`), and `:countTokens`.

Token counts (Anthropic's `POST /v1/messages/count_tokens` and Gemini's `:countTokens`) come from
the upstream's own count endpoint: Anthropic's for a Claude account on Anthropic's own API (base URL
`https://api.anthropic.com`, the default) or an OpenAI-compatible account with `"wire_api": "messages"`, Gemini's for a Gemini account (Gemini API or Vertex AI) or one with
`"wire_api": "gemini"`. A count from an upstream of the client's own kind comes back as the upstream sent it;
otherwise core answers with the count in the client's shape. An account whose upstream has no count
endpoint (Codex, xAI, Kimi, Meta, Devin, Antigravity, and Chat or Responses accounts), and a Claude
account whose `base_url` is not Anthropic's own (today's core draws the same line), cannot count:
core tries the model's next account, and when the count gets no answer and one of the accounts
tried could not count, it answers with its own estimate in the client's shape, as today's core
does with a tokenizer for those accounts (it counts such an account itself, so a pool that mixes
both kinds gets an estimate when the account that counts refuses). The estimate is a
heuristic written with the standard library, not a tokenizer: it splits the text the way OpenAI's
`o200k_base` tokenizer does (today's core uses that one for Claude, xAI and Meta accounts) and
counts each piece by its length. On English prose, code and JSON it comes within a few percent of
`o200k_base`, but a given request can be off by more. It is the same number whichever account the
request would have gone to, where today's core's number depends on the account's provider. It
counts the system text, each message's role and text, reasoning, tool calls and results, the
tools' names, descriptions and parameters, and the tool choice; images and files add nothing.
`token_count_estimate` (top level, default `true`) turns the estimate off: core then answers an
error saying the account cannot count. When every account tried can count, an error from an
upstream that counts (a 400, 401 or 429) stays an error.

A model name may end in a thinking setting in brackets, as today's core reads it: a level
(`gpt-5(high)`; `minimal`, `low`, `medium`, `high`, `xhigh` or `max`), `none`, `auto`, or a token
budget (`claude-sonnet-4-5(16000)`; `0` is none, `-1` auto). Core takes it off the name before
routing, so the request goes to the model's own accounts, and the account's extension applies it to
the upstream request in place of any reasoning or thinking settings the request has: as a
reasoning effort (a budget becomes the level it falls in: up to 512 tokens minimal, 1024 low, 8192
medium, 24576 high, more xhigh), or for Claude and Gemini models that take a budget, as that
budget. Bracketed text that is none of these stays part of the model name; today's core refuses it.

Responses clients may also open a WebSocket on `GET /v1/responses` ([OpenAI's WebSocket
mode](https://developers.openai.com/api/docs/guides/websocket-mode)): each `response.create`
message is a turn, answered with that turn's stream events, one message each. When the chosen
account's extension holds sessions (protocol 0.1, such as a Codex account with `"websockets":
true`), the socket's turns go to one session on that account, so a turn can continue the previous
response with `previous_response_id`, and later turns stay on that account. On any other account
each turn is a request that carries the whole conversation so far, which core keeps for the
socket. `response.append`,
`response.interrupt` and warmups (`"generate": false`) are served as today's core serves them.

The Codex CLI sends its requests to `POST /backend-api/codex/responses` and opens its WebSocket on
`GET /backend-api/codex/responses` when its `chatgpt_base_url` points at core. As in today's core,
both paths are aliases of `/v1/responses` and its WebSocket: the same handlers and the same client
key check.

When an upstream ends a Responses stream with a `response.completed`, `response.incomplete` or
`response.failed` whose `output` is empty, core fills it with the items the stream's
`response.output_item.done` events finished, as today's core does.

A Messages client's usage carries `output_tokens_details.thinking_tokens` (at most `output_tokens`)
whenever the upstream reports its reasoning tokens; today's core writes it only for a Responses
upstream.

OpenAI's legacy text completions (`POST /v1/completions`) are served as Chat Completions, as
today's core serves them: the prompt (a string, or a list of one string) becomes one user message,
with `max_tokens`, `temperature`, `top_p`, `frequency_penalty`, `presence_penalty`, `stop`,
`stream`, `logprobs`, `top_logprobs` and `echo`, and the reply comes back as `text_completion`
choices with `text`. Several prompts in one request are refused; `n`, `suffix`, `best_of` and the
rest are left out.

Clients send the client key as a bearer token or, as Anthropic clients do, in `x-api-key`; Gemini
clients may also send it in `x-goog-api-key` or the `key` query parameter. Today's core takes
the key in the `key` and `auth_token` query parameters on every route; here only the Gemini routes
take `key`, since some Gemini clients send it there (n8n's Gemini credential does), and a key in a
URL ends up in logs and browser history. `client_keys` must hold
at least one key: core does not start on a config without one, and a reload or a management
change that would leave none is refused, keeping the running config. Today's core serves anyone
who reaches it when it has no client keys.

The example config's client keys (`your-api-key-1`, `your-api-key-2` and `your-api-key-3`) are
public, so as in today's core (its example API-key safe mode) core starts with one in
`client_keys` but refuses every client request, with a 403 whose code is `unsafe_example_api_key`,
until none is left; it logs that at every load, and the management API stays open, so the keys
can be replaced there. The refusal goes to a request that carries a configured key; a request
without one gets the 401 it always gets, where today's core answers every request with the 403.

Web pages may call the client API from a browser only from the origins listed in
`cors.allowed_origins` (each `scheme://host[:port]`, or `"*"` for every origin): core then adds
the CORS headers to their replies and answers their preflight `OPTIONS` requests with 204 before
checking the client key. The list is empty by default, so no page can. Today's core allows every
origin on every route; here CORS covers the client API only, never the panel or the management API.

```json
{"cors": {"allowed_origins": ["http://localhost:5173"]}}
```

With `tls.enable`, core serves `listen` over HTTPS, with HTTP/2 or HTTP/1.1, using the
certificate in `tls.cert` (PEM, the certificate first, then its chain) and its private key in
`tls.key` (PEM); relative paths are relative to the config file. Core does not start when the
files do not load. It looks at the files at most once a second while connections arrive and,
when they change, serves the new pair from the next connection on, so a renewed certificate
needs no restart; while the files do not make a pair (one written, the other not yet) it keeps
serving the one it has. Turning TLS on or off, or pointing it at other files, needs a restart.
Today's core reads the files once at start. The extension pages listener stays plain HTTP: the
[Mixed Content](https://www.w3.org/TR/mixed-content/) rules let an HTTPS page embed a page from a
[potentially trustworthy](https://www.w3.org/TR/secure-contexts/) address, which `127.0.0.1`
always is and `localhost` is where the browser resolves it only to loopback; those are the
default pages sites. For a panel reached by another name, put the site `extension_pages.url`
names behind HTTPS of your own.

```json
{"tls": {"enable": true, "cert": "certs/fullchain.pem", "key": "certs/privkey.pem"}}
```

Core's log lines about a request name where it came from: the address of the connection, or,
when that is one of `trusted_proxies` (IP addresses and CIDR ranges of the proxies in front of
core), the client the proxy names. In `X-Forwarded-For` (all its lines, as one list) that is the
last address that is not a trusted proxy, read from the right, so an address a client wrote in
itself is skipped; without that header, the first line of `X-Real-IP`. A header with an entry that is not an IP
address is not used. The list is empty by default, so no header counts, as in today's core; a
reload applies a change, where today's core needs a restart.

```json
{"trusted_proxies": ["127.0.0.1", "10.0.0.0/8"]}
```

A reply to a request that went to an account carries `X-CPA-TRACE-ID`, as today's core sends it:
the time and a random part, such as `20261109061500-3f9a0c2e5b7d4e18`. Core's log lines about the
request (a failure, what translation lost) carry the same ID and the account, so a reply can be
matched with them. A request refused before that (no client key, a body that does not parse, no
account for the model) gets none.

`drop_policy` (top level) says what happens when translating a request to the upstream's format
would drop part of it: `"report"` (the default) sends it and logs what was dropped, `"reject"`
refuses it with a 400 that names what could not be carried.

`token_count_estimate` (top level, default `true`) says what a token count gets when an account
for the model cannot count: `true` answers with core's estimate, `false` answers with an error.
Today's core has no setting for this: it always estimates for those accounts.

A config written for today's core, with its key names, works too: see
[config.yaml with today's key names](#configyaml-with-todays-key-names).

## Codex

The Codex provider extension (`extensions/codex`) serves OpenAI's Codex models from an OpenAI API
key, a ChatGPT subscription or a Sign in with ChatGPT (SIWC) sign-in (below). Build it as `extensions/codex/codex` and name its manifest:

```json
{
  "management_key": "choose-a-management-key",
  "extension": {
    "manifest": "extensions/codex/pluxy-extension.json",
    "accounts": [
      {"id": "openai", "credential": {"kind": "api_key", "api_key": "sk-..."}}
    ]
  }
}
```

A subscription is added by signing in through the management API (see
[Accounts added by login](#accounts-added-by-login)), with method `browser` or `device`; `api_key`
adds an API key the same way. A browser sign-in listens for its redirect on `localhost:1455`
when that port is free on core's machine; otherwise, or when core runs elsewhere, send the
address the browser ended on as the login's `redirect_url` input. Core renews a subscription's
tokens four days before they expire (tokens that last less than that, five minutes before).

Every request goes to `{base_url}/responses` as Codex takes it: streamed, not stored, with the
reasoning's encrypted content included, sampling settings removed, and the Codex terminal
client's `User-Agent` and `originator` (turn off with `"codex_identity": false`). Chat
Completions, Messages and Gemini requests are translated to Responses. A Messages client (such as
Claude Code) gets each reasoning item's encrypted content as its thinking block's signature, and
replaying the block sends the reasoning back, so it carries over between turns. Codex refuses IDs
and names over its limits, so, as today's core does, every request's input item IDs are made at
most 64 characters of ASCII letters, digits, `_` and `-` (an ID gets its item type's prefix
such as `msg_`, a longer one is cut to 47 characters and ends with a hash, and a reasoning item
with encrypted content and an ID over the limit is left out). A translated request also gets
call IDs over 64 bytes cut the same way, and function names made at most 64 bytes of the same
characters (an MCP name `mcp__server__tool` becomes `mcp__tool`; names that come out the same get
`_1`, `_2`); the reply gives the client its own names back. A Responses client's function names
and call IDs go as it wrote them. Three schema forms
the Codex endpoint rejects, which MCP servers' tools often use, are rewritten in every function
tool's parameters (also inside namespace tools, and in compaction requests), whichever client
sent them: a `pattern` or
`patternProperties` key with a Unicode property escape (`\p{...}`) or `\0` is removed, a numeric
bound that is a whole number outside the 64-bit integer range is removed, and a property whose
`oneOf` or `anyOf` has eight or more branches that are only distinct constants becomes an `enum`.
Today's core also turns `number` into `integer` for named tools of the Codex app; that is not built.
A Responses request (compaction too) that replays `web_search_call` items in `input` but declares
no web search tool gets `{"type":"web_search"}` added to `tools`, because the upstream refuses such
a history without one; a Responses-lite request has those items removed instead.
A request is Responses-lite, as today's core reads it, when its
`X-Openai-Internal-Codex-Responses-Lite` header is `true` or its `client_metadata` carries
`ws_request_header_x_openai_internal_codex_responses_lite` as `true` (a WebSocket client sends the
header that way); another header value is not. A Responses-lite request gets no image generation
tool and has parallel tool calls off, and a Responses client's lite request without `instructions`
goes without them rather than with `""`.
Account `settings`, all optional:

| Field | Meaning |
| --- | --- |
| `base_url` | Default `https://api.openai.com/v1` for an API key and a SIWC sign-in, `https://chatgpt.com/backend-api/codex` for a subscription. |
| `codex_identity` | Default `true`: send the Codex client's `User-Agent` and `originator`. |
| `image_generation_tool` | Default `true`: offer the hosted `image_generation` tool, as today's core does (never to a free plan or a `-spark` model). |
| `callback_listen` | Default `true`: let a browser or SIWC sign-in listen on port 1455. |
| `images` | Default `true`: serve the Images API on the account (below). |
| `signin_with_chatgpt_identifier` | Default `pluxy`: the name a SIWC sign-in gives this program, shown on the sign-in page (`agent_name_hint`) and starting its requests' `User-Agent`. Today's core calls it `codex.signin-with-chatgpt-identifier`, with the default `CLIProxyAPI`. |
| `websockets` | Default `false`: send a Responses WebSocket client's turns to `{base_url}/responses` over one upstream WebSocket per client socket (Codex's WebSocket transport), as today's core does with `websockets: true`. A socket the upstream drops is not replaced in the middle of a turn: the client gets `upstream_websocket_lost` and sends the whole conversation again. |
| `alpha_search` | Default `false` ("Enable Alpha Search"): let an API key account serve Codex's Alpha Search, as today's core does with `alpha-search: true`; a key without it is not asked, since not every upstream has the endpoint. A ChatGPT sign-in always serves it. |

**Compaction.** The Codex CLI shortens a long conversation with `POST /v1/responses/compact`
(also `POST /backend-api/codex/responses/compact`, where it sends a ChatGPT sign-in's requests when
its `chatgpt_base_url` points at core). Core routes it by its `model` to an account's extension as
an [endpoint call](protocol/README.md#endpoint); the Codex extension sends the body to
`{base_url}/responses/compact` as today's core does, nearly as it came: `stream` removed,
`instructions` set to `""` when absent (left out for a Responses-lite request), parallel tool
calls turned off on a Responses-lite request, the model renamed for the account, a thinking suffix's
effort applied, and a prompt cache key (with `Session-Id`) derived as for `/responses` when the
client names none; the upstream's JSON reply comes back to the client as it is. A request with
`"stream": true` is refused with a 400, as today's core refuses it. An account of another extension
does not serve it, so a model only those serve gets an error.

**Alpha Search.** Codex's standalone search endpoint is `POST /v1/alpha/search` (also `POST
/backend-api/codex/alpha/search`, for a ChatGPT sign-in's requests). Core routes it as an [endpoint call](protocol/README.md#endpoint)
to a Codex account: a ChatGPT sign-in always, an API key only with `alpha_search` on. The request
may name no model, and then any such account may serve it; one that names a model goes by it, as
other requests do. The extension sends the body to `{base_url}/alpha/search` as it came, but with
`prompt_cache_key` and `prompt_cache_retention` removed and the model renamed for the account
(today's core renames it only for an API key). It sends `originator: codex_cli_rs`, the bearer token, a sign-in's `Chatgpt-Account-Id`
and the client's `Version`, `User-Agent`, `Session_id` and `X-Client-Request-Id` as they came; the
upstream's JSON reply comes back to the client whole, with its content type. An upstream error
reaches the client as an error in OpenAI's shape (a 404 blocks no model on the account), and an API key without `alpha_search`, or
without a `base_url` of its own (today's core keeps no Codex API key without one), is skipped
for this endpoint only. When no account is eligible the client gets an error saying so.

**Capacity refusals.** A refusal that says the turn or the model is full, not that the account's allowance is used up, is a rate limit on that model for that account: the code `turn_rate_limit_exceeded`, `model_at_capacity` or `model_is_at_capacity`, or, with no code, an "at capacity" message from a `rate_limit_error` or about the model. Whatever status the upstream gave, the client sees a 429, core tries another account, and the account keeps serving its other models. The upstream's `Retry-After` is kept; `Retry-After: 0` (retry at once) cannot be passed on, so the model waits about a second. The same refusal inside an open stream blocks the model the same way, but the request is not tried again, as in today's core. A WebSocket `websocket_connection_limit_reached` message is an error message like any other: it keeps its status (a 429 is then a retryable rate limit on that model for the account, a 400 an invalid request), and the next turn opens a new upstream socket.

**Stream repairs.** As today's core does, on HTTP and on the upstream WebSocket alike: an output
item of a terminal `response.completed` or `response.incomplete` that has no `id` (or a null or
blank one) gets the `id` of the item `response.output_item.done` finished at the same position; and
a `response.incomplete` that produced nothing (no output delta with text, no finished item, no
output) and reports `output_tokens` of exactly `0` is an upstream that gave up silently, so it fails
with an `upstream` error (502, scope `request`, not retryable) instead of finishing the reply. The
error is not retried on another account, because the upstream may have run the request, and it puts
no cooldown on the account.

**Images.** `POST /v1/images/generations` and `POST /v1/images/edits` (OpenAI's Images API) go
to a Codex account as [endpoint calls](protocol/README.md#endpoint), routed by `model` (default
`gpt-image-2`), as today's core serves them: the body goes to `{base_url}/images/generations` or
`{base_url}/images/edits` with the model named as the upstream knows it and `stream` left out
unless it is `true`, and the upstream's reply (JSON, or server-sent events for a stream) comes back
as it is. An edit may be JSON or `multipart/form-data`; core reads a form into its JSON form, the
files as data URLs, as today's core sends it. A request without a `prompt`, or a form edit without
an image file, is refused with a 400. The image models are `gpt-image-1.5`, `gpt-image-2`,
`gpt-image-2.5`, `gpt-image-2.5-flare` and `gpt-image-2.5-sunburst`, which every Codex account
lists, as in today's core; any other model is refused. A free ChatGPT plan, and an account with
`"images": false`, does not serve them, so core tries another account.

**Sign in with ChatGPT (SIWC).** Login method `siwc` ("Sign in with ChatGPT (SIWC)") signs in to
ChatGPT as an agent other than the Codex client and adds an account whose token calls the OpenAI
API on the user's ChatGPT plan, as the `siwc` section of a Codex credential does in today's core.
It runs like the browser sign-in (a redirect to port 1455 on `127.0.0.1`, or the pasted address as
the `redirect_url` input), as OpenAI's agent client `dynamic_agent_client`, for the resource
`https://api.openai.com/v1` with the `chatgpt.tokens.use.direct` scope, which the grant must
include, naming a new device ID and `signin_with_chatgpt_identifier` as the agent's name. The code
exchange and every renewal use the client ID the sign-in result carries; core renews the tokens
five minutes before they expire. The account's requests follow today's core's SIWC rules. They go
to `{base_url}/responses` (default `https://api.openai.com/v1`) with the SIWC token and the
`User-Agent` `<identifier> (<GOOS>; <GOARCH>)`, the session (when there is one) as `session_id`
and `X-Client-Request-Id`, and none of the Codex client's headers, `Originator`,
`Chatgpt-Account-Id` or routing hint; `codex_identity` and `image_generation_tool` do not apply,
and no image tool is offered. `configuration_update` input items, which the OpenAI API refuses on
such a token, are removed and the last one's reasoning effort becomes the request's (a thinking
suffix's effort wins); `temperature`, `prompt_cache_retention`, `prompt_cache_options` and
`max_completion_tokens` are removed, and blank `instructions` are left out. Compaction goes to
`{base_url}/responses/compact` under the same rules, without `store`; a WebSocket turn goes without
`stream` and `background`. The account answers unsupported, so core tries another account, for a
request from a Codex client (a `User-Agent` naming `codex-tui`, `codex_cli_rs`, `codex-rs`,
`codex_exec` or Codex Desktop), which today's core serves with the subscription's own token, which
a SIWC account does not have; and for every endpoint but compaction, so it serves no Images API
(today's core makes a SIWC account's images through Responses, which is not built). It lists its
plan's models without the image models and has no quota to report. Binding SIWC to an existing
subscription, as today's core does through its management API (`/v8/management/oauth/siwc/*`)
and its `-siwc-bind` flag, is not built: the login method replaces it.

**Quota.** For a ChatGPT sign-in account, `GET /v0/management/accounts/{id}/quota` reads `GET {backend}/wham/usage`, where `{backend}` is the account's `base_url` without a trailing `/codex` (default `https://chatgpt.com/backend-api`), with the access token, the `Chatgpt-Account-Id` header and the same `User-Agent` and `Originator` the account's requests carry (so `codex_identity` applies). It reports the plan (`plan_type`), the 5-hour and weekly or monthly windows (`five_hour`, `weekly`, `monthly`), code review's windows (`code_review_*`), each extra limit such as one model's (`<name>_five_hour` and so on) and the credit balance (`credits`), with the share used, reset time and window length. An API key or a SIWC account answers unsupported. A sign-in's grant carries `identity` `{"email", "account_id"}`: members of one ChatGPT workspace share its account ID, so the e-mail address tells them apart.

## Claude

The Claude extension (`extensions/claude`) talks to Anthropic's Messages API with an API key or a
Claude subscription. Point `extension.manifest` at `extensions/claude/pluxy-extension.json`. An
API-key account in the config:

```json
{"id": "anthropic", "credential": {"kind": "api_key", "api_key": "sk-ant-..."}}
```

A subscription account is added by login through the management API (below), with method
`subscription`: the reply's prompt carries a sign-in URL; after signing in, send the address the
browser was sent to (or the code Claude's page shows) as `{"input": {"redirect_url": "..."}}` to
`POST /logins/{id}/input`. Method `api_key` adds an account from a key, after checking it with
Anthropic. Method `setup_token` adds an account from the long-lived token `claude setup-token`
prints, sent as `{"input": {"setup_token": "sk-ant-oat01-..."}}`: the token is stored as an
`api_key` credential, unchecked, because no call is known that checks it without spending from the
subscription, and it is never renewed (it has no refresh token). Any key that contains
`sk-ant-oat` is such an OAuth token, however it was added (this login, `api_key`, the config or a
`claude-api-key` entry in a `config.yaml`), and is sent as one, as today's core does. Core renews a
subscription's token an hour before it expires.

| Setting | Meaning |
| --- | --- |
| `base_url` | Where the Messages API is. Default `https://api.anthropic.com`; requests go to `{base_url}/v1/messages`. A token count goes to `{base_url}/v1/messages/count_tokens` only when this is Anthropic's own origin (`https` and the host `api.anthropic.com`, port 443 or none, no user info); with any other base the account cannot count and core estimates the count. |
| `client_id` | Claude's OAuth client. Default is Claude's. |
| `rebuild_mid_system_message` | `true` moves the text of each `system` message in a Messages client's conversation into the top-level `system` prompt, after what it holds, and removes those messages, for a Claude-compatible upstream that refuses them there; non-text blocks in them are left out. Default `false`: such messages go upstream where the client put them. Requests in other formats already have every system message in the top-level prompt. |

A Messages request goes to Anthropic unchanged: its body, so thinking blocks, signatures and
`cache_control` markers arrive as the client wrote them, and its `anthropic-version`,
`anthropic-beta` and identification headers (`User-Agent`, `X-App`, the `X-Stainless-*` headers of
Anthropic's SDKs, `X-Claude-Code-Session-Id`, `X-Client-App` and
`X-Anthropic-Additional-Protection`; and, only from a request that names itself as Claude Code
(`X-App: cli`, a `claude-cli/` User-Agent and the `claude-code-20250219` beta), Claude Code's
subagent, request-class and remote-session headers such as `X-Claude-Code-Agent-Id` and
`X-Claude-Remote-Session-Id`, as today's core forwards them only from Claude Code). An API key goes in `x-api-key`; a
subscription's token or a setup token goes as a bearer token, with the `oauth-2025-04-20` beta added
after the client's betas. Chat Completions, Responses and Gemini requests are
translated to Messages; their reasoning effort becomes adaptive thinking with that effort on
Claude 4.6 and later, and a thinking budget from Claude 3.7 to 4.5 (low 1024, medium 8192, high
24576 tokens); a model name the extension does not know as a Claude model gets no thinking
settings, and the effort is reported as dropped. The extension never changes how a client presents itself: a client that is
not Claude Code is not made to look like Claude Code.

Every request, a Messages client's or a translated one, also gets the betas its body needs, after
the client's own and without repeats, as today's core adds them: `advisor-tool-2026-03-01` for a
tool whose type starts with `advisor_`, `extended-cache-ttl-2025-04-11` for a `cache_control`
marker with `ttl` `"1h"` (not on a token count), and `thinking-binding-controls-2026-08-01` for
`thinking.block_binding`. A top-level `betas` list in the body, which Messages does not take there,
is moved into `anthropic-beta`. The client's own betas are never removed. Not built: today's
`anthropic-beta-strip` and `anthropic-beta-append` settings, and the Claude Code beta profile
today's core sends for clients it makes look like Claude Code.

**Tool shapes.** A translated request's tools get the shapes Messages takes, as today's core
gives them: a function name with characters other than letters, digits, `_` and `-`, or longer than
64, is sent with each other character as `_` and cut to 64 (two names that would come out the same
each get a hash of the client's name), and the reply's calls go back to the client under its own
name, in every client format (today's core maps them back for Responses clients only). A parameter
schema becomes an object schema without `anyOf`, `oneOf` or `allOf` at its root: the branches'
properties move to the root, and `allOf`'s required names too. Patterns stay as written, `\p{...}`
included, which Messages takes. Every request, a Messages client's own body included, has two parts
Anthropic refuses repaired: an empty `allowed_domains` or `blocked_domains` list on a built-in
`web_search_*` tool is removed, and a `tool_use` id or `tool_result` `tool_use_id` with other
characters becomes `cpa_tid_v1_` and the ID in URL-safe base64, the same ID the same way.

A 429 from a subscription whose usage window is used up blocks the account until the window
resets; a 529 (overloaded) moves the request to another account.

**Quota.** For a subscription account, quota reads `GET {base_url}/api/oauth/usage` and `GET {base_url}/api/oauth/profile` (default base `https://api.anthropic.com`) with the access token, the OAuth beta in `anthropic-beta` and `anthropic-version`. Like the account's requests, it presents no client identity of its own. It reports the 5-hour window (`five_hour`), the weekly windows (`seven_day`, `seven_day_oauth_apps`, `seven_day_opus`, `seven_day_sonnet`, `seven_day_cowork`, the Fable window), any window counted in dollars, and extra usage (`extra_usage`), with the share used and reset time; the plan (Free, Pro, Max, Max 5x, Max 20x or Team) comes from the profile, and usage is still reported when the profile cannot be read. A setup token's quota reads only the
usage, not the profile: such a token is made for inference and lacks the profile scope (today's core
skips its profile lookup too), so no plan is reported. An API key answers unsupported. An account
check (`check`) reads the model list for an API key; a subscription or a setup token is not checked. A subscription login's grant carries `identity` `{"email", "account_id", "organization_id"}` from the token reply, the fields today's core compares on a re-login.

## Gemini

The Gemini extension (`extensions/gemini`) talks to Google's Gemini models through the Gemini API
or Vertex AI. Point `extension.manifest` at `extensions/gemini/pluxy-extension.json`. A Gemini API
key:

```json
{"id": "gemini", "credential": {"kind": "api_key", "api_key": "AIza..."}}
```

A Vertex AI account has `vertex` in its settings and either a service account or an express mode
API key:

```json
{
  "id": "vertex",
  "settings": {"vertex": {"project": "my-project", "location": "us-central1"}},
  "credential": {"kind": "service_account", "data": {"client_email": "...@my-project.iam.gserviceaccount.com", "private_key": "-----BEGIN PRIVATE KEY-----\n..."}}
}
```

The service account's credential holds the secret part of the JSON key Google Cloud gives you
(`client_email` and `private_key`). The key's `token_uri` must name Google's token endpoint,
`https://oauth2.googleapis.com/token` (or `https://accounts.google.com/o/oauth2/token`, which
older keys name), as keys Google Cloud makes for its standard universe do; a key naming any
other endpoint is refused, since the extension sends that endpoint a signed assertion and sends
Vertex the token it gets back. Keys for Google's sovereign-cloud universes, which name their own
token endpoint, are therefore not supported. Today's core uses whatever endpoint the key names. The
whole key file works too, but core removes every string of a credential from replies, so its
project ID and other plain fields would be cut out of model output; keep them in settings. The
extension signs a JWT with the key, trades it for an access token at Google's token endpoint and
keeps the token until five minutes before it expires, or until Vertex refuses it. Accounts can also be added by login
(management API, below) with method `api_key` (a Gemini API key, checked by listing a model, or a
Vertex express key) or `service_account` (the JSON key; the account's settings must name
`vertex.project`). `POST /v0/management/vertex/import` takes the key file as a form upload and runs
that login for you (see [Accounts added by login](#accounts-added-by-login)).

| Setting | Meaning |
| --- | --- |
| `base_url` | The Gemini API (default `https://generativelanguage.googleapis.com`; requests go to `{base_url}/v1beta/models/{model}:generateContent`), or for Vertex the host that replaces Google's. |
| `vertex.project` | The Google Cloud project; default the service account key's `project_id` when the credential holds the whole key. |
| `vertex.location` | Default `global` (`aiplatform.googleapis.com`); `us` and `eu` use the multi-region hosts, any other location `{location}-aiplatform.googleapis.com`. |
| `interactions` | `true` serves the Interactions API (below) to Interactions clients. Default `false`. |

A Gemini request goes upstream as the client wrote it, except for the turn fixes below. Chat Completions, Responses and Messages requests are
translated to GenerateContent with every safety filter off (as today's core sends them); their
reasoning effort becomes a thinking level on Gemini 3 and later and a thinking budget on Gemini 2.5
(low 1024, medium 8192, high 24576 tokens, within the model's range), with thought summaries
included. A Messages client's `max_tokens` is left to the model's own limit, since Messages makes
it mandatory. Each thought signature Gemini sends reaches a Messages client as a thinking block's
signature and a Responses client as a reasoning item's `encrypted_content`; when the client replays
it, the signature goes back on the part that follows it, so Gemini 3's function calls keep their
reasoning between turns. A function call with no signature gets the value Google documents for
calls it did not issue.

**Interactions API.** Core serves Gemini's Interactions API at `POST /v1beta/interactions` (Google's
clients may send the client key in `x-goog-api-key` or `?key=`). Only accounts whose settings say
`"interactions": true` serve it, as today's core serves it only on its `interactions-api-key`
group and on Vertex accounts that opt in:

```json
{"id": "gemini-interactions", "settings": {"interactions": true}, "credential": {"kind": "api_key", "api_key": "AIza..."}}
```

A request names a `model` or an `agent` (one of Google's agents, such as Deep Research); core
refuses one that names both or neither, a `stream` that is not a boolean, and a body that is not
JSON, each with 400, and removes a `models/` prefix from the model. A request with an agent goes
to any account that serves the API. The body goes upstream as the client sent it, with the model
renamed for the account, and with the input IDs the API refuses removed as today's core removes
them: a `function_call` step takes its `call_id` as its `id`, other steps and content parts lose
their `id`, a `thought` step keeps only its signature, and one without a signature is left out.
The client's `Api-Revision` header goes with it (default `2026-05-20`). A Gemini API key sends it
to `{base_url}/v1beta/interactions`; a Vertex service account to
`/v1beta1/projects/{project}/locations/global/interactions` on the Vertex host (always `global`, as
in today's core), and an express key to `/v1beta1/interactions` (or the project path when
`vertex.project` is set). The upstream's reply comes back as it is: one JSON object, or, for
`"stream": true` (Vertex is also asked with `alt=sse`), each server-sent event with its name as soon
as it arrives. Its `usage` counts toward usage statistics.

Not served: Interactions clients on any other account (today's core translates them onto every
provider; here a request no opted-in account serves gets the usual error), a thinking suffix on the
model name (refused with 400; set `generation_config.thinking_level` instead), and today's check
that drops a thought step whose signature does not look like one Gemini issued. An
`"interactions": true` account still serves Chat Completions, Responses, Messages and Gemini
clients, through GenerateContent with the same key.
**Turn fixes.** Google refuses some conversations that clients send, so the extension changes the
turns of every GenerateContent request to the Gemini API or Vertex AI, as today's core does, a
Gemini client's own request included. A first turn from the model gets an empty user turn
(`{"role":"user","parts":[{"text":""}]}`) before it. A last turn from the model gets the same turn
after it, except a last turn of function responses, which the model answers; a function response
that carries an image or file is not answered, so it gets the user turn too. On Vertex AI, trailing
model turns made only of thoughts are dropped first (at least one turn is kept), and so are the
turns holding only a replayed thought signature that a translated request has where a client
replayed reasoning. A countTokens request gets the first fix only. A request that needs no change
is sent byte for byte. Today's core orders the first fix and the Vertex drop differently for a
stream than for a reply that is not streamed, which shows only for a request that is a single
thought turn; the extension keeps that order. Today's core leaves out a trailing assistant
message of a Chat Completions or Responses request (an assistant prefill), so its request ends
with the user's turn; this extension keeps the message and adds the user turn after it.

**Function call IDs.** A translated request names a function call and its response with the call's
ID. On Vertex AI the extension leaves the IDs out of requests from Responses and Chat Completions
clients, as today's core does: Vertex rejects the IDs of a Responses client's calls, and today's core
never writes a Chat client's tool call IDs to a Gemini upstream. The response is still matched to
its call by the function's name. A Messages client's IDs, a Gemini client's own, and every ID sent
to the Gemini API are kept.

Today's core can also relay Gemini requests through a browser tab signed in to AI Studio, which
connects to core over a WebSocket. This extension does not: the extension protocol gives a
program no listener that outlives its calls, and core stops idle programs.

**Quota and identity.** Gemini has no usage endpoint, so quota answers unsupported. A `service_account` login's grant carries `identity` `{"client_email"}`, the key's service account; an API key login has none.

## Kimi

The Kimi extension (`extensions/kimi`) serves Kimi Code, Moonshot AI's coding subscription. Point
`extension.manifest` at `extensions/kimi/pluxy-extension.json`. Accounts are added by login
through the management API (below) with method `device`: the reply's prompt carries a code and
Kimi's page to enter it on, and the login finishes once it is approved there. The sign-in is the
OAuth device authorization grant (RFC 8628) at Kimi's login host, with the public client of Kimi's
own CLI, since Kimi registers no other clients. Core renews the token five minutes before it
expires. A Kimi Code API key works too, as an account in the config; it has no login method yet:

```json
{"id": "kimi", "credential": {"kind": "api_key", "api_key": "sk-kimi-..."}}
```

| Setting | Meaning |
| --- | --- |
| `domain` | `kimi.com` (default) or `kimi.ai`, the site the account belongs to. |
| `base_url` | Where Kimi Code's API is. Default `https://api.{domain}/coding`; a trailing `/v1` is taken as the same place. |

Kimi Code takes Chat Completions at `{base_url}/v1/chat/completions`, Responses at
`/v1/responses` and Messages at `/v1/messages`; each client format goes to its own, and a Gemini
request is translated to Chat Completions. A Messages request goes with its body and Anthropic
headers as the client wrote them, less Claude Code's billing block in the system prompt, which Kimi
would read as prompt text. The other formats get what Kimi's API needs, as today's core sends
them: Kimi's names for its coding models (`kimi-k2.8` is `kimi-for-coding`, and other names lose
their `kimi-` prefix), a temperature only where Kimi takes it (1.0, or 0.6 with thinking off),
tool schemas with their `$ref`s inlined, usage asked for on Chat streams, the reasoning effort as
Kimi's `thinking` setting (low, high or max; none turns it off), the latest reasoning repeated on
each Chat tool call, and a Responses batch of function call outputs moved next to its calls. Each
request names pluxy in Kimi's device headers, with the device ID made at login (none for a key); the device's host
name, which Kimi's CLI also sends, is left out. A 429 that says the quota is used up is reported
as a used-up quota, not a rate limit.

**Quota.** For a device login, quota reads `GET {base_url}/v1/usages` and the plan from `GET {base_url}/v1/me` (`user_level_name`) on the account's Kimi Code host, with the access token and the same device headers (`X-Msh-*`, `User-Agent`) the account's requests carry. It reports each entry of `limits` (labelled by its name, else by its window length, else "Limit #n"), the usage summary ("Weekly limit") and the monthly ratios ("Monthly limit", "Monthly coding limit"). An API key answers unsupported. Kimi's device login returns no user ID, so its grant has no identity and a re-login is not checked, as in today's core.

## xAI

The xAI extension (`extensions/xai`) serves xAI's Grok models with an xAI API key or a Grok
account. Point `extension.manifest` at `extensions/xai/pluxy-extension.json`. An API key comes from
the config, with the key as the credential:

```json
{"id": "xai", "credential": {"kind": "api_key", "api_key": "xai-..."}}
```

A Grok account is added by login through the management API (below) with method `device`, as the
Grok CLI and today's core sign in: the OAuth device authorization grant with the Grok CLI's own
public client (it has no secret), at the endpoints xAI's OpenID discovery document
(`https://auth.x.ai`) names, which must be HTTPS on `x.ai`. The reply's prompt carries a code and
xAI's page to enter it on. The credential keeps the access and refresh tokens and the token
endpoint, and the account is labelled with the ID token's e-mail address. Core renews the token 5
minutes before it expires; a renewal keeps the refresh token when xAI sends no new one, and an
`invalid_grant` or revoked token asks for a new login. A token poll that fails before it was sent,
or gets a 429, is tried again while the code is good; a 5xx, or a failure after the poll was sent,
ends the login.

A Grok login's requests go to the Grok CLI's chat endpoint, `https://cli-chat-proxy.grok.com/v1`,
unless `base_url` names another address than xAI's API, as today's core routes them, and carry the
Grok CLI's identity, which that endpoint checks: `User-Agent: xai-grok-workspace/<version>`,
`X-XAI-Token-Auth: xai-grok-cli`, `x-grok-client-version`, `x-grok-client-identifier: grok-shell`
and `x-authenticateresponse: authenticate-response`. The version is today's core's fallback,
1.0.46. Today's core sends these headers only to the exact CLI endpoint address; the extension
sends them on every request of a login.

| Setting | Meaning |
| --- | --- |
| `base_url` | Where xAI's API lives. Default `https://api.x.ai/v1`; requests go to `{base_url}/responses`. For a Grok login, the default means the Grok CLI's endpoint. |
| `client_version` | Replaces the Grok CLI version a login's requests name (default 1.0.46). |

Every client format goes to xAI's Responses API. A Responses client's request passes through with
the changes listed below. Chat Completions, Messages and Gemini clients are translated to
Responses, and what the translation cannot carry is reported with the reply. An API key's
User-Agent is `pluxy-xai/<version>`, with no Grok CLI header.

What changes in a request:

- The stream flag follows the client's: a request that is not streamed is sent to xAI with
  `"stream": false`, and its reply is read as one response.
- `stop`, `previous_response_id`, `prompt_cache_retention`, `safety_identifier` and
  `stream_options` are removed. Today's core removes them on this path too; it does not record why.
- A custom tool is sent as a function tool, keeping its other fields. A function's local `$ref`
  references are inlined and its `$defs` and `definitions` are removed. A function without
  parameters gets an empty object schema. A request with more than 200 tools keeps the first 200,
  the most xAI accepts in one request.
- A custom tool call and its output are sent as a function call and a function call output.
- `store` is false unless the client set it: xAI keeps responses by default, and with
  `previous_response_id` removed a kept response is never used again. `include` asks for
  `reasoning.encrypted_content`, which carries a reasoning model's thinking to the next turn
  without stored responses.
- The `x-grok-conv-id` header is set to the request's `prompt_cache_key`, when the request has one.

Errors: 401, and a 403 whose body says the credential is bad, are authentication errors. Other 403
replies are permission errors. A 429 that says the free usage is used up is a quota error, which
holds the model back on the account for 24 hours. Other 429 replies are rate limits and keep xAI's `Retry-After`.
A 404 is a model error, and a 5xx is an upstream error.

Not built, and why:

- Namespace tools (from the Codex app) are sent as the client wrote them. Today's core folds them
  into functions and restores the names in the reply. That needs the reply side, which this
  extension does not have yet.
- An `apply_patch` custom tool is not wrapped in an `input` field, and its call is not restored
  on the way back. Today's core does both, so a Codex `apply_patch` tool does not work through this
  extension yet.
- Today's core sends an empty `instructions`, a string input as a list, and tool defaults
  (`tool_choice`, `parallel_tool_calls`, `strict`, `additionalProperties`) on every Responses
  request, through its Codex translator. These are not rules about xAI, so this extension does not
  send them. Whether xAI needs any of them is not verified: the tests do not reach xAI.
- A session ID for `prompt_cache_key` and `x-grok-conv-id` when the client sends none. Today's
  core derives one. This extension has no session identity to derive it from.
- Checking the reasoning effort against each model's levels, and a default effort. Both need
  today's model catalog.
- Image references, encrypted reasoning content, websockets, text to speech, image and video
  generation, `/responses/compact`, and the optional `x_search` tool.
- For a Grok login: reading the Grok CLI's latest version from npm every 3 hours (set
  `client_version` when xAI asks for a newer CLI), the file name and legacy-file adoption today's
  core uses for its auth files, and opening the browser at sign-in.

**Quota.** A Grok login's quota is what the Grok CLI shows: `GET {cli}/billing?format=credits` and `GET {cli}/billing` on the CLI's chat endpoint (the host the account's requests go to), and `GET {cli}/user?include=subscription` and `GET {cli}/settings` for the plan's name, with the login's token and the Grok CLI identity headers the requests carry. It reports the weekly credit window (`weekly`), the monthly credits (`monthly_credits`), the pay-as-you-go cap and use (`pay_as_you_go`) and the prepaid balance (`prepaid`), in `usd`. Nothing that spends from the account is sent (today's panel sent a one-token chat for paid accounts; that is not rebuilt). An API key answers unsupported. A Grok login's grant carries `identity` `{"sub", "email"}` from its ID token.

## Meta

The Meta extension (`extensions/meta`) serves Meta's Muse models on `api.meta.ai`. Point
`extension.manifest` at `extensions/meta/pluxy-extension.json`. A Muse Code subscription is added
by login through the management API (below) with method `device`: the reply's prompt carries a
code and Meta's page to enter it on. The sign-in is the OAuth device authorization grant (RFC 8628)
at `auth.meta.com` with Muse Code's own public client, as today's core signs in, since Meta
registers no other clients. The device token it yields (`dca:...`) is traded at Meta's key
endpoint (`https://api.meta.ai/muse-code/key`) for an API key; the credential keeps the key as its
access token and the device token as its refresh token. A token poll that fails before it was
sent, or gets an answer with no OAuth error that is not a 5xx, is tried again while the code is
good; a 5xx, or a failure after the poll was sent, ends the login. The key does not expire on a
timer: when Meta refuses it, core renews the credential once, which makes a new key from the
device token. A Meta API key works too, by login with method `api_key` or in the config; a `dca:` token is refused
as a key:

```json
{"id": "meta", "credential": {"kind": "api_key", "api_key": "..."}}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | Meta's API (requests go to `{base_url}/responses`). Default: where the key endpoint said, for a login; `https://api.meta.ai/v1` otherwise. |

Every client format goes to Meta's Responses API: a Responses body passes through, and the other
formats are translated to Responses. As today's core sends them, every request is streamed (a
client that wants one reply gets the stream gathered), `generate`, `prompt_cache_retention`,
`safety_identifier`, `stream_options` and `client_metadata` are removed, `store` is false unless
the client set it, `reasoning.encrypted_content` is asked for, a `web_search` tool loses
`search_content_types`, and a request with tools also carries Muse Code's own `muse` tool
namespace, which Meta wants on a subscription key's tool requests. Requests name Muse Code
(1.4.14) in their User-Agent and `X-Client-Id` and carry a new Muse session ID each. A 429 that
says the subscription quota is used up is reported as a used-up quota until Meta's `resets_at`;
a 404 holds the account back from the model until then, or for five minutes. A failure in the
stream (an `error` event or `response.failed`) is classified by the status it names, a number or a
string, and is not retry-safe once the reply began. A renewal keeps the API address the credential
holds when Meta's key endpoint names none, and a rate-limited renewal keeps Meta's `Retry-After`.

Not built, from today's core: the Codex translator's defaults it adds to every request (empty
instructions, a list input, a medium effort, `tool_choice`, `parallel_tool_calls`, `strict`
false), apply_patch rewriting for Codex Desktop, dropping encrypted reasoning Meta did not issue,
Responses compaction through Meta, and token counting.

**Quota.** Meta reports a Muse Code login's usage only from its key endpoint, so quota sends `POST https://api.meta.ai/muse-code/key` with an empty object, the device token, `x-api-version: 1.0.0` and Muse Code's client identity headers. The device token renews the account, so this goes only where a renewal goes, never to `base_url`. The call makes a new API key, which is dropped and never returned or stored; the account's own key is left alone, and if Meta retires it, core renews the credential on the next refused request (whether Meta does is not known). It reports `window` ("Usage limit", with its length) and `weekly` ("Weekly limit") from `subs_usage`, and the plan from `subs_tier_name`. An API key answers unsupported. A device login's grant carries `identity` `{"email"}`.

## Antigravity

The Antigravity extension (`extensions/antigravity`) serves the Gemini and Claude models a Google
account gets in Google Antigravity, through Google's Cloud Code API. Point `extension.manifest` at
`extensions/antigravity/pluxy-extension.json`. Accounts are added by login through the management
API (below) with method `browser`: the reply's prompt carries Google's sign-in link, and the login
finishes when the browser comes back to `http://localhost:51121/oauth-callback`, which the
extension serves on this machine while the login runs. When core runs elsewhere, or the port is
taken, paste the address the browser ended on as the prompt's `redirect_url` input.

The sign-in is Google's OAuth for installed apps with the Antigravity app's own OAuth client and
scopes, as today's core signs in, plus PKCE. Google's token endpoint also takes that client's
secret. The extension has it built in: it is the installed-app client secret of the OAuth client the
Antigravity desktop app uses, which ships inside every copy of the app, and today's core carries the
same value (`ClientSecret` in `core/internal/auth/antigravity/constants.go`). It is not a user's
credential and gives no access to an account without a sign-in. A login asks for no input, the
credential does not keep the secret, and every renewal sends it, so an account renews whether it
was logged in or imported. After signing in, the
extension asks Cloud Code for the account's Google Cloud project (`loadCodeAssist`), or has one
made on the account's default tier (`onboardUser`); the credential keeps the project too. Core
renews the token 30 minutes before it expires; a renewal also looks up a project the credential
lacks.

An account written in the config holds the same credential:

```json
{
  "id": "antigravity",
  "credential": {"kind": "oauth", "access_token": "ya29...", "refresh_token": "1//...",
    "data": {"project_id": "my-project-123"}}
}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | The Cloud Code host (default `https://daily-cloudcode-pa.googleapis.com`, the one today's core uses); the project calls go there too. |
| `user_agent` | Replaces the Antigravity app's User-Agent (`antigravity/hub/2.29.0 (aidev_client; ...)`). |
| `callback_listen` | `false` keeps the login from listening on port 51121; only a pasted address finishes it. |

A request is built as a Gemini GenerateContent body: a Gemini client's own, or a translation of
any other format with its reasoning effort as thinking settings, as the Gemini extension makes
them. It goes to `{base_url}/v1internal:generateContent` (`:streamGenerateContent?alt=sse` for a
stream) inside Cloud Code's envelope, as today's core sends it: the model, the project, request
type `agent` (`image_gen` for image models), a request ID, and a session ID hashed from the first
user text. Safety settings are left out; a Gemini model gets no output limit and has turns of
function responses sent as the model's, and a conversation that starts or ends with the model gets
an empty user turn at that end; a Claude model gets function calling in `VALIDATED` mode;
translated system instructions go as a user turn. Each function declaration's schema is sent as
`parameters`, and a response schema as `responseSchema`, in the subset Cloud Code takes, as today's
core cleans them: local `$ref`s inlined, `const` as an enum, constraints and short enums moved into
the description as hints (`minLength: 1`, `Allowed: a, b`), a type list such as
`["string","null"]` as one nullable type, and other JSON Schema keywords removed; for a Claude
model, a union becomes its strongest branch. Claude models, Gemini 3 Pro and the image model are always
asked on the stream method and gathered into one reply when the client wants one. Replies are
taken out of the envelope's `response` field. A 429 is a used-up quota when Google says
`QUOTA_EXHAUSTED` or asks for a wait of five minutes or more, and a rate limit otherwise, with the
wait from Google's retry details or message. It holds back only that model on the account, as
today's core cools only the model that answered, except a used-up credit balance
(`INSUFFICIENT_G1_CREDITS_BALANCE`), which holds back the account.

Not built, from today's core: the paid Google AI credits fallback, its own cache of Antigravity's
reasoning (thought signatures travel in band, as for Gemini), the per-account model probe
(`fetchAvailableModels`), Responses compaction through Antigravity, structured output for Claude
5.5 as prompt text, sensitive-word obfuscation (`antigravity.sensitive-words`), the connection
pool settings (`antigravity.connection-pool.*`), the app version lookup at every start, token
counting (core has no count-tokens route), and the placeholder property today's core adds to a
Claude function's objects that require nothing.

**Quota.** Quota reads `POST {base_url}/v1internal:retrieveUserQuotaSummary` with the credential's project, and the plan from `POST {base_url}/v1internal:loadCodeAssist`, with the access token and the account's Antigravity user agent. Each bucket of each model group becomes a limit, such as "Gemini models 5-hour limit", with the share used, the reset time, the window length where known and the group's models. A login's grant carries `identity` `{"email"}`.

## Devin

The Devin extension (`extensions/devin`) serves the models a Devin account gets (SWE-2, Claude,
GPT, Gemini and others), through Devin's API server, as Devin's CLI talks to it. Point
`extension.manifest` at `extensions/devin/pluxy-extension.json`. Accounts are added by login
through the management API (below):

- `browser`: Devin's sign-in, as its CLI and today's core sign in. The prompt carries the link to
  `app.devin.ai`; the browser comes back to a loopback address the extension serves on this
  machine while the login runs, or the user pastes the code Devin's page shows (or the address
  the browser ended on) as the prompt's `code` input. The code and a PKCE verifier are traded at
  `api.devin.ai` for a session token. Devin's login has no client ID or secret.
- `token`: a session token (`devin-session-token$...`) the user already has, entered as the
  `session_token` input and checked against Devin's profile endpoint.

Either way the credential is the session token, kept as an API key: it does not expire and has no
renewal, so when Devin refuses it, the user signs in again. The account is labelled with its Devin
user name. An account written in the config holds the token the same way:

```json
{"id": "devin", "credential": {"kind": "api_key", "api_key": "devin-session-token$..."}}
```

| Setting | Meaning |
| --- | --- |
| `base_url` | Devin's API server (default `https://server.codeium.com`, as today's core uses). |
| `device_seed` | Makes the device fingerprint every request carries the same each time, as today's core's `device_seed`; without it the fingerprint is random per request. |
| `callback_listen` | `false` keeps the login from listening for the browser; the user pastes the code. |

Every client format is sent as Devin's chat request, a protobuf message over Connect, to
`{base_url}/exa.api_server_pb.ApiServerService/GetChatMessage`, built as today's core builds it:
the CLI's client metadata with the token and a device fingerprint, the system prompt, the turns
(user, assistant with its reasoning, signature and tool calls, and tool results; a result for no
call goes as the user's text; images as base64 data, named in the user's text as Devin's CLI names
pasted images), the completion settings (the output limit, at most 64000; the temperature, 1 by
default), the function tools with their JSON Schema, a new session, and the model ID. The headers
are the CLI's: the token twice as Basic credentials, Connect's version, a Sentry trace, no
User-Agent and no compression. What Devin has no place for is reported as dropped (and refused
under `drop_policy` `reject`): a client's fields kept beside the model (such as a Chat message's
`name` or `seed`), parallel tool calls turned off, strict tool arguments, and parts other than
text and base64 images. A tool result given as content blocks keeps its text. A user turn left
with nothing once media Devin cannot take is dropped is refused, as today's core refuses it.

The reply's frames become the reply's events as they arrive: reasoning, text, tool calls and
usage. Reasoning stays open until the reply ends or new reasoning follows other output, so
Devin's signature, which may come after the answer began, reaches it as the reasoning's opaque
data; a signature with no reasoning text gets a reasoning item of its own. Each tool call is
found by its ID, so a call can continue after another one started, and starts once its name is
known. Arguments Devin could not make JSON (its `invalid_json_str`) are passed on as text. A
character split across frames is passed on whole.

Devin names a model and its reasoning effort as one model ID (`claude-opus-5-5-medium`). A client
names the model as today's core lists it, `devin/claude-opus-5-5` (or without the `devin/`
prefix), and the extension adds the request's reasoning effort, or the model's default, moved to
the nearest effort the model has, as today's core does; a name that already ends in an effort
(`claude-opus-5-5-high`) is sent as it is. The efforts of each model come from today's core's
Devin catalog (`models.go`); a model not in it is sent as named, with the request's effort when it
has one. A few models name their efforts as variants, as in today's core: SWE-1.7 is sent bare
unless `medium` is asked for, SWE-1.6 bare unless `fast` is, GLM-5.2 bare but for `none` and `max`
(`glm-5-2-max-1m` for the long-context model), and the Claude 4.6 models as `-thinking` with any
effort but `none`.

An error in Devin's end frame is classified by its Connect code, as today's core maps it. A 429
(or `resource_exhausted`, a demand refusal or a credit limit) holds back only that model on the
account, unless Devin says the account's daily or weekly quota is exhausted, which holds back the
account. A monthly or unnamed quota holds back only the model. Devin's `canceled` is a cancelled
request (499), as in today's core. An error after Devin sent any frame (output, usage or a
signature) is not retry-safe, and a reply that ends without its end frame is an error.

Not built, from today's core: removing Claude Code's and Codex's attribution lines from the system
prompt and `devin.sensitive-words`; rewriting tool descriptions; encoding tool call IDs (they pass
through as given); a session's turn count (each request is a new session, as today's core sends
for a client that names none); the account's plan and quota status (`GetUserStatus`) and a used-up
quota's reset time; the remote model catalog refresh; Devin's names for older models
(`claude-haiku-4-5`, `gpt-4-1`, Sonnet 4.5, Claude 4.5 Opus) and the `-fast` variants built from a
`<effort>-fast` request; the usage fallback from response dimensions; images in tool results;
signature types other than `sealed`; namespace tools and custom (freeform) tools such as
`apply_patch`, which are reported as dropped, and today's core's checks and recovery of a malformed
`apply_patch` call; and token counting (core has no count-tokens route). A replayed reasoning
signature is sent as given, whoever issued it.

Gaps outside the extension: a Messages or Gemini client gets Devin's reasoning without its
signature, because those renderers pass on only Responses and Gemini signatures, so such a client
cannot replay it. A Gemini client's `thoughtSignature` on a function call is reported as dropped;
today's core sends it to Devin as a sealed signature.

**Quota.** Quota calls Devin's `GetUserStatus` on the API host (`{base_url}/exa.seat_management_pb.SeatManagementService/GetUserStatus`, Connect with a protobuf body), as today's core does, with the session token and the client identity chat requests carry. It reports "Daily limit" and "Weekly limit" (share used, reset time) and the plan name. A login's grant carries `identity` `{"user_id"}` from `/v3/self`; a re-login whose `/v3/self` call fails has no identity and is refused for an account that stored one.

## Accounts and routing

For each request core picks an account, sends the request to the extension with it, and reads
the outcome. A refusal the extension reports as retry-safe (the upstream certainly did not run
the request, for example a 429 or a 401) moves the request on to another account; any other
failure goes to the client. Once a reply has started, nothing is retried. Extensions never
retry on their own.

An account is eligible when it serves the model, no block covers it and it is under its
concurrency limit. Refusals block what they are about, for as long as the upstream said
(`Retry-After`) or else by backoff: a rate limit or quota on one model (a plain 429, as today's
core cools only the model that answered) or on the whole credential (when the provider says the
account's allowance is used up, such as a subscription's usage window), a refused key for 30 minutes, a missing model for 12 hours, a failing endpoint with backoff up
to a minute. Results count in the order their requests started on the account: a success
clears the blocks that failures of earlier-started requests left on its account (its credential,
its model and its endpoint), and a failure that ends after a later-started request succeeded on
the same scope leaves no block and no last error. Among eligible accounts, core prefers the
session's account (with session affinity on), then the highest `priority`, then picks by
strategy.

```json
{
  "routing": {
    "strategy": "round-robin",
    "max_attempts": 0,
    "max_accounts": 0,
    "max_wait_ms": 0,
    "session_affinity": {"ttl_ms": 3600000}
  },
  "extension": {
    "accounts": [
      {"id": "a", "priority": 1, "weight": 2, "max_concurrency": 4, "endpoint": "openai", "...": "..."}
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| `strategy` | `round-robin` (default; takes turns after the account picked last), `weighted` (by `weight`, interleaved), `fill-first` (first eligible account in order), `least-active` (fewest running requests). |
| `max_attempts` | Upstream attempts per request, counting the first. 0: no fixed limit; each account is tried once per round. |
| `max_accounts` | Distinct accounts one request may try in a round, as today's `max-retry-credentials` limits them; each round gets its own count, and an account the limit left out takes part in a later round. 0: no limit. |
| `max_wait_ms` | Longest a request waits, in total, for a blocked account; with `request_retry`, before each round. 0 (default): never; when every account is blocked the request fails at once with a 429 and `retry_after_ms`. A request never tries an account before the time the upstream gave. |
| `request_retry` | When set, rounds run as today's `request-retry` runs them: at most this many after the first, each trying every eligible account again. A round starts at once when no account is blocked, or else when the first block ends if that is within `max_wait_ms`; an account refused for rate or quota in the request waits at least 10 seconds; a refused key (401) or a missing model (404) starts none. An account's own `request_retry` counts for it instead. Unset (here and on every account): rounds are bounded by `max_attempts` and `max_wait_ms`, and each needs a wait. |
| `disable_cooling` | `true`: failures leave no block, so every account stays eligible. |
| `transient_error_cooldown_ms` | How long a failing upstream or endpoint (a 5xx or a broken connection) is blocked when it gave no `Retry-After`: 0 (default) backs off from a second to a minute, above 0 that long, below 0 not at all, `Retry-After` or not. |
| `quota_response_mode` | What a client gets when every account that serves its model is out of quota until a reset the upstream named, the first of them more than 5 minutes away: `retryable` (default) a retryable 429 with `Retry-After`, as for any block; `usage-limit` the final usage-limit reply below. |
| `session_affinity` | When present, requests of one session go to the same account while it is eligible. The session comes from the `X-Claude-Code-Session-Id`, `Session-Id`, `X-Session-Id` or `X-Session-Affinity` header, or the body's `prompt_cache_key`; bindings are per client key and model. |
| account `priority` | Higher first; lower accounts serve only while every higher one is blocked. Default 0. |
| account `weight` | For `weighted`. Default 1; 0 leaves the account out. |
| account `disable_cooling` | Overrides `disable_cooling` for the account. |
| account `max_concurrency` | Requests running at once on the account. 0: no limit. |
| account `request_retry` | Rounds after the first that the account takes part in, in place of `request_retry` (today's per-key `request-retry`): the account leaves a request's rounds past its own count, and a count above routing's runs more rounds with only the accounts whose count reaches them. Setting it on an account runs rounds even when routing sets none; accounts without one then take part in the first only. |
| account `endpoint` | Accounts naming the same endpoint share its outages. Default: the account's ID. |

A client can pin a request to one account with the `X-Pluxy-Account: <id>` header. A pinned
request never goes to another account.

With `quota_response_mode` `usage-limit`, a request that ends with a rate or quota refusal (or
finds every account blocked) gets today's core's usage-limit reply when each account that serves
its model is blocked last by a quota refusal whose reset the upstream named (Codex's
`usage_limit_reached`, an OpenAI-compatible `insufficient_quota` with `Retry-After`, a Claude
subscription's used-up window, Google's `QUOTA_EXHAUSTED`), and the first reset is more than 5
minutes away. As in today's core, such a block on the whole account counts on its own and its
reset is the account's; a block on the model or the endpoint counts only when no block of another
kind on the account ends as late. The reply is a 429 that tells the client to stop until the reset rather than retry:
no `Retry-After`, `X-Should-Retry: false`, and, when the upstream said how long the used-up window
is, Codex's `X-Codex-Primary-*` headers (`X-Codex-Secondary-*` for a window of a day or more) with
`Used-Percent` 100, `Window-Minutes` and `Reset-At`. OpenAI, Responses and Gemini clients get this
body (Responses WebSocket clients the same object in an `error` message, with the headers in its
`headers`):

```json
{"error": {"code": "model_cooldown", "type": "usage_limit_reached", "retryable": false,
  "message": "All credentials for model gpt-5 are cooling down via provider codex (last error: usage_limit_reached: The usage limit has been reached)",
  "model": "gpt-5", "provider": "codex", "resets_at": 1767225600, "reset_seconds": 7200, "reset_time": "2h0m0s",
  "limit_window_minutes": 300, "last_upstream_error": "usage_limit_reached: The usage limit has been reached"}}
```

Anthropic clients get a `rate_limit_error` whose message ends with `(reset after 2h0m0s)` and that
carries `resets_at` and `retryable: false`. `provider` (the extension's name without `pluxy/`) is
there only when every account belongs to one extension; `limit_window_minutes` only when the window
is known. `last_upstream_error` and the message's last error have credentials, tokens and local
paths replaced, as today's core replaces them. A stream that has already started ends with the
same error, and a Chat Completions stream then with `data: [DONE]`.

### Account checks

When core starts, and when a reload adds or changes an account (its settings, its credential or
its extension) or a login adds one, core checks the account: it asks the account's extension
whether the account works, and the extension makes one call upstream that costs the account
nothing, such as its model list. Nothing is generated and the credential is not renewed for it.
Up to 4 checks run at once, each for at most 30 seconds, in the background: core serves requests
while they run, and the result does not change routing. The management API's `GET /accounts`
shows each account's last `check`:

| `check.state` | Meaning |
| --- | --- |
| `checking` | The check is running. |
| `ok` | The account works; `detail` says what was checked. |
| `failed` | It does not; `error` is why (code `auth` for a refused key, `unavailable` for an upstream that cannot be reached). |
| `not_checked` | The extension cannot check it without a generation, or has no check; `detail` says which. |

Core also logs each result (`account "work": check passed: ...` or `check failed: ...`). Each
extension checks with its upstream's model list, and only accounts with an API key:

| Extension | Check |
| --- | --- |
| `pluxy/openai-compatible` | `GET {base_url}/models`; an upstream without that list (404) leaves the account not checked. |
| `pluxy/claude` | `GET {base_url}/v1/models`. A subscription login is not checked. |
| `pluxy/gemini` | `GET {base_url}/v1beta/models`. A Vertex account is not checked. |
| `pluxy/xai` | `GET {base_url}/models`. A Grok login is not checked. |

The other bundled extensions do not check accounts yet. The protocol side is the
[`check` operation](protocol/README.md#check).

An account that cannot serve a request (its upstream speaks another format and the request
cannot be translated to it, or the upstream says it does not have the model) passes the request
on to the next account within the same request.

## Model lists and model names

Clients list the models they can ask for in their own API's shape, with the client key as for
any request:

| Request | Reply |
| --- | --- |
| `GET /v1/models` | OpenAI's list: `{"object": "list", "data": [{"id", "object", "created", "owned_by"}]}`. A request with an `anthropic-version` header or Claude Code's user agent (`claude-cli/...`) gets Anthropic's list instead: each model's `display_name`, `created_at`, `max_input_tokens` and `max_tokens` (200000 and 64000 when not known), sorted by display name, with `has_more`, `first_id` and `last_id`. |
| `GET /v1/models/{id}` | One model from that list, or a 404 with code `model_not_found`. |
| `GET /v1beta/models` | Gemini's list, `{"models": [...]}`: each model's `name` (`models/{id}`), `displayName`, `description`, token limits and `supportedGenerationMethods` (`["generateContent"]` when not known). |
| `GET /v1beta/models/{name}` | One model, named with or without `models/`, or a 404. |

A list holds each model the accounts serve, once under each name a client can ask for it by,
sorted by name. An account whose config lists `models` lists those. An account without `models`
lists the models its extension says the account serves (the protocol's `models` operation, from
version 0.3), under the account's naming rules below; an extension that cannot say adds nothing
for it. The same list decides which models such an account serves: a request for a model no
account serves is refused without calling an upstream, as today's core refuses it (404
`model_not_found`; today's core answers 400). An account whose extension has no list, an empty
one, or one it could not read within 15 seconds serves any model, and core asks again a minute
later. Core asks an
extension once per account and keeps the answer until the accounts change: a reload, or an
account added, removed or signed in again.

An account names its models with these fields, applied in this order:

```json
{
  "routing": {"force_model_prefix": false},
  "extension": {
    "accounts": [
      {
        "id": "team",
        "models": ["gpt-6.1-sol", {"name": "gpt-6-astra-2026-10", "alias": "gpt-6-astra"}],
        "excluded_models": ["*-mini"],
        "model_alias": [{"name": "gpt-6-luna", "alias": "luna", "fork": true, "display_name": "Luna"}],
        "prefix": "team",
        "...": "..."
      }
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| account `models` | The only models the account serves. Each is the upstream's name, or `{"name": ..., "alias": ...}` to serve the model `name` under the name `alias` only. |
| account `excluded_models` | Models the account does not serve: names, or patterns with `*` for any text (`gpt-5-*`, `*-mini`, `*preview*`), whatever their case. They match the name clients use for an entry of `models` (its `alias`, when it has one, as in today's core), and for any other model its name before `model_alias` renames it. |
| account `model_alias` | Renames: a request for `alias` (exactly as written) goes to the model `name` (any case). Without `fork` the model is served only as `alias`; with `"fork": true` under both names. `display_name` replaces the model's display name in lists. |
| account `prefix` | The account also serves each model as `{prefix}/{model}`, so a client can ask for a model on that account alone. No `/` in it. |
| `routing.force_model_prefix` | `true`: an account with a prefix serves its models only with the prefix, except a model whose name is the prefix itself. |

A request for a renamed or prefixed model reaches the upstream under the upstream's name, and
blocks on a model are kept under that name, so asking for the same model by another name does not
get around a block.

An extension's settings (`extension`, or its entry in `extensions`) may also hold `model_alias`
and `excluded_models` for the accounts added to it by a sign-in (not those added with an API
key, as today's `oauth-model-alias` and `oauth-excluded-models` skip API keys). Its `model_alias` rules come after
an account's own, leaving out a rule for an alias the account already has; its
`excluded_models` apply to an account that has none of its own.

## Accounts added by login

Besides the accounts in the config, a provider extension that signs in to accounts (OAuth in a
browser, a device code, or an API key it checks) can add accounts through the management API.
Core keeps each one in its credential store, a folder with one file per account readable only by
its owner (`credentials_dir`, default `credentials` beside the config file), and renews its
credential through the extension shortly before it expires. Only the extension that created an
account ever receives its credential. The protocol side is in the
[protocol README](protocol/README.md#login-and-refresh).

The management API is served under `/v0/management` when the config sets `management_key`;
send it as a bearer token. With a management key set, the config may set no accounts.

| Request | Does |
| --- | --- |
| `GET /accounts` | Lists every account (`source` `config` or `login`), its credential `kind`, `state` (`active`, or `reauth_required` when the provider refused the grant), `disabled`, `expires_at`, the last renewal error and the last [account check](#account-checks); its routing fields (`priority`, `weight`, `disable_cooling`, `max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models`, `model_alias`, `endpoint`); and what routing knows about it now: `cooldowns` (each live block's `scope`, `credential`, `model` with its `model`, or `endpoint`; `kind`, `rate_limit`, `quota` or `cooldown` for failures without a reset time; `until`; `backoff_level`; and the `error` that set it), `cooldown_until` (when the latest block on the whole account ends; model blocks do not count), `last_error` and `last_error_at` (the latest failure that set a block, until a request on the account that started after it succeeds; like a cooldown, it is not cleared by a request that started earlier, and a failure from a request that started before a success on the account sets none) and `active_requests`. An account that gets no requests (disabled, or waiting for a login) has no routing state. Never a secret. |
| `POST /accounts` | Starts a login that adds an account: `{"id": "work", "extension": "pluxy/codex", "method": "device", "input": {...}, ...}` plus any account fields from the config (`settings`, `models`, `priority`, ...). `extension` names the extension that signs in and owns the account; it may be left out while only one extension runs. Replies 202 with the login, usually carrying its first prompt (a URL or a device code). |
| `GET /logins/{id}` | The login's state (`pending`, `done`, `failed`, `cancelled`), prompt or error. |
| `POST /logins/{id}/input` | `{"input": {...}}`: what the user entered for the prompt, such as the redirect URL of a browser login. |
| `DELETE /logins/{id}` | Cancels the login. |
| `POST /accounts/{id}/login` | Signs in to an existing account again (`{"method": ..., "input": ...}`), replacing its credential. The login must sign in to the same upstream account: when the provider reports who signed in (the grant's [`identity`](protocol/README.md#login), such as an e-mail address and an account ID), every field core stored for the account must match, letter case aside; an account stored without one is checked by its `label`. Otherwise the login ends `failed` with code `invalid_request` and the account keeps its credential, as today's core refuses with "Authenticated account does not match target credential". To use the other account, add it as a new one. |
| `POST /accounts/{id}/refresh` | Renews the account's credential now. |
| `POST /vertex/import` | Adds a Vertex AI account from a Google service account key file, as today's `POST /v0/management/vertex/import` does: `multipart/form-data` with the key in `file` and an optional `location` (a form field, else a query parameter; default `us-central1`), at most 1 MiB in all. It runs the Gemini extension's [`service_account` login](#gemini) with the account ID `vertex-<project_id>` (`/`, `\` and `:` in the project ID become `_`, spaces `-`), the settings `vertex.project` and `vertex.location`, and the label `<project_id> (<client_email>)`, waits for it (one token fetch, at the key's `token_uri`) and answers once: 200 `{"status": "ok", "account", "project_id", "email", "location"}`. Errors have the usual `{"error": {"code", "message"}}` shape: 400 `file_required`, `invalid_json`, `project_id_missing` and `invalid_service_account` (also when the token endpoint refuses the key), 400 `invalid_request` for a location or project ID the extension refuses, 409 `conflict` for an account of that ID in the config, 413 for a larger request, 502 with the login's code (`unavailable`) when the token endpoint cannot be reached or fails, and 504 when it does not answer in 45 seconds. When a login added an account of that ID before, the import signs in to it again, so the key replaces the old one only when its `client_email` is the same service account (else 400 `invalid_request`, the account keeps its key) and a `location` the account does not have is a 409; today's core overwrote the file silently. It does not repair a key damaged by pasting (today's core rebuilds a mangled PEM); it takes the file Google gives. |
| `PATCH /accounts/{id}` | `disabled` (`true` or `false`; disabled accounts get no requests) and any of the routing fields `priority`, `weight`, `disable_cooling`, `max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models` and `model_alias`, checked as the config's are; `null` sets one back to its default. A bad field changes nothing (400). Replies with the account. |
| `POST /accounts/{id}/cooldown/reset` | Clears the account's cooldowns, on its credential, its models and its endpoint (which accounts naming the same endpoint share), with their backoff steps and its last error, so it serves again at once. Works for config accounts too. Replies with `cleared`, the number of live blocks it cleared, and the account. |
| `GET /accounts/{id}/models` | The models the account serves: its config's `models` when it lists them, else the ones its extension lists for it (the protocol's [`models`](protocol/README.md#models)). Each has its upstream `id`, what the extension says about it, `names` (the names clients use for it after the account's exclusions, aliases and prefix, and its extension's defaults for logins) and `excluded` when `excluded_models` leaves it out. |
| `POST /accounts/{id}/test` | Sends the account one small request (`Hi`, at most 16 tokens of reply), straight to its extension: its cooldowns are ignored and its routing state is left as it is. `{"model": ...}` names the model by its upstream name; without it, the first the account serves. Replies 200 with `result`: `ok`, `model`, `served_model`, `latency_ms`, the start of the `reply`, `finish_reason` and `usage`, or `error`. The request spends what a request that size costs. |
| `GET /accounts/{id}/quota` | The account's plan and usage, read from its provider when asked (the protocol's [`quota`](protocol/README.md#quota)): `quota` with `plan`, `limits` (each with `id`, `label`, and what the provider gives of `used_percent`, `used`, `limit`, `remaining`, `unit`, `unlimited`, `resets_at`, `window_seconds`, `models`) and `raw`, the provider's replies; and `fetched_at`. 422 with code `unsupported` for an account with no plan to report, such as an API key, or whose extension reads no quota; 502 with the provider's error code when the provider refused or failed. Each provider's section says what it reports. |
| `GET /login-methods` | For each running extension that signs in, its `extension` ID and `methods`: each `method`, `kind` (`browser`, `device`, `api_key`), `label` and the `inputs` it asks for (`name`, `label`, `secret`, `optional`), as its describe reports them; what a panel needs to offer "add account". |
| `DELETE /accounts/{id}` | Removes the account and its credential. |

Accounts set in the config are listed but changed only in the config; their cooldowns can be
reset and they can be tested and their models listed like any other.

A login's codes and a renewal's refresh token go only to the provider's own login hosts, over
HTTPS. No account setting moves them; `base_url`, where requests go, is a setting. Tests that
sign in to a fake provider start core with `PLUXY_TEST_LOGIN_HOSTS=1` in its environment, which
core passes on to its extensions and which makes them take the fake's address from the account's
settings (`auth_base_url` and the like) instead of ignoring it. Never set it in a real run.

## Reloading config and swapping extensions

Core reloads its config file without a restart: when the file changes (it looks every second;
`-watch-interval` changes that, `0` turns it off), on `SIGHUP`, or on `POST
/v0/management/reload`. A reload reads the file, validates it, prepares what it needs and only
then switches to it in one step; requests that already started finish on the config and
extension version they started with. A file core has already seen, or an empty one (an editor
in the middle of saving), is skipped; the file watcher waits until the file has stopped changing.
A reload that fails at any step, from broken JSON to an extension that does not start, leaves
the running config in place, and management shows why. That reason, in the log, in status, and
when core refuses to start or `-import-auth` refuses the config, has the config's secrets replaced by placeholders, as `GET
/v0/management/config` shows them. `listen`, `tls`, `credentials_dir`,
`packages.dir` and `extension_pages.listen` cannot change without a restart.

When the extension's manifest or program changes, a reload starts the new version and waits for
its handshake before switching. The old version then drains: it finishes the requests that
started on it and stops, at the latest after `drain_timeout_ms`. A Responses WebSocket's session
lives in the old version, so it closes once its running turn ends: the client's next turn that
continues it gets `previous_response_not_found`, and one with the whole conversation opens a
session on the new version. Routing state (blocks,
cooldowns, session bindings, the round-robin turn) carries over for every account whose ID,
extension and `settings` stay the same; an account whose `settings` change starts fresh.
Accounts added by login stay in the credential store through reloads and serve requests on every
version of the extension that logged them in.

An extension's program runs only while it is needed: core starts it at load to check it, stops
it after `idle_stop_ms` without requests, and starts it again for the next request. A program
that exits on its own is restarted after a backoff that doubles with each crash in a row; after
`quarantine_after` crashes in a row it is quarantined: requests fail with a 503 until a reload
starts it afresh.

```json
{
  "management_key": "choose-a-management-key",
  "extension": {
    "manifest": "...",
    "accounts": ["..."],
    "idle_stop_ms": 600000,
    "drain_timeout_ms": 600000,
    "restart": {"backoff_ms": 1000, "max_backoff_ms": 60000, "quarantine_after": 5, "stable_ms": 600000}
  }
}
```

| Field | Meaning |
| --- | --- |
| `management_key` | Bearer token for the management API. Without it the management API is off. |
| `idle_stop_ms` | Stop the program after this long without requests. Default 10 minutes; 0 keeps it running. |
| `drain_timeout_ms` | Longest a replaced version may keep finishing its requests. Default 10 minutes. |
| `restart.backoff_ms` | Wait before the first restart after a crash; doubles per crash in a row. Default 1 second. |
| `restart.max_backoff_ms` | Cap on that wait. Default 1 minute. |
| `restart.quarantine_after` | Crashes in a row that quarantine the extension. Default 5. |
| `restart.stable_ms` | A program that ran this long before crashing starts a new count. Default 10 minutes. |

`./pluxy -safe-mode` starts core with no extensions: client requests fail with a 503, and the
config can still be checked and reloaded through management.

### Management API

Beside the account calls [above](#accounts-added-by-login), management serves these. Every call
needs `Authorization: Bearer <management_key>`.

The management API answers only this machine: a call from a loopback address (`127.0.0.1`,
`::1`). Any other client gets a 403 with the code `remote_disabled`, before its key is looked at
(so its keys do not count for the lockout below),
unless `management_allow_remote` is `true`, as today's `management.allow-remote`. The client is
the address [`trusted_proxies`](#run-it) gives. A call that names another client in
`X-Forwarded-For`, `X-Real-IP` or `Forwarded` does not count as this machine unless core took the
client from that header (a trusted proxy's `X-Forwarded-For` or `X-Real-IP`; core does not read
`Forwarded`), so a tunnel or proxy on this machine that forwards calls from elsewhere does not
open the management API; today's core counts those calls as local. The client API and the panel's
page are not gated; the panel's calls are.

```json
{"management_key": "choose-a-management-key", "management_allow_remote": true}
```

A client that sends a missing or wrong management key 5 times in a row is refused for 30 minutes,
with a 403 and `Retry-After`, even when it then sends the right key; core logs a line naming it.
A right key clears its count, and a count is forgotten after 2 hours without a failure. These are
today's core's numbers. The client is the address [`trusted_proxies`](#run-it) gives, and an
IPv6 client is counted by its /64, so it cannot start again by moving to another address in the
block it was given; today's core counts each address. Behind a proxy that is not in
`trusted_proxies`, every client has the proxy's address, so one client's wrong keys block them all.

| Call | What it does |
| --- | --- |
| `GET /v0/management/status` | `desired` (the newest config version core has seen, with `error` when it was refused) and `active` (the one serving), each with `revision` and `hash`; `extensions`, one entry per running generation with `state` (`starting`, `active`, `idle`, `crashed`, `quarantined`, `draining`, `stopped`), `current`, `pid`, `restarts`, `last_error` and `active_requests`; `safe_mode`. |
| `POST /v0/management/reload` | Reload now. 200 with the status when the new revision is active; 422 with the status when it was refused. |
| `GET /v0/management/ui` | The pages of the extensions that run and where they are served; see [extension pages](ui/extension-pages/README.md#how-core-serves-pages). |
| `GET`, `PATCH /v0/management/extensions/{publisher}/{name}/config` | An extension's settings, checked against the schema its manifest declares; see [extension pages](ui/extension-pages/README.md#how-core-serves-pages). |

### Changing the config through management

These calls read and change the config file core runs on, whether it is the JSON config or a
`config.yaml` with today's key names. A change goes through the same steps as a reload: core
checks the new contents and prepares them, and only when they are ready to serve does it write
the file and switch to them. The file is replaced in one step (a new file renamed over it, with
the same permissions, through a symbolic link if the path is one), so nothing ever reads half of
it. When a change is refused, the file and the running config stay as they were, and status
keeps showing the file as it is. When another program writes the file while a change is being
prepared, the change is dropped with a 409, so that write is not lost. Today's core writes its file in place and saves a change that
its reload may then refuse.

A change made with the structured calls (client keys, management key, settings) writes the file
out again. A `config.yaml` keeps its comments and the order of its keys, with two-space indents
and no blank lines, as today's core writes it when it saves; a JSON config keeps the order of
its keys and its indent. `PUT /config` writes the document as sent.

| Call | What it does |
| --- | --- |
| `GET /v0/management/config` | The config file: `format` (`json` or `yaml`), `hash` (as in status) and `content`, the file as written with each secret replaced by a placeholder such as `pluxy-redacted-4f1c...`. Secrets are found by key name, in both layouts of a `config.yaml` and in the JSON config: client keys, the management key, every `api-key` and `api_key`, everything in an account's `credential` except its `kind`, passwords, tokens and secrets, header values about authentication (`Authorization`, `X-Api-Key`, cookies), and any URL with a user name, password or query. Comments are shown as written. 422 when the file does not parse. Today's core returns its config, the management key's hash and provider keys included, as they are. |
| `POST /v0/management/config/validate` | `{"content": "..."}`: checks a document as a reload would, without starting extensions or writing anything. 200 with `valid: true` and the `notices` a `config.yaml` load logs, or `valid: false` and `error`. |
| `PUT /v0/management/config` | `{"content": "...", "base_hash": "..."}`: replaces the file with the document and applies it. 200 with the file as `GET /config` shows it; 409 when `base_hash` is set and the file has changed since; 422 with `error` when core refuses the document. |
| `GET /v0/management/client-keys` | The client keys: each one's `id` and `masked` form (first and last two characters, as today's panel shows them; a key shorter than 8 characters is hidden whole). |
| `POST /v0/management/client-keys` | Adds a new random client key (`sk-` and 26 random characters). 201 with the key's `id` and `masked` form and its `value`, which no later call shows. |
| `DELETE /v0/management/client-keys/{id}` | Removes the client key. 204; 404 when no key has the ID; 409 for the last key, since core refuses a config without client keys. |
| `POST /v0/management/management-key/rotate` | Replaces the management key with a new random one and returns it as `value`. The old key stops working as the reply is sent. |
| `GET /v0/management/settings` | The basic and routing settings, by today's core's names, and `token_count_estimate`, this core's own, under its own name (below). |
| `PATCH /v0/management/settings` | Changes the settings the body names (`{"routing": {"strategy": "fill-first"}}`) and replies with them all. 400 for a name or value it does not take. |

In the document, a placeholder stands for the secret it replaced, so a document read with `GET
/config`, edited and sent back keeps its secrets: core puts each one back from the file as it is
now. To change a secret, write the new value in its place. A placeholder is accepted only where a
secret goes, and only while the file still holds that secret and core has not restarted (the
placeholders are keyed hashes with a key core picks at start); otherwise the call answers 409
(read the config again) or refuses the document. Messages in replies never show a secret either.

A `config.yaml` keeps each setting under the name it already has there: the v8 name, or the
older spelling when only that is in the file. New client keys go to `access.api-keys`, or to the
older layout's `api-keys` list when the file has that and no `access.api-keys`; the management key
to `management.secret-key`, or `remote-management.secret-key` when only that is there.

| Setting | JSON config | Values |
| --- | --- | --- |
| `routing.strategy` | `routing.strategy` (`weighted` for `weighted-round-robin`) | `round-robin`, `fill-first`, `weighted-round-robin`, `least-active`; today's other spellings (`wrr`, `ff`, ...) are taken too. |
| `routing.session-affinity` | `routing.session_affinity` there or not | `true` or `false`. |
| `routing.session-affinity-ttl` | `routing.session_affinity.ttl_ms` | A duration such as `1h` (default) or `30m`. In a JSON config it needs session affinity on. |
| `routing.force-model-prefix` | `routing.force_model_prefix` | `true`: an account with a `prefix` serves its models only under the prefix. |
| `routing.retry.max-retry-credentials` | `routing.max_accounts` | Accounts one request may try in a round, as in today's core; 0 is no limit. |
| `routing.retry.max-retry-interval` | `routing.max_wait_ms` (seconds × 1000) | Seconds one request may wait for a blocked account: before each round with `request-retry` set, else in all. |
| `routing.retry.request-retry` | `routing.request_retry` | Rounds a request may run after its first, as in today's core. `null` while a JSON config leaves rounds to this core's own rules; a `config.yaml` always has a number (default 0). |
| `routing.cooldown.disable-cooling` | `routing.disable_cooling` | `true`: failures leave no cooldown. An account's own `disable_cooling` wins. |
| `routing.cooldown.transient-error-cooldown-seconds` | `routing.transient_error_cooldown_ms` (seconds × 1000) | Seconds a failing upstream cools down: 0 for this core's backoff (1 second doubling to a minute), -1 for none. |
| `routing.cooldown.quota-response-mode` | `routing.quota_response_mode` | `retryable` (the default) or `usage-limit`: what a client gets when every account is out of quota (see [Accounts and routing](#accounts-and-routing)). |
| `observability.logs.logging-to-file` | `logs.to_file` | `true` or `false`: whether core also writes its log to [files](#log-files). |
| `observability.logs.logs-max-total-size-mb` | `logs.max_total_size_mb` | Megabytes the logs folder's `.log` files may take; 0 (the default) is no cap. |
| `observability.logs.request-log` | `logs.request_log` | `true` or `false`: whether every client request is written to a [request log](#request-logs). |
| `observability.logs.error-logs-max-files` | `logs.error_logs_max_files` | How many error logs are kept, the newest; 0 keeps all. Default 10. |
| `observability.usage.usage-statistics-enabled` | `usage.enabled` | `true` or `false`: whether core keeps the [usage record](#usage-statistics). On unless the config turns it off. |
| `token_count_estimate` (top level) | `token_count_estimate` (top level, in a `config.yaml` too) | `true` (the default) or `false`: whether a token count no account can give is answered with core's estimate or with an error. |

The settings shown are those of the running config. Today's other basic settings (`proxy-url` and
`ws-auth`) do not exist in this core yet, and `debug` is dropped (this
core writes no debug-level lines); `max_attempts`, which today's core has no
name for, is changed in the document.

## Usage statistics

Core keeps a usage record: one event for each generation request that reached routing (a request
refused for its client key, or whose body does not parse, is not recorded), including Responses
WebSocket turns. Token counts (`count_tokens`, `:countTokens`) generate nothing and are not
recorded. An event holds the request's trace ID (`id`, the `X-CPA-TRACE-ID` of its reply),
when it arrived, the `endpoint`, whether it streamed, the `model` the client asked for (without a
thinking suffix) and the account's own name for it when that differs (`upstream_model`: an alias
or prefix resolved), the `provider` (extension) and `account` its last attempt went to, the
client key's `key_id` (the `id` `GET /client-keys` lists it under, never the key), the
`client_ip` it came from (the connection's peer, or the client a trusted proxy names; see
`trusted_proxies`), whether it `failed` with which `error_code`, the HTTP `status` of the reply, `latency_ms`, and the `tokens`
the upstream reported: `input_tokens` (cached ones included), `output_tokens` (reasoning
included), `reasoning_tokens`, `cached_tokens`, `cache_creation_tokens` and `total_tokens` (input
plus output). A token count the upstream did not report is absent, not 0. A streamed reply
that fails after it began keeps its `status` 200 with `failed` true; a client that goes away is
recorded as `cancelled`, with no `status` when nothing had reached it.

The record lives in memory and is gone after a restart. It keeps the last `max_events` requests
(default 10000, at most 200000) and none recorded (when the request ended) longer ago than
`max_age_ms` (default 24 hours, at most 30 days); a reload that lowers either drops the oldest at once. Reading it never removes anything:
today's core hands its usage queue out once and deletes what it hands out.

```json
{"usage": {"enabled": true, "max_events": 10000, "max_age_ms": 86400000}}
```

`usage.enabled` (today's `usage-statistics-enabled`) is on unless the config turns it off. Off,
requests are not recorded and what was recorded stays readable.

| Call | What it does |
| --- | --- |
| `GET /v0/management/usage` | What the recorded requests add up to: `total_requests`, `success_count`, `failure_count`, `success_rate` (0 to 1), `average_latency_ms` and `tokens` (the sums of each token count); the same per model (`models`), extension (`providers`), account (`accounts`, each with its `provider`) and client key (`keys`, by `key_id`), most requests first; and `series`, the requests, failures and tokens in each of the last `buckets` steps of `bucket` (default 60 steps of `1m`; `bucket` from `1s` to `24h`, `buckets` up to 1440), oldest first, ending with the step that holds now. `oldest` is the arrival time of the oldest event kept, `evicted` how many events the limits have pushed out since core started, and `enabled` whether recording is on. |
| `GET /v0/management/usage/events` | The recorded events, oldest first: up to `limit` (default 100, at most 1000) with a `seq` above `after`. The reply's `next` is the `seq` to pass as `after` for the next page, `more` says whether more match, and `lost` says whether events after `after` were pushed out before this read, so a program that polls with `after` knows it missed some. |

Both calls take the filters `since` (an RFC 3339 time, or a duration back from now such as
`24h`), `until` (an RFC 3339 time), `model`, `provider`, `account`, `key_id` and `failed`
(`true` or `false`). A filter that does not parse is a 400.

## Log files

Core writes its log to standard error. With `logs.to_file` on (today's `logging-to-file`), it
also writes it to `main.log` in its logs folder, the folder `logs` beside the config file, each
line with its time and level in front as today's core writes them:

```text
[2026-11-09 12:00:00.123] [info ] config revision 2 active (management API: settings)
[2026-11-09 12:00:01.456] [warn ] warning: no client_keys set; anyone who can reach ...
```

Lines are `info`, or `warn` for warnings; core has no debug lines, so today's `debug` setting is
dropped. Today's core writes only to the file while the setting is on; this core keeps writing
to standard error too. `main.log` is rotated when it reaches 10 MB, as in today's core: it is
renamed `main-<time>.log` and a new one begins. `logs.max_total_size_mb` (today's
`logs-max-total-size-mb`) caps the folder: when its `.log` files take more, the oldest are deleted
first, `main.log` never; 0 (the default) is no cap. The cap is applied when a file is rotated, when a
request log is written and when the setting changes. A change of either setting takes effect at the reload that makes it.

```json
{"logs": {"to_file": true, "max_total_size_mb": 100}}
```

| Call | What it does |
| --- | --- |
| `GET /v0/management/logs` | Lines of `main.log` and its rotated files (not the request logs), oldest first: with `after` (a time in Unix seconds, such as a reply's `latest-timestamp`), the oldest `limit` lines written after it, so a reader can page forward; without it, the newest `limit` lines. `limit` defaults to 1000, at most 10000. The reply holds `lines`, `line-count` and `latest-timestamp` (the newest returned line's time, in seconds with milliseconds; `after` itself when there is none). A line with no time of its own counts as written with the line before it. |
| `DELETE /v0/management/logs` | Empties `main.log` and deletes its rotated files; replies with `removed`, how many files it deleted. Request logs stay. |

Both answer 400 while logging to file is off, as today's core does. Today's core also takes a
`cursor` for the logs page; here `after` serves to page forward, with the time to the
millisecond, so lines written in the same millisecond as `latest-timestamp` after it was read
are not returned.

## Request logs

With `logs.request_log` on (today's `request-log`), core writes each client request that passed
the client key check, and its reply, to a file in its logs folder (the folder `logs` beside the
config file), as today's core does. The file is named after the request's path, its time and its
trace ID, such as `v1-chat-completions-2026-11-09T120000-20261109120000-3f9a0c2e5b7d4e18.log`,
and holds today's core's sections: `=== REQUEST INFO ===` (URL, method, time, the client's
address (the connection's peer, or the client a trusted proxy names), trace ID and the account
its last attempt went to), `=== HEADERS ===`, `=== REQUEST BODY ===` and `=== RESPONSE
===` (status, headers and body). A Responses WebSocket turn is written as one request: the
client's `response.create` message as its body and, under `=== WEBSOCKET MESSAGES ===`, the
messages sent back, one per line; a turn answered with an error counts as failed. A WebSocket
message refused before it starts a turn (an unknown type, no model, an `input` that is not a
list) is answered on the socket and not written. A reply over 1 MiB keeps its first 256 KiB and its last 768
KiB, with a line saying how many bytes were left out between them. While only error logs are
written, every request keeps its reply's start and end until it ends, in case it fails, so an
error log keeps less of a reply: the first 32 KiB and the last 96 KiB.

With `logs.request_log` off (the default), core writes only the requests that fail, as
`error-<name>.log`, as today's core does: a reply status of 400 or more, or a streamed reply that
fails after it began; a client that goes away is not a failure. `logs.error_logs_max_files`
(today's `error-logs-max-files`, default 10) keeps the newest of them; 0 keeps them all. Request
logs count toward `logs.max_total_size_mb`, which is applied after each file is written.

Secrets are left out of every request log: values of headers and query parameters whose names
hold `authorization`, `api-key`, `apikey`, `api_key`, `token`, `secret`, `cookie` or `password`,
and `key` (an authorization header keeps its scheme, as in `Bearer [redacted]`), and, anywhere in
the file, every client key, the management key and every config account's credential, in the
forms the credential redaction finds (JSON escapes, percent-encoding and the rest). Replies come
from the extension with the account's own credential already removed. Today's core writes the
reply's headers unmasked, logs requests that fail the client key check, and also writes what it
sent to the upstream and what came back; here the upstream exchange happens in the provider
extension, so request logs hold what the client sent and received.

```json
{"logs": {"request_log": true, "error_logs_max_files": 10}}
```

| Call | What it does |
| --- | --- |
| `GET /v0/management/request-error-logs` | The error logs, newest first: `files`, each with `name`, `size` and `modified` (Unix seconds). |
| `GET /v0/management/request-error-logs/{name}` | One error log, as a download. 400 for a name that is not an error log's as core writes them, 404 when there is none or the name is a link. |
| `GET /v0/management/request-log-by-id/{id}` | The newest request log, plain or error, of the request with that trace ID (the `X-CPA-TRACE-ID` of its reply), as a download; 404 when there is none. |

## Packages

Extensions are installed as packages: a release archive from a registry, checked against the
digest the registry pins, unpacked and checked, then kept in its own folder and run like the
extension the config names. The package and registry formats are in the
[protocol README](protocol/README.md#release-archives). A package is **installed** (its files
are in the packages folder), **enabled** (core runs it), and **configured** when it has accounts:
in the config, or added by login. Removing a package keeps its accounts: they serve again if it
is installed again.

```json
{
  "management_key": "choose-a-management-key",
  "packages": {
    "dir": "packages",
    "registries": [{"name": "mine", "url": "https://example.com/pluxy/index.json"}]
  },
  "extensions": {
    "pluxy/codex": {
      "accounts": [{"id": "openai", "credential": {"kind": "api_key", "api_key": "sk-..."}}],
      "idle_stop_ms": 600000
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `packages.dir` | Where packages are kept, relative to the config file. Default `packages` beside it. Each release has its own folder, `{dir}/{publisher}/{name}/{version}`, and `{dir}/packages.json` records what is installed: each package's version, registry, archive URL and digest, the digest of its unpacked manifest and program, and whether it is enabled. |
| `packages.registries` | Registries to install from, searched in order and before `bundled`: `name` and `url` (an `https://` URL or a path relative to the config file). |
| `extensions` | Settings of installed packages, by package ID: `accounts`, `idle_stop_ms`, `drain_timeout_ms` and `restart`, as in `extension`. They apply while the package is enabled. |

`extension` (the extension run from a manifest path) is optional once packages are used. Account
IDs are unique across `extension` and every `extensions` entry.

An enabled package's program runs only while it is needed, as any extension does. Each time core
starts it, core checks its manifest and program still have the digest recorded at install; one
that changed is not run. An enabled package that cannot start when core starts (its files
changed, or it needs a protocol version this core does not speak) is skipped, and management
shows why; core serves with the rest. A reload that cannot prepare a package that runs now is
refused, and the running version stays. Installing the same release again replaces files that
changed. Management never shows a registry or archive URL's user name, password or query.

### Bundled packages

The packages a pluxy install ships with are a registry like any other, named `bundled`: by
default `bundled/index.json` beside the `pluxy` program, or the index `-bundled` names. On its
first start with a packages folder that has no `packages.json`, core installs and enables every
package the bundled registry lists, through the same steps as any install. Bundled packages are
removed like any other, and stay removed; installing one again takes it from `bundled`.

### Package management API

Every call needs `Authorization: Bearer <management_key>`. `{id}` is the package ID,
`publisher/name`.

| Call | What it does |
| --- | --- |
| `GET /v0/management/packages` | Installed packages: `id`, `version`, `registry`, `url`, `sha256`, `enabled`, `installed_at`, and `state`: `disabled`, the running extension's state (`active`, `idle`, `starting`, `crashed`, `quarantined`), `failed` with `error` when it is enabled but could not start, or `stopped` when it is enabled in safe mode. |
| `GET /v0/management/registries` | Each registry's `name`, `url` and what it offers: `packages`, each with its `releases` (`version`, `protocol`, `platforms`); or `error` when it could not be read. |
| `POST /v0/management/packages` | Installs a package: `{"id": "pluxy/codex", "version": "0.1.0", "registry": "mine", "enable": true}`. Only `id` is required: without `version`, the first release listed that runs here; without `registry`, the first registry listing the package. Installing another version of an installed package replaces it; when it is enabled, the new version starts before the old one drains, and if the new one fails to start the old one stays. 200 with the package; 404 when no registry has it; 422 when the archive or its manifest fails a check (nothing is installed) or it does not start (it stays installed, disabled); 502 when the registry or archive could not be fetched. |
| `POST /v0/management/packages/{id}/enable` | Starts the package and routes to its accounts. 200 with the package; 422 when it did not start (it stays disabled). |
| `POST /v0/management/packages/{id}/disable` | Stops routing to it; requests already running on it finish, up to its drain timeout. Its files stay. |
| `DELETE /v0/management/packages/{id}` | Disables the package and removes its files once its requests have finished. Its accounts stay. 204. |

Package changes go through the same reload transaction as a config change: core reads the
config file, prepares everything with the package change applied, and switches in one step, so
a config file that does not load also refuses the package change. One package change runs at a
time.

## config.yaml with today's key names

Core also reads a `config.yaml` written for today's core (`core/`), so an existing config moves
over without renaming keys. A config file whose name ends in `.yaml` or `.yml` is read with
today's key names; any other file is read as the JSON config above. Both layouts today's core
reads work: the v8 layout of `core/config.example.yaml` (`server.port`, `access.api-keys`,
`api-keys.codex`, ...) and the older spellings (`port`, `api-keys` as a list,
`codex-api-key`, ...). When a file has both spellings of one setting, the v8 one wins, as in
today's core.

```bash
./pluxy -config config.yaml
```

The JSON config stays: the desktop app writes one, and it is the form core checks and runs on.
A `config.yaml` is turned into that form when it is loaded, so both files go through the same
checks and hot reload, and nothing behaves differently depending on which file a setting came
from.

Each time core loads a `config.yaml`, it logs one line naming the settings the file uses that
this core does not support yet, and one line naming the settings it ignores for good (the
"dropped" rows below). A setting that is off, zero, empty or at today's default asks for
nothing and is not named, nor is any setting of a feature whose `enabled` switch is off.
Neither line stops core from starting. Settings that did nothing in today's core either (marked
"dead") are accepted without a word. A key today's core does not know is named as unknown.

Settings only this core has keep their JSON names in a `config.yaml`: `packages`, `extensions`,
`extension`, `extension_pages`, `drop_policy`, `token_count_estimate`, `credentials_dir` and `cors`
(today's core has no CORS setting: it allows every origin). Provider accounts set in
the file go to the installed package of their provider (`pluxy/codex`, `pluxy/claude`,
`pluxy/gemini`, `pluxy/xai`, `pluxy/openai-compatible`), so those packages must be installed:
the bundled ones are, on the first start.

### Provider key sections

Each key in a provider section becomes one account of that provider's extension, with the key
as its `api_key` credential. A key with no `api-key` (today's core serves an upstream that
needs none) is not supported yet: it adds no account and is named in the log.

| Section (v8; older spelling) | Extension | Account settings |
| --- | --- | --- |
| `api-keys.codex` (`codex-api-key`) | `pluxy/codex` | `base-url` is `base_url`; `websockets` is `websockets`; `alpha-search` is `alpha_search`; `disable-codex-cloaking: true` is `codex_identity: false` |
| `api-keys.claude` (`claude-api-key`) | `pluxy/claude` | `base-url` is `base_url`; `rebuild-mid-system-message: true` is `rebuild_mid_system_message: true` |
| `api-keys.gemini` (`gemini-api-key`) | `pluxy/gemini` | `base-url` is `base_url` |
| `api-keys.vertex` (`vertex-api-key`) | `pluxy/gemini`, Vertex AI with the key as an express mode key | `base-url` is `base_url`; `interactions: true` on a key is `interactions: true` in the account's settings, which serves the Interactions API |
| `api-keys.interactions` (`interactions-api-key`) | `pluxy/gemini` | `base-url` is `base_url`; every account gets `interactions: true`, which serves the Interactions API (see [Gemini](#gemini)) |
| `api-keys.xai` (`xai-api-key`) | `pluxy/xai` | `base-url` is `base_url` |
| `api-keys.openai-compatibility` (`openai-compatibility`) | `pluxy/openai-compatible`, one account per entry in `keys` (`api-key-entries`) | `base-url` is `base_url`; a group with `disabled: true` adds no accounts |

In every section, `priority` is the account's `priority` and a key's `weight` is its `weight`.
A group's `models` list becomes the account's `models`: each entry's `name` is the model the
upstream knows, and its `alias` the name clients use (the first entry for a name wins, as in
today's core); the account serves only those models. A group's or key's `prefix` is the account's
`prefix`, and its `excluded-models` its `excluded_models` (today's core has no
`excluded-models` for `openai-compatibility`, so it is accepted there and does nothing). See
[Model lists and model names](#model-lists-and-model-names). An account's ID is the group's `name`
for a group with one key, and `<name>-<n>` (`n` from 1) for each key of a group with more;
entries without a name are `<section>-<n>`, counted across the section, such as
`codex-api-key-1`. Characters an account ID cannot hold (anything but letters, digits, `.`, `_`
and `-`) become `-`.

### What each setting does here

v8 name first, older spellings in brackets. **Supported** settings work as described.
**Later** settings are not supported yet and are named in the log when set. **Dropped** settings are ignored and named in
the log when set, for the reason given. **Dead** settings did nothing in today's core either and
are accepted silently.

| Setting | Here | Status |
| --- | --- | --- |
| `config-version` | Must be 8 when present, as in today's core. | Supported |
| `server.host` (`host`), `server.port` (`port`) | `listen` is `host:port`. An empty host listens on every interface and a port of 0 or none is 8317, as in today's core. | Supported |
| `access.api-keys` (`api-keys` as a list) | `client_keys`. At least one key is required; today's core serves anyone when the list is empty. | Supported |
| `management.secret-key` (`remote-management.secret-key`) | `management_key`, exactly as written: turns on the management API and the panel. A key already hashed with bcrypt (today's core can write one) is not supported yet: the management API stays off and the log says why. | Supported; hashed keys later |
| `management.allow-remote` (`remote-management.allow-remote`) | `management_allow_remote`: without it the management API answers only this machine, as in today's core. | Supported |
| `management.disable-control-panel` | The panel is on exactly when the management API is. | Dropped: the management key already turns the panel on and off |
| `management.disable-auto-update-panel`, `management.panel-github-repository`, `server.github-token` (`github-token`) | The panel is built into core; nothing is downloaded. | Dropped: no panel download |
| `management.base-url` | | Dropped: no terminal UI |
| `server.max-request-body-bytes` | Request bodies are capped at 24 MiB. | Dropped: core needs a fixed cap for its request frame |
| `server.response-compression.*` | | Later |
| `server.tls.enable`, `.cert`, `.key` (`tls`) | `tls`, the same three keys. Relative paths are relative to the config file, where today's core reads them relative to the folder it was started in. | Supported |
| `server.trusted-proxies` (`trusted-proxies`) | `trusted_proxies`; a change applies on reload. | Supported |
| `server.commercial-mode` (`commercial-mode`) | | Dropped: it turns off features this core does not have |
| `server.discovery.*` (`discovery`) | | Later |
| `server.discovery.advertise-management` | | Dead |
| `routing.strategy` | `routing.strategy`: `round-robin`, `fill-first` (also `fillfirst`, `ff`), `weighted-round-robin` (also `weightedroundrobin`, `wrr`; this core's `weighted`); this core's `weighted` and `least-active` work too. A name today's core does not know is `round-robin`, as there, and the log says so. | Supported |
| `routing.session-affinity`, `routing.session-affinity-ttl` | `routing.session_affinity` with `ttl_ms` from the duration (default `1h`). | Supported |
| `routing.session-affinity-subagents`, `routing.session-affinity-rebind-on` | | Later |
| `routing.force-model-prefix` (`force-model-prefix`) | `routing.force_model_prefix`. | Supported |
| `routing.retry.max-retry-credentials` (`max-retry-credentials`) | `routing.max_accounts`. | Supported |
| `routing.retry.request-retry` (`request-retry`) | `routing.request_retry`: rounds after the first, run as today's core runs them. Default 0, as in today's core (its example config's 3 is only an example). | Supported |
| `routing.retry.max-retry-interval` (`max-retry-interval`) | `routing.max_wait_ms`, in seconds as in today's core: the longest wait before each round. Default 0: a round that needs a wait does not start. | Supported |
| `routing.cooldown.disable-cooling` (`disable-cooling`) | `routing.disable_cooling`. | Supported |
| `routing.cooldown.transient-error-cooldown-seconds` (`transient-error-cooldown-seconds`) | `routing.transient_error_cooldown_ms`: above 0 that many seconds, below 0 none. 0 is this core's backoff, from a second doubling to a minute, where today's core waits a fixed minute. | Supported |
| `routing.cooldown.quota-response-mode` (`quota-cooldown-response-mode`) | `routing.quota_response_mode`: `retryable` (default) or `usage-limit`, letter case aside; another value fails the load, as in today's core. | Supported |
| `routing.cooldown.save-cooldown-status` (`save-cooldown-status`) | Blocks live in memory and carry over reloads. | Dropped: no need found |
| `quota-exceeded.switch-project`, `quota-exceeded.switch-preview-model` | | Dead |
| `requests.proxy-url` (`proxy-url`) and every `proxy-url` | | Later |
| `requests.payload.*` (`payload`) | | Later |
| `requests.tls-handshake-timeout-seconds`, `requests.passthrough-headers`, `requests.nonstream-keepalive-interval`, `requests.streaming.*` (each also at the top level) | A request is retried on another account until its reply starts. | Dropped: off by default and no need found |
| `client.codex.enable-apply-patch`, `client.codex.optimize-multi-agent-v2` (`codex.optimize-multi-agent-v2`) | | Later |
| `client.codex.web-search-result-cache.*`, `client.claude.show-codex-commentary` | | Dropped: off by default and no need found |
| `upstream.codex.disable-codex-cloaking` (`codex.disable-codex-cloaking`) | `codex_identity: false` on the Codex subscription accounts `-import-auth` adds. It does not change accounts already imported. | Supported when importing |
| `upstream.codex.websocket-compression` | Always on; `false` is named in the log. | Supported (fixed on) |
| `upstream.codex.model-level-cooling`, `upstream.claude.model-level-cooling` | Blocks are kept per model where the upstream says the limit is per model; usage limits remain account-wide. | Supported (fixed on) |
| `upstream.codex.upstream-request-compression`, `-min-bytes`, `upstream.codex.stream-bootstrap-buffering`, `stream-bootstrap-timeout` | | Later |
| `upstream.codex.response-steering`, `upstream.codex.orphan-delegation-compatibility`, `oauth.providers.codex.header-defaults.*` (`codex-header-defaults`) | | Dropped: experimental or off by default, no need found |
| `upstream.claude.header-defaults.*`, `disable-claude-cloak-mode`, `disable-cloaking-model-list`, `session-date-pin-days` (also `claude`, `claude-code`, `claude-header-defaults`), and the Claude keys' `cloak` and `fingerprint-profile` | This core never changes how a client presents itself. | Dropped: making other clients look like Claude Code is out of scope |
| `upstream.claude.tool-search`, the Claude keys' `native-tool-search` | | Later |
| `rebuild-mid-system-message` (Claude keys) | The account's `rebuild_mid_system_message`. | Supported |
| `upstream.xai.inject-x-search` (`xai`) | | Later |
| `experimental-cch-signing` (Claude keys) | | Dead |
| `oauth.auth-dir` (`auth-dir`) | The folder `-import-auth` reads (see below). Core never reads or writes it while it runs; at each load it logs how many credential files there are not imported yet. | Supported |
| `oauth.auth-auto-refresh-workers` (`auth-auto-refresh-workers`) | Each account is renewed shortly before it expires. | Dropped: no need for a pool size |
| `oauth.model-alias` (`oauth-model-alias`), `oauth.excluded-models` (`oauth-excluded-models`) | Each channel's rules become the `model_alias` and `excluded_models` of the package that serves its accounts, for the accounts added to it by a sign-in (not with an API key): `codex` is `pluxy/codex`, `claude` is `pluxy/claude`, `vertex` is `pluxy/gemini`, `kimi` (and `kimi-ai`, `kimi.ai`, `kimi.com`) is `pluxy/kimi`, `xai` is `pluxy/xai`, `meta` is `pluxy/meta`, `antigravity` is `pluxy/antigravity`, `devin` is `pluxy/devin`. Rules for another channel are named in the log and ignored. An alias's `force-mapping` and `openapi` are later. | Supported |
| `oauth.settings` (`oauth-settings`) | | Dropped: needs a model catalog, and no page uses it |
| `oauth.request-scoped-errors` (`oauth-request-scoped-errors`) and every `request-scoped-errors` | | Later |
| `oauth.providers.aistudio.ws-auth` (`ws-auth`) | | Later: the AI Studio relay is not built |
| `oauth.providers.codex.live-media-relay.*` | | Dropped: realtime and WebRTC are out of scope |
| `oauth.providers.codex.signin-with-chatgpt-identifier` (`codex.signin-with-chatgpt-identifier`) | `signin_with_chatgpt_identifier` on the Sign in with ChatGPT (SIWC) accounts `-import-auth` adds. Left unset, they use the extension's default, `pluxy`. | Supported when importing |
| `oauth.providers.antigravity.*` (`antigravity`, `antigravity-signature-*`, `quota-exceeded.antigravity-credits`), `oauth.providers.devin.*` (`devin`) | | Later |
| `api-keys.meta` (`meta-api-key`) | | Later |
| Every `headers` and `connection-pool` | | Later |
| Every `disable-cooling` on a group or key | The account's `disable_cooling`. | Supported |
| Every `request-retry` on a group or key | The account's `request_retry`: a key's own, else its group's (an `openai-compatibility` group's for all its keys, which have none of their own there). A negative one inherits `routing.retry.request-retry`, as in today's core. | Supported |
| The keys' `models[]` catalog fields (`display-name`, `max-context-length`, `force-mapping`, `is-compat`, `thinking`, `image`, `input-modalities`, `output-modalities`, `use-max-completion-tokens`, `support-configuration-update`, `openapi`) | A model's display name in lists is its upstream name. | Later |
| Vertex keys' `openapi`, `openapi-location`; openai-compatibility's `support-prompt-cache-key`; xAI keys' `websockets` | | Later |
| `multimedia.disable-image-generation` (`disable-image-generation`) | `true`, `"chat"` and `"passthrough"` are `image_generation_tool: false` on Codex accounts from the file and on those `-import-auth` adds; `true` is also `images: false` there, which turns the Images API off on them (today's core answers its image routes 404). | Supported |
| `multimedia.gpt-image-2-base-model`, `multimedia.video-result-auth-cache-ttl` (each also at the top level) | Images are served through the Codex upstream's Images API, the path today's core takes for its built-in image models; the Responses-based path this model sets is not built, and there are no video routes yet. | Later |
| `observability.logs.logging-to-file` (`logging-to-file`), `observability.logs.logs-max-total-size-mb` (`logs-max-total-size-mb`) | `logs.to_file` and `logs.max_total_size_mb`: [log files](#log-files). A negative cap is none, as in today's core. | Supported |
| `observability.logs.debug` (`debug`) | | Dropped: this core writes no debug-level lines |
| `observability.logs.request-log` (`request-log`), `observability.logs.error-logs-max-files` (`error-logs-max-files`) | `logs.request_log` and `logs.error_logs_max_files`: [request logs](#request-logs). A negative count is the default, 10, as in today's core. | Supported |
| `observability.usage.usage-statistics-enabled` (`usage-statistics-enabled`) | `usage.enabled`: the [usage record](#usage-statistics). Today's example config sets it to `false`, which turns the record off here too. | Supported |
| `observability.usage.redis-usage-queue-retention-seconds` (`redis-usage-queue-retention-seconds`) | The record keeps requests by `usage.max_events` and `usage.max_age_ms`. | Dropped: it was how long a queued usage record waited for a reader that removed it; reading the record here removes nothing, and its default of 60 seconds would leave little to read |
| `observability.pprof.*` (`pprof`) | | Dropped: a developer tool, off by default |
| `models.catalog`, `models.codex-catalog`, `models.devin-catalog` | | Later |
| `plugins.*` | Extensions are separate programs (packages), not libraries loaded into core. | Dropped: replaced by packages |
| `credentials.*` (`credential-concurrency`, `credential-in-flight`) | | Dropped: only the Home control plane used them |

Environment variables today's core reads (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`,
`OBJECTSTORE_*`, ...) and its `.env` file are not read.

### Importing today's auth folder

```bash
./pluxy -config config.yaml -import-auth ~/.cli-proxy-api
```

`-import-auth` copies the credential files in today's auth folder (the JSON files at its top
level, as today's core reads them) into core's credential store (`credentials_dir`), prints what
it imported and skipped, and exits. Run it while core is stopped, then start core. Each file
becomes an account added by login, with the file's name (less `.json`) as its ID, so running it
again skips the files already imported. A file whose account ID the config or the store already
uses is skipped, as is a file larger than 1 MiB. The import never changes the auth folder, and
it refuses a `credentials_dir` that is the auth folder or inside it.

Both cores renewing the same login signs one of them out: the provider replaces the refresh
token at each renewal, and the other core's copy stops working. Once an account is imported,
use it from one core only, or sign in again in the other.

| File `type` | Becomes | Notes |
| --- | --- | --- |
| `codex` | `pluxy/codex` subscription account | `access_token`, `refresh_token`, `expired` (the expiry); `id_token` and `account_id` stay with the credential; `websockets` is the account's `websockets`. A `siwc` section with a token (`access_token`, else `access`, or `refresh_token`) also becomes a [SIWC](#codex) account, `<file id>-siwc` (with `-2`, `-3` and so on when another file in the folder imports as that ID), even when the file's own tokens are missing: its `access_token`, `refresh_token`, `expires_at` (else `expired`), and `client_id`, `device_id`, `scope` and `id_token` with the credential. It is disabled unless the file says `use_siwc: true`; it shares the file's label and routing fields below, and takes only `websockets` and the identifier setting of the account settings. |
| `claude` | `pluxy/claude` subscription account | `access_token`, `refresh_token`, `expired`. |
| `kimi`, `kimi-ai` | `pluxy/kimi` account | `access_token`, `refresh_token`, `expired`, `device_id`; `base_url` is an account setting, and so is the site, `domain`, decided as today's core decides it: from `domain`, else `base_url`'s host, else the type (`kimi.ai` for `kimi-ai`). |
| `vertex` | `pluxy/gemini` Vertex AI account with a service account | `service_account` is the credential; `project_id` and `location` (default `us-central1`, as in today's core) are the account's `vertex` settings. `interactions` or `native_interactions` set to JSON `true` at the top of the file is the account's `interactions` setting. |
| `antigravity` | `pluxy/antigravity` account | `access_token`, `refresh_token`, `expired`; `project_id` stays with the credential and `base_url` is an account setting. The file holds no OAuth client secret (today's core has it built in, and so does the extension), so the account renews on its own, as it does in today's core. |
| `xai` | `pluxy/xai` Grok login account | `access_token`, `refresh_token`, `expired`; `token_endpoint` stays with the credential and `base_url` is an account setting. |
| `meta` | `pluxy/meta` Muse Code login account | `api_key` (else `access_token`) is the minted key and `dca_token` (else a `dca:` `access_token`) the device token that mints a new one; `base_url` stays with the credential. A file with only the device token mints its key on the first request. |
| `devin` | `pluxy/devin` account with a session token | `session_token` (else `api_key`) is the credential; `base_url` is an account setting and `user_name` the label when the file has no `label` or `email`. |
| anything else | Skipped, named in the output | Today's core does not load a file with no `type` or with `gemini` either. |

From every file: `disabled: true` imports the account disabled, `priority` and `weight` are the
account's, `disable_cooling` (or `disable-cooling`) is its `disable_cooling`, `request_retry`
(or `request-retry`, a number or numeric text; a negative one inherits routing's) is its
`request_retry`, and `label` (else `email`) is its label. A file's `prefix` is the account's `prefix`,
its `excluded_models` (a list, or one comma-separated string) its `excluded_models`, and the
entries of its `model_aliases` (or `models`) with a `name` and an `alias` its `model_alias`, as
today's core reads them. A file's `proxy_url` and `headers` are not supported yet and are named
in the output.

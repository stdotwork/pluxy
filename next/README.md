# next: the new pluxy core

A new core for pluxy, built from scratch to replace the old core, the Go proxy that was in `core/`.
(In this README and in the corpus, "today's core" and "the old core" mean that core, and `core/...`
paths are files in it. The folder has been removed from the repository, so those paths name files
you cannot open here.) Core receives client
requests and hands them to **provider extensions**: separate programs, one per kind of upstream, that core
starts and talks to over a local socket. Today it serves OpenAI Chat Completions, OpenAI
Responses, Anthropic Messages and Gemini GenerateContent (streamed and not, over HTTP) through the
OpenAI-compatible API-key provider, which talks to an upstream that speaks any of them. A request
in the upstream's own format passes through unchanged; a Responses, Messages or Gemini request to a
Chat upstream, a Messages or Gemini request to a Responses upstream, and a Gemini request to a
Messages upstream are translated, and what the translation could not carry is logged. The
[Codex provider](#codex) serves OpenAI's Codex models from an API key, a ChatGPT subscription or a Sign in with ChatGPT (SIWC) sign-in.

This file is the reference for core. To build pluxy and send a first request, follow the
[quick start](../README.md#quick-start); to point a coding tool at it, see
[docs/clients.md](../docs/clients.md).

## Layout

| Path | What it is |
| --- | --- |
| `cmd/pluxy` | The core program. |
| `cmd/pluxy-pack` | Builds each extension's release archives and the bundled registry index; packs the desktop app's native tools with `-app-tools`. |
| `cmd/pluxy-bench` | Measures the time and memory pluxy adds between a client and its upstream, against a mock upstream, and checks them against budgets in CI ([docs/perf.md](../docs/perf.md)). |
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
| `extensions/devin` | Provider extension for Devin: the models a Devin account gets, signed in as Devin's CLI signs in or with a session token, over Devin's protobuf API; `devintest` is a fake Devin for its tests. |
| `examples/scripted` | The tutorial's example extension. |
| `examples/oauth` | An example provider that signs in by device code, browser (PKCE) or API key and renews its tokens, with a fake OAuth provider for tests (`oauthtest`). |
| `management` | The management API's contract ([README](management/README.md)), its request and reply types, and a client; the `pluxy` command's subcommands use it. |
| `internal/host`, `internal/server` | Core's extension host and HTTP API (client formats and the management API). |
| `internal/catalogue` | Embedded `models.dev` snapshot of model limits and capabilities, refreshed daily into `state/catalogue.json`. |
| `internal/cred` | Core's credential store and renewal of accounts added by login. |
| `internal/private` | Core's private folders and files (credentials, extensions' settings, logs): 0700 and 0600 on Linux and macOS, an owner-only access list on Windows. |
| `internal/packages` | Registries, release archives and the record of installed packages. |
| `internal/panel` | Serves the web panel at `/panel/`, embedded in core's binary. |
| `internal/pages` | Serves extension pages on a site of their own ([extension pages](ui/extension-pages/README.md)). |
| `internal/logfile` | Core's [log files](#log-files): `main.log`, its rotation, the folder's size cap, reading lines back. |
| `internal/usage` | The [usage record](#usage-statistics): one event per request, kept in memory and written asynchronously to daily JSONL files. |
| `internal/extconfig` | Extensions' settings: the schema from the manifest, checking values, and the store core keeps them in. |
| `ui/` | The UI kit the panel and the desktop app share, the panel's browser build, and the [theme contract](ui/themes/README.md) ([README](ui/README.md)). |
| `app/` | The desktop app: the kit's second build in a Tauri window, with a tray icon and three ways to reach core ([README](app/README.md)). Linux only for now; its agent clients and the record of the old desktop app's features are in [PARITY.md](app/PARITY.md). |
| `app-tools/` | The desktop app's optional native tools: agent client setup, the launcher, the usage hub and its parsers, the Codex session reader, and the libraries they share ([README](app-tools/README.md)). |
| `e2e/` | Black-box tests that drive the built core over HTTP. |
| `corpus/` | Differential test corpus: cases replayed against this core and compared with today's core's recorded replies, with listed exceptions ([README](corpus/README.md)). |

Go extensions build each upstream client's transport with `sdk.Upstream(base)`, where `base` is
their configured `*http.Transport`. This includes login, token refresh and WebSocket handshake
clients. The wrapper sends a request through the account's proxy and with its extra headers
and payload rules, records it when core asks for a capture, and sends a request tracked with
`sdk.TrackSend` on a connection of its own; see `sdk.Upstream`. When `base` sets `MaxIdleConns`
and not `MaxIdleConnsPerHost`, the wrapper sets the per-host limit to `MaxIdleConns`, so a burst
of concurrent requests to one upstream keeps its connections for the next one. Build requests with the
handler's context: it carries core's cancellation and `sdk.UpstreamInfo(ctx)`, which exposes the
account ID and settings, request model and native format, and operation, without the credential.

## Run it

```bash
go generate ./internal/panel   # builds the web panel core embeds; needs Bun (skip it for a core without the panel)
go run ./cmd/pluxy-pack -out dist   # packs the provider extensions and their registry index for this platform
go build -o pluxy ./cmd/pluxy
go build -ldflags "-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=1.2.3 -X github.com/stdotwork/pluxy/next/internal/buildinfo.Commit=$(git rev-parse HEAD) -X github.com/stdotwork/pluxy/next/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o pluxy ./cmd/pluxy   # sets the build info; without -ldflags the version is dev, and a git checkout still records its commit and time
```

`config.json`:

```json
{
  "listen": "127.0.0.1:8400",
  "client_keys": ["choose-a-client-key"],
  "management_key": "choose-a-management-key"
}
```

```bash
./pluxy serve -config config.json -bundled dist/bundled/index.json
```

`pluxy-pack` gives a source build the same provider packages a release ships. Without
`-bundled` (or a `bundled` folder beside the program), core has no registry: Setup has no
providers to offer and Extensions has nothing to install. Add Account has no sign-in methods
unless the config names an extension directly. After starting core, open Setup in the panel and
tick the providers you use, or install one by ID from the terminal with
`./pluxy setup apply --pick pluxy/claude`.

`pluxy -config config.json -bundled dist/bundled/index.json`, with no subcommand, serves too.
With no file at the config's path, `pluxy serve -bundled dist/bundled/index.json` writes a first
config there and starts on it: it listens on `127.0.0.1` port 8317, or a free port when 8317 is
taken, with one new client key and a new management key, in a file only its user can read. Core
logs the file's path and the address, never the keys; read them in the file. `pluxy init
[-config file] [-port n]` writes the same config without starting core (`-port 0`: a free port),
and never replaces a file. A test that starts a first core sets `PLUXY_INIT_PORT=<port>` to choose
that port instead, so a test never takes 8317 on the machine it runs on (`0` is a free port); a
value that is not a port stops core, and no real run sets it. A config with no management key is
refused at start, with a message saying how to add one. A first start installs no package: install
the providers you use from the bundled registry ([Packages](#packages)) or the panel. Flags:

- `-config`: the config file (default `config.json`).
- `-version`: print the build's version, commit and build date on one line, `pluxy <version> (<commit>, <date>)`, and exit. A build that recorded no commit or date (one made with `go run`, with `-buildvcs=false` or outside a git checkout) says so instead: `pluxy dev (no commit or build date recorded)`. It does not read the config. Core logs the same line when it starts.
- `-safe-mode`: start with no extensions: client requests fail, the management API works.
- `-watch-interval`: how often core looks for changes to the config file; `0` turns the watch off (default `1s`).
- `-bundled`: the index of the bundled packages (default `bundled/index.json` beside the program, when it exists); see [Packages](#packages).
- `-listen-fd`: serve on the listening TCP socket inherited at this file descriptor instead of binding `listen` (which must still be set). A program that starts core, such as the desktop app in managed mode, binds the socket, hands it over and keeps its own copy, so no other program can take the port while core stops or after it exits.

A running core answers `curl http://127.0.0.1:8400/healthz` with `ok`.

The web panel is at `http://127.0.0.1:8400/panel/`: core's status,
accounts, extensions and themes, the pages extensions bring and their settings. Sign in with the
management key. Extension pages are served from a listener of their own, on another site than
the panel (`extension_pages` in the config); see [extension pages](ui/extension-pages/README.md).

An account may list `models` it serves; without a list it serves the models its extension lists
for it, as today's core serves an account the models of its provider's catalog, and any model
when the extension has no list for it. Accounts can rename,
exclude and prefix models, and clients list the models on `GET /v1/models`: see
[Model lists and model names](#model-lists-and-model-names). Core runs the
extension the config names and every [installed package](#packages) that is enabled, and spreads
requests over all their accounts: see [Accounts and routing](#accounts-and-routing).

The OpenAI-compatible extension also adds accounts through the management API with method
`api_key`. Its inputs are `api_key` (secret), `base_url` (required unless the account already has
one), optional `wire_api` (`chat`, `responses`, `messages` or `gemini`; default `chat`), and
optional `quota` (`openrouter`, `deepseek` or `none`).
`base_url` includes the API path, such as `https://api.openai.com/v1` or a Gemini host's `/v1beta`.
The extension trims its trailing slash, checks the key with `GET {base_url}/models` using the
selected API's authentication header, and returns the URL and wire API as non-secret account
settings. Each key is a separate account; several keys for one upstream use the same URL.
The key is kept only in core's credential store, never written to `config.yaml` or shown in
management replies. A re-login needs only `api_key` and uses the account's existing settings.
A refused key keeps the upstream's reason with secrets removed. The check accepts any 2xx
reply from the model-list URL, so a wrong URL that answers 2xx is accepted too, as with Claude
and Gemini key checks. Configured accounts continue to work as before.

An account's `settings` may set `"wire_api": "responses"` for an upstream that speaks OpenAI
Responses (`POST {base_url}/responses`), or `"wire_api": "messages"` for one that speaks Anthropic
Messages (`POST {base_url}/messages`, with the key in `x-api-key` and the client's
`anthropic-version` and `anthropic-beta` headers), or `"wire_api": "gemini"` for one that speaks
Gemini GenerateContent (`base_url` ending in `/v1beta`; `POST {base_url}/models/{model}:generateContent`
or `:streamGenerateContent?alt=sse`, with the key in `x-goog-api-key`); the default is `"chat"`. A
Responses account serves Responses, Messages and Gemini clients; a Messages account serves Messages
and Gemini clients; a Gemini account serves only Gemini clients for now.

A request translated to Chat Completions gets the tool names that API takes: ASCII letters,
digits, `_` and `-`, at most 64 bytes. Any other name is sent with each other character as `_`
and cut, two names that would come out the same each get a hash of the client's name, and the
reply's calls go back to the client under its own name. A replayed call whose arguments are not a
JSON object is sent as `{}` and reported as an approximated loss; a Chat client's custom tool call
keeps its free-form input.

**Codex tools on other upstreams.** A Responses client's tools that a Messages, GenerateContent or
Chat Completions upstream has no form for are sent as functions, on every account that translates
to one of those formats, and the upstream's calls go back in the form the client declared:

- A custom tool (`apply_patch`, say) is a function with one required string argument, `input`, and
  its grammar moves into the description. Its call comes back as a `custom_tool_call` with the same
  events the Responses API sends, but only when its arguments are exactly one whole `{"input":"..."}`
  object (an empty string counts). Any other call never reaches the client as a call, because Codex
  runs a finished call whatever its status says, so a command cut off by the token limit would run:
  it has no done event and is not in the final output. A reply that stopped on the token limit or a
  content filter stays `response.incomplete` and reports the call as dropped. On any other finish
  the reply fails with an upstream error that names the tool and does not repeat its arguments.
- A namespace's tools are functions named `namespace__tool`. A name that already has that prefix or
  starts with `mcp__` keeps its name, and so does a tool in the Codex client's default namespace,
  `functions`. Calls come back with their name and namespace. The namespace's description goes on
  its first tool, since a function has no group to describe.
- An `additional_tools` input item's tools count as the request's.
- A `tool_search` the client runs (`execution: "client"`) is a function, and its call comes back as
  a `tool_search_call`. Tools marked `defer_loading` stay off the request until a
  `tool_search_output` in the conversation loads them. A `tool_search` the provider runs has no
  counterpart on these upstreams: it is reported as dropped, and the tools it would have loaded
  are declared from the start.
- Replayed calls and outputs of these tools go up as function calls and results.
- When names are the same once flattened, one tool stays and the others are reported as dropped:
  the `tools` list comes before an `additional_tools` item, a top-level tool before a namespace's,
  and then the first declared.
- A `tool_choice` or `allowed_tools` entry that names one of these tools names its function.

Each target's name limits apply to the flattened name, and the client still gets its own name back.

**Files and images** are sent in the form each upstream takes: an image by URL, by inline data or
by file id, and a file by URL, inline data or file id, wherever the target has that form. An
inline image is sent only when its data is base64 and its media type is an image type the target
takes (any `image/*` on the Chat Completions and Responses wire, whose vendors take types OpenAI's
own list does not name, and Claude's and Gemini's documented lists); otherwise it is dropped and
reported, so `drop_policy` `"reject"` refuses the request. Gemini takes a file URL only as a Files
API URI; any other URL, a Cloud Storage (`gs://`) one included, is dropped and reported, never
fetched. A Messages plain-text document has no block in the other three formats, so its text goes
as a text part between lines naming its title, reported as an approximated loss.

A Chat Completions reply's reasoning is read from `reasoning_content`, `reasoning` or
`reasoning_details` (OpenRouter's list, whose `reasoning.text` and `reasoning.summary` entries are
taken and whose encrypted entries are not shown), in that order from the first field that has any,
so the same text in two fields is shown once; `reasoning_content` may be a list of parts. A
reasoning run that opens the reply's content in a `think` tag, and its closing tag, becomes
reasoning as well, including when a tag is split across stream chunks, while a tag later in the
text stays text. A `refusal` on the message or a delta reaches a client in another format as the
reply's text and finishes the reply as a content filter, which a Messages client gets as
`stop_reason` `"refusal"`; a Chat client whose reply came from a Chat upstream gets it in
`message.refusal` or `delta.refusal`, with `content` left out and the upstream's finish reason
kept. A Chat client whose reply came from a Chat upstream gets the choice's `logprobs` back, on
each chunk or joined into the choice's when the reply is not streamed.

A Chat or Responses account sends a reasoning effort the upstream may not take as the nearest one
it does, as today's core does: `"reasoning_levels"` in its `settings` lists the efforts the
upstream accepts (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`), by default `low`,
`medium` and `high`, so `xhigh` and `max` go as `high`, `minimal` and `none` as `low`, and `auto`
as `medium`. Today's core refuses `minimal` with a 400 instead. A client that asked for an effort the
account moved is told in the translation report: an approximated loss naming the effort asked and
the effort sent.

A Chat account shapes the requests it sends with three more settings, for upstreams stricter than
OpenAI's own. `"developer_role"` is `"developer"` (the default) or `"system"`, the role a developer
message goes as. `"string_content"`, when true, sends a message whose content is only text parts as
one string. `"reasoning_passback"` decides what happens to the `reasoning_content` of earlier assistant
turns: true sends it back (for DeepSeek's thinking mode with tools, which answers 400 to a turn
without it, a blank one is sent for a turn that kept none, reported as an approximated loss; a
request without tools or with `thinking` disabled is left alone), false sends none, and unset leaves
what the client sent. A setting left unset takes its vendor's preset by `base_url` host, and a
setting you give always wins: `api.deepseek.com` takes `developer_role` `"system"`,
`string_content` true and `reasoning_passback` true (its messages have no developer role, a system or
assistant message's content is a string, and thinking mode wants the reasoning back);
`api.mistral.ai` takes `developer_role` `"system"` and `reasoning_passback` false (its messages have
no developer role and an assistant message takes no `reasoning_content`). Other hosts, OpenRouter and
Groq included, have no preset: their documentation names nothing they need shaped.

A request from another client format carries the client's service tier and verbosity only to a host
whose documentation names the field on that wire, and only a value that documentation lists (a tier in the
Chat Completions or Responses create reference's list, a verbosity of `low`, `medium` or `high`; any other is
dropped and reported): `api.openai.com` on the Chat Completions and
Responses wires (both), `api.anthropic.com` on the Messages wire (the service tier: `auto` and
`default`; the priority tier needs fast mode's beta header, which this extension does not add, so it
is dropped) and `generativelanguage.googleapis.com` on the Gemini wire (the service tier). Any other
host, Mistral and OpenRouter included, gets neither on a translated request, which reports each as
dropped at the client's field (and the `reject` drop policy refuses it), since an upstream may refuse a
field it does not list. A client's own body goes upstream as it came, whatever the host.

For an OpenAI-compatible account whose vendor offers a usage or balance endpoint for a normal API
key, `GET /v0/management/accounts/{id}/quota` reads it: OpenRouter's `GET /api/v1/key` (reporting
the plan `free` or `paid` from `is_free_tier`, the key's USD spending limit and remaining allowance
`limit`, the daily, weekly and monthly USD spend `usage_daily`, `usage_weekly` and `usage_monthly`,
and `free_model_daily_requests` when present) or DeepSeek's `GET /user/balance` (reporting `available`
or `unavailable` from `is_available` and each currency's `total_balance` from `balance_infos` as
`balance_<currency>`). An account's `settings` (or its `api_key` login input) may set `"quota"` to
`"openrouter"`, `"deepseek"` or `"none"`; when unset, the extension picks by `base_url`'s host
(`openrouter.ai` for OpenRouter, `api.deepseek.com` for DeepSeek) and answers unsupported for any
other host.

Responses compaction (`POST /v1/responses/compact` and `POST /backend-api/codex/responses/compact`)
is served on OpenAI-compatible accounts by `wire_api`: a `"responses"` account passes the client's
body through to `POST {base_url}/responses/compact` unchanged with the account's key header and
returns the upstream's reply as it came (a 404 from that path that does not say the model is unknown
means the upstream has no compaction endpoint: it fails that request only, as a request error with a
message that says so, and blocks no model on the account); a `"chat"` account translates the Responses conversation to
one non-streamed `POST {base_url}/chat/completions` request with `tool_choice` set to `"none"`,
structured output and stop sequences cleared, and the Codex CLI's compaction prompt (`prompt.md`)
appended as a last user message, and returns OpenAI's `response.compaction` object carrying one user
message with the Codex CLI's `summary_prefix.md` followed by a newline and the summary (`reasoning_content`
is excluded, and an empty or cut-off summary is an error on the request); a `"messages"` or
`"gemini"` account answers unsupported so core tries another account.

The Images API (`POST /v1/images/generations` and `POST /v1/images/edits`) is served on
OpenAI-compatible accounts whose `wire_api` is Chat or Responses, for any model the account lists.
Core routes the request by its model,
and the extension sends it to `POST {base_url}/images/generations` or
`POST {base_url}/images/edits` with the account's bearer key and the model renamed for the
account. A generation is sent as JSON. Core turns a multipart edit into its JSON form before the
extension sees it, with uploaded files as data URLs; the extension rebuilds multipart from that
form, keeping every field value and file byte, and sends it with a new multipart boundary. A JSON
edit stays JSON. The upstream's reply body and content type come back as they came, including
server-sent events for a streamed reply.

An OpenAI-compatible upstream may lack a side endpoint. A 404 from
`POST {base_url}/responses/input_tokens`, `/embeddings`, `/messages/count_tokens`,
`/models/{model}:countTokens`, `/images/generations` or `/images/edits` that does not say the model
is unknown fails that request only, as a request error that names the endpoint and path, and blocks
no model on the account, as for `/responses/compact` above. A 404 that says the model does not exist
still blocks it, and so does a 404 from the generation path. A token count that gets this 404 is an
error for the client, not an estimate.

Gemini clients call `POST /v1beta/models/{model}:generateContent` or `:streamGenerateContent`
(a stream is server-sent events, with or without `alt=sse`), and `:countTokens`.

Once a streamed reply has started, core writes the client the SSE comment `: keep-alive` whenever
15 seconds pass with nothing written to it, so a client or proxy does not drop a reply while the
model thinks. It does this on `/v1/chat/completions`, `/v1/completions`, `/v1/responses`,
`/backend-api/codex/responses` and `/v1/messages`, and on an SSE reply from
`/v1/responses/compact`, `/backend-api/codex/responses/compact`, `/v1/alpha/search`,
`/backend-api/codex/alpha/search`, `/v1/images/generations` or `/v1/images/edits`. No Gemini
client route gets one, including `/v1beta/models/{model}:streamGenerateContent` and
`/v1beta/interactions`: most SSE readers skip a comment, but Google's Python SDK reads every line
as JSON and fails on one, and Google's API never sends them. Replies that are not streamed and
WebSocket sessions get none.

Token counts (Anthropic's `POST /v1/messages/count_tokens` and Gemini's `:countTokens`) come from
the upstream's own count endpoint: Anthropic's for a Claude account on Anthropic's own API (base URL
`https://api.anthropic.com`, the default) or an OpenAI-compatible account with `"wire_api": "messages"`, Kimi's Messages count endpoint for a Kimi account, Gemini's for a Gemini account (Gemini API or Vertex AI) or one with
`"wire_api": "gemini"`. A count from an upstream of the client's own kind comes back as the upstream sent it;
otherwise core answers with the count in the client's shape. An account whose upstream has no count
endpoint (Codex, xAI, Meta, Devin, and Chat or Responses accounts), and a Claude
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
upstream that counts (a 400, 401 or 429) stays an error, and so does a refusal under
`drop_policy` `"reject"`.

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
key check. The bare `/responses` (and its WebSocket) is the same alias, for clients such as Droid's
OpenAI provider that post to it.

When an upstream ends a Responses stream with a `response.completed`, `response.incomplete` or
`response.failed` whose `output` is empty, core fills it with the items the stream's
`response.output_item.done` events finished, as today's core does.

A reply's reasoning tokens are written where each format's API has a place for them:
`completion_tokens_details.reasoning_tokens` on Chat, `output_tokens_details.reasoning_tokens` on
Responses, `output_tokens_details.thinking_tokens` on Messages (at most `output_tokens`) and
`thoughtsTokenCount` on Gemini. A Messages upstream's own
`output_tokens_details.thinking_tokens` is read into the same count, though Anthropic's
`output_tokens` already includes them; today's core writes that field only for a Responses
upstream. A count the upstream did not report is left out of the reply rather than written as 0,
except where the API requires it. Chat's usage carries `prompt_tokens`, `completion_tokens` and
`total_tokens` whenever it has any count, a missing one as 0, and so does a Response with
`input_tokens`, `input_tokens_details`, `output_tokens`, `output_tokens_details` and `total_tokens`;
a usage with no counts at all is left out, and Messages requires `input_tokens` and `output_tokens`.
Gemini's `usageMetadata` has no required field, so its unknown counts stay out.

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
who reaches it when it has no client keys. A missing or wrong client key gets a 401 with
`X-Should-Retry: false`, in every client format and in the error message of an open Responses
WebSocket; an upstream's own 401 does not carry that header.

The example config's client keys (`your-api-key-1`, `your-api-key-2` and `your-api-key-3`) are
public, so as in today's core (its example API-key safe mode) core starts with one in
`client_keys` but refuses every client request, with a 403 whose code is `unsafe_example_api_key`,
until none is left; it logs that at every load, and the management API stays open, so the keys
can be replaced there. The refusal goes to a request that carries a configured key; a request
without one gets the 401 it always gets, where today's core answers every request with the 403.

`GET /healthz` is the health probe for a container or load balancer. It answers `200` with the
text `ok` and needs no key. It is not affected by the panel, CORS, trusted proxies or the
management lockout, writes no request log, and with `tls.enable` it is served over HTTPS too.
`HEAD /healthz` gets the same status and headers with no body. Any other method does not get a
`200`.

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

A reply to a request that may use the client API carries `X-CPA-TRACE-ID`, as today's core sends it:
the time and a random part, such as `20261109061500-3f9a0c2e5b7d4e18`. Core's log lines about the
request (a failure, what translation lost) carry the same ID and the account, so a reply can be
matched with them. Today's core sends it only once it has chosen an account; this core sends it
also when no account was tried (every account cooling down, a body that does not parse, no
account for the model). A request refused for its client key (401, 403) gets none. A Responses
WebSocket's handshake carries one for the socket.

A reply to a request that went to an account also says which: `X-Pluxy-Account` (the account's
public ID, below), `X-Pluxy-Provider` (its extension, such as `pluxy/codex`), `X-Pluxy-Model` (the
name sent upstream) and `X-Pluxy-Attempts` (how many accounts the request tried). An error from
an upstream's refusal carries them too: they name the last account tried. A WebSocket's handshake
has none of them, as its turns choose their accounts later.

An account's **public ID** is what replies name it by: `acc_` and 8 hex digits, a keyed digest of
the account's ID. The ID an account has in the config or the store can be an email address or a
label someone typed (an import of today's auth files names an account by its file, which holds the
email address), so it never leaves core in a reply, and no error `message` names an account either (a message says "the account was removed"; the header and the member say which). The digest's key is made at the first start and
kept in `state/account-key` beside the config, so an account keeps its public ID across restarts
and a reload; deleting the file gives every account a new one. `GET /accounts` and `pluxy account list`
show each account's `public_id`, and a request may pin an account (`X-Pluxy-Account`, below) by it.

A request that ends in an error without a reply says what was tried and why the rest were left
out, in a `pluxy` member of the error object, in the OpenAI, Anthropic and Gemini formats alike (a
Responses WebSocket turn's error message has it too):

```json
{"error": {"code": "model_cooldown", "message": "...", "type": "rate_limit_error",
  "pluxy": {"trace": "20261109061500-3f9a0c2e5b7d4e18",
    "tried": [{"account": "acc_4e26e0c2", "provider": "pluxy/codex", "model": "gpt-5", "result": "quota"}],
    "left_out": [{"account": "acc_306dcbdb", "state": "exhausted", "until": "2026-11-09T17:00:00Z"},
                 {"account": "acc_0702c008", "state": "banned"}]}}}
```

`tried` lists the attempts in order; `result` is the code of the error the attempt ended with (such
as `rate_limit`, `quota`, `auth`, `account_closed`, `upstream`). `left_out` lists the accounts that
serve the model and were not tried: `state` is the account's health (`cooling`, `exhausted`,
`banned`, `needs_login` or `disabled`), `busy` when it was at its `max_concurrency`, or `skipped` when
the request's limits ended it before the account's turn; `until` is when the block ends, when known.
An account that is turned off or waits for a login is named when its own `models` name the model, or
when it shares its extension with an account that serves it. The member holds codes and times,
never the words of an upstream's reply. `routing.explain` `false` leaves out the `X-Pluxy-*` headers
and the member, and keeps the trace ID.

An upstream's error keeps the fields the client's format shares with the upstream's. A Messages
client keeps `request_id` and the text `error_code` of `error.details` from a Messages upstream
(the rest of `details` is not forwarded, since it can identify the upstream's organization), a Chat or Responses client
keeps `param` from an OpenAI-format upstream, and a Gemini client keeps `details` from a Google
upstream; no field is copied into a format that does not define it. `param` is the upstream's own
path, so on a translated request it can name a field in the upstream's shape. A 429, 503 or 529
carries `Retry-After` when the upstream said how long to wait. A Google upstream's error names the
Google project, organization, folder, billing account or person behind the pluxy account (in its
message, and in its details); the Gemini extension — and the OpenAI-compatible extension for a
`googleapis.com` host or a Google-shaped error — replace those names with `[redacted]` in what a
client gets (a `QuotaFailure` subject and a `ResourceInfo` owner whole in their own field, and in
an Interactions reply only the error), and keep the rest, so `RetryInfo` and the quota's metric,
ID, value and dimensions still read. The owner's own account check and quota reading keep the
names.

When an upstream says the prompt does not fit the model's context window, the client gets it in
the shape it acts on: status 400 with code `context_length_exceeded` on Chat, Responses and the
Responses WebSocket; a 400 `invalid_request_error` whose message says `prompt is too long` on
Messages; a 400 `INVALID_ARGUMENT` on Gemini. The Claude, Codex, Gemini and OpenAI-compatible
extensions recognise it by that code or by the upstream messages LiteLLM's exception mapping
lists, and a Gemini 500 by the words `input context is too long`, the cause Google's error table
gives for 500 `INTERNAL`. It fails that request only: it blocks no account or model and is not tried on another
account.

`pluxy route <model>` (`GET /v0/management/route?model=<name>`) says what a request for the model
would do now without sending one: the accounts in the order it would try them (the step of the
model's fallback chain, the account's priority, then the strategy's turns) and those it would leave
out, as above. For a model with a chain the reply has the chain (`chain`) and each account its step
(`step`, from 1). Asking changes no routing state.

`drop_policy` (top level) says what happens when translating a request to the upstream's format
would drop part of it: `"report"` (the default) sends it and logs what was dropped, `"reject"`
refuses it with a 400 that names what could not be carried.

`token_count_estimate` (top level, default `true`) says what a token count gets when an account
for the model cannot count: `true` answers with core's estimate, `false` answers with an error.
Today's core has no setting for this: it always estimates for those accounts.

A config written for today's core, with its key names, works too: see
[config.yaml with today's key names](#configyaml-with-todays-key-names).

### Pack the extensions

`pluxy-pack` builds every extension under `extensions/` as a release archive and writes the
bundled registry index an install ships with:

```bash
go run ./cmd/pluxy-pack -out dist -platforms linux/amd64,darwin/arm64
```

It writes one `dist/bundled/<publisher>-<name>-<version>-<goos>-<goarch>.tar.gz` per extension
and platform, and `dist/bundled/index.json`, which pins each archive's SHA-256 and size. Packing
the same sources twice gives byte-identical archives and index. `-platforms` defaults to the host
platform; `-extensions codex,claude` packs only those extension folders, by folder name or
package ID. `go run ./cmd/pluxy-pack -h` lists the flags.

With `-app-tools <folder>` it packs the desktop app's native tools under `app-tools/` instead
(not the test fixture `usage-fixture`), each for the platforms among `-platforms` that its
manifest names. It builds nothing: Rust tools are built on each platform's own runner, and it
takes each program from `<folder>/<goos>-<goarch>/<program file name>`, failing when one is
missing. It writes `dist/app-tools/<publisher>-<name>-<version>-<goos>-<goarch>.tar.gz` and
`dist/app-tools/index.json`, an index for the app's device registries (no `protocol`). The app
installs a native tool only with a curated signature, which this does not add, so nothing
publishes these yet; `.github/workflows/next-app-tools-pack.yml` builds and packs them to check:

```bash
go run ./cmd/pluxy-pack -out dist -app-tools programs -platforms linux/amd64,windows/amd64
```

The index also says what each package is, from its manifest: `title`, `summary`, `kind`
(`provider`, or `client` for the app's tools that point a coding client at core) and `tier`
(`pick` for the packages a new install is offered, `verified` for the rest of pluxy's own; the
list of picks is in `cmd/pluxy-pack`). Core installs none of them by itself: start it with the
bundled index and install what you use:

```bash
go build -trimpath -ldflags "-s -w" -o pluxy ./cmd/pluxy
./pluxy -config config.json -bundled dist/bundled/index.json
pluxy ext available                 # what the bundled registry offers, with titles
pluxy ext install pluxy/claude      # install and enable one
```

`pluxy` itself is a plain `go build` of `./cmd/pluxy`, as above; the `-ldflags` only make it
smaller. Without `-bundled`, core looks for `bundled/index.json` beside the `pluxy` program, so
an install is the program with the packed `bundled` folder next to it. The same `index.json`,
uploaded beside the archives to an `https://` location, is a registry like any other: its
relative archive URLs resolve against the index.

### Run it in a container

`Dockerfile` (build context: this folder) builds an Alpine image with a static `pluxy`, the web
panel built in, and every extension packed by `pluxy-pack` as the bundled packages, which it
installs only when asked (`pluxy ext install`, or the panel). It runs as user `65532`, not root.

```bash
docker build -t pluxy .
```

Core takes no settings from its environment: everything is set in `config.json`, and everything
core keeps is in one folder: `/config`, a volume. It holds `config.json` and, beside it, the
folders core makes and fills: `credentials`, `packages`, `logs` and `state` (routing state, the
setup scan, stored themes, the model catalogue, and the keys public account IDs and client-key
IDs are made with). Make a folder with a config file before the first start (with an
empty `/config`, core writes a first config that listens on the container's own `127.0.0.1`:
unreachable from outside, and the health check fails); `listen` must be `0.0.0.0:8400`, since the container's own `127.0.0.1`
is not reachable from outside it, and the image's health check asks port 8400. Inside a container,
requests through a published port come from Docker's network address, not from `127.0.0.1`, so
the management API refuses them unless `management_allow_remote` is `true` (it still needs the
management key). Publish the port on `127.0.0.1` only, as below, unless you want other machines
to reach it:

```json
{
  "listen": "0.0.0.0:8400",
  "client_keys": ["choose-a-client-key"],
  "management_key": "choose-a-management-key",
  "management_allow_remote": true
}
```

```bash
mkdir config   # put config.json in it
sudo chown -R 65532:65532 config   # the image's user must be able to write to it
docker run -d --name pluxy -p 127.0.0.1:8400:8400 -v "$PWD/config:/config" pluxy
```

[`compose.example.yaml`](compose.example.yaml) runs the same with Docker Compose, also with a
read-only root file system (`/tmp` is a `tmpfs`) and no added privileges.

The image's `HEALTHCHECK` asks `http://127.0.0.1:8400/healthz` every 30 seconds. With another
listen port or with `tls.enable`, replace it (`docker run --health-cmd ...`, or `healthcheck:` in
Compose). Extension pages are served on a port of their own, by default a free port on the host
of `listen`, which Docker does not publish: to use them from a container, set
`extension_pages.listen` to a fixed port such as `0.0.0.0:8401`, publish it, and set
`extension_pages.url` (see [extension pages](ui/extension-pages/README.md)).

A release (a `next-v*` tag, built by `.github/workflows/next-release.yml`) uploads every
extension's archives for Linux (amd64, arm64), macOS (amd64, arm64) and Windows (amd64) and a
`registry.json` to its GitHub release, and pushes the image to `ghcr.io/stdotwork/pluxy`. Core
knows no hosted registry by default and does not fetch from the network until asked: to install
from a release's packages, add its `registry.json` under `packages.registries`, for example
`https://github.com/stdotwork/pluxy/releases/download/next-v1.2.3/registry.json`.

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
client's `User-Agent` and `originator` (turn off with `"codex_identity": false`). The client's
`metadata` is not sent (Codex's own request has none), an integer output budget under 16, which the
endpoint refuses, is raised to 16, and a reasoning effort the model does not take (by the model's
levels in the Codex catalog) is changed to the nearest level it does, below the effort when there is
one, and reported as a loss. A Claude Code request's prompt cache key follows its session and agent
(`X-Claude-Code-Session-Id` and `X-Claude-Code-Agent-Id`, or the session in `metadata.user_id`). Chat
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
| `image_generation_off_models` | Default none: model names, or patterns with `*` and `?` (any case), that are not offered the `image_generation` tool. A tool the client lists itself stays. |
| `codex_client_version` | Default `0.159.0`: the version in the Codex client's `User-Agent` when `codex_identity` is on, and in the ChatGPT model list's `client_version`. |
| `prompt_cache_retention_passthrough` | Default `false`: send the client's `prompt_cache_retention` on, for an upstream that takes it (neither today's core nor the Codex client sends it to the ChatGPT backend, and a SIWC sign-in never sends it). |
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
`"stream": true` is refused with a 400, as today's core refuses it. xAI and Claude accounts serve it
too (see their sections); an account of any other extension does not, so a model only those serve
gets an error.

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

A Responses-lite client's stream, and a Responses-lite WebSocket turn, skip the `id` restoration, as
today's core does: the final event's output is sent as the upstream sent it. A lite WebSocket turn
on a Codex account also keeps an empty `output` empty; an empty `output` on a lite HTTP stream is
still filled from the finished items, as today's HTTP handler does. A lite request that is not
streamed gets the repairs.

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
`Chatgpt-Account-Id` or routing hint; `codex_identity`, `codex_client_version`, `image_generation_tool`,
`image_generation_off_models` and `prompt_cache_retention_passthrough` do not apply,
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

**Quota.** For a ChatGPT sign-in account, `GET /v0/management/accounts/{id}/quota` reads `GET {backend}/wham/usage`, where `{backend}` is the account's `base_url` without a trailing `/codex` (default `https://chatgpt.com/backend-api`), with the access token, the `Chatgpt-Account-Id` header and the same `User-Agent` and `Originator` the account's requests carry (so `codex_identity` applies). It also reads `GET {backend}/subscriptions?account_id=...` for `active_until` (reported as optional `plan_ends_at` in Unix seconds), and `GET {backend}/wham/rate-limit-reset-credits` for `available_count` (the `reset_credits` limit, labelled "Manual resets", with `remaining` in `resets`). Both extra reads use the same identity; a failed extra read leaves its field out without failing the usage read. Nothing spends a reset. It reports the plan (`plan_type`), the 5-hour and weekly or monthly windows (`five_hour`, `weekly`, `monthly`), code review's windows (`code_review_*`), each extra limit such as one model's (`<name>_five_hour` and so on) and the credit balance (`credits`), with the share used, reset time and window length. An API key or a SIWC account answers unsupported. A sign-in's grant carries `identity` `{"email", "account_id"}`: members of one ChatGPT workspace share its account ID, so the e-mail address tells them apart.

A request in another client format carries the client's service tier and verbosity to Codex
(Responses' `service_tier` and `text.verbosity`) only where the account's base URL host is
`api.openai.com` or `chatgpt.com`, the two defaults (a custom `base_url` may be a third-party host
that refuses a field it does not list, so it gets neither, reported as dropped at the client's
field), and then as the routed model's entry in the client catalog (`client_models.json`) allows, as
the Codex CLI filters on the same data: a service tier only if the entry lists it (`fast` counts as
`priority`; no model lists `ultrafast`), a verbosity only if the entry says `support_verbosity`, and
a verbosity only if it is `low`, `medium` or `high`. A model the catalog does not list gets neither.
`auto` and `default` are Codex's standard service: they are omitted and report no loss. Any other
tier Codex cannot take, and a verbosity it cannot take, is reported as dropped at the client's field
(and the `reject` drop policy refuses the request). A Responses client's own body goes as it came,
through the endpoint's own filter, which keeps `priority` (`fast` too) and `ultrafast`. The
subscription's routing hint names the tier sent.

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
| `model_level_cooling` | `true` keeps a used-up shared subscription window (5-hour or 7-day) on the model that hit it, so the account's other models stay in use; default `false` blocks the whole account. Today's `claude.model-level-cooling`. |
| `cache_ttl` | Lifetime of `cache_control` markers pluxy adds to translated requests that set none: `"5m"` (default) or `"1h"`. |

A Messages request goes to Anthropic unchanged: its body, so thinking blocks, signatures and
`cache_control` markers arrive as the client wrote them, and its `anthropic-version`,
`anthropic-beta` and identification headers (`User-Agent`, `X-App`, the `X-Stainless-*` headers of
Anthropic's SDKs, `X-Claude-Code-Session-Id`, `X-Client-App` and
`X-Anthropic-Additional-Protection`; and, only from a request that names itself as Claude Code
(`X-App: cli` or `cli-bg`, a `claude-cli/` User-Agent and the `claude-code-20250219` beta), Claude
Code's subagent, prompt, request-class and remote-session headers such as `X-Claude-Code-Agent-Id`,
`X-Claude-Code-Prompt-Id` and `X-Claude-Remote-Session-Id`, as today's core forwards them only
from Claude Code). An API key goes in `x-api-key`; a
subscription's token or a setup token goes as a bearer token, with the `oauth-2025-04-20` beta added
after the client's betas. Chat Completions, Responses and Gemini requests are
translated to Messages; a Chat client's own `cache_control` markers (top-level, on tools, on
`tool_calls`, and on messages and parts) are carried over, a Responses client's
`prompt_cache_retention` sets the added markers' TTL (`"24h"` as `"1h"`, `"in_memory"` as `"5m"`),
and their reasoning effort becomes adaptive thinking with that effort on
Claude 4.6 and later, and a thinking budget from Claude 3.7 to 4.5 (low 1024, medium 8192, high
24576 tokens); a model name the extension does not know as a Claude model gets no thinking
settings, and the effort is reported as dropped. A Responses client's `reasoning.summary` becomes
`thinking.display` (`auto`, `concise` and `detailed` as `summarized`, `none` as `omitted`). A
client's `json_schema` response format goes as `output_config.format` (`json_object` drops as
unsupported), a Messages client's `output_config.format` carries over to other formats, and a
content-filter stop from another upstream becomes `stop_reason` `"refusal"`.

**Thinking settings.** Every request, a Messages client's own included, has its thinking settings
made ones the model takes, by Anthropic's per-model tables, and each change is reported:

- Effort `xhigh` goes as `xhigh` from Claude 4.7 on, and as `max` to Claude 4.6, which has no
  `xhigh`; `minimal` goes as `low`.
- Thinking off (`none`, or `thinking` `disabled` or `between_tools`) goes as each model turns it
  off: Fable, Mythos and Opus 5.5 always think, so it goes without `thinking`, at effort `low`
  unless one is set; Sonnet 5.5 takes `between_tools`; Opus 5 and Haiku 5.5 take `disabled` at
  effort `high` or below, so a higher effort goes as `high`; older models take `disabled`.
- A thinking budget goes to Claude 4.7 and later as adaptive thinking at the effort it falls in.
- A forced tool choice (`any` or a named tool) goes as `auto` to Opus 5.5, Sonnet 5.5, and Fable
  and Mythos 5.1 and later, which refuse one; with a thinking budget it goes without thinking.
- `top_k` is left out on Claude 4.7 and later, and on older models while thinking is on.
- With a thinking budget, a tool result whose assistant turn has no thinking block, in a
  conversation that has none, goes with adaptive thinking on Claude 4.6 and without thinking on
  older models. On Claude 4.5 and older, a conversation that ends on an assistant turn loses that
  turn while thinking is on, and with thinking off loses the reasoning it ends on. The extension never changes how a client presents itself: a client that is
not Claude Code is not made to look like Claude Code.

**Sampling options.** A translated request's `temperature` and `top_p` reach Claude only where
the model takes them, by Anthropic's documented limits for the `anthropic-version` translated
requests are sent with (`2023-06-01`):

- Claude 4.7 and later: one of them alone is sent only at its default (`temperature` 1, `top_p`
  0.99); when both are set, neither is sent.
- A `claude-` model name the extension does not know as a Claude version (such as
  `claude-mythos-preview`): neither is sent.
- An older Claude model with thinking on: `temperature` is sent only at 1, `top_p` only from 0.95.
- An older Claude model, with thinking on or off: `top_p` is left out when `temperature` is sent
  too, since Claude refuses the two together.
- Any other model name: both go as the client sent them.

The model name is read in lower case, without a Bedrock prefix (`anthropic.`, or `us.`, `eu.`,
`apac.` or `global.` before it), a context window in brackets (`[1m]`) or a Vertex AI version
after `@`; the same reading decides the thinking settings above, so Claude Mythos models get
thinking from a reasoning effort too. Each one left out is reported as dropped, so
`drop_policy` `"reject"` refuses a generation request, or a token count sent to Anthropic's own
API, that would lose one. A Messages client's own sampling settings go upstream as it wrote them.
Today's core leaves both out of every translated request, on every model.

Every request, a Messages client's or a translated one, also gets the betas its body needs, after
the client's own and without repeats, as today's core adds them: `advisor-tool-2026-03-01` for a
tool whose type starts with `advisor_`, `extended-cache-ttl-2025-04-11` for a block- or tool-level
`cache_control` marker with `ttl` `"1h"` (not on a token count),
`thinking-binding-controls-2026-08-01` for `thinking.block_binding`,
`mid-conversation-tool-changes-2026-07-01` for `tool_addition` or `tool_removal` blocks, and
`inline-tools-2026-09-15` when a `tool_addition` defines a tool inline (`tool_definition`). A top-level `betas` list in the body, which Messages does not take there,
is moved into `anthropic-beta`. The client's own betas are never removed. Not built: today's
`anthropic-beta-strip` and `anthropic-beta-append` settings, and the Claude Code beta profile
today's core sends for clients it makes look like Claude Code.

**Web search.** A Responses client's `web_search` tool becomes Claude's server web search tool
(`web_search_20250305`, with its `name`, `max_uses`, `filters.allowed_domains` and `user_location`;
none when `external_web_access` is `false`), as today's core sends it. Claude's `server_tool_use`
and `web_search_tool_result` blocks come back as one `web_search_call` item with the query, status
and results (each hit as Claude sent it, so its `encrypted_content` survives); a search Claude left
without results when it paused the turn (`pause_turn`) stays `in_progress`. A replayed
`web_search_call` goes back as the two blocks, in the assistant message around it; a paused search
at the end of the history goes back as its `server_tool_use` alone, so Claude carries on. Chat
clients get the text without the search blocks, as today. Claude's `citations` on the answer's
text are not passed on, and a replayed message's `annotations` are not sent back to Claude. Today's
core copies each citation as it is into the text's `annotations`, in Claude's own shape
(`web_search_result_location`), which is none of the annotation types Responses defines, so a
Responses client has no defined way to show it. Claude does not need citations in the history: the
replayed search blocks carry the results. Not built: the web search result cache and Codex's tool
search on Claude.

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
characters becomes `cpa_tid_v1_` and the ID in URL-safe base64, the same ID the same way; one
whose base64 form would pass 64 bytes becomes the prefix and a SHA-256 digest of the ID instead,
so every ID stays within the 64 bytes Anthropic's upstreams take.

**Fast mode.** A request whose body has `"speed": "fast"` (a Messages client's own, or a Responses
client's `"service_tier"` of `priority` or `fast`, which becomes it on any `base_url`; at Anthropic's
own API (`https://api.anthropic.com`) so does any other client format's priority tier, Chat
Completions' `priority` or `fast` and Gemini's `"serviceTier": "priority"`; `auto`, `default`,
`flex` and an absent tier never do) gets the `fast-mode-2026-02-01` beta in `anthropic-beta`, which
Anthropic requires with that field, also on a token count. A request that names that beta itself,
in its `anthropic-beta` header or in the `betas` list of its body, is a fast-mode request too, as in
today's core. A 429 saying fast mode needs usage credits is the request's: it blocks nothing and
the request is not moved to another account, since no account fares better for it and the account
serves other requests. On Anthropic's own API (`https://api.anthropic.com`), every failure of a
fast-mode request is the request's in the same way, except a refused shared usage window, as in
today's core.

A 429 is read as today's core reads Anthropic's rate-limit headers. A refused shared 5-hour or
7-day window (`anthropic-ratelimit-unified-5h-status` or `-7d-status` `rejected`, or the
aggregate `anthropic-ratelimit-unified-status` `rejected` for another reason than below) is a
quota refusal that blocks the whole account (only the model with `model_level_cooling`). A
refusal of only a model's own weekly window (`7d_oi`) or of overage, while the shared windows
have room, and any other 429, block only that model on the account, so its other models stay in
use. The block lasts until the latest reset among the refused windows and `Retry-After`, when
that is in the future and at most 7 days and an hour away (a later one is a billing boundary);
otherwise it backs off. A model's own window or overage refusal waits for none of those resets,
and an aggregate reset equal to overage's billing reset is not waited for. A refusal of a
per-minute limit (one of Anthropic's exact per-minute messages, no quota status header, one
`Retry-After`) waits for its `Retry-After` alone. When the reset kept is the 5-hour or 7-day
window's, the quota refusal carries `quota_window_minutes` 300 or 10080. Unlike today's core,
no random 1 to 30 seconds are added to the wait. A 529 (overloaded) moves the request to another
account.

**Compaction.** `POST /v1/responses/compact` (a Responses client's request to shorten its
conversation, which the Codex CLI sends) is served on Claude accounts through the protocol's
endpoint operation, with Anthropic's [on-demand
compaction](https://platform.claude.com/docs/en/build-with-claude/compaction-on-demand). The
Responses body is translated to Messages as for `/v1/responses` and sent once, not streamed, with
`"compaction": {"type": "summarize"}` and the `compact-2026-09-04` beta, and without what
Anthropic refuses on a compaction request: stop sequences, structured output, a `tool_choice` of
type `any` or `tool`, and `context_management`. A conversation that ends on the model's reply is
sent with it, which a compaction request takes although a generation on Claude 4.6 and later would
refuse it as a prefill. The reply is OpenAI's compaction reply, one JSON object
(`"object": "response.compaction"`), whose `output` is Claude's summary as one user message that
starts with the Codex CLI's own summary prefix, the shape the Codex CLI keeps in place of the
conversation when it compacts on its own side; its `usage` is the summarization call's (Anthropic's
`usage.iterations`). Anthropic's signed compaction block is not passed on, so the next request
can go to any account and needs no beta. A reply without a summary (a `stop_reason` other than
`compaction`, such as `max_tokens` or `refusal`) is a 502 for that request, which blocks
nothing. Every other endpoint call is answered as not served, so core tries another account.

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
reasoning between turns. A translated request's function names are the ones GenerateContent
takes: ASCII letters, digits, `_`, `.`, `:` and `-`, at most 128 bytes, and starting with a
letter or `_` for Vertex AI. Any other name is sent with each other character as `_` and cut, two
names that would come out the same each get a hash of the client's name, and the reply's calls go
back to the client under its own name. Gemini's function arguments are a protobuf Struct, so an
integer parameter can come back as a double (2500.0); on a translated reply the extension writes
it as an integer (2500) where the request's tool schema declares that property integer, nested
objects and array items included, and leaves a number, an unknown schema, a fraction and a value
beyond 2^53 as they came. A function call with no signature gets the value Google documents for
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

**Compaction.** `POST /v1/responses/compact` (also `POST /backend-api/codex/responses/compact`) is
served on every Gemini account (Gemini API keys, Vertex AI service accounts and express mode keys,
without needing `interactions`). The Responses request is translated to `GenerateContent` with the
extension's schema, turn and call-ID repairs, stop sequences and structured output cleared, tools
kept with `toolConfig` in `NONE` mode so the model answers in text while keeping tool declarations
for any tool history, and the Codex CLI's compaction prompt (`prompt.md`) appended as a user
turn. The request runs once, not streamed; thought parts are excluded, and the summary comes back
as OpenAI's `response.compaction` object carrying one user message with the Codex CLI's
`summary_prefix.md` followed by a newline and the summary. An incomplete or empty summary fails as
a non-retryable request error.

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

**Quota and identity.** Google offers no quota reading or rate-limit headers for Gemini API keys or Vertex AI accounts, so `quota` reports the last-seen values from the account's most recent HTTP 429 reply (`google.rpc.QuotaFailure`, `google.rpc.RetryInfo` or HTTP `Retry-After`, and `google.rpc.ErrorInfo`), kept in extension memory only (nothing is written to disk) and labelled with when the 429 was seen and that it is not a live reading; an account that has not received a 429 answers with an empty limit list stating that no rate-limit reply has been seen yet. A `service_account` login's grant carries `identity` `{"client_email"}`, the key's service account; an API key login has none.

A request in another client format carries the client's service tier to the Gemini API as
`serviceTier` (`standard`, `flex` or `priority`; any other tier is reported as dropped) only where
the account's `base_url` host is `generativelanguage.googleapis.com`, and none to a custom host or
Vertex AI, whose `generateContent` request has no such field; the client's verbosity has no place in
either.

## Kimi

The Kimi extension (`extensions/kimi`) serves Kimi Code, Moonshot AI's coding subscription. Point
`extension.manifest` at `extensions/kimi/pluxy-extension.json`. Accounts are added by login
through the management API (below) with method `device`: the reply's prompt carries a code and
Kimi's page to enter it on, and the login finishes once it is approved there. The sign-in is the
OAuth device authorization grant (RFC 8628) at Kimi's login host, with the public client of Kimi's
own CLI, since Kimi registers no other clients. Core renews the token five minutes before it
expires. A Kimi Code API key can be added with method `api_key` and secret input `api_key`:
the extension checks it with `GET {base_url}/v1/models` before storing it in core's credential
store. Refused keys report Kimi's reason with secrets removed. A key also works as an account
in the config:

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
each Chat tool call, and a Responses batch of function call outputs moved next to its calls. A
translated request's tool names are the ones Chat Completions takes, and the reply's calls go back
to the client under its own name. Each
request names pluxy in Kimi's device headers, with the device ID made at login (none for a key); the device's host
name, which Kimi's CLI also sends, is left out. A 429 that says the quota is used up is reported
as a used-up quota, not a rate limit.

**Thinking replay.** Kimi's Messages API wants an assistant tool-use turn back with its signed
thinking, and some Messages clients resend earlier turns without it. As today's core does, the
extension keeps each Messages reply that has signed thinking and a tool call, and when a later
Messages request resends that turn without thinking but otherwise exactly as Kimi sent it, puts the
kept thinking back. What is kept belongs to one client key (the request's protocol `caller`), one
Claude Code conversation (its `X-Claude-Code-Session-Id` header, else the session in
`metadata.user_id`) and one model family; a request without a client key or a session is sent as
written. Kept turns expire an hour after their last use; at most 10,240 turns and 256 MiB (session IDs included) are kept,
and a turn over 8 MiB or 512 blocks is not. Kimi refusing a request whose thinking was put back
(400, 422 or an error in the stream) drops that turn. The turns are kept in the extension's memory,
so they are lost when core stops the idle extension (after 10 minutes without requests) or
restarts it. Unlike today's core, which keeps only a conversation's latest reply, every kept turn
stays until it expires, so Claude Code's subagents, which share their conversation's session ID,
do not replace each other's turns. Chat clients are not covered, as in today's core: a Chat tool
call gets Kimi's `reasoning_content` from the request itself (above).

**Token counts.** A count (`POST /v1/messages/count_tokens`, or Gemini's `countTokens`) goes to
Kimi's own count endpoint, `{base_url}/v1/messages/count_tokens?beta=true`, as today's core sends
it, for every account. A Messages client's body goes as for generating (Kimi's model name, no
billing block), with its Messages headers; any other client's request is rebuilt as Messages with
the fields a count takes. The client's `anthropic-beta` gets `token-counting-2024-11-01` added.
Kimi's count comes back as it is; an error from Kimi reaches the client, with no estimate in its
place.

**Quota.** For a device login, quota reads `GET {base_url}/v1/usages` and the plan from `GET {base_url}/v1/me` (`user_level_name`) on the account's Kimi Code host, with the access token and the same device headers (`X-Msh-*`, `User-Agent`) the account's requests carry. It reports each entry of `limits` (labelled by its name, else by its window length, else "Limit #n"), the usage summary ("Weekly limit") and the monthly ratios ("Monthly limit", "Monthly coding limit"). An API key answers unsupported. Kimi's device login returns no user ID, so its grant has no identity and a re-login is not checked, as in today's core.

A request in another client format goes to Kimi without the client's service tier and verbosity,
which are reported as dropped at the client's field: nothing says Kimi's API takes them. A client's
own body goes as it came.

## xAI

The xAI extension (`extensions/xai`) serves xAI's Grok models with an xAI API key or a Grok
account. Point `extension.manifest` at `extensions/xai/pluxy-extension.json`. An API key can be
added with method `api_key` and secret input `api_key`, or from the config with the key as the
credential. The login checks `GET {base_url}/models` and keeps an accepted key only in core's
credential store; a refusal reports xAI's reason with secrets removed:

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
| `inject_x_search` | Default `false`. Add xAI's native `x_search` tool when the request does not already have one, except when a forced hosted web search or image generation tool must be the only choice. Applies to API keys and Grok logins. |

Every client format goes to xAI's Responses API. A Responses client's request passes through with
the changes listed below. Chat Completions, Messages and Gemini clients are translated to
Responses, and what the translation cannot carry is reported with the reply. An API key's
User-Agent is `pluxy-xai/<version>`, with no Grok CLI header.

What changes in a request:

- The stream flag follows the client's: a request that is not streamed is sent to xAI with
  `"stream": false`, and its reply is read as one response.
- `stop`, `previous_response_id`, `prompt_cache_retention`, `safety_identifier` and
  `stream_options` are removed. Today's core removes them on this path too; it does not record why.
- A function's local `$ref` references are inlined and its `$defs` and `definitions` are removed.
  A function without parameters gets an empty object schema. A request with more than 200 tools
  keeps the first 200, the most xAI accepts in one request.
- A custom tool call and its output are sent as a function call and a function call output.
- Every custom (freeform) tool the client declares, at the top level, in a namespace or in an
  `additional_tools` item, is sent as a function whose one required string argument, `input`, holds
  the tool's text, and xAI's function call to it goes back to a Responses client as a
  `custom_tool_call` with that text as its `input`, in the same events as `apply_patch` below.
  A custom tool's `format` is dropped and put in the function's description (a grammar, as the
  client wrote it; a text format adds nothing), after the tool's own description and a line that
  tells the model to put the text in `input`. A history call to such a tool carries its text in
  `input`, also when the text is itself a JSON object, and a `tool_choice` or an `allowed_tools`
  entry of type `custom` that names it names the function. When a name is declared more than once
  the first declaration stays, as for `apply_patch`. This differs from today's core, which sends
  a custom tool other than `apply_patch` as a function with an empty-object schema and returns a
  `function_call`: a client cannot run that call for a tool it declared custom (the Codex CLI ends
  the turn with an "incompatible payload" error), and the schema gives the model no field for the
  text. A function tool the client declared stays a function, and xAI's own `x_search` calls,
  which come back as custom tool calls, are left as they are.
- Codex's `apply_patch` custom tool is one such tool with its own description. It is sent as a
  function whose one argument, `input`, holds the patch text: its description tells the model so
  and keeps the client's grammar, its history calls carry the text in `input` (a call whose input
  is not a string is an invalid request, as for any custom tool), and a
  `tool_choice` that names it names a function. A request that declares `apply_patch` more than
  once keeps the first declaration. xAI's function call to it goes back to a Responses client as
  the custom tool call it expects: an added item, input events carrying the patch text as it
  arrives, the end of the input, the done item and the final response's output all show a
  `custom_tool_call` with the text as its `input`. The events keep xAI's sequence numbers. Arguments
  that are not one `input` string end the reply with an upstream error that does not repeat them,
  as do arguments of more than 16 MiB for one call or 32 MiB for the calls of one reply, white space
  included (the extension keeps the patch text and not the arguments). A call that has its
  arguments but no done event is finished when the reply ends, and is in the final output.
  Only a Responses client can declare a custom tool, so only its reply is turned back; today's core
  does not turn a Chat Completions client's `apply_patch` custom tool back either.
- Namespace tools (from the Codex app, `{"type":"namespace","name":N,"tools":[...]}`) are sent as
  functions, because xAI has no such tool. When the tools flattened are no more than 200, each
  child is a function named `N__child` (a name that has the prefix, or starts with `mcp__`, keeps
  its name), and `tool_search` children are left out. The count includes the `x_search` tool that
  `inject_x_search` adds, as today's core counts it. With more than 200, each namespace is one
  function named `N` whose arguments are `{"name": child, "arguments": {...}}` and whose
  description lists the children with their schemas; folded namespaces come first when the tools
  are cut to 200. A function call in the history that names a namespace, and a `tool_choice` that
  does, are written the same way. xAI's call to such a function goes back to a Responses client as
  the call to the child in its namespace, in the added item, the done item and the final response;
  a folded namespace's call is unwrapped when its arguments name one of the namespace's children,
  and stays a call to the namespace otherwise. A namespace declared more than once is one
  namespace with the children of every declaration, each as its first declaration has it. A call
  or a tool choice in a namespace goes to the namespace's function only when the namespace really
  was folded, not to a plain function that has its name. Where a name repeats, the first tool stays.
  An `apply_patch` function in a namespace is an ordinary function, not the patch tool.
  A custom `apply_patch` in a namespace, declared in the tools or in an `additional_tools` item, is
  the patch tool of that namespace: it is sent as the patch function (`ns__apply_patch`, or as a
  child described in the folded namespace's function), its history calls carry the text in `input`,
  a `tool_choice` or an `allowed_tools` entry that names it names that function, and xAI's call to
  it goes back as a `custom_tool_call` named `apply_patch` in its namespace, with the same events as
  the top-level tool. Any other custom tool in a namespace is handled the same way: it is sent as a
  function and comes back as a `custom_tool_call` in its namespace. A name belongs to its namespace,
  so a top-level `apply_patch` and a namespace's are two tools. When one name is declared more than
  once, as `ns__apply_patch` is for the child `apply_patch` of `ns`, the winner is the first, and a
  tool of the tools list beats one of an `additional_tools` item, and a top-level tool beats a
  child, as in today's core. For a folded namespace the child is known only from the call's
  arguments, so the events of a call to that namespace are held until they name it, only for a
  namespace that has a custom tool. A call to a custom tool then shows the added item and the whole
  input at the end of the arguments, with no input fragments, because the fragments are the
  wrapper's, not the tool's text, and the client is never sent a `function_call` event for it. A
  call to another child shows the same events as before, a little later, with the child's name in
  its added item. A stream that opens with such a call, before `response.created`, ends with an
  upstream error, since its first event cannot be held.
- The tools of an `additional_tools` input item (how a Responses Lite client, the Codex app,
  declares tools) are moved to the end of the tools list and the item is removed, because xAI does
  not take that item. This happens before the rules above, so a namespace in an item is flattened
  or folded, counted toward the 200 tools and restored in the reply like one declared at the top
  level, and a `tool_choice` that names a tool of an item is written the same way. The first
  declaration of a function or custom tool name stays: a top-level tool beats one of an item, and
  an earlier item beats a later one. A namespace of an item with the name of a top-level namespace
  is one namespace with the children of both, each as its first declaration has it, as in today's
  core. Items that carry no tools are removed too.
- The Codex app's `automation_update` tool (in the `codex_app` or `codex_apps` namespace) is sent
  with parameters that accept any object, and `strict` false: with its own schema, a large `oneOf`
  with `$ref` references, xAI accepts the request and sends no events.
- `store` is false unless the client set it: xAI keeps responses by default, and with
  `previous_response_id` removed a kept response is never used again. `include` asks for
  `reasoning.encrypted_content`, which carries a reasoning model's thinking to the next turn
  without stored responses.
- The `x-grok-conv-id` header is set to the request's `prompt_cache_key`, when the request has one.

**X Search.** A client's native `{"type":"x_search"}` tool and its options go to xAI as
written. With `inject_x_search`, the extension adds that tool once and also allows it in an
`allowed_tools` selection. The final tools list keeps its first 200 entries. For requests using
X Search, the extension hides its internal `x_user_search`, `x_semantic_search`,
`x_keyword_search` and `x_thread_fetch` call traces, including their streamed deltas, and
renumbers the remaining output indexes. The answer, citations, hosted search items and usage
are kept. Client functions with those names are kept unless their call ID starts with
`xs_call`; namespaced client calls are kept. Filtering applies to both JSON and streamed
upstream replies and to every client format.

Errors: 401, and a 403 whose body says the credential is bad, are authentication errors. Other 403
replies are permission errors. A 429 that says the free usage is used up is a quota error, which
holds the model back on the account for 24 hours. Other 429 replies are rate limits and keep xAI's `Retry-After`.
A 404 is a model error, and a 5xx is an upstream error.

**Compaction.** `POST /v1/responses/compact` is served through the protocol's endpoint
operation for API keys and Grok logins. It goes to `{base_url}/responses/compact`, using the
official API when the base URL is the Grok CLI chat proxy: that proxy has no compaction endpoint
and returns 404. A login uses its access token with standard API headers, without CLI identity
headers. An explicit non-CLI base URL is kept. The request removes `stream`, `tools`,
`tool_choice`, `max_output_tokens`, `temperature`, `top_p`, `top_k` and `stop`, removes
`compaction_trigger` input items, and keeps a nonempty `previous_response_id`. As in today's
core, compaction leaves `store` and `include` to the client. The upstream's JSON reply comes
back whole, with usage recorded. Streaming is refused. An endpoint 404 is a request error
and creates no account or model cooldown; a 404 naming a missing model is scoped to that model.

Not built, and why:

- The root-union schema rule of today's core (a function whose schema is a `oneOf` or `anyOf` with
  `$ref` or non-object branches gets the permissive schema, and a union branch without a type gets
  `object`) is built only for the Codex app's `automation_update`, which is what hangs xAI.
- Today's core sends an empty `instructions`, a string input as a list, and tool defaults
  (`tool_choice`, `parallel_tool_calls`, `strict`, `additionalProperties`) on every Responses
  request, through its Codex translator. These are not rules about xAI, so this extension does not
  send them. Whether xAI needs any of them is not verified: the tests do not reach xAI.
- A session ID for `prompt_cache_key` and `x-grok-conv-id` when the client sends none. Today's
  core derives one. This extension has no session identity to derive it from.
- Checking the reasoning effort against each model's levels, and a default effort. Both need
  today's model catalog.
- Image references, encrypted reasoning content, websockets, text to speech, image and video
  generation.
- For a Grok login: reading the Grok CLI's latest version from npm every 3 hours (set
  `client_version` when xAI asks for a newer CLI), the file name and legacy-file adoption today's
  core uses for its auth files, and opening the browser at sign-in.
- Today's core answers `GET /v1/models` from the Grok CLI (a `grok-shell` User-Agent) with its own
  entry shape (`api_backend`, context window, reasoning efforts). The Grok CLI reads a proxy's model
  list only when its models endpoint is set to the proxy; today's desktop app instead writes each
  model into the Grok CLI's config with its own base URL, so the list is not read there. pluxy's
  model data has no reasoning efforts to fill that shape. The Grok CLI gets the ordinary OpenAI
  model list.
- Today's core turns an upstream Codex `keepalive` stream event into an SSE comment for the Grok
  CLI, which ends the turn on an event type it does not know. pluxy's Responses stream passes on
  only `response.*` and `error` events from the upstream, so no `keepalive` event reaches any client
  (a test in `format/openairesponses` pins this). pluxy sends no comment in its place: a long
  upstream silence reaches the client as silence.

**Quota.** A Grok login's quota is what the Grok CLI shows: `GET {cli}/billing?format=credits` and `GET {cli}/billing` on the CLI's chat endpoint (the host the account's requests go to), and `GET {cli}/user?include=subscription` and `GET {cli}/settings` for the plan's name, with the login's token and the Grok CLI identity headers the requests carry. It reports the weekly credit window (`weekly`), the monthly credits (`monthly_credits`), the pay-as-you-go cap and use (`pay_as_you_go`) and the prepaid balance (`prepaid`), in `usd`. Nothing that spends from the account is sent (the old panel sent a one-token chat for paid accounts; that is not rebuilt). An API key answers unsupported. A Grok login's grant carries `identity` `{"sub", "email"}` from its ID token.

A request in another client format goes to xAI without the client's service tier and verbosity,
which are reported as dropped at the client's field: nothing says xAI's API takes them. A Responses
client's own body goes as it came.

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

A request in another client format goes to Meta without the client's service tier and verbosity,
which are reported as dropped at the client's field: nothing says Meta's API takes them. A Responses
client's own body goes as it came.

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
text and base64 images. A tool result given as content blocks keeps its text; its images and
error flag are reported as dropped. A user turn left
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

A refusal with an HTTP error status takes its message from a Connect error body's `message`, and
an error in the end frame takes the frame's. Both are cleaned, as is the code when it is not one
of Connect's, and the error's raw body is the body or frame rebuilt from the cleaned code and
message only, so its other fields are dropped. Unlike today's core, which passes any other body
on as it is, the extension passes such a body on only as the message, only when it read the body
whole (under 1 MiB, and not broken off), and cleaned. The cleaned text is one line, cut after
1000 characters with an ellipsis. Replaced by `[redacted]` are: every run of token characters
(letters, digits and `_-.+/=~`) holding 4 or more consecutive characters of one of the account's
secrets, or 8 or more of a long token (see [Account checks](#account-checks)): its session token after the public `devin-session-token$` prefix, the values of its
secret headers, its proxy's user name and password and the secrets in its settings (4
characters all within a secret's public vendor prefix, such as `sk-proj-`, do not count; see
[Account checks](#account-checks)); `Bearer` and
`Basic` values; the values of fields named like `access_token`, `token` or `api_key`; and any
other run of 24 or more token characters. A run is also read with its escapes decoded (JSON's
`\/` and `\uXXXX`, `%XX`), so a piece written with escapes between its characters is replaced
too. Core removes whole copies of an account's secrets everywhere, and pieces of them by the same
rule from every extension's errors (see [Account checks](#account-checks)), but not
the other rules or the bound, so the extension keeps its own cleaning. Otherwise the message
names only the status (`Devin answered 500 Internal Server Error`), or, for a quota read whose
reply broke off, says that it broke off. The rule can also replace a word that shares 4 characters
(8 for a long token) with one of the secrets, such as `proxy` for a proxy user name `proxy-user-1`.

Not built, from today's core: removing Claude Code's and Codex's attribution lines from the system
prompt and `devin.sensitive-words`; rewriting tool descriptions; encoding tool call IDs (they pass
through as given); a session's turn count (each request is a new session, as today's core sends
for a client that names none); a used-up quota's reset time; the remote model catalog refresh; Devin's names for older models
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

A model name is served by the accounts of one provider: the provider (extension) of the
highest-priority account that serves the name. On a tie, the account that comes first decides:
the accounts of the `extension` section, then those of the `extensions` sections in order of
extension ID, then the accounts added by login, in order of account ID. Accounts
whose model list could not be read serve any model, but they decide the provider only when no
account lists the name. Accounts that cannot serve now count in this choice too: a banned or
blocked account, one turned off and one waiting for a new login keep the name on their provider,
and the error of a request they cannot serve names them with their state in `left_out`; only a
change to accounts, priorities, weights or chains moves a name to another provider. Under the
`weighted` strategy an account with `weight: 0` takes no part: it neither serves the name nor
decides its provider. When another provider
serves the same name too, its accounts take none of these requests, however blocked the first
provider's are; a fallback chain (below) is how a name moves to another provider or model.

An account is eligible when it serves the model, no block covers it and it is under its
concurrency limit. Refusals block what they are about, for as long as the upstream said
(`Retry-After`) or else by backoff: a rate limit or quota on one model (a plain 429, as today's
core cools only the model that answered) or on the whole credential (when the provider says the
account's allowance is used up, such as a subscription's usage window) backs off from 1 second,
doubling, to 30 minutes; a failing endpoint or other transient upstream failure back off from 1
second, doubling, to 1 minute; a refused key is left alone for 30 minutes, and a missing model for
12 hours. Results count in the order their requests started on the account: a success clears the
blocks that failures of earlier-started requests left on its account (its credential, its model
and its endpoint), and a failure that ends after a later-started request succeeded on the same
scope leaves no block and no last error. Clearing a block clears its backoff step too; a scope
with no live block for an hour is forgotten, and its step with it. Among eligible accounts, core
prefers the session's account (session affinity is on unless the config turns it off), then the
highest `priority`, then picks by strategy.

A reset the upstream names is used when it is between 1 second and 8 days away, or, when the
upstream also names the usage window's length, up to that length plus an hour (Codex's monthly
window, 28 to 31 days, resets a month off). A shorter one is held for 1 second. A longer one is
not believed: the account is held for 30 minutes and checked after 15. The upstream's newest word
replaces the block's end, sooner or later; a backoff core chose itself only moves a block later.
A rate limit or quota block on an account or one of its models with more than 15 minutes left is
checked every 15 minutes. The check reads the account's usage from its provider (the extension's
`quota` call). A usage window covers a block on the whole account when it counts every request on
the account, and a block on one model when it does that or counts that model's requests; a window
that counts other models only, or a feature of the plan such as code review, covers neither (the
extension says which; see `protocol/README.md`, "quota"). When no window that covers the block is
full, the block ends; otherwise it lasts until the covering full window that resets last resets. An account with no usage reading, such as an API key, lets one
request through at the check instead, and that request's outcome decides. A real request is never
the check where a reading exists. A check that cannot be made leaves the block and is made again
15 minutes later. Quota blocks are saved to `state/routing.json` beside the config file (mode
0600) and read back at start, where each is checked at once, so a restart sends no requests to
accounts whose usage ran out. Bans are saved in the same file and read back at start too: for
each, the account, the provider's reason and when it was banned. A saved ban whose account the
config no longer has is dropped, at start and on a reload; one for an account that is only out of
routing for now, such as an extension that is stopped or a login still due, is kept. A ban has no
expiry: it ends when the account signs in again, is removed, or is turned back on (`pluxy account
enable`), and `pluxy account clear` does not lift it. A file core cannot read, or that another
version wrote, is not loaded and is left as it is until a block must be saved; it is then moved to
`routing.json.bad`. `pluxy account clear` (`POST /accounts/{id}/cooldown/reset`) still clears an
account's blocks at once. Each account's `health` in `GET /accounts` sums this up in one word (see
`management/README.md`, "Accounts").

When every account is blocked, the reply tells the client how long is left in whole seconds,
rounded up and at least 1 while anything is left: the message's "try again in 1s" (or `1m1s`),
`Retry-After`, and the usage-limit reply's `reset_time`, `reset_seconds` and "reset after" all
come from the same rounding, so they agree. `retry_after_ms` keeps the exact wait. Today's core
rounds `Retry-After` and `reset_seconds` up, but shows `reset_time` and "reset after" to the
nearest second (at least 1), so with 1.2 s left it says `reset_time` "1s" next to
`reset_seconds` 2; core says "2s" in all of them. That is an intended difference.

```json
{
  "routing": {
    "strategy": "round-robin",
    "max_attempts": 0,
    "max_accounts": 0,
    "max_wait_ms": 0,
    "session_affinity": {"ttl_ms": 3600000},
    "chains": {
      "claude-opus-5-5": [{"model": "claude-opus-5-5", "provider": "pluxy/claude"}, {"model": "gpt-6.1-sol"}]
    }
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
| `strategy` | `round-robin` (default; takes turns after the account picked last), `weighted` (by `weight`, interleaved), `fill-first` (first eligible account in order), `least-active` (fewest running requests), `soonest-reset` (below). |
| `quota_refresh_ms` | For `soonest-reset`: how often core reads every enabled account's usage from its provider (the extension's `quota` call), in the background and never on a request's path; the first comes a minute after core starts. Default 1800000 (30 minutes); 0 turns the readings off, and accounts are then ranked only by the readings core takes when it checks a long block. Accounts whose provider has no usage reading (API keys) are never read. |
| `max_attempts` | Upstream attempts per request, counting the first. 0: no fixed limit; each account is tried once per round. |
| `max_accounts` | Distinct accounts one request may try in a round, as today's `max-retry-credentials` limits them; each round gets its own count, and an account the limit left out takes part in a later round. 0: no limit. |
| `max_wait_ms` | Longest a request waits, in total, for a blocked account; with `request_retry`, before each round. 0 (default): never; when every account is blocked the request fails at once with a 429 and `retry_after_ms`. A request never tries an account before the time the upstream gave. |
| `request_retry` | When set, rounds run as today's `request-retry` runs them: at most this many after the first, each trying every eligible account again. A round starts at once when no account is blocked, or else when the first block ends if that is within `max_wait_ms`; an account refused for rate or quota in the request waits at least 10 seconds; a refused key (401) or a missing model (404) starts none. An account's own `request_retry` counts for it instead. Unset (here and on every account): rounds are bounded by `max_attempts` and `max_wait_ms`, and each needs a wait. |
| `disable_cooling` | `true`: failures leave no block, so every account stays eligible. |
| `transient_error_cooldown_ms` | How long a failing upstream or endpoint (a 5xx or a broken connection) is blocked when it gave no `Retry-After`: 0 (default) backs off from a second to a minute, above 0 that long, below 0 not at all, `Retry-After` or not. |
| `quota_response_mode` | What a client gets when every account that serves its model is out of quota until a reset the upstream named, the first of them more than 5 minutes away: `retryable` (default) a retryable 429 with `Retry-After`, as for any block; `usage-limit` the final usage-limit reply below. |
| `explain` | `false` leaves out of replies the headers that name the account (`X-Pluxy-Account`, `X-Pluxy-Provider`, `X-Pluxy-Model`, `X-Pluxy-Attempts`) and the `pluxy` member of errors (see [Run it](#run-it)), for a core whose client keys go to people who should not see which accounts serve them. The trace ID stays. Default `true`. |
| `session_affinity` | On by default, with no setting needed: requests of one session go to the same account while it is eligible, so a coding agent's conversation keeps its provider-side prompt cache. `{"enabled": false}` turns it off; `{"ttl_ms": 1800000}` sets how long a session stays bound after its last request (default one hour, 3600000). The session comes from the `X-Claude-Code-Session-Id`, `Session-Id`, `X-Session-Id` or `X-Session-Affinity` header, or the body's `prompt_cache_key`; a request with none takes turns as usual. Bindings are per client key and model. A `config.yaml` keeps today's default, off (below). |
| `timeouts` | Upstream HTTP request timeouts in milliseconds: `header_ms` (default 60000) for response headers and `idle_ms` (default 300000) between raw upstream bytes on streamed requests, and `total_ms` (default 600000) in all on non-streamed requests; `0` turns a limit off. For an extension that speaks protocol 0.23, core also ends an attempt on which the extension sent it nothing at all for as long as the SDK may wait on the upstream plus a minute (the longer of `header_ms` and `idle_ms` when streamed, the longest of the three when not) with `extension_timeout`, which moves the request to another account when no reply has started; with any limit it needs at `0`, there is no such deadline. A timeout before any reply byte (this one or an upstream limit's) moves a request to one more account, once: a second timeout ends the request with that error, so a stuck upstream costs a client two limits and not one per account. |
| `chains` | Fallback chains, by the model name clients ask for (below). Default: none. |
| account `priority` | Higher first; lower accounts serve only while every higher one is blocked. Default 0. |
| account `weight` | For `weighted`. Default 1; 0 leaves the account out. |
| account `disable_cooling` | Overrides `disable_cooling` for the account. |
| account `max_concurrency` | Requests running at once on the account. 0: no limit. |
| account `request_retry` | Rounds after the first that the account takes part in, in place of `request_retry` (today's per-key `request-retry`): the account leaves a request's rounds past its own count, and a count above routing's runs more rounds with only the accounts whose count reaches them. Setting it on an account runs rounds even when routing sets none; accounts without one then take part in the first only. |
| account `endpoint` | Accounts naming the same endpoint share its outages. Default: the account's ID. |
| account `timeouts` | Overrides `routing.timeouts` (`header_ms`, `idle_ms`, `total_ms`) for the account; `0` turns a limit off. |

`soonest-reset` spends allowance that is about to reset before it is lost: among the eligible
accounts of the top priority, a new request goes to the account whose longest usage window (a
Codex or Claude account's weekly window) resets soonest, by its provider's own usage reading.
Reset times are rounded to the hour, so accounts whose windows end minutes apart tie and take
turns. Accounts with no known reset come after those with one, and an account whose reading shows
a full window comes last until that window resets; a blocked account is never picked, whatever
its reset. Priority comes first, and session affinity still keeps a session on its account.
`pluxy route <model>` shows the reset each account was ranked by. It is not the default.

A client can pin a request to one account with the `X-Pluxy-Account: <id>` header, naming the
account by its ID or its public ID (the one replies give). A pinned request never goes to another
account.


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

### Fallback chains

A chain lists, under a model name clients ask for, the steps a request for it goes through in
order: each step is a model, and optionally the one provider (`provider`, an extension's ID) and
the accounts (`accounts`, their IDs) that serve it. A step that names neither is served as a name
with no chain is: by one provider's accounts. A request uses the first step's accounts, by priority
and strategy as above, and moves to the next step only when no account of the step can serve it
now: each is blocked, at its concurrency limit, or refused the request in a way that is safe to try
elsewhere. Moving on needs no wait; when no step can serve now, the request waits, within
`max_wait_ms`, for the first account of any step to come back. It never moves once its reply has
started, nor for an error about the request itself (a 400, a refusal of its content), which would
end the same way on the next step. A session bound to an account stays on that account and its
step's model while it can serve, even once an earlier step could serve again.

A chain has at most 8 steps. A step names a model, never another chain (a step may name the
chain's own name: that is the model of the name), and no step repeats another. Two steps that come
to the same account and model (`m@pluxy/claude`, then `m` served by Claude) try it once. A chain's name may be one no account serves,
such as `my-fast`: it is then in the model lists, as its first served step's model.

| Call | Command | What it does |
| --- | --- | --- |
| `GET /v0/management/chains` | `pluxy chain list` | The chains, by name |
| `PUT /v0/management/chains/{name}` | `pluxy chain set <name> <step>...` | Sets the chain, `{"steps": [{"model", "provider", "accounts"}]}`, and writes it to the config file. A step on the command line is `model[@provider][:account,account]`. What follows the last `@` is the provider only when it is an extension's ID (`publisher/name`), so `claude-opus-4-7@20260416` is a model; a model whose name holds a colon ends with an `@` (`llama3:8b@`, or `llama3:8b@pluxy/ollama:a1` with a provider and an account). 422 for more than 8 steps, a step that names another chain, a step that repeats another, a step no account serves now, or a step that names an account core does not know or one that does not serve the step (named in the error; an account waiting for a login or turned off that serves it is accepted). Under the `weighted` strategy an account of `weight` 0 serves no step. A config.yaml cannot hold chains (409) |
| `DELETE /v0/management/chains/{name}` | `pluxy chain remove <name>` | Removes it: the name goes back to one provider's accounts. 404 when there is none |

The reply of a request served through a chain names the account, provider and model of the step
that served it (`X-Pluxy-*`), and its usage event the step (`chain_step`, from 1). An error's
`left_out` entries then also name each account's `provider` and `model`, since one account can
serve two steps; `pluxy route` shows the chain and the step of each account.

### Outbound proxy

`proxy_url` sends an extension's upstream traffic through a proxy (today's `proxy-url`): model
requests, WebSocket connections, token renewals and logins alike. Set it at the top level for
every account, or on an account for that one:

```json
{
  "proxy_url": "socks5://user:password@proxy.example:1080",
  "extensions": {"pluxy/codex": {"accounts": [
    {"id": "work", "proxy_url": "http://proxy.example:3128", "...": "..."},
    {"id": "home", "proxy_url": "direct", "...": "..."}
  ]}}
}
```

A value is an `http://`, `https://`, `socks5://` or `socks5h://` URL, with a user name and password
when the proxy needs them, or `direct` or `none` for no proxy. An account's own value wins; one set
to `direct` goes without a proxy even when the top-level one is set. A login that adds an account
uses the `proxy_url` it names, else the top-level one, so the first sign-in goes through the proxy
too. Any other value fails the load with a message. With a SOCKS5 proxy the proxy resolves host
names, for `socks5` as for `socks5h`. Extensions run with a cleared environment, so
`HTTP_PROXY` and `ALL_PROXY` never reach them; this is the only way to set a proxy.

The user name and password are secret: the management API shows the URL with `redacted` in their
place (`socks5://redacted@proxy.example:1080`), and they are removed from what extensions log and
send back. An extension older than protocol 0.11 cannot use a proxy, so core does not send it the
accounts that have one: their requests go to other accounts, and the error says why.

### Upstream headers

`headers` on an account adds HTTP headers to every upstream request of that account (today's
`headers`), such as the key a gateway or relay in front of the provider asks for:

```json
{"extensions": {"pluxy/openai-compatible": {"accounts": [
  {"id": "relay", "headers": {"X-Gateway-Key": "...", "X-Title": "pluxy"}, "...": "..."}
]}}}
```

They go on the same traffic as the [outbound proxy](#outbound-proxy): model requests and their
streams, WebSocket handshakes, token renewals, logins, model lists, checks and quota reads. A
configured header replaces the extension's own value for that name (letter case aside); the
extension's other headers stay. A value is sent as written: today's `$Header` (copy a header
from the client's request) and `$CPA-SESSION-ID` are not supported. A login that adds an account
uses the `headers` it names. `Host`, `Content-Length`, `Transfer-Encoding` and `Connection`, a
name that is not a valid header name or is given twice (letter case aside), an empty value and a
value with a line break fail the load with a message that names the header, never its value.
A redirect to another host or scheme (`https` to `http`) than the request's first one carries
the headers that are not secret (below) but not the secret ones, even one the provider set
itself under that name, as an HTTP client leaves out `Authorization` there.
`PATCH /accounts/{id}` with `headers` as an object replaces all the account's headers (one left
out is removed), and with `null` or `{}` removes them all.

The value of a header whose name is about authentication (it holds `auth`, `key`, `token`,
`secret`, `cookie`, `session`, `password`, `passphrase` or `signature`, letter case aside) is a secret, by the
rule config redaction uses: `GET /config` and `GET /accounts` show the same placeholder in its
place (that of the header core sends, also for a value of a provider section of a `config.yaml`
written as a number or with spaces around it), except for a value written as `true` or `false`,
which `GET /config` shows as written, as it shows `true` and `false` everywhere, while `GET
/accounts` shows a placeholder. A placeholder sent back keeps the value, and the value is removed
from what extensions log and send back. Core writes no header value in its own logs or errors. An extension older than
protocol 0.12 cannot send headers, so core does not send it the accounts that have them: their
requests go to other accounts, and the error says why.

### Payload rules

`payload` sets, fills in or removes fields of the JSON body an extension sends upstream (today's
`payload`), for the requests its rules name. The five lists and every rule field keep today's
names, so a `config.yaml`'s `payload` (or `requests.payload`) works as written:

```json
{"payload": {
  "override": [{"models": [{"name": "gpt-5.4-fast", "protocol": "codex"}], "params": {"service_tier": "priority"}}],
  "filter": [{"models": [{"name": "gpt-*", "protocol": "codex"}], "params": ["tools.#(name==\"apply_patch\")"]}]
}}
```

`default` and `default-raw` write a field the body does not have (the first rule that writes it
wins), `override` and `override-raw` write it over what is there (the last wins), the `-raw`
lists take JSON text, and `filter` removes fields. They run in that order on the body as the
extension finally sends it, once per upstream request: a request tried again on another account
gets them again on its new body, never twice on one. Each extension applies them as it sends;
core sends it only the rules that apply to the request.

A rule applies when any entry of its `models` does:

| Field | Matches |
| --- | --- |
| `name` | The model the request reaches the upstream as, or the one the client asked for (before an alias or prefix, with its thinking suffix or without it). `*` matches any text. |
| `protocol` | The extension: `openai` (`pluxy/openai-compatible`), `claude`, `codex`, `gemini` (Gemini API keys and Vertex), `kimi`, `xai`, `meta`; for another package the last part of its ID. Today's core used the upstream's API format here, so its `openai` also covered Kimi, which is `kimi` here. |
| `from-protocol` | The API the client called: `openai` (Chat Completions, legacy Completions), `responses` (also compaction), `claude`, `gemini`, `interactions`, `codex-alpha-search`, `openai-image`. |
| `headers` | The client's request headers, each value a pattern with `*`. Core checks them; they are not sent to the extension. |
| `match`, `not-match`, `exist`, `not-exist` | The body about to go upstream. |

Paths are dotted keys (`\.` for a dot in a key), array indexes and the queries `#(...)` (the first
match) and `#(...)#` (every match), the parts today's example config uses. A path with anything
else (wildcards, `|`, `@`, `#` counts, `-1`) or a raw value that is not JSON fails the load, a
reload or a settings change, with a message that names the rule and the path.

A default checks the body the extension built, where today's core checked the client's request
translated to the upstream's format: a field the extension fills in itself counts as present, such
as the Claude extension's `max_tokens` of 32000 for a client that sets none. To replace such a
value, use `override`, as today's example config does for `max_tokens`.

A turn on an upstream WebSocket (a Codex Responses WebSocket) gets them too: they apply once to
the `response.create` message the extension sends for the turn, selected for that turn as for any
request. A message that is no turn, such as the client's interrupt, goes upstream as it is. A
client WebSocket turn that goes upstream as an HTTP request gets them as an HTTP request does.
Rules do not reach Devin, whose upstream body is protobuf. An extension older than protocol 0.14
cannot apply rules (0.15 for a turn on an upstream WebSocket), so a request that rules apply to is
not sent to it: it goes to other accounts, and the error says why.

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

Core also logs each result (`account "work": check passed: ...` or `check failed: ...`). The
result and the log line, like everything else a call on an account sends back and the
extension's own output, show `[redacted]` in place of the account's credential and of each
secret in its settings (the values `GET /config` and `GET /accounts` show as placeholders), such
as a `base_url` with a token in its query that an unreachable upstream's error quotes. A
settings secret shorter than 8 bytes is left in text, since removing every occurrence of so
short a string would mangle unrelated text. An error's message and the string values of its raw
body, from every extension and for every call (requests, checks, quota and model reads,
refreshes, sessions and logins), also lose every run of token characters (letters, digits and
`_-.+/=~`, and any other character such a secret holds, but whitespace, control characters,
quotes, brackets, commas and backslashes; read as written and with JSON and percent escapes decoded) that holds 4 or more
consecutive characters of one of the account's secrets, or 8 or more of a long token (trimmed, one
run of 32 or more token characters that is not base64 of text): an upstream that refuses a key often
echoes its start (`invalid x-api-key: sk-ant-api03-AbCdEf...`), which whole-secret redaction
misses. The secrets are the credential's key and tokens, secret header values, the proxy's user
name and password and the secrets core finds in the settings (a URL by its user name, password
and secret query or fragment values only), and on a login the inputs it marks secret (the
extension protocol's [Accounts](protocol/README.md#accounts) says which). A value counts by its
parts, so only a scheme, a public prefix, a real cookie list's names and Basic's user name beside
a password stay out of the pieces. A value that is one `name=value`, or holds one behind a scheme
or prefix, counts that pair whole as well as by its value, except in the `Cookie` header; a list
with anything in it but pairs with values counts whole too. A header, proxy or settings secret of
8 bytes or more with no part of 8 bytes or more that nothing else splits counts whole, so a word
that shares 4 characters with its scheme or cookie name goes too. A known vendor's
public key prefix (`sk-proj-`, `sk-kimi-`, `xai-` and the others listed there) gives no piece,
so `project` or `kimi` stays in an error for such a key; a secret of under 16 bytes after its
prefix keeps every piece. Any other word of the error that shares 4 characters with a secret
(8 with a long token) goes too, such as `token` for a header `Bearer gw-token-12345`; a reply that
succeeded is never changed by the rule.
Each
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
| `GET /v1/models` | OpenAI's list: `{"object": "list", "data": [{"id", "object", "created", "owned_by", "context_length", "max_output_tokens", "capabilities", "from"}]}` (`context_length`, `max_output_tokens`, `capabilities` and `from` — `"account"`, `"catalogue"` or `"extension"` — come from merging the account's upstream list, core's `models.dev` catalogue and the extension's built-in list, with the smaller positive token limit winning). A request with an `anthropic-version` header or Claude Code's user agent (`claude-cli/...`) gets Anthropic's list instead: each model's `display_name`, `created_at`, `max_input_tokens` and `max_tokens` (200000 and 64000 when not known), sorted by display name, with `has_more`, `first_id` and `last_id`. |
| `GET /v1/models/{id}` | One model from that list, or a 404 with code `model_not_found`. |
| `GET /v1beta/models` | Gemini's list, `{"models": [...]}`: each model's `name` (`models/{id}`), `displayName`, `description`, token limits and `supportedGenerationMethods` (`["generateContent"]` when not known). |
| `GET /v1beta/models/{name}` | One model, named with or without `models/`, or a 404. |

For each name, core merges what the account's upstream list, its `models.dev` catalogue and the
extension's built-in list say, taking the smallest positive token limit, read in that order so
that an equal limit stays with the earlier source. Every other field, such as the display name,
description, owner, version and modalities, comes from the first source that sets it, the
account's list before the extension's list and that before the catalogue, except the capabilities,
which come from the first of the account's list, the catalogue and the extension that has any, a
later source adding only the reasoning levels and deprecation time an earlier one lacked. `from`
names the source that gave the context limit, else the output limit, else the capabilities.

A list holds each model the accounts serve, once under each name a client can ask for it by,
and the name of each fallback chain no account serves, sorted by name. An account whose config lists `models` lists those. An account without `models`
lists the models its extension says the account serves (the protocol's `models` operation, from
version 0.3), under the account's naming rules below; an extension that cannot say adds nothing
for it. The same list decides which models such an account serves: a request for a model no
account serves is refused without calling an upstream, as today's core refuses it (404
`model_not_found`; today's core answers 400). An account whose extension has no list, an empty
one, or one it could not read within 15 seconds serves any model. A list that could not be read is
asked for again a minute later; an empty list counts as known, so it is refreshed with the others.
Core asks again for each known list every 30 minutes with jitter, when an account's
credential is renewed or it signs in again, and on `POST /v0/management/models/refresh`; a
refresh that fails, times out or comes back empty keeps the last good list, and so does one that
falls back to the extension's built-in list when the account's list was read from the provider.

### Codex client catalog

`GET /v1/models?client_version=0.153.0` returns the Codex client's catalog,
`{"models": [...]}`, instead of the normal model list. The presence of `client_version`, even
with an empty value, selects this reply before the Anthropic client check. Without that query
parameter the lists above keep their usual shape.

Core resolves the models served by accounts of `pluxy/codex` with the same exclusions, aliases
and prefixes as the normal list. For a name outside a fallback chain it lists the name only when
routing chooses `pluxy/codex` as the name's provider. It also lists each fallback chain's name
when a step of it can go to a `pluxy/codex` account, as that step's model. It sends one
`codex.models` endpoint call containing the client version and each model's public and upstream
names. The Codex extension loads its embedded catalog on the first such call and selects templates
by upstream name. It removes `cpa_capabilities`, replaces `slug` with the public name and
`display_name` when provided, and clears Codex-only options for chain names whose fallback chain
has a step a non-Codex account can serve. The catalog
keeps `minimal_client_version` from the template;
clients below 0.144.0 receive reasoning levels only through `xhigh`, while empty or unparseable
versions keep modern levels. Models without a template are omitted: a routing entry alone
does not establish the capabilities the Codex client needs. The extension filters reasoning
levels for older clients as today's core does. Catalog data stays in the extension program;
core neither embeds it nor keeps a copy per account. No upstream request or remote refresh is
made for this list.

The call goes directly to the first enabled, usable Codex account in account order, with only
its ID and no credential. It ignores cooldowns and concurrency limits, renews no credentials,
records no routing result or usage event, and leaves account rotation alone. Model discovery
uses saved credentials for plan metadata without renewing them. With no enabled, usable Codex
account (including a configuration with only other extensions), the reply is 200
`{"models":[]}`.

To refresh the catalog by hand, fetch `codex_client_models.json` from the
[router-for-me/models](https://github.com/router-for-me/models) repository, the source the old
core refreshes its copy from, and rebuild the extension. From `next/`:

```bash
tmp=$(mktemp -d) && git -C "$tmp" init -q
git -C "$tmp" fetch --depth 1 https://github.com/router-for-me/models main
git -C "$tmp" show FETCH_HEAD:codex_client_models.json > extensions/codex/client_models.json
rm -rf "$tmp"
go build -o extensions/codex/codex ./extensions/codex
```

Review the file and its changes before keeping it, run the tests in `extensions/codex`
(`TestClientModelTemplates` in `client_models_test.go` expects 12 templates, so a changed count
needs the test updated too), run the Codex catalog corpus and e2e tests, and reload core after
replacing the extension program. The desktop app's Codex setup tool (`app-tools/codex`) embeds the same file:
rebuild it too. The routing list in
`extensions/codex/models.json` remains separate from this client metadata.

### Account model names

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
| account `models` | The only models the account serves. Each is the upstream's name, or `{"name": ..., "alias": ...}` to serve the model `name` under the name `alias` only. One name clients use maps to one upstream model: the config check refuses an entry whose name or alias another entry already uses for a different upstream model, naming the alias and the account (in the config file, and as a 400 from `PATCH /accounts/{id}`, which the panel uses). Listing the same model twice passes. Today's core pools such entries in `openai-compatibility`; a `config.yaml` makes such a pool a [fallback chain](#provider-key-sections). |
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
core's user and, on Windows, SYSTEM (`credentials_dir`, default `credentials` beside the config file), and renews its
credential through the extension shortly before it expires. Only the extension that created an
account ever receives its credential. The protocol side is in the
[protocol README](protocol/README.md#login-and-refresh).

On Windows, where file modes do not limit who can read a file, the folder and each file get a
protected access list that lets in only core's user and SYSTEM. Core checks the folder each time it
loads or saves the accounts and makes it private if it is not. It refuses to start, or to save, when
`credentials_dir` is a link or a file, or a folder owned by another account than core's user (or
SYSTEM). A folder made by pluxy run as administrator is owned by the Administrators group, so a run
without administrator rights refuses it: take ownership of the folder and its files from an
elevated prompt (`takeown /f <folder> /r`), or move the folder away and sign the accounts in again.
A file in the folder that is a link, or that another account owns, is skipped with a notice in the
log rather than loaded. A file that does not read as a record (it is not JSON, or its `id` is not
its file name) is skipped the same way, and the account its file name gives is listed as `broken`
([management API](management/README.md#accounts)) until the file is fixed or the account is
removed, rather than stopping core. A record file with a list of its own that lets in more (one
written before the folder was private, or moved in from another folder), which making the folder
private does not reach, is written again as a private file when the accounts are loaded, with a
notice in the log.
The same access list and folder checks keep the extensions' settings ([extension pages](ui/extension-pages/README.md)) and the [log files](#log-files) private.

An extension may return non-secret account settings with a login grant. When adding an
account, core uses those as defaults: properties explicitly supplied in `POST /accounts.settings`
win (including `null`; nested objects are replaced as a whole). The extension validates and
normalises its defaults. Settings must never hold secrets. A login to an existing account
ignores grant settings and keeps the account's existing settings.

The management API is served under `/v0/management`; a config without `management_key` is refused
at start and on a reload. Send it as a bearer token. The config may set no accounts.

| Request | Does |
| --- | --- |
| `GET /accounts` | Lists every account (`source` `config` or `login`), its credential `kind`, `state` (`active`, or `reauth_required` when the provider refused the grant), `disabled`, `expires_at`, the last renewal error and the last [account check](#account-checks); its routing fields (`priority`, `weight`, `disable_cooling`, `max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models`, `model_alias`, `endpoint`), its own `proxy_url`, with `redacted` in place of a user name and password, and its
[`headers`](#upstream-headers), with a placeholder in place of each secret value; its `health` (`state` `ok`, `cooling`, `exhausted`, `needs_login`, `banned`, `disabled` or `broken`, with `until`, `why`, `source` and `next_check`; see `management/README.md`, "Accounts"); and what routing knows about it now: `cooldowns` (each live block's `scope`, `credential`, `model` with its `model`, or `endpoint`; `kind`, `rate_limit`, `quota` or `cooldown` for failures without a reset time; `until`; `backoff_level`; and the `error` that set it), `cooldown_until` (when the latest block on the whole account ends; model blocks do not count), `last_error` and `last_error_at` (the latest failure that set a block, until a request on the account that started after it succeeds; like a cooldown, it is not cleared by a request that started earlier, and a failure from a request that started before a success on the account sets none) and `active_requests`. An account that gets no requests (disabled, broken, or waiting for a login) has no routing state. An account added by login also shows its `settings`, with each secret replaced by a placeholder in the same form as `GET /config` (see [Changing the config through management](#changing-the-config-through-management)), by the same rules made stricter: everything below a key named like a secret is a secret, however deep; `settings_inexact` is `true` when they hold a number that a client reading numbers as float64 and writing back the shortest decimal for each, as a browser does, would change (such as `0.10000000000000001`, `1e-400` or `9007199254740993`). Never a secret. |
| `POST /accounts` | Starts a login that adds an account: `{"id": "work", "extension": "pluxy/codex", "method": "device", "input": {...}, ...}` plus any account fields from the config (`settings`, `models`, `priority`, ...). `extension` names the extension that signs in and owns the account; it may be left out while only one extension runs. Replies 202 with the login, usually carrying its first prompt (a URL or a device code). |
| `GET /logins/{id}` | The login's state (`pending`, `done`, `failed`, `cancelled`), prompt or error. |
| `POST /logins/{id}/input` | `{"input": {...}}`: what the user entered for the prompt, such as the redirect URL of a browser login. |
| `DELETE /logins/{id}` | Cancels the login. |
| `POST /accounts/{id}/login` | Signs in to an existing account again (`{"method": ..., "input": ...}`), replacing its credential. The login must sign in to the same upstream account: when the provider reports who signed in (the grant's [`identity`](protocol/README.md#login), such as an e-mail address and an account ID), every field core stored for the account must match, letter case aside; an account stored without one is checked by its `label`. Otherwise the login ends `failed` with code `invalid_request` and the account keeps its credential, as today's core refuses with "Authenticated account does not match target credential". To use the other account, add it as a new one. |
| `POST /accounts/{id}/refresh` | Renews the account's credential now. |
| `POST /vertex/import` | Adds a Vertex AI account from a Google service account key file, as today's `POST /v0/management/vertex/import` does: `multipart/form-data` with the key in `file` and an optional `location` (a form field, else a query parameter; default `us-central1`), at most 1 MiB in all. It runs the Gemini extension's [`service_account` login](#gemini) with the account ID `vertex-<project_id>` (`/`, `\` and `:` in the project ID become `_`, spaces `-`), the settings `vertex.project` and `vertex.location`, and the label `<project_id> (<client_email>)`, waits for it (one token fetch, at the key's `token_uri`) and answers once: 200 `{"status": "ok", "account", "project_id", "email", "location"}`. Errors have the usual `{"error": {"code", "message"}}` shape: 400 `file_required`, `invalid_json`, `project_id_missing` and `invalid_service_account` (also when the token endpoint refuses the key), 400 `invalid_request` for a location or project ID the extension refuses, 409 `conflict` for an account of that ID in the config, 413 for a larger request, 502 with the login's code (`unavailable`) when the token endpoint cannot be reached or fails, and 504 when it does not answer in 45 seconds. When a login added an account of that ID before, the import signs in to it again, so the key replaces the old one only when its `client_email` is the same service account (else 400 `invalid_request`, the account keeps its key) and a `location` the account does not have is a 409; today's core overwrote the file silently. It does not repair a key damaged by pasting (today's core rebuilds a mangled PEM); it takes the file Google gives. |
| `POST /accounts/import` | Adds the accounts of one credential file of today's auth folder to the running core, read and added as [`pluxy import cpa`](#moving-from-cliproxyapi-pluxy-import-cpa) reads and adds each file of the folder: `{"name": "<file name>.json", "content": "<the file's text>"}`. The content is the file itself, never a path on core's machine; `name` (a `.json` file name, no folder) gives the account ID, as the file's name does. The accounts serve at once, like an account a login adds. 200 `{"imported": [{"id", "extension", "ignored"}], "skipped": [...]}`: each account added, with the file's settings not supported yet that it left out (`ignored`), and why any other account of the file was skipped (a Codex file can hold two). Nothing added: 400 `invalid_request` for a file that is not JSON, of a type the import does not take or holding no token, with routing fields routing refuses once read (such as a `prefix` with a `/` inside, `a/b`; surrounding spaces and slashes are trimmed first), or a bad `name`; 409 `conflict` when the store or the config already has the account's ID, or a sign-in still running will take it (letter case aside); 413 for a file larger than 1 MiB. The message says why. Neither the reply nor core's log holds anything from the file but the account's ID. |
| `PATCH /accounts/{id}` | `disabled` (`true` or `false`; disabled accounts get no requests) and any of the routing fields `priority`, `weight`, `disable_cooling`, `max_concurrency`, `request_retry`, `prefix`, `models`, `excluded_models`, `model_alias`, `proxy_url` and `headers`, checked as the config's are (a `proxy_url` sent back as `GET /accounts` shows it keeps the password it hides, and a header's placeholder sent back keeps its value; a placeholder that stands for no value the account has is a 400); `null` sets one back to its default. `settings` replaces the account's settings as a whole: an object, or `null` for none. A placeholder from `GET /accounts` keeps the secret it stands for: it is accepted only where that secret is now (an item of a list may move within its list; elsewhere 400), and only while the account's settings still hold that secret and core has not restarted (else 409, read the account again); to change a secret, send the new value. Core passes the settings to the extension as they are. An account whose `settings` change starts with fresh routing state, as on a reload: its blocks, cooldowns, last error and session bindings are dropped, while a ban stands (only signing in again, removing the account or turning it back on lifts it). A bad field changes nothing (400). Replies with the account. |
| `POST /accounts/{id}/cooldown/reset` | Clears the account's cooldowns, on its credential, its models and its endpoint (which accounts naming the same endpoint share), with their backoff steps and its last error, so it serves again at once unless it is banned: a ban, and the last error of a banned account, stay (see [Accounts and routing](#accounts-and-routing)). Works for config accounts too. Replies with `cleared`, the number of live blocks it cleared, and the account. |
| `GET /accounts/{id}/models` | The models the account serves: its config's `models` when it lists them, else the ones its extension lists for it (the protocol's [`models`](protocol/README.md#models)). Each has its upstream `id`, what the extension says about it, `names` (the names clients use for it after the account's exclusions, aliases and prefix, and its extension's defaults for logins) and `excluded` when `excluded_models` leaves it out. |
| `POST /accounts/{id}/test` | Sends the account one small request (`Hi`, at most 16 tokens of reply), straight to its extension: its cooldowns are ignored and its routing state is left as it is. `{"model": ...}` names the model by its upstream name; without it, the first the account serves. Replies 200 with `result`: `ok`, `model`, `served_model`, `latency_ms`, the start of the `reply`, `finish_reason` and `usage`, or `error`. The request spends what a request that size costs. |
| `GET /accounts/{id}/quota` | The account's plan and usage, read from its provider when asked (the protocol's [`quota`](protocol/README.md#quota)): `quota` with `plan`, optional `plan_ends_at` (the subscription end in Unix seconds), `limits` (each with `id`, `label`, and what the provider gives of `used_percent`, `used`, `limit`, `remaining`, `unit`, `unlimited`, `resets_at`, `window_seconds`, `models`) and `raw`, the provider's replies; and `fetched_at`. 422 with code `unsupported` for an account with no plan to report, such as an API key, or whose extension reads no quota; 502 with the provider's error code when the provider refused or failed. Each provider's section says what it reports. |
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
when core refuses to start, has the config's secrets replaced by placeholders, as `GET
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
| `management_key` | Bearer token for the management API and the panel. Required: a config without one is refused at start. |
| `idle_stop_ms` | Stop the program after this long without requests. Default 10 minutes; 0 keeps it running. |
| `drain_timeout_ms` | Longest a replaced version may keep finishing its requests. Default 10 minutes. |
| `restart.backoff_ms` | Wait before the first restart after a crash; doubles per crash in a row. Default 1 second. |
| `restart.max_backoff_ms` | Cap on that wait. Default 1 minute. |
| `restart.quarantine_after` | Crashes in a row that quarantine the extension. Default 5. |
| `restart.stable_ms` | A program that ran this long before crashing starts a new count. Default 10 minutes. |

`./pluxy -safe-mode` starts core with no extensions: client requests fail with a 503, and the
config can still be checked and reloaded through management.

### Management API

[`management/README.md`](management/README.md) is the API's contract and describes the `pluxy`
command, its client: `pluxy serve` runs core, and the other subcommands (`pluxy status`,
`pluxy account list`, `pluxy help` for the rest) call a running core's management API.

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

A client on another machine that sends a wrong management key 5 times in a row is refused for 30
minutes, with a 403 `blocked` and `Retry-After`, even when it then sends the right key; core logs a
line naming it. A call with no key at all is a 401 that does not count, and a call from this machine
is never counted or blocked, so the person at the keyboard can always get back in.
A right key clears its count, and a count is forgotten after 2 hours without a failure. These are
today's core's numbers. The client is the address [`trusted_proxies`](#run-it) gives, and an
IPv6 client is counted by its /64, so it cannot start again by moving to another address in the
block it was given; today's core counts each address. Behind a proxy on another machine that is not
in `trusted_proxies`, every client has the proxy's address, so one client's wrong keys block them all.

| Call | What it does |
| --- | --- |
| `GET /v0/management/status` | `desired` (the newest config version core has seen, with `error` when it was refused) and `active` (the one serving), each with `revision` and `hash`; `extensions`, one entry per running generation with `state` (`starting`, `active`, `idle`, `crashed`, `quarantined`, `draining`, `stopped`), `current`, `pid`, `restarts`, `last_error` and `active_requests`; `safe_mode`; `first_run`; `version`, `commit` and `built`, the build's version, commit and build date as `-version` prints them. |
| `GET /v0/management/setup`, `POST /v0/management/setup/scan`, `POST /v0/management/setup/apply`, `POST /v0/management/setup/done`, `GET /v0/management/setup/clients/{id}/paste` | First-run setup scan, package picks, applying picks, ending first run, and the settings to paste into a client tool; see [`management/README.md`](management/README.md#setup). |
| `POST /v0/management/reload` | Reload now. 200 with the status when the new revision is active; 422 with the status when it was refused. |
| `GET /v0/management/ui` | The pages of the extensions that run and where they are served; see [extension pages](ui/extension-pages/README.md#how-core-serves-pages). |
| `GET`, `PATCH /v0/management/extensions/{publisher}/{name}/config` | An extension's settings, checked against the schema its manifest declares; see [extension pages](ui/extension-pages/README.md#how-core-serves-pages). |

### Changing the config through management

These calls read and change the config file core runs on, whether it is the JSON config or a
`config.yaml` with today's key names. A change goes through the same steps as a reload: core
checks the new contents and prepares them, and only when they are ready to serve does it write
the file and switch to them. The file is replaced in one step (a new file renamed over it, with
the same permissions, on Windows the same access list, through a symbolic link if the path is
one), so nothing ever reads half of it. When a change is refused, the file and the running
config stay as they were, and status keeps showing the file as it is. When another program writes the file while a change is being
prepared, the change is dropped with a 409, so that write is not lost. Today's core writes its file in place and saves a change that
its reload may then refuse.

A change made with the structured calls (client keys, management key, settings) writes the file
out again. A `config.yaml` keeps its comments and the order of its keys, with two-space indents
and no blank lines, as today's core writes it when it saves; a JSON config keeps the order of
its keys and its indent. `PUT /config` writes the document as sent.

| Call | What it does |
| --- | --- |
| `GET /v0/management/config` | The config file: `format` (`json` or `yaml`), `hash` (as in status) and `content`, the file as written with each secret replaced by a placeholder such as `pluxy-redacted-4f1c...`. Secrets are found by key name, in both layouts of a `config.yaml` and in the JSON config: client keys, the management key, every `api-key` and `api_key`, everything in an account's `credential` except its `kind`, passwords, tokens and secrets (names are compared in lower case whatever joins their words, or none, so `accessToken` and `accesstoken` are `access_token`, with letters folded as core matches a JSON key to a setting, so `client_keyſ` is `client_keys`; `aws_secret_access_key`, `client_secrets` and `passwords` are ones too, and so is a name that ends in a number or a word saying how the value is written, such as `apiKey2`, `api_key_value` or `privateKeyPem`), header values about authentication (`Authorization`, `X-Api-Key`, cookies) or named like a secret in any mapping whose name holds `header` (`extra_headers`, `headers_override`), and any URL with a user name, password or query, or with a parameter named like a secret in its fragment. Everything below a key named like a secret is a secret, however deep, also when given through a YAML alias (as a value, or as a key's name), as `GET /accounts` shows a login account's settings, except in today's core's provider sections at the top of a `config.yaml` (the sections of the v8 `api-keys` mapping, such as `codex`, and the older lists such as `codex-api-key`), whose entries are read by these rules; any other key of the v8 `api-keys` mapping is read as a secret key. A string secret of 8 bytes or more written again as a whole value anywhere else in the file (a note holding a key) is hidden there too; a shorter copy, or a number, is shown, and so is a copy of a value that is a secret only for being in a mapping below a secret key. `true`, `false`, `null` and the empty string never are secrets, as they carry none. Comments are shown as written. 422 when the file does not parse. Today's core returns its config, the management key's hash and provider keys included, as they are. |
| `POST /v0/management/config/validate` | `{"content": "..."}`: checks a document as a reload would, without starting extensions or writing anything, placeholders put back as `PUT /config` puts them (below). 200 with `valid: true` and the `notices` a `config.yaml` load logs, or `valid: false` and `error`, such as for a placeholder where its secret is not. |
| `PUT /v0/management/config` | `{"content": "...", "base_hash": "..."}`: replaces the file with the document and applies it. Each placeholder goes back only where its own secret is in the file now: the same account or list item (by its `id` or `name` when it holds that item's secrets, else by its secrets), and the same keys, also where a YAML alias or merge key reads it (below). 200 with the file as `GET /config` shows it; 409 when `base_hash` is set and the file has changed since; 422 with `error` when core refuses the document, such as for a placeholder anywhere else, naming its line; a refused document writes nothing. |
| `GET /v0/management/client-keys` | The client keys: each one's `id` (a keyed hash whose key is kept in `state/key-id.key` beside the config file, mode `0600`, so IDs stay the same across restarts) and `masked` form (first and last two characters, as the old panel showed them; a key shorter than 8 characters is hidden whole). |
| `POST /v0/management/client-keys` | Adds a new random client key (`sk-` and 26 random characters). 201 with the key's `id` and `masked` form and its `value`, which no later call shows. |
| `DELETE /v0/management/client-keys/{id}` | Removes the client key. 204; 404 when no key has the ID; 409 for the last key, since core refuses a config without client keys. |
| `POST /v0/management/management-key/rotate` | Replaces the management key with a new random one and returns it as `value`. The old key stops working as the reply is sent. |
| `GET /v0/management/models` | The models `GET /v1/models` lists, as one list in the same order: `models`, each with `id` (the name clients use), what the account, the catalogue and the extension say about the model (`owned_by`, `display_name`, `context_length`, `max_output_tokens`, `capabilities`, `from`, ...), `upstream` (its name upstream), `extension` (the extension of the account whose entry the list shows) and `accounts` (the IDs of the accounts a request for the name uses, as routing chooses them: one provider's, or with a fallback chain each step's in step order, highest priority first within a step). An account that serves any model because its list is not known adds no name, but is among the accounts of the names its provider serves; a fallback chain that only such accounts serve is listed as the first of them, with its step's model as `upstream`. Under the `weighted` strategy an account of `weight` 0 is in no entry. A name whose accounts all wait for a login or are turned off is left out. Never a credential. |
| `POST /v0/management/models/refresh` | Ask providers for their model lists again now; optional JSON body `{"account": "<id>"}` for one account. Returns the updated `models` list and per-account `accounts` results (`models`, `kept`, `error`). A read that fails, times out, comes back empty or falls back to the extension's built-in list keeps the last good list (the last live one, when it was read from the provider). |
| `GET /v0/management/settings` | The basic and routing settings, by today's core's names, and `token_count_estimate` and `routing.timeouts`, this core's own, under their own names (below). |
| `PATCH /v0/management/settings` | Changes the settings the body names (`{"routing": {"strategy": "fill-first"}}`) and replies with them all. 400 for a name or value it does not take. |

In the document, a placeholder stands for the secret it replaced, so a document read with `GET
/config`, edited and sent back keeps its secrets: core puts each one back from the file as it is
now. A secret that is not a string has its type in its placeholder, so the number `1234` and the
string `"1234"` have different placeholders and each goes back as it was written; one written
with an explicit YAML tag (`!!int 0x1F`, `!!binary ...`) goes back with its tag. To change a
secret, write the new value in its place. A placeholder goes back only where its own secret is
in the file now: at the same keys (the management key, a client key, a registry's URL), and in a
list, in the same item. An item of the document stands for an item of the file that holds, at the
same keys, a secret of each of its placeholders, and that no other item stands for. Among those,
it stands for the one with its `id` (an account), or else its `name` (a registry, a named
provider entry of a `config.yaml`), when it can; otherwise, as an item with neither does (an
entry of an older list such as `codex-api-key`, a key of an entry), for one that holds its
secrets: an account renamed, or given another account's `id`, is the account whose secrets it
holds. So items can be reordered and renamed. An item with a placeholder may not have its `id`
or `name` twice, in any case (core's JSON reader takes the last of them, matching names in any
case). A plain value in a list, such as a client key, may move within its list. A copy's
placeholder goes back the same way, where the file holds that copy now, and only while the
document still holds the secret it copies: a copy whose key was renamed, that moved to another
item of a list, or whose secret was changed or removed is refused. In a `config.yaml`, a secret
is wherever core reads it: an alias (`*name`) reads its anchor at the alias's place, and a merge
key (`<<`) reads the merged mapping's keys in the mapping it is in, as core's loader reads them.
Each place core would read a placeholder at must be one where the file, read the same way, has
that secret now: a file that already aliases a secret is taken (it is written out again, as core
reads it, so its quotes and spacing may change), but an alias or merge key that reads a
placeholder anywhere else is refused, naming the alias's or the merge key's line, and so is a
mapping key that is a placeholder or an alias of one, which would make a secret a key's name,
such as a header's. So a placeholder cannot take one account's key, the management key or a client key into
another account, another entry, a header or another setting: core refuses the document, naming
the line, and the value must be written there itself. `POST /config/validate` runs the same
check and answers `valid: false` with the reason. One limit: an account or a provider entry that
keeps its secrets' placeholders keeps its secrets when its address or its proxy changes (an
account's `base_url` or `proxy_url`, here or through `PATCH /accounts/{id}`, an entry's
`base-url` or `proxy-url`), whether it is known by its `id`, its `name` or its secrets, so a
client that may change an account or an entry may point its kept secrets at another address. A placeholder is accepted only while the file still holds that secret and core
has not restarted (the placeholders are keyed hashes with a key core picks at start); otherwise
the call answers 409 (read the config again). Messages in replies never show a secret either,
however short, except a number shorter than 8 bytes that is a secret only for being in a
mapping below a secret key (the `3` of `client_secret: {rotate_after_days: 3}`), which is left
in text: replacing every `3` in a message would garble it.

A `config.yaml` keeps each setting under the name it already has there: the v8 name, or the
older spelling when only that is in the file. New client keys go to `access.api-keys`, or to the
older layout's `api-keys` list when the file has that and no `access.api-keys`; the management key
to `management.secret-key`, or `remote-management.secret-key` when only that is there.

| Setting | JSON config | Values |
| --- | --- | --- |
| `routing.strategy` | `routing.strategy` (`weighted` for `weighted-round-robin`) | `round-robin`, `fill-first`, `weighted-round-robin`, `least-active`, `soonest-reset`; today's other spellings (`wrr`, `ff`, ...) are taken too. |
| `routing.session-affinity` | `routing.session_affinity.enabled` | `true` or `false`. A `config.yaml` keeps today's default, `false`; a JSON config has it on until it says `false`. |
| `routing.session-affinity-ttl` | `routing.session_affinity.ttl_ms` | A duration such as `1h` (default) or `30m`. In a JSON config it needs session affinity on. |
| `routing.force-model-prefix` | `routing.force_model_prefix` | `true`: an account with a `prefix` serves its models only under the prefix. |
| `routing.retry.max-retry-credentials` | `routing.max_accounts` | Accounts one request may try in a round, as in today's core; 0 is no limit. |
| `routing.retry.max-retry-interval` | `routing.max_wait_ms` (seconds × 1000) | Seconds one request may wait for a blocked account: before each round with `request-retry` set, else in all. |
| `routing.retry.request-retry` | `routing.request_retry` | Rounds a request may run after its first, as in today's core. `null` while a JSON config leaves rounds to this core's own rules; a `config.yaml` always has a number (default 0). |
| `routing.cooldown.disable-cooling` | `routing.disable_cooling` | `true`: failures leave no cooldown. An account's own `disable_cooling` wins. |
| `routing.cooldown.transient-error-cooldown-seconds` | `routing.transient_error_cooldown_ms` (seconds × 1000) | Seconds a failing upstream cools down: 0 for this core's backoff (1 second doubling to a minute), -1 for none. |
| `routing.cooldown.quota-response-mode` | `routing.quota_response_mode` | `retryable` (the default) or `usage-limit`: what a client gets when every account is out of quota (see [Accounts and routing](#accounts-and-routing)). |
| `routing.timeouts` | `routing.timeouts` | The default upstream timeouts of every account, in milliseconds: `header_ms`, `idle_ms` and `total_ms`. `0` turns a limit off, a field left out stays as it is, and `null` removes them. This core's own setting: a `config.yaml` has no name for it. |
| `observability.logs.logging-to-file` | `logs.to_file` | `true` or `false`: whether core also writes its log to [files](#log-files). |
| `observability.logs.logs-max-total-size-mb` | `logs.max_total_size_mb` | Megabytes the logs folder's `.log` files may take; 0 (the default) is no cap. |
| `observability.logs.request-log` | `logs.request_log` | `true` or `false`: whether every client request is written to a [request log](#request-logs). |
| `observability.logs.error-logs-max-files` | `logs.error_logs_max_files` | How many error logs are kept, the newest; 0 keeps all. Default 10. |
| `observability.usage.usage-statistics-enabled` | `usage.enabled` | `true` or `false`: whether core keeps the [usage record](#usage-statistics). On unless the config turns it off. |
| `token_count_estimate` (top level) | `token_count_estimate` (top level, in a `config.yaml` too) | `true` (the default) or `false`: whether a token count no account can give is answered with core's estimate or with an error. |
| `proxy-url` (top level) | `proxy_url` | The [outbound proxy](#outbound-proxy) of every account without its own, shown with `redacted` in place of a user name and password; `""` removes it. Sending back the value as shown keeps the password. |
| `payload` (top level) | `payload` (`requests.payload` in a `config.yaml`, or `payload` where the file has only that) | The [payload rules](#payload-rules), all five lists at once; absent when there are none. A change replaces them, `null` or `{}` removes them, and a field the rules do not have or a path outside the supported syntax is refused. |

The settings shown are those of the running config. Today's other basic setting `ws-auth` is
dropped (the AI Studio relay is not built), and `debug` is dropped (this
core writes no debug-level lines); `max_attempts`, which today's core has no
name for, is changed in the document.

## Usage statistics

Core keeps a usage record: one event for each generation request that reached routing (a request
refused for its client key, or whose body does not parse, is not recorded), including Responses
WebSocket turns. A WebSocket warmup (`generate: false`) that core answers itself reaches no
upstream and is not recorded. Token counts (`count_tokens`, `:countTokens`) generate nothing
and are not recorded. An event holds the request's trace ID (`id`, the `X-CPA-TRACE-ID` of its reply),
when it arrived, the `endpoint`, whether it streamed, the `model` the client asked for (without a
thinking suffix), the account's own name for it when that differs (`upstream_model`: an alias
or prefix resolved) and the model the upstream reply says served it when the reply names one
(`served_model`), the `provider` (extension) and `account` its last attempt went to, the
client key's `key_id` (the `id` `GET /client-keys` lists it under, never the key), the
`client_ip` it came from (the connection's peer, or the client a trusted proxy names; see
`trusted_proxies`), whether it `failed` with which `error_code`, the HTTP `status` of the reply, `latency_ms`, and the `tokens`
the upstream reported: `input_tokens` (cached ones included), `output_tokens` (reasoning
included), `reasoning_tokens`, `cached_tokens`, `cache_creation_tokens` and `total_tokens` (input
plus output). A token count the upstream did not report is absent, not 0. A streamed reply
that fails after it began keeps its `status` 200 with `failed` true; a client that goes away is
recorded as `cancelled`, with no `status` when nothing had reached it. A WebSocket turn is
recorded before its last message (`response.completed` or `response.incomplete`) is written, so
a client that has that message finds the turn in the record; a client that goes away while it
is written leaves the turn recorded as finished, with the tokens the upstream reported. For a
reply core translates, that holds for the `.done` messages written just before it too.

The recent window lives in an in-memory ring holding the last `max_events` requests
(default 10000, at most 200000) and none recorded (when the request ended) longer ago than
`max_age_ms` (default 24 hours, at most 30 days); a reload that lowers either drops the oldest at once.
Core also writes every recorded event asynchronously through a bounded 4,096-event queue to one
JSONL file per UTC day under `usage/` beside the config file (`usage/YYYY-MM-DD.jsonl`, mode `0600`,
`"v": 1` on each line), without `fsync` per line. When the queue is full, new events are dropped from
the disk queue and counted in `dropped` so slow storage never blocks requests; an event the disk
refuses (a full disk, a file it cannot open) is counted in `write_errors` and logged with its error,
and stays in the in-memory record. On startup, the in-memory ring is refilled from today's and
yesterday's files (skipping a torn last line), `seq` continues from the highest `seq` in the refilled
files or in the newest day file left (numbering older unsequenced lines in order), and day files
older than `keep_days` (default 90; `0` keeps everything) are deleted at startup and daily.
`keep_days` covers the day files only: it does not delete the summaries below. The
highest `seq` numbered is also kept in `usage/seq.json` (mode `0600`), written at startup, before a
day file is pruned and on shutdown, so `seq` continues above it even after every day file that held
it is gone.
When a range in `GET /v0/management/usage` covers closed UTC days (before today), each closed day
gets a summary file `usage/summary/YYYY-MM-DD.json` (mode `0600`) built on demand from its day file,
cached in memory after, and kept after `keep_days` removes old `.jsonl` files, so `usage/summary`
grows by one small file for each closed day a range has covered; summaries store
per-hour, model, provider, account, client key, failure split, latency sum and all six token counts
so long ranges never re-read closed `.jsonl` files. Each summary also records the size of the day
file it was built from and is rebuilt when that file has grown since, so a late event that lands in
a closed day reaches the totals whether or not the summary's invalidation got through. Closed days
are counted by whole UTC hours, so a `since` or `until` inside an hour on a closed day counts that
whole hour, and a series counts that hour in the first step it overlaps, so its steps add up to the
totals. Today's portion comes from the in-memory ring (or today's `.jsonl` file if earlier events
from today rolled out of the ring).
Client key IDs (`key_id`) use an HMAC key stored in `state/key-id.key` beside the config file (mode `0600`), so key IDs match across restarts. Reading the record never removes anything: today's core hands its usage queue out once and deletes what it hands out.

```json
{"usage": {"enabled": true, "max_events": 10000, "max_age_ms": 86400000, "keep_days": 90}}
```

`usage.enabled` (today's `usage-statistics-enabled`) is on unless the config turns it off. Off,
requests are not recorded and what was recorded stays readable.

| Call | What it does |
| --- | --- |
| `GET /v0/management/usage` | What the recorded requests add up to over the kept range: `total_requests`, `success_count`, `failure_count`, `success_rate` (0 to 1), `average_latency_ms` and `tokens` (the sums of each token count); the same per model (`models`), extension (`providers`), account (`accounts`, each with its `provider`) and client key (`keys`, by `key_id`), most requests first, plus `by` and `breakdown` when `by` (`model`, `account`, `key`, `provider`) is asked; and `series`, the requests, failures and tokens in each step (`bucket` from `1s` to `24h`, `buckets` up to 1440; defaults to 60 steps of `1m` with no range, `1h` steps for ranges up to 48 hours and `24h` steps beyond), oldest first, ending at `until` (or with the step that holds now). `oldest` is the arrival time of the oldest event considered, `evicted` how many events the limits have pushed out since core started, `dropped` how many events were dropped since core started because the disk writer's queue was full, `write_errors` how many the disk refused to write, each logged, and `enabled` whether recording is on. |
| `GET /v0/management/usage/events` | The recorded events in the recent ring, oldest first with `after` (`seq` above `after`) or newest first with `before` (`seq` below `before`, or `0` for the latest page), up to `limit` (default 100, at most 1000). The reply's `next` is the `seq` to pass for the next page, `more` says whether more match, and `lost` says whether events after `after` were pushed out before this read, so a program that polls with `after` knows it missed some. |

Both calls take the filters `since` (an RFC 3339 time, or a duration back from now such as `24h`, `7d` or `30d`), `until` (an RFC 3339 time), `model`, `provider`, `account`, `key` (or `key_id`) and `failed` (`true` or `false`). A filter that does not parse is a 400.

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

The logs folder and the files core writes in it (`main.log`, its rotated files and the
[request logs](#request-logs)) can be read only by core's user (and, on Windows, SYSTEM). On
Linux and macOS the folder is made 0700 and each file 0600. On Windows the folder and each file
get the protected access list of the [credentials folder](#accounts-added-by-login), and the
folder is checked the same way each time core opens `main.log` and each time it writes a
request log: a folder whose list is not private is made private, and one that is a link or a
file, or that another account owns, is refused. Core still starts then, but writes no log files,
and says why on standard error. A `main.log` that another account owns is refused too.

Core also writes one access line when each client API request ends: after a streamed reply ends,
and when a Responses WebSocket closes. It writes none for `/healthz`, the management API, the
panel and its assets, or a path no route serves (a 404).

```text
200 | 1.234s | 127.0.0.1 | POST /v1/chat/completions | 20261109120000-3f9a0c2e5b7d4e18 | openai-main
```

The line is `<status> | <latency> | <client ip> | <METHOD> <path> | <trace id> | <account>`: the
reply's status; the time the request took, to the millisecond (to the second above a minute); the
client's address (the connection's peer, or the client a trusted proxy names, as in request logs);
the method and the path only, never the query string (Gemini clients send their key in `?key=`),
and no header or body; the reply's `X-CPA-TRACE-ID` and the account its last attempt went to, or
`-` in either place when there is none, as for a request refused before routing. A reply of 400 or
more is a warning: the line starts with `warning: `, like the other warnings, so `main.log` marks
it `[warn ]`; every other line is `[info ]`.

```json
{"logs": {"to_file": true, "max_total_size_mb": 100}}
```

| Call | What it does |
| --- | --- |
| `GET /v0/management/logs` | Lines of `main.log` and its rotated files (not the request logs), oldest first: with `after` (a time in Unix seconds, such as a reply's `latest-timestamp`), the oldest `limit` lines written after it, so a reader can page forward and miss none (every write has a time later than the write before it, and a page that ends inside one write's lines holds the rest of them, so it can have a few more than `limit`); without it, the newest `limit` lines. `limit` defaults to 1000, at most 10000. The reply holds `lines`, `line-count` and `latest-timestamp` (the newest returned line's time, in seconds with milliseconds; `after` itself when there is none). A line with no time of its own counts as written with the line before it. |
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
error log keeps less of a reply: the first 32 KiB and the last 96 KiB. A secret cut in two at
that line would not be recognised, so the bytes beside it that could be part of one are left out
too, and counted with the rest. On each side that is, first, a window of 12 bytes for each byte
of the longest form of a secret the log removes that holds a character other than a key's (the
secret as it is, or escaped, whichever is longest). That holds every part of such a secret the
line cut in a spelling of up to 11 bytes for each of its bytes; the log also removes spellings
with deeper escapes (such as a percent escape nested 6 times, `%252525252573` for `s`, 13 bytes
for each byte), and a part of one of those cut at the line can stay. Secrets made only of
letters, digits and `-_.~+/=%\`, as API keys and tokens are, need no window: the run of those
characters that goes on from the edge is left out in any case, and it holds any part of such a
secret, whatever its length. Where the edge then cuts through a secret in one of its forms, 4
bytes or more of it go. Where it cuts through a secret the log would find whole within a window
of it, the edge moves to where that secret starts (or ends), and the run of key characters there
goes too, so the edge never stands inside a run of key characters or inside a secret the log
finds whole around it. Where the log's redaction gives up on the text around the edge (escapes
that decode too many ways), or the edge still cuts through a secret after 4 moves, all of that
side goes. With client keys, API keys and OAuth tokens the window is 0 and only the run goes. A
secret with other characters makes it grow with the secret: a secret header's forms hold its
name, so a 22-byte value of a header named `X-Gateway-Key` makes it 612 bytes, a 207-byte
`Bearer` value 2,856 bytes and a 1,507-byte value about 18 KiB; an OAuth scope (`openid profile
email offline_access`), 420 bytes. A service account's private key (PEM text, about 1.7 KB for a
2048-bit key, with spaces and line breaks) makes it about 21 KiB a side, most of what an error
log keeps of a reply's start. Where the text there is one long run of key characters, such as
base64 data, all of that run goes. Text that repeats the start of a secret there, such as a
secret header's name, loses one repeat, not all of them.

A request log also holds what the provider extension sent upstream and what came back: between
the client's request and its reply, an `=== UPSTREAM REQUEST ===` section (the account, time, the
whole URL, method, headers and body) and an `=== UPSTREAM RESPONSE ===` section (time, status,
headers and body, or the error that kept a response from coming) for each upstream request, so a
refused attempt and the retry on another account show as two pairs. Each body keeps its first 32
KiB and its last 96 KiB, less what beside the cut could be part of a secret, as for the reply, a
streamed reply recorded as the extension reads it, without holding it back; when a request made
more than two upstream requests, the first and the last are kept, with a line saying how many
were left out between them. A body that was not read to its end (the extension read only the
first MiB of an error or stopped at a stream's last event, a read failed, or the transport stopped
sending a request body) ends with the line `[... the body was not read past here ...]`, and what
before it could be the start of a secret cut off there is left out: 4 bytes or more that start a
form of the account's credential, and, for a secret the log removes, what it leaves out on the
start's side of a cut (above), with the window reaching back no further than the last line break.
No spelling of a secret the log decodes holds a raw line break, and each line of a secret that
spans lines is removed on its own, so a stream keeps every whole event; but such a secret with a
line of fewer than 8 bytes can keep that line where the read stopped after it: always when it is 1
or 2 bytes, and when it is 3 to 7 bytes if the body spells it with JSON or percent escapes or ends
it with a carriage return and a line feed. Upgrade if a config takes such a secret. A body with no
line break near its end, such as JSON on one line or server-sent events with bare carriage returns,
loses the whole window, all of a short one: that is accepted for a body whose read really
stopped. A streamed reply ends with the line too when the extension stops at the last event of
the upstream's format, as the Claude, Codex, Kimi, xAI, Meta and OpenAI-compatible providers do
(Chat's `data: [DONE]`, Responses' `response.completed`, Messages' `message_stop`): then the reply
was not cut, and what was left unread is only what the upstream sent after that event, usually
nothing but the end of the transfer. Devin's reply is binary frames with no line breaks, so the
Devin provider reads on past its end frame to the body's end, for at most 64 KiB and 1 second;
past that its reply is complete but its body is marked, and its end within the window is left
out. The Gemini provider reads its streams to the end. A response with neither a
length nor chunked encoding whose connection closes early reads, in Go, as a body that ended
there, also over TLS closed without its closing alert between two records (a close inside a
record is marked), so it gets no line and nothing is left out: marking such bodies would cut
complete ones too. That needs protocol 0.17; with an older extension a body ends as the read stopped. A Responses WebSocket turn that opens the upstream socket holds its
handshake; the messages on that socket are not written. This needs an
extension that implements [protocol](protocol/README.md#upstream-capture) 0.13; it is captured
only while `logs.request_log` is on, so error logs written with it off have no upstream sections,
and with it off nothing is recorded or sent.

With `logs.request_log` off (the default), core writes only the requests that fail, as
`error-<name>.log`, as today's core does: a reply status of 400 or more, or a streamed reply that
fails after it began; a client that goes away is not a failure. Core also writes one for a handler
that panics, whether or not it had sent a reply. `logs.error_logs_max_files`
(today's `error-logs-max-files`, default 10) keeps the newest of them; 0 keeps them all. Request
logs count toward `logs.max_total_size_mb`, which is applied after each file is written.

Secrets are left out of every request log: values of headers and query parameters whose names
hold `authorization`, `api-key`, `apikey`, `api_key`, `token`, `secret`, `cookie` or `password`,
and `key` (an authorization header keeps its scheme, as in `Bearer [redacted]`), and, anywhere in
the file, every client key, the management key, every config account's credential and the
secrets in its settings (as `GET /config` finds them, those of 8 bytes or more), and the user info of
the global `proxy_url` and of each account's (the forms the extension removes from its replies: the
user info as written and decoded, its base64 `Proxy-Authorization` login, and the user name and
password alone when each is 8 bytes or more), in the forms the credential redaction finds (JSON
escapes, percent-encoding and the rest). A secret made of parts, such as a session token behind a
public prefix (`devin-session-token$<jwt>`), the token of a `Bearer` value or a cookie's value, has
each part of 8 bytes or more removed on its own too (the extension protocol's
[Accounts](protocol/README.md#accounts) lists the structures). Replies and upstream
sections come from the extension with the account's own credential, the secrets in its settings,
its proxy's password and its secret header values already removed, and an error's text and the
body of an upstream reply with an error status (400 or more), or of every reply of a request
that failed, without the pieces of the account's secrets (see [Account checks](#account-checks)):
a failed request's log can lose words of the model's text that share 4 characters (8 for a long
token) with a secret;
in the upstream sections the
values of headers and query parameters whose names hold `auth`, `key`, `token`, `secret`,
`cookie`, `session`, `password`, `passphrase` or `signature` are masked too, the URL's user name and password
are left out, and the characters a secret could be made of on each side of a body's left-out
middle are left out with it. Today's core writes the reply's headers unmasked and logs requests
that fail the client key check.

```json
{"logs": {"request_log": true, "error_logs_max_files": 10}}
```

| Call | What it does |
| --- | --- |
| `GET /v0/management/request-error-logs` | The error logs, newest first: `files`, each with `name`, `size` and `modified` (Unix seconds). |
| `GET /v0/management/request-error-logs/{name}` | One error log, as a download, or with `?format=json` as `{"name", "size", "content"}` (`size` is bytes). JSON reads accept at most 2 MiB; larger files answer 413 with code `log_too_large`, without partial content. Plain downloads keep their existing size behavior. 400 for a name that is not an error log's as core writes them, 404 when there is none or the name is a link. |
| `GET /v0/management/request-log-by-id/{id}` | The newest request log, plain or error, of the request with that trace ID (the `X-CPA-TRACE-ID` of its reply), as a download, or with `?format=json` as `{"name", "size", "content"}` under the same 2 MiB limit (413 above it); 404 when there is none. |

## Profiling pages

Core can serve Go's profiling pages under `/debug/pprof/`: heap, goroutine and CPU profiles of
the running core. They are off by default, and only a build made with the `pprof` build tag
has them:

```bash
go build -tags pprof -o pluxy ./cmd/pluxy
```

Turn them on in `config.json`. `addr` defaults to `127.0.0.1:8316`:

```json
{"pprof": {"enable": true, "addr": "127.0.0.1:8316"}}
```

The pages are served on a listener of their own, at `addr`, and nowhere else: not on the
client or management address. `addr` must be a loopback address: `127.0.0.0/8`, `::1` or
`localhost`. Any other address fails the config load, and a reload that sets one is refused
while the old config keeps running. Core logs the address it listens on when the listener
starts. A reload that changes `enable` or `addr` starts, moves or stops the listener, without a
restart.

To read a heap profile from a running core:

```bash
go tool pprof http://127.0.0.1:8316/debug/pprof/heap
```

The index at `/debug/pprof/` lists every profile. A CPU profile of 30 seconds is
`/debug/pprof/profile?seconds=30`.

A default build with the setting on logs one line saying it has no profiling pages, and
serves nothing on any address.

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
default `bundled/index.json` beside the `pluxy` program, or the index `-bundled` names. It needs
no network. Core installs nothing from it by itself, on a first start or later: a package is
installed from it like from any registry (`POST /packages` with `"registry": "bundled"`, or
`pluxy ext install <id>`), and removed like any other.

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

[Moving from CLIProxyAPI](../docs/moving-from-cpa.md) walks through a move step by step; this
section is the reference for it.

Core also reads a `config.yaml` written for today's core (`core/`), so an existing config moves
over without renaming keys. A config file whose name ends in `.yaml` or `.yml` is read with
today's key names; any other file is read as the JSON config above. Both layouts today's core
reads work: the v8 layout of `internal/legacy/testdata/config.example.yaml` (a byte-identical
copy of the old core's example config; `server.port`, `access.api-keys`,
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
Keys are matched exactly, as today's core matches them: `Port:` is not `port:`, so it is not
read, and a key in another letter case in the `payload` rules is dropped and named in the log. The
`payload` rules that today's core skipped or dropped when it applied them (a `-raw` string that
is not JSON, a `.inf` or `.nan`) are dropped and named too; the table row for `requests.payload.*`
below lists them.
A setting today's core reads into a true-or-false field takes `yes`, `on`, `no` and `off` (and `y`,
`n`, in lower, title or upper case) as today's core's YAML reader does.
[`pluxy import cpa`](#moving-from-cliproxyapi-pluxy-import-cpa) reports each of these settings
before anything is written.

A migrated config needs its management key set again once. Today's core writes a bcrypt hash over
`management.secret-key` in the file after it reads the key, and this core cannot use a hash: it
refuses the config and says so. Replace the hash with the key itself, in plain text. A config with
no management key at all is refused too, with a message saying how to add one. `pluxy import cpa`
does not bring the file's management key, plain or hashed: core keeps its own (see
[moving from CLIProxyAPI](#moving-from-cliproxyapi-pluxy-import-cpa)).

Settings only this core has keep their JSON names in a `config.yaml`: `packages`, `extensions`,
`extension`, `extension_pages`, `drop_policy`, `token_count_estimate`, `credentials_dir` and `cors`
(today's core has no CORS setting: it allows every origin). Provider accounts set in
the file go to the installed package of their provider (`pluxy/codex`, `pluxy/claude`,
`pluxy/gemini`, `pluxy/xai`, `pluxy/openai-compatible`), so those packages must be installed:
install them from the bundled registry (`pluxy ext install pluxy/codex`) before they serve.

### Provider key sections

Each key in a provider section becomes one account of that provider's extension, with the key
as its `api_key` credential. A key with no `api-key` (today's core serves an upstream that
needs none) is not supported yet: it adds no account and is named in the log.

| Section (v8; older spelling) | Extension | Account settings |
| --- | --- | --- |
| `api-keys.codex` (`codex-api-key`) | `pluxy/codex` | `base-url` is `base_url`, and a key without one is dropped and named in the log, as today's core drops it; `websockets` is `websockets`; `alpha-search` is `alpha_search`; `disable-codex-cloaking: true` is `codex_identity: false` |
| `api-keys.claude` (`claude-api-key`) | `pluxy/claude` | `base-url` is `base_url`; `rebuild-mid-system-message: true` is `rebuild_mid_system_message: true` |
| `api-keys.gemini` (`gemini-api-key`) | `pluxy/gemini` | `base-url` is `base_url` |
| `api-keys.vertex` (`vertex-api-key`) | `pluxy/gemini`, Vertex AI with the key as an express mode key | `base-url` is `base_url`; `interactions: true` on a key is `interactions: true` in the account's settings, which serves the Interactions API |
| `api-keys.interactions` (`interactions-api-key`) | `pluxy/gemini` | `base-url` is `base_url`; every account gets `interactions: true`, which serves the Interactions API (see [Gemini](#gemini)) |
| `api-keys.xai` (`xai-api-key`) | `pluxy/xai` | `base-url` is `base_url` |
| `api-keys.openai-compatibility` (`openai-compatibility`) | `pluxy/openai-compatible`, one account per entry in `keys` (`api-key-entries`) | `base-url` is `base_url`; a group with `disabled: true` adds no accounts |

In every section, `priority` is the account's `priority` and a key's `weight` is its `weight`.
A group's `models` list becomes the account's `models`: each entry's `name` is the model the
upstream knows, and its `alias` the name clients use; the account serves only those models. When
entries share a name clients use, the first wins in every section, as in today's core, except
`openai-compatibility`: there today's core treats them as a pool and rotates requests over them,
and falls back to the next on a failure. This core makes the pool a fallback chain
(`routing.chains`) named by the alias, with one step per upstream model in the order the group
lists them, each served by the group's accounts; the account lists each model under its own name
for the steps, and the log names the chain (a model listed twice adds no step). A name that
accounts of more than one provider serve, by their `models` lists or an `oauth.model-alias` alias,
which today's core shared among them, becomes a chain too, with one step per provider in the order
core reads the provider sections (`codex`, `claude`, `gemini`, Vertex, `xai`,
`openai-compatibility`, `interactions`), not the file's. A chain
tries its steps in order rather than in turn: see [Fallback chains](#fallback-chains). A group's or key's `prefix` is the account's
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
| `management.secret-key` (`remote-management.secret-key`) | `management_key`, exactly as written. Required: a config without one, or with a key already hashed with bcrypt (today's core can write one), is refused, saying what to set. | Supported; hashed keys refused |
| `management.allow-remote` (`remote-management.allow-remote`) | `management_allow_remote`: without it the management API answers only this machine, as in today's core. | Supported |
| `management.disable-control-panel` | The panel is always on, with the management API. | Dropped: the panel needs the management key |
| `management.disable-auto-update-panel`, `management.panel-github-repository`, `server.github-token` (`github-token`) | The panel is built into core; nothing is downloaded. | Dropped: no panel download |
| `management.base-url` | | Dropped: no terminal UI |
| `server.max-request-body-bytes` | Request bodies are capped at 24 MiB. | Dropped: core needs a fixed cap for its request frame |
| `server.response-compression.*` | | Dropped: a reverse proxy compresses replies |
| `server.tls.enable`, `.cert`, `.key` (`tls`) | `tls`, the same three keys. Relative paths are relative to the config file, where today's core reads them relative to the folder it was started in. | Supported |
| `server.trusted-proxies` (`trusted-proxies`) | `trusted_proxies`; a change applies on reload. | Supported |
| `server.commercial-mode` (`commercial-mode`) | | Dropped: it turns off features this core does not have |
| `server.discovery.*` (`discovery`) | | Dropped: no client here reads the advertisement |
| `server.discovery.advertise-management` | | Dead |
| `routing.strategy` | `routing.strategy`: `round-robin`, `fill-first` (also `fillfirst`, `ff`), `weighted-round-robin` (also `weightedroundrobin`, `wrr`; this core's `weighted`); this core's `weighted`, `least-active` and `soonest-reset` work too. A name today's core does not know is `round-robin`, as there, and the log says so. | Supported |
| `routing.session-affinity`, `routing.session-affinity-ttl` | `routing.session_affinity` with `ttl_ms` from the duration (default `1h`). A `config.yaml` without `session-affinity: true` has it off, as today's core does, though a JSON config has it on by default. | Supported |
| `routing.session-affinity-subagents` | A Claude Code subagent sends its session's `X-Claude-Code-Session-Id`, so with `routing.session-affinity` it stays on the session's account, as today's default `true` keeps it. `false` (spread subagents over the pool) is not built, and the log names it. | Dropped: a subagent stays on its session's account, and no need was found to spread subagents over the pool |
| `routing.session-affinity-rebind-on` | | Dropped: no need found |
| `routing.force-model-prefix` (`force-model-prefix`) | `routing.force_model_prefix`. | Supported |
| `routing.retry.max-retry-credentials` (`max-retry-credentials`) | `routing.max_accounts`. | Supported |
| `routing.retry.request-retry` (`request-retry`) | `routing.request_retry`: rounds after the first, run as today's core runs them. Default 0, as in today's core (its example config's 3 is only an example). | Supported |
| `routing.retry.max-retry-interval` (`max-retry-interval`) | `routing.max_wait_ms`, in seconds as in today's core: the longest wait before each round. Default 0: a round that needs a wait does not start. | Supported |
| `routing.cooldown.disable-cooling` (`disable-cooling`) | `routing.disable_cooling`. | Supported |
| `routing.cooldown.transient-error-cooldown-seconds` (`transient-error-cooldown-seconds`) | `routing.transient_error_cooldown_ms`: above 0 that many seconds, below 0 none. 0 is this core's backoff, from a second doubling to a minute, where today's core waits a fixed minute. | Supported |
| `routing.cooldown.quota-response-mode` (`quota-cooldown-response-mode`) | `routing.quota_response_mode`: `retryable` (default) or `usage-limit`, letter case aside; another value fails the load, as in today's core. | Supported |
| `routing.cooldown.save-cooldown-status` (`save-cooldown-status`) | Blocks live in memory and carry over reloads. | Dropped: no need found |
| `quota-exceeded.switch-project`, `quota-exceeded.switch-preview-model` | | Dead |
| `requests.proxy-url` (`proxy-url`) | `proxy_url`: the [outbound proxy](#outbound-proxy) of every account. A value that is not an `http`, `https`, `socks5` or `socks5h` URL, `direct` or `none` fails the load, where today's core logs it and goes without the proxy. | Supported |
| Every `proxy-url` on a group or key | The account's `proxy_url`: a key's own, else its group's. | Supported |
| `requests.payload.*` (`payload`) | `payload`: the [payload rules](#payload-rules), with today's lists and fields. A path outside the parts today's example config uses fails the load. Today's `protocol` named the upstream's API format; here it names the extension. Each rule is carried as today's core applied it, and every difference below is named in the log. A key is matched exactly: one in another letter case (`Override:`, `Name:`) or not a payload key was never read there, so it is dropped. A `-raw` param goes across as today's core sent it: a date (`2024-01-15`) is the JSON string `"2024-01-15T00:00:00Z"`, and a param that is null, `.inf` or `.nan` (today's core skipped it, and applied the rule's other params) is dropped. A `-raw` rule with a string param that is empty or not JSON is dropped as a whole, as today's core dropped it with a warning. A `.inf` or `.nan` (alone or inside a list or mapping) in a `default` or `override` param is dropped: today's core skipped the param or wrote `+Inf`, `NaN` or `-Inf` into the body, which no upstream accepts. A `model` whose `match` holds one is dropped, as today's core never matched it, and a `not-match` condition that holds one is dropped, as it never excluded a request there. | Supported |
| `requests.tls-handshake-timeout-seconds`, `requests.passthrough-headers`, `requests.nonstream-keepalive-interval`, `requests.streaming.*` (each also at the top level) | A request is retried on another account until its reply starts. | Dropped: off by default and no need found |
| `client.codex.enable-apply-patch` | Models without a Codex template are left out of the [Codex client catalog](#codex-client-catalog), so there is no entry to add `apply_patch` to. | Later: built when someone uses a non-Codex model through the Codex client and needs `apply_patch` on it |
| `client.codex.optimize-multi-agent-v2` (`codex.optimize-multi-agent-v2`) | | Later: built when Codex multi-agent v2 sub-agents need to reach non-Codex providers |
| `client.codex.web-search-result-cache.*`, `client.claude.show-codex-commentary` | | Dropped: off by default and no need found |
| `upstream.codex.disable-codex-cloaking` (`codex.disable-codex-cloaking`) | `codex_identity: false` on the Codex subscription accounts `pluxy import cpa` adds. It does not change accounts already imported. | Supported when importing |
| `upstream.codex.websocket-compression` | Always on; `false` is named in the log. | Supported (fixed on) |
| `upstream.codex.model-level-cooling` | Blocks are kept per model where the upstream says the limit is per model; usage limits remain account-wide. | Supported (fixed on) |
| `upstream.claude.model-level-cooling` (`claude.model-level-cooling`) | `model_level_cooling: true` on the Claude keys' accounts and on the Claude subscription accounts `pluxy import cpa` adds (see [Claude](#claude)). It does not change accounts already imported. | Supported |
| `upstream.codex.upstream-request-compression`, `-min-bytes` | | Dropped: it needs a compression library this core does not carry, for an off-by-default saving no one has measured |
| `upstream.codex.stream-bootstrap-buffering`, `stream-bootstrap-timeout` | The Codex extension does not move a turn that ends in an overload error to another account. | Later: built when pooled Codex accounts are seen failing on overload errors that moving the turn to another account would hide |
| `upstream.codex.response-steering`, `upstream.codex.orphan-delegation-compatibility`, `oauth.providers.codex.header-defaults.*` (`codex-header-defaults`) | | Dropped: experimental or off by default, no need found |
| `upstream.claude.header-defaults.*`, `disable-claude-cloak-mode`, `disable-cloaking-model-list`, `session-date-pin-days` (also `claude`, `claude-code`, `claude-header-defaults`), and the Claude keys' `cloak` and `fingerprint-profile` | This core never changes how a client presents itself. | Dropped: making other clients look like Claude Code is out of scope |
| `upstream.claude.tool-search`, the Claude keys' `native-tool-search` | | Dropped: Codex's tool search on Claude accounts, off by default with no panel or app control |
| `rebuild-mid-system-message` (Claude keys) | The account's `rebuild_mid_system_message`. | Supported |
| `upstream.xai.inject-x-search` (`oauth.providers.xai.inject-x-search`, `xai.inject-x-search`) | xAI accounts get `inject_x_search`; applies to API keys and imported Grok logins | Supported |
| `experimental-cch-signing` (Claude keys) | | Dead |
| `oauth.auth-dir` (`auth-dir`) | The folder `pluxy import cpa` reads (see below). Core never reads or writes it while it runs. | Supported |
| `oauth.auth-auto-refresh-workers` (`auth-auto-refresh-workers`) | Each account is renewed shortly before it expires. | Dropped: no need for a pool size |
| `oauth.model-alias` (`oauth-model-alias`), `oauth.excluded-models` (`oauth-excluded-models`) | Each channel's rules become the `model_alias` and `excluded_models` of the package that serves its accounts, for the accounts added to it by a sign-in (not with an API key): `codex` is `pluxy/codex`, `claude` is `pluxy/claude`, `vertex` is `pluxy/gemini`, `kimi` (and `kimi-ai`, `kimi.ai`, `kimi.com`) is `pluxy/kimi`, `xai` is `pluxy/xai`, `meta` is `pluxy/meta`, `devin` is `pluxy/devin`. Rules for another channel are named in the log and ignored. An alias's `force-mapping` and `openapi` are later, as the same settings are on the keys' models below. | Supported |
| `oauth.settings` (`oauth-settings`) | | Dropped: needs a model catalog, and no page uses it |
| `oauth.request-scoped-errors` (`oauth-request-scoped-errors`) and every `request-scoped-errors` | | Dropped: a request-scoped error is already not the account's fault |
| `oauth.providers.aistudio.ws-auth` (`ws-auth`) | | Dropped: the AI Studio relay needs a long-lived listener the extension protocol does not have |
| `oauth.providers.codex.live-media-relay.*` | | Dropped: realtime and WebRTC are out of scope |
| `oauth.providers.codex.signin-with-chatgpt-identifier` (`codex.signin-with-chatgpt-identifier`) | `signin_with_chatgpt_identifier` on the Sign in with ChatGPT (SIWC) accounts `pluxy import cpa` adds. Left unset, they use the extension's default, `pluxy`. | Supported when importing |
| `oauth.providers.devin.*` (`devin`) | Only `sensitive-words`. | Dropped: rewriting words to get past a content filter is out of scope |
| `api-keys.meta` (`meta-api-key`) | | Dropped: the login is built; no need for a bare key |
| Every `headers` on a group or key | The account's [`headers`](#upstream-headers): a key's own, else its group's (a key's `headers: {}` sends none), and an `openai-compatibility` group's for all its keys. Names and values are trimmed and empty ones dropped, as in today's core. A header whose value today's core takes from the client's request (`$` and a header name, or `$CPA-SESSION-ID`) is named in the log and not imported. A header this core refuses (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`) fails the load, where today's core sends it. | Supported |
| Every `connection-pool` | | Dropped: extensions own their connections |
| Every `disable-cooling` on a group or key | The account's `disable_cooling`. | Supported |
| Every `request-retry` on a group or key | The account's `request_retry`: a key's own, else its group's (an `openai-compatibility` group's for all its keys, which have none of their own there). A negative one inherits `routing.retry.request-retry`, as in today's core. | Supported |
| The keys' `models[]` `display-name`, `max-context-length`, `input-modalities`, `output-modalities` | A model's display name in lists is its upstream name; use `model_alias` for a different one. | Dropped: list metadata only, no need found |
| The keys' `models[]` `thinking` | The thinking suffix and the extensions cover it. | Dropped: no need found |
| The keys' `models[]` `is-compat`, `use-max-completion-tokens` | | Dropped: single-provider quirks, no need found |
| The keys' `models[]` `force-mapping` | A reply carries the upstream's model name, not the alias the client asked for. | Later: built when a client needs a reply's model to be the alias it asked for |
| The keys' `models[]` `openapi`; Vertex keys' `openapi`, `openapi-location` | | Later: built when someone calls Vertex partner models (Claude, Llama, Mistral) through a key |
| The keys' `models[]` `image` (openai-compatibility) | A Chat or Responses OpenAI-compatible account serves the Images API for any model it lists; the old per-model flag is not imported. | Dropped: the model list already decides which Images requests route to the account |
| The keys' `models[]` `support-configuration-update` | | Dropped: an off-by-default prompt-cache saving for one kind of relay, no need found |
| xAI keys' `websockets` | xAI is served over HTTP. | Dropped: HTTP gives the same replies |
| openai-compatibility's `support-prompt-cache-key` | A [payload rule](#payload-rules) can set a fixed `prompt_cache_key`. | Dropped: no need found |
| `multimedia.disable-image-generation` (`disable-image-generation`) | `true`, `"chat"` and `"passthrough"` are `image_generation_tool: false` on Codex accounts from the file and on those `pluxy import cpa` adds; `true` is also `images: false` there, which turns the Images API off on them (today's core answers its image routes 404). | Supported |
| `multimedia.gpt-image-2-base-model`, `multimedia.video-result-auth-cache-ttl` (each also at the top level) | Images are served through the Codex upstream's Images API, the path today's core takes for its built-in image models; the Responses-based path this model sets is not built, and there are no video routes. | Dropped: the first names the model of a path that is not built, the second belongs to video routes that are not built |
| `observability.logs.logging-to-file` (`logging-to-file`), `observability.logs.logs-max-total-size-mb` (`logs-max-total-size-mb`) | `logs.to_file` and `logs.max_total_size_mb`: [log files](#log-files). A negative cap is none, as in today's core. | Supported |
| `observability.logs.debug` (`debug`) | | Dropped: this core writes no debug-level lines |
| `observability.logs.request-log` (`request-log`), `observability.logs.error-logs-max-files` (`error-logs-max-files`) | `logs.request_log` and `logs.error_logs_max_files`: [request logs](#request-logs). A negative count is the default, 10, as in today's core. | Supported |
| `observability.usage.usage-statistics-enabled` (`usage-statistics-enabled`) | `usage.enabled`: the [usage record](#usage-statistics). Today's example config sets it to `false`, which turns the record off here too. | Supported |
| `observability.usage.redis-usage-queue-retention-seconds` (`redis-usage-queue-retention-seconds`) | The record keeps requests by `usage.max_events` and `usage.max_age_ms`. | Dropped: it was how long a queued usage record waited for a reader that removed it; reading the record here removes nothing, and its default of 60 seconds would leave little to read |
| `observability.pprof.enable` (`pprof.enable`), `observability.pprof.addr` (`pprof.addr`) | `pprof.enable` and `pprof.addr`: the [profiling pages](#profiling-pages). Only a build with `-tags pprof` serves them; the default build logs that the setting does nothing. An address that is not loopback is refused. | Supported (in the `pprof` build) |
| `models.catalog`, `models.codex-catalog`, `models.devin-catalog` | Each extension carries its model list, and a developer refreshes it by hand (see [Codex client catalog](#codex-client-catalog)). | Dropped: this core has no remote model catalog |
| `plugins.*` | Extensions are separate programs (packages), not libraries loaded into core. | Dropped: replaced by packages |
| `credentials.*` (`credential-concurrency`, `credential-in-flight`) | | Dropped: only the Home control plane used them |

Environment variables today's core reads (`MANAGEMENT_PASSWORD`, `PGSTORE_*`, `GITSTORE_*`,
`OBJECTSTORE_*`, ...) and its `.env` file are not read.

### Moving from CLIProxyAPI: pluxy import cpa

```bash
./pluxy import cpa ~/.cli-proxy-api/config.yaml --dry-run
./pluxy import cpa ~/.cli-proxy-api/config.yaml [--auth-dir <dir>]
```

`pluxy import cpa` brings a CLIProxyAPI setup (today's core's `config.yaml` and its auth folder)
across to a running core, through the management API's
[`POST /setup/import`](management/README.md#setup). Core reads both on its own machine. It prints
three lists: what was **brought**, what was **changed** on the way (with what it was, what it is now,
why, and how to keep the old behaviour), and what was **not brought** (with why). Together they name
every key of the file and every file of the auth folder, each once; an import that cannot place an
item fails, naming it, and writes nothing. `--dry-run` prints the same lists and writes nothing;
`--json` prints the reply. The lists end with the provider packages the imported accounts need and
core has not installed: install them with `pluxy ext install <id>`.

The file's settings, client keys, API-key accounts, aliases and chains go into core's config file
in one change, through the same checks as any change; the auth folder's accounts go into the
credential store and serve at once. Core keeps its own `listen` (it cannot change while core runs),
`credentials_dir`, `packages`, `extension` and `extension_pages`; an account or chain core already has by that ID
or name stays as it is, and the lists say so, so running the import again adds nothing twice. The
auth folder defaults to the file's `oauth.auth-dir`; the import never changes it, and refuses a
`credentials_dir` that is the auth folder or inside it. A core that runs on a `config.yaml` itself
takes dry runs only.

What the import changes, each under **changed**:

- `server.host` and `server.port`: core keeps its own `listen` while it runs, so every listen row
  is under **changed**: it names the file's reach in `from` and core's address in `to`, and
  `keep_with` gives the `"listen"` value to write and restart core with. A file with no
  `server.host` (or an empty one) is one of these: today's core listened on every interface then,
  and pluxy listens on `127.0.0.1` unless told otherwise.
- The example config's public client keys (`your-api-key-1` and the rest) are left out.
- Pooled models become fallback chains, as [Provider key sections](#provider-key-sections) describes.

A `management.secret-key` is not brought, neither a plain key nor the bcrypt hash today's core may
have written over it: core keeps its own management key, which guards every client of it, the
desktop app's core among them, so an import does not change who may manage core. Its **not
brought** row says so. Change core's key with `POST /v0/management/management-key/rotate`, which
returns the new key once.

Each file of the auth folder becomes an account added by login, with the file's name (less
`.json`) as its ID. A file whose account ID the config or the store already uses is not brought,
nor is a file larger than 1 MiB, nor anything but a `.json` file at the top of the folder.

To add one file to a running core instead, send it to the management API's
[`POST /accounts/import`](#accounts-added-by-login) (the panel's "Upload File" on Accounts): it
reads the file the same way and the account serves at once.

Both cores renewing the same login signs one of them out: the provider replaces the refresh
token at each renewal, and the other core's copy stops working. Once an account is imported,
use it from one core only, or sign in again in the other.

| File `type` | Becomes | Notes |
| --- | --- | --- |
| `codex` | `pluxy/codex` subscription account | `access_token`, `refresh_token`, `expired` (the expiry); `id_token` and `account_id` stay with the credential; `websockets` is the account's `websockets`. A `siwc` section with a token (`access_token`, else `access`, or `refresh_token`) also becomes a [SIWC](#codex) account, `<file id>-siwc` (with `-2`, `-3` and so on when another file in the folder imports as that ID), even when the file's own tokens are missing: its `access_token`, `refresh_token`, `expires_at` (else `expired`), and `client_id`, `device_id`, `scope` and `id_token` with the credential. It is disabled unless the file says `use_siwc: true`; it shares the file's label and routing fields below, and takes only `websockets` and the identifier setting of the account settings. |
| `claude` | `pluxy/claude` subscription account | `access_token`, `refresh_token`, `expired`. |
| `kimi`, `kimi-ai` | `pluxy/kimi` account | `access_token`, `refresh_token`, `expired`, `device_id`; `base_url` is an account setting, and so is the site, `domain`, decided as today's core decides it: from `domain`, else `base_url`'s host, else the type (`kimi.ai` for `kimi-ai`). |
| `vertex` | `pluxy/gemini` Vertex AI account with a service account | `service_account` is the credential; `project_id` and `location` (default `us-central1`, as in today's core) are the account's `vertex` settings. `interactions` or `native_interactions` set to JSON `true` at the top of the file is the account's `interactions` setting. |
| `xai` | `pluxy/xai` Grok login account | `access_token`, `refresh_token`, `expired`; `token_endpoint` stays with the credential and `base_url` is an account setting. |
| `meta` | `pluxy/meta` Muse Code login account | `api_key` (else `access_token`) is the minted key and `dca_token` (else a `dca:` `access_token`) the device token that mints a new one; `base_url` stays with the credential. A file with only the device token mints its key on the first request. |
| `devin` | `pluxy/devin` account with a session token | `session_token` (else `api_key`) is the credential; `base_url` is an account setting and `user_name` the label when the file has no `label` or `email`. |
| `antigravity` | Not brought | pluxy no longer ships the Antigravity provider. |
| anything else | Not brought | Today's core does not load a file with no `type` or with `gemini` either. |

From every file: `disabled: true` imports the account disabled, `priority` and `weight` are the
account's, `disable_cooling` (or `disable-cooling`) is its `disable_cooling`, `request_retry`
(or `request-retry`, a number or numeric text; a negative one inherits routing's) is its
`request_retry`, and `label` (else `email`) is its label. A file's `prefix` is the account's `prefix`,
its `excluded_models` (a list, or one comma-separated string) its `excluded_models`, and the
entries of its `model_aliases` (or `models`) with a `name` and an `alias` its `model_alias`, as
today's core reads them. A file's `proxy_url` (or `proxy-url`) is the account's `proxy_url`; a file
whose value this core cannot use is skipped rather than imported without its proxy. A file's
`headers` are the account's `headers`: text values only, trimmed, empty ones dropped, as today's
core reads them; one whose value today's core takes from the client's request (`$` and a header
name, or `$CPA-SESSION-ID`) is named under **changed** and not brought, and a file with a header this
core refuses is skipped rather than imported without it.

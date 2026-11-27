# Translation

A client talks to pluxy in one API format: OpenAI Chat Completions, OpenAI Responses, Anthropic
Messages or Gemini GenerateContent. A provider's upstream speaks one format of its own. When the two
differ, core and the provider's extension translate the request on the way in and the reply on the way
back. This page is a reference for that translation: what carries over unchanged, what is changed and
how, what is dropped or refused, and what the client then sees. Sign-in, account settings and model
lists are on [Providers](providers.md). Routing between accounts is on [Routing](routing.md). The
reasons for this design are in [Design](design.md); what is planned is in [Plans](plans.md).

## How to read the tables and sections

Words used throughout:

- **Client format**: the API the client speaks to pluxy. **Upstream format**: the API the provider's
  extension speaks to its upstream.
- **As written**: the client's own body is sent to the upstream with only the changes the section
  lists. This happens when the client format is the upstream format.
- **Rebuilt**: the extension builds a new body from core's internal model of the request. This happens
  when the formats differ.
- **Loss**: one thing a translation could not carry over exactly. It is *approximated* (the meaning
  went across in another form) or *dropped* (the target has no place for it). The **translation
  report** is the list of one request's losses. Core writes it to its log; the client does not receive
  it. A request's reply is normal.
- **`drop_policy`**: the config key that says what a loss does. With `report` (the default) the request
  is sent without the dropped part. With `reject` a request that would lose anything *dropped* is
  refused with a 400 that names the dropped parts. An *approximated* loss never refuses a request. See
  [`drop_policy`](#drop_policy) and [Config](config.md#top-level).

In the table below, a cell says what happens to that feature on that provider:

| Cell | Meaning |
| --- | --- |
| **works** | The feature reaches the upstream with the same meaning. |
| **changed** | It is carried, but rewritten or mapped to a different feature. The provider's section says how. |
| **dropped** | The extension leaves it out and sends the rest. The loss is in the translation report. With `drop_policy: "reject"` the request is refused. |
| **refused** | The request fails with an error whatever `drop_policy` says. |
| **not supported** | The extension has no code for it. For an endpoint call, core tries the model's next account. For a token count, core answers with its own estimate unless `token_count_estimate` is `false`. |

Each cell is the outcome for a request that has to be translated, and where a feature has several
settings it shows the strongest outcome among them (for example `dropped` when any setting is
dropped). A request whose client format is the provider's own passes as written, so most of these
features reach the upstream as the client wrote them; each provider's section lists the changes that
still apply. No cell says **refused**, because a refusal depends on the request (an empty turn, a
streamed compaction, a request body over the size limit) or on `drop_policy`: those are in the shared
rules below and in each provider's section.

## Feature by provider

| Feature | Claude | Codex | Gemini | OpenAI-compatible | xAI | Kimi | Meta (Muse) | Devin |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Function tools | works | works | works | works | works | works | works | works |
| Tool schemas | changed | changed | changed | works | changed | changed | works | works |
| Tool names | changed | changed | changed | changed | changed | changed | works | works |
| Tool choice | changed | changed | changed | changed | changed | changed | changed | dropped |
| Parallel tool calls | changed | changed | changed | works | works | works | works | dropped |
| Custom and namespace tools | changed | works | changed | changed | changed | works | works | dropped |
| Reasoning and thinking | changed | changed | changed | changed | changed | changed | changed | changed |
| Sampling settings | dropped | dropped | dropped | dropped | dropped | dropped | dropped | dropped |
| Images in | changed | works | changed | works | works | works | works | changed |
| Files and documents in | changed | changed | changed | changed | changed | dropped | works | dropped |
| Audio in | dropped | changed | works | works | changed | works | works | dropped |
| Speech and transcription | not supported | not supported | changed | works | not supported | not supported | not supported | not supported |
| Images out (Images API) | not supported | works | changed | works | not supported | not supported | not supported | not supported |
| Web search | changed | changed | changed | dropped | dropped | dropped | dropped | dropped |
| Code execution | changed | dropped | changed | dropped | dropped | dropped | dropped | dropped |
| Citations | changed | changed | changed | dropped | dropped | works | dropped | not supported |
| Structured output | changed | changed | changed | changed | changed | changed | changed | dropped |
| Service tier | changed | dropped | changed | dropped | dropped | dropped | dropped | dropped |
| Verbosity | dropped | dropped | dropped | dropped | dropped | dropped | dropped | dropped |
| Prompt caching | changed | changed | dropped | dropped | dropped | works | dropped | dropped |
| Multiple candidates (`n`) | dropped | dropped | changed | dropped | dropped | dropped | dropped | dropped |
| Embeddings | not supported | not supported | changed | works | not supported | not supported | not supported | not supported |
| Compaction | changed | changed | changed | changed | changed | not supported | not supported | not supported |
| Token counting | works | not supported | works | not supported | not supported | works | not supported | not supported |
| Rate-limit headers | changed | changed | dropped | changed | dropped | dropped | dropped | dropped |

Notes to the table:

- **Sampling settings** are `temperature`, `top_p`, output token limit, stop sequences, `top_k`, seed
  and the penalties. `top_k`, seed and the penalties are dropped everywhere. The others work, except
  where a provider's section says it removes them (Codex removes `temperature` and `top_p`; Kimi
  removes a `temperature` other than 1.0; Devin drops `top_p` and stop sequences).
- **Verbosity, service tier, web search, code execution and `n`** from another format are sent only to
  the hosts the provider's section names ("Service tier, verbosity and gated features" in the shared
  rules). A client whose format is the provider's own sends them as written.
- **Citations** for the providers marked `dropped` are carried to Responses clients, to Gemini clients
  and to non-streamed Chat Completions clients, and dropped for Messages clients and streamed Chat
  Completions clients.
- **Token counting**: Claude counts upstream only on `https://api.anthropic.com`; OpenAI-compatible
  counts upstream only on the `messages` and `gemini` wires; Codex counts only the Responses
  `input_tokens` route, on an API-key account.
- **OpenAI-compatible** cells are for the default `chat` wire. Each section tells where another
  `wire_api` differs.
- **Embeddings, speech and Images API** are endpoint calls: a provider that does not serve one is
  skipped for it.

The sections after the table are the shared rules, then one section per provider.

## Shared rules

Scope: the rules that hold for more than one provider. They live in `next/format` (one package per
client format, plus shared helpers), `next/llm` (the internal model), `next/internal/host`,
`next/internal/server` and `next/internal/route`. A rule says which providers it applies to when the
code makes that visible. Provider-only rules (host checks, account settings, model lists) are in the
provider sections.

Terms used below:

- **Client format**: the API a client speaks to pluxy.
- **Upstream format**: the API a provider extension speaks to its upstream.
- **Internal model**: what every client request and every upstream reply is turned into, so any
  client format can meet any upstream format.
- **Loss**: one thing a translation could not carry over exactly. It has a kind (`approximated` or
  `dropped`), a path (the field in the client's body, such as `input[1].content[0]`), and a detail.
- **Translation report**: the list of losses for one request. It is not sent to the client (see
  "The translation report").

### Client formats and routes

| Client format | Routes |
| --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions`; `POST /v1/completions` (the legacy text API, served as Chat Completions) |
| OpenAI Responses | `POST /v1/responses`, `POST /responses`, `POST /backend-api/codex/responses`; a WebSocket on `GET` of the same three paths |
| Anthropic Messages | `POST /v1/messages`, `POST /v1/messages/count_tokens` |
| Gemini GenerateContent | `POST /v1beta/models/{model}:generateContent`, `:streamGenerateContent`, `:countTokens`, `:embedContent`, `:batchEmbedContents` |

- `:streamGenerateContent` always answers with server-sent events, with or without `alt=sse`. A
  Gemini model name may contain slashes.
- Other client routes are endpoint calls, not translations: `POST /v1/responses/compact`,
  `/v1/responses/input_tokens`, `/backend-api/codex/responses/compact`, `/v1/alpha/search`,
  `/backend-api/codex/alpha/search`, `/v1/images/generations`, `/v1/images/edits`,
  `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/translations`, `/v1/embeddings`,
  `/v1beta/interactions`. The body goes to an account whose extension serves that endpoint, and the
  extension writes the reply. An extension that does not serve it answers `unsupported`, and
  routing tries the next account. When every account answered `unsupported`, the client gets
  `503` with code `auth_not_found` and message `no auth available`, unless a refusal carried status
  400 or was about the request itself. `drop_policy` is not applied to endpoint calls.
- A request body may be up to 24 MiB. A larger body gets `413` (`request body is too large`).
  Core decodes a `gzip` body; any other `Content-Encoding` gets `415`.
- A reply that is not streamed is collected up to 64 MiB. A larger one fails with an `upstream`
  error that tells the client to send `"stream": true`.

### The internal model

- A request is an ordered list of items (`message`, `tool_call`, `tool_result`, `reasoning`, and
  `native` for an item core could not read), a list of tools (`function`, `web_search`,
  `code_execution`, `native`), and options: `max_output_tokens`, `temperature`, `top_p`, `stop`,
  `tool_choice`, `parallel_tool_calls`, `response_format`, `reasoning_effort`, `thinking_suffix`,
  `service_tier`, `verbosity`, `candidate_count`.
- A reply is a stream of events: `response.start`, `item.start`, text, reasoning and tool-input
  deltas, `item.end`, `media`, `candidate`, and exactly one `finish` or `error`.
- Fields the model has no place for stay with the item, tool or request they came from, together
  with the client format and path. An encoder for the **same** client format writes them back. An
  encoder for **another** format reports each one as a `dropped` loss, with the detail
  `no place for it in <format>`.
- These request settings are modelled; every other top-level field is kept only as described above:

| Setting | Chat Completions | Responses | Messages | GenerateContent |
| --- | --- | --- | --- | --- |
| Output token limit | `max_completion_tokens`, else `max_tokens` | `max_output_tokens` | `max_tokens` | `generationConfig.maxOutputTokens` |
| Temperature, top P | `temperature`, `top_p` | `temperature`, `top_p` | `temperature`, `top_p` | `temperature`, `topP` |
| Stop sequences | `stop` | not modelled | `stop_sequences` | `stopSequences` |
| Reasoning effort | `reasoning_effort` | `reasoning.effort` | `thinking`, `output_config.effort` | `thinkingConfig` |
| Structured output | `response_format` | `text.format` | `output_config.format` | `responseMimeType`, `responseSchema`, `responseJsonSchema` |

- Not modelled, so dropped on every translation to another format (one loss per field): sampling
  settings such as `top_k`, `seed`, `frequency_penalty`, `presence_penalty`, `logprobs`,
  `logit_bias`; and request fields such as `user`, `metadata`, `store`, `previous_response_id`,
  `include`, `prompt_cache_key`. This applies to all providers.
- A translated Chat Completions request is written with `max_tokens`. A translated Messages request
  without a limit gets `max_tokens` 32000 (a provider may use the model's own limit instead).
- Passthrough: when the client format is the upstream format, the extension sends the client's
  body (kept as `native`) instead of rebuilding it from the model. The extension may still change
  that body, and each change is a loss in the report. Providers and formats that pass through:
  Claude (Messages), Codex (Responses), Gemini (GenerateContent), xAI and Meta (Responses),
  OpenAI-compatible (the format its `wire_api` names), Kimi (Chat Completions, Responses and
  Messages, each to its own API). Devin never passes through: every client format is rebuilt as
  Devin's own chat request.
- Which upstream format a rebuilt request gets: Claude, Messages; Codex, xAI and Meta, Responses;
  Gemini, GenerateContent; OpenAI-compatible, the format its `wire_api` names; Kimi, Chat
  Completions (for Chat and Gemini clients). The shared encoders are `openaichat`, `openairesponses`,
  `anthropicmessages` and `geminigenerate`.

### The translation report

- Each extension adds a report (`from`, `to`, `passthrough`, `losses`) to the reply's
  `response.start` event. A passthrough report lists what the extension changed in the client's
  body (for example a thinking-suffix effort or a history repair).
- Core writes the report to its log only; no client format carries it. One line per request:
  `translation: request dropped <path> (<detail>); reply dropped <path> (<detail>)`. It names at most
  20 losses and says how many more there were; a path or detail is cut at 200 bytes.
- Reply-side losses (what the client format cannot show) are collected by the client format's
  writer and logged when the reply ends.
- A report lists at most 100 losses; one more entry says that more were left out.
- `approximated`: the meaning was carried in a different form. `dropped`: the target has no place
  for it. Examples of `approximated`: tool arguments that are not a JSON object sent as `{}`; a
  reasoning summary sent as the next assistant message's `reasoning_content`; assistant text after
  tool calls sent in the message with the calls; a tool result's image sent in a user message after
  the tool messages; a Messages plain-text document sent as text; `parallel_tool_calls: false` on
  Gemini; a `defer_loading` tool declared from the start. Reply-side losses are all `dropped`,
  except an unreadable `tool_search` call's arguments (`approximated`).

### `drop_policy`

`drop_policy` (top level of `config.json`; `"report"` or `"reject"`, default `"report"`; any other
value stops the config from loading).

- `"report"`: the request is sent with the loss left out, and the loss is logged. The client sees
  nothing.
- `"reject"`: if the request would lose anything of kind `dropped`, the extension does not send it.
  `approximated` losses never refuse a request. Reply losses are not refused (the reply has already
  started).
- Core only copies the setting into each generate, token-count and WebSocket-turn request. The
  extension checks it, after it has built the body and listed the losses.
- The refusal is an error with code `unsupported`, scope `credential`, retryable, and the dropped
  paths. Routing then tries the next account for the model (for example one that speaks the
  client's own format). If no other account takes it, the client gets the last error: status
  `400`, and the message names the upstream format and the dropped paths where the extension adds
  them.
- Devin refuses with scope `request` and not retryable, so no other account is tried.

What the client sees for the refusal (the same `400` for every translated provider):

| Client format | Body |
| --- | --- |
| Chat Completions, Responses | `{"error":{"message","type":"invalid_request_error","code":"unsupported"}}` |
| Messages | `{"type":"error","error":{"type":"invalid_request_error","message"}}` |
| GenerateContent | `{"error":{"code":400,"message","status":"INVALID_ARGUMENT"}}` |
| Responses WebSocket turn | `{"type":"error","status":400,"error":{"message","type":"invalid_request_error"}}`; no `code` member |

The message by provider:

| Provider | Message | Checked on |
| --- | --- | --- |
| Claude | `Anthropic Messages cannot carry parts of this request: <paths>` | translated and passed-through bodies |
| Codex | `OpenAI Responses cannot carry parts of this request: <paths>`; after shaping: `the Codex endpoint cannot take parts of this request: <paths>` | translated bodies; shaped bodies (also passthrough) |
| Gemini | `Gemini GenerateContent cannot carry parts of this request: <paths>` | translated bodies |
| OpenAI-compatible | `<Chat Completions, OpenAI Responses, Anthropic Messages or Gemini GenerateContent> cannot carry parts of this request: <paths>` | translated bodies |
| xAI, Meta | `Responses cannot carry parts of this request` (no paths) | translated bodies |
| Kimi | `Chat Completions cannot carry parts of this request` (no paths) | the Chat Completions rebuild; token counts too |
| Devin | `Devin has no place for <paths>` | every request |

The error carries a `pluxy` member that lists the accounts tried, with result `unsupported`, unless
`routing.explain` is `false`.

Core also refuses under `reject` when an extension that implements an older protocol minor version
would lose a tool result's parts or error flag, a service tier, a verbosity or a candidate count:
the message names the protocol versions and the paths. Core narrows a tool choice's allowed list
for such an extension without reporting a loss.

### Tools

- **Function tools**: carried by every translation. A schema is sent as the client wrote it, except
  to Messages: the root is made an object, and `anyOf`, `oneOf` and `allOf` branches at the root
  are merged into it. That change is not reported. Applies to Claude and OpenAI-compatible
  (`wire_api: messages`). `strict` is dropped (reported) for GenerateContent.
- **Tool choice**: `auto`, `none`, `required` and a named tool are carried to every format.
  `required` is `any` on Messages. `parallel_tool_calls: false` becomes `disable_parallel_tool_use`
  on Messages, is sent as it is to Responses, and is sent to Chat Completions only when the request
  has tools; Gemini cannot limit a reply to one call, so function calling is turned off (mode
  `NONE`) and the loss is `approximated`.
- **Allowed tools** (Chat Completions and Responses `allowed_tools`, `mode` `auto` or `required`,
  function tools only; GenerateContent `allowedFunctionNames`): carried as the same list to Chat
  Completions and Responses; as `allowedFunctionNames` (`VALIDATED` for auto, `ANY` for required)
  to GenerateContent. Messages has no list, so only the allowed function tools are defined and
  every other tool is left off, unreported; an allowed name the request does not define is a
  `dropped` loss; when no function tool is left the request fails with `400`
  `tool_choice allows no function tool this request defines: <names>`.
- **Custom tools, namespace tools, `tool_search`** (what the Codex client sends): sent unchanged to
  a Responses upstream. For a Chat Completions, Messages or GenerateContent upstream they are sent
  as functions (all providers that build those formats), and the calls come back in the form the
  client declared:
  - A custom tool is a function with one required string argument `input`; its grammar goes into the
    description. Its call returns as a `custom_tool_call` only when the arguments are exactly one
    whole `{"input":"..."}` object (an empty string counts). Any other call never reaches the
    client as a call, because Codex runs a finished call whatever its status says. If the reply
    stopped for the token limit or a content filter, the reply stays `response.incomplete` and the
    call is a `dropped` loss. On any other finish the reply fails with an `upstream` error
    (`invalid arguments for custom tool <name> received from upstream`, status 502).
  - A namespace's tools are functions named `namespace__tool`. A name that already has the prefix,
    a name that starts with `mcp__`, and a tool in the namespace `functions` keep their name. The
    namespace's description goes on its first tool.
  - An `additional_tools` input item's tools count as the request's.
  - A client-run `tool_search` (`execution: "client"`) is a function named `tool_search`; its call
    returns as a `tool_search_call`. A tool marked `defer_loading` stays off the request until a
    `tool_search_output` in the conversation loads it. A provider-run `tool_search` is a `dropped`
    loss and the tools it would load are declared from the start (`approximated`).
  - Replayed calls and outputs of these tools go up as function calls and results.
  - Names that are equal once flattened are one tool: the `tools` list first, then an
    `additional_tools` item, a top-level tool before a namespace's tool, then the first declared;
    the others are `dropped` losses.
  - A `tool_choice` or `allowed_tools` entry that names one of these tools names its function.
- A Chat Completions custom tool (`type: "custom"`) and its calls pass to a Chat or Responses
  upstream; to Messages and GenerateContent a custom call is `dropped`; any other tool type that
  the target format does not know is `dropped`.
- **Tool names**: Responses upstreams take any name. The other three upstream formats take only
  some names, so a name that does not fit is sent changed, and the reply's call goes back to the
  client under its own name. The same function sets the names for the request and the reply.

| Upstream format | Allowed characters | Length | Hash part |
| --- | --- | --- | --- |
| Chat Completions | `a-z A-Z 0-9 _ -` | 64 bytes | after the first 53 bytes |
| Messages | `a-z A-Z 0-9 _ -` | 64 bytes | after the first 53 bytes |
| GenerateContent | `a-z A-Z 0-9 _ . : -`; first character a letter or `_` | 128 bytes | after the first 117 bytes |

  Another character becomes `_`; a name is cut to the limit. When two names would come out equal,
  or a name is already taken, each of them gets `_` and the first 10 hex digits of a SHA-256 of the
  client's name. Declared tools are named first, then the names of replayed calls.

### Reasoning and the model-name thinking suffix

- A model name may end in a thinking setting in brackets: a level (`minimal`, `low`, `medium`,
  `high`, `xhigh`, `max`), `none`, `auto`, or a token budget (a whole number of 0 or more; `-1` is
  `auto`). Case and spaces inside the brackets are ignored. Text in brackets that is none of these,
  and a name that is only a bracket, stay part of the model name.
- Core removes the suffix before routing, so the request is routed by the model's own name. It
  also removes it from the model in the client's body (not for GenerateContent, whose model is in
  the path). This holds on every route that names a model: generate, token counts, endpoint calls
  and both kinds of WebSocket turn.
- The suffix wins over any reasoning setting in the request. As an effort, a budget is the level it
  falls in: `0` `none`, up to 512 `minimal`, up to 1024 `low`, up to 8192 `medium`, up to 24576
  `high`, more `xhigh`.
- Where it is applied: Claude and Gemini apply a budget as a budget on models that take one (and
  the level otherwise); Codex and OpenAI-compatible apply the level (OpenAI-compatible also
  passes it to Messages and GenerateContent upstreams in Claude's and Gemini's way); Kimi sends
  the level as `thinking: {type, effort}` on its Chat Completions API (the level `minimal` or
  `low` is `low`, `xhigh` or `max` is `max`, anything else `high`); Devin picks the model variant
  by level. xAI and Meta do not read the suffix: in a request translated to Responses the level
  appears as `reasoning.effort`, and a Responses client's own body keeps its own setting.
- To Messages and GenerateContent the encoders drop the client's effort (`reasoning_effort`), because
  thinking budgets depend on the model; Claude and Gemini remove that loss when they apply the
  setting themselves.
- Reasoning from another provider is not replayed to Messages, Responses or GenerateContent
  upstreams (`dropped`: the signatures would not verify), except: a Messages thinking block whose
  signature is a Responses upstream's encrypted reasoning goes back as that reasoning item (the
  summary text is not sent: `approximated`). To a Chat Completions upstream it is `approximated`:
  the text goes into the next assistant message's `reasoning_content`.
- What the client gets: Chat, `reasoning_content` on the message or delta; Responses, a `reasoning`
  item with a `summary_text` and `encrypted_content` when the upstream gave an opaque record;
  Messages, a `thinking` block, its `signature` carrying a Responses upstream's encrypted reasoning,
  a Gemini thought signature or a Devin signature; GenerateContent, a `thought` part (a Devin
  signature as `thoughtSignature`).
- Reasoning token counts: see "Usage".

### Reading a Chat Completions reply (upstreams that speak Chat Completions)

Applies to OpenAI-compatible (`wire_api: chat`) and Kimi's Chat API.

- Reasoning is read from `reasoning_content`, `reasoning` or `reasoning_details`, in that order,
  from the first field that has text, so the same text in two fields shows once.
  `reasoning_content` may be a string or a list of parts. In `reasoning_details`, an entry's `text`
  is used, else its `summary`; an encrypted entry (`reasoning.encrypted`, `encrypted`) gives nothing.
- A `<think>` tag that opens the content (after optional white space) and its closing tag become
  reasoning, also when a tag is split across stream chunks. An unclosed tag makes the rest of the
  content reasoning. A tag later in the text stays text. Text equal to a reasoning field's text is
  not repeated.
- A tool call with no function name is dropped (reported); a call with no ID gets `call_<n>`; a
  finish of `tool_calls` with no call left becomes `stop`.
- Refusal: a `refusal` on a message or delta becomes the reply's text, and the finish becomes a
  content filter. What the client format shows for a content-filter finish: Chat Completions
  `content_filter` (when the reply came from a Chat Completions upstream, the refusal stays in
  `message.refusal` or `delta.refusal`, `content` is left out if there is no other text, and the
  upstream's finish reason is kept); Responses, `response.incomplete` with
  `incomplete_details.reason` `content_filter`; Messages, `stop_reason` `refusal`;
  GenerateContent, `finishReason` `SAFETY`. A Messages `refusal` stop, a Responses
  `content_filter` incomplete and a Gemini `SAFETY` finish (or prompt block) become a content
  filter the same way.
- A Chat client whose reply came from a Chat upstream gets the choice's `logprobs` back (on each
  chunk, or joined for a reply that is not streamed). No other client gets logprobs.

### Images, files, audio and documents in requests

| Target format | Inline image (base64 data URL) | Image by URL | Image by file id | File (document) | Audio in |
| --- | --- | --- | --- | --- | --- |
| Chat Completions | any `image/*` type | yes | `dropped` | file id or inline data; a file by URL is `dropped` | `input_audio` |
| Responses | any `image/*` type | yes | yes | `input_file`: file id, data or URL | `input_audio` |
| Messages | JPEG, PNG, GIF, WebP | yes | yes | inline PDF or `image/*` only; URL; file id | `dropped` |
| GenerateContent | PNG, JPEG, WebP, HEIC, HEIF, GIF, AVIF | only a Files API URI (`https://generativelanguage.googleapis.com/v1/files/<id>` or `/v1beta/files/<id>`), else `dropped` | `dropped` | inline data (any type); the same URI | inline data |

- An inline image that is not a base64 data URL, has invalid base64, or has a type the target does
  not take is `dropped` (reported). Any other URL, a Cloud Storage `gs://` URL included, is dropped
  for GenerateContent and never fetched. The checks apply to parts translated from another
  format; a part the client sent in the same format is passed as it came. File data must be
  base64, a base64 data URL or an `http`/`https` URL, else `dropped`.
- Images and files in an assistant message are `dropped` for Chat Completions, Responses and
  Messages. The GenerateContent encoder does not check the role.
- A tool result's images and files: to Chat Completions they go in a user message after the tool
  messages (`approximated`; the tool message says they follow); to GenerateContent they become the
  function response's parts, and a text part that is not in the result's text is `dropped`;
  Responses and Messages send them with the result. A tool result's `is_error` is `dropped` for
  Chat Completions and Responses.
- A user turn that has nothing left to send after parts are dropped fails with
  `400` `invalid_request`: `<path>: nothing in this user turn can be sent to <format>`.
- A Messages plain-text document (`document` with a `text/plain` text source) has no block in the
  other three formats, so its text is sent as a text part between lines naming its title
  (`approximated`). Any other Messages document block is `dropped` for them.
- Chat Completions `input_audio` is read as an inline audio file part; `wav`, `mp3`, `ogg`,
  `flac`, `aac`, `webm`, `pcm16`, `g711_ulaw` and `g711_alaw` map to their media types.

### Output media, web search, code execution and citations (replies)

These cross formats only through the internal model; each client format shows them as follows.
Which upstream produces them depends on the provider and format.

| Item | Chat Completions | Responses | Messages | GenerateContent |
| --- | --- | --- | --- | --- |
| Generated image | dropped | dropped | dropped | `inlineData` part |
| Generated audio | only from a Chat upstream (`message.audio`), else dropped | dropped | dropped | `inlineData` part |
| Citations | `url_citation` annotations on a reply that is not streamed; a streamed reply carries them only from a Chat upstream | `url_citation` annotations | dropped | `groundingMetadata` |
| Uncited web sources | dropped | a `web_search_call` item when the upstream is Messages; else dropped | dropped | `groundingChunks` |
| Code execution results | dropped | dropped (only a Responses upstream's own are shown) | dropped | `executableCode` and `codeExecutionResult` (Python only) |

- When the reply comes from an upstream of the client's own format, the upstream's body is passed
  on and nothing is dropped.
- Only an `http` or `https` URL is carried as a citation or source. A citation with no usable
  range covers the whole text of its item. Citations and sources past 4 MiB each, code runs past
  4 MiB (a run's code up to 1 MiB), and media past 24 MiB a part or 96 MiB a reply are left out and
  reported. A generated media type must be a listed image or audio type; `image/svg+xml` is never
  carried.
- Web search and code execution as request tools: a client's `web_search`, `web_search_options`
  (Chat), `web_search_20250305` (Messages) or `googleSearch` (GenerateContent) tool, and a client's
  `code_interpreter`, `code_execution` or `codeExecution` tool, become canonical tools. They are
  sent to a different format only when the provider says its upstream takes them (see "Service
  tier, verbosity and gated features"); otherwise they are `dropped`. Settings with no place in the
  target tool (for example `max_uses`, domain lists, a container) are `dropped` one by one.
  Chat Completions takes web search only as `web_search_options` on search models and without
  function tools; it has no code execution tool. GenerateContent does not combine either tool with
  function declarations or structured output (model dependent). Messages takes `web_search_20250305`
  and `code_execution_20250825` on listed Claude models.

### Service tier, verbosity and gated features

- Service tier (`auto`, `default`, `flex`, `priority`, `scale`, `ultrafast`; `fast` is `priority`)
  and verbosity (`low`, `medium`, `high`) are carried to another format only when the provider says
  its upstream takes the field, and only with a value that format's reference lists. Otherwise each
  is a `dropped` loss at the client's field (`service_tier`, `speed`, `serviceTier`, `verbosity`,
  `text.verbosity`). A client's own body keeps its own tier and verbosity, whatever the host.
- Tier names by format: Chat Completions and Responses take their own lists; Messages takes `auto`
  and `default` (as `standard_only`) and `priority` as `speed: "fast"` only with fast mode; GenerateContent
  takes `default` (as `standard`), `flex` and `priority`. Any other tier is `dropped`, never moved
  to the nearest one. Verbosity exists only in Chat Completions and Responses.
- Who allows what:

| Provider | Tier / verbosity / web search / code execution / `n` sent for a request from another format |
| --- | --- |
| Claude | on `https://api.anthropic.com` only: tier, web search, code execution; fast mode also for a Responses client |
| Codex | on `api.openai.com` and `chatgpt.com`: tier, verbosity, web search (bare on `chatgpt.com`); code execution on `api.openai.com` |
| Gemini | tier, web search, code execution on `generativelanguage.googleapis.com` (web search and code execution also on Google's Vertex AI hosts); `n` on the Gemini API, and on Vertex AI without streaming |
| OpenAI-compatible | by host: `api.openai.com` (Chat: tier, verbosity, web search, `n`; Responses: tier, verbosity, web search, code execution), `api.anthropic.com` (Messages), `generativelanguage.googleapis.com` (GenerateContent); any other host gets none |
| xAI, Kimi, Meta | none: each is `dropped` |
| Devin | `n` > 1 `dropped`; no tier or verbosity |

### Multiple candidates (`n`)

- `n` (Chat Completions, 1 or more) and `generationConfig.candidateCount` (GenerateContent) are the
  candidate count. A count of 1 sends no field. To the same format it is sent as it is.
- To a different format: Chat Completions upstreams get `n` only for a documented OpenAI model
  (`gpt-3.5-turbo`, `gpt-4`, `gpt-4-turbo`, `gpt-4o`, `gpt-4o-mini`, `gpt-4.1` family,
  `chatgpt-4o-latest`) and at most 128; GenerateContent upstreams get it only for the `gemini-2.5`,
  `gemini-2.0` and `gemini-1.5` model families and at most 8. Otherwise it is `dropped`. Messages
  and Responses upstreams have no setting: `dropped`.
- Replies: the first candidate is the reply's main content. Later candidates go to a Chat Completions
  client as choices `1` and up and to a GenerateContent client as candidates; a Responses client
  gets none of them (reported `dropped`); a Messages client never receives any.

### Usage

- The model's `input_tokens` counts every input token, cached or not; `output_tokens` counts every
  output token, reasoning included. A count the upstream did not send is left out, not written as 0,
  except where the client format requires it.
- Chat Completions: `prompt_tokens`, `completion_tokens` and `total_tokens` are all written whenever
  any count is known (a missing one as 0); a usage with no counts is left out. Cached tokens in
  `prompt_tokens_details.cached_tokens` (and `cache_write_tokens`); reasoning in
  `completion_tokens_details.reasoning_tokens`. A streamed reply carries the usage chunk only when
  the client asked for it with `stream_options.include_usage`.
- Responses: `input_tokens`, `input_tokens_details`, `output_tokens`, `output_tokens_details` and
  `total_tokens` are all written when any count is known; reasoning in
  `output_tokens_details.reasoning_tokens`.
- Messages: `input_tokens` leaves out cached and cache-written tokens, which go in
  `cache_read_input_tokens` and `cache_creation_input_tokens`; reasoning in
  `output_tokens_details.thinking_tokens` (at most `output_tokens`). A Messages upstream's own
  `thinking_tokens` is read the same way.
- GenerateContent: `usageMetadata` has no required field. `candidatesTokenCount` leaves out
  `thoughtsTokenCount`; cached tokens in `cachedContentTokenCount`.
- A usage field with no place in the client's format is a `dropped` loss (`usage.<field>`), unless
  the value is 0 or null.

### Finish reasons

The model has `stop`, `length`, `tool_calls`, `content_filter` and `other`. Chat Completions writes
them as they are (`other` as the upstream's own value, else `stop`). Messages writes `end_turn`,
`max_tokens`, `tool_use` and `refusal`. Responses writes a `completed` response, or `incomplete`
with `max_output_tokens` or `content_filter`. GenerateContent writes `STOP`, `MAX_TOKENS`, `SAFETY`
and `OTHER`. A same-format reply keeps the upstream's own value.

### Stream behaviour

- Core sends nothing until the first event arrives, so an error before it is a normal HTTP error. After
  the first event the status is `200`, and an error ends the stream as described under "Errors".
- Keep-alive: once a streamed reply has started, core writes the comment `: keep-alive` after 15
  seconds with nothing written, and only between events. It does this on `/v1/chat/completions`,
  `/v1/completions`, `/v1/responses`, `/responses`, `/backend-api/codex/responses`, `/v1/messages`,
  and on any server-sent-events reply of an endpoint route except `/v1beta/interactions`. The
  GenerateContent routes get none (Google's SDKs read every line as JSON). Replies that are not
  streamed and WebSocket sessions get none.
- A Responses stream whose final `response.completed`, `response.incomplete` or `response.failed`
  has an empty `output` gets the items the stream's `response.output_item.done` events finished.
- A Messages stream holds the deltas of a block that is not the open one until it becomes the open
  one.

### Rate-limit headers

- Extensions read an allowlist of the upstream's documented rate-limit headers for the upstream's
  format and attach them to the reply: Messages upstreams `anthropic-ratelimit-*`,
  `anthropic-priority-*` and `anthropic-fast-*`; Chat Completions and Responses upstreams
  `x-ratelimit-*`. GenerateContent upstreams give none. A value is kept only if it is a
  non-negative integer, `-1`, an RFC 3339 time, or a duration such as `6m0s`; at most 32 headers
  and 256 bytes a value.
- Core writes a header to the client only if it is on the allowlist of the **client's** format. So a
  Messages client receives only `anthropic-*` headers (from a Messages upstream), Chat and Responses
  clients only `x-ratelimit-*` headers (from a Chat or Responses upstream), and GenerateContent
  clients none. Headers come with a successful reply (streamed or not), a token count reply and an
  error reply, except the usage-limit reply and a rejected client key. Endpoint routes and
  WebSocket sessions get none. Core removes pieces of the account's secrets from the values.

### Errors

Every error is one internal error with a code and a status. The client format decides the shape.

| Client format | Shape | Status when the error has none |
| --- | --- | --- |
| Chat Completions, Responses | `{"error":{"message","type","code", "param"?, "pluxy"?}}`; `type` is `authentication_error` (401), `permission_error` (403), `rate_limit_error` (429), `invalid_request_error` (other 4xx), `server_error` (5xx); `code` is the upstream's code, else the internal code | see below |
| Messages | `{"type":"error","error":{"type","message","details"?,"pluxy"?},"request_id"?}`; `type` follows the status (`invalid_request_error`, `authentication_error`, `permission_error`, `not_found_error`, `conflict_error`, `request_too_large`, `rate_limit_error`, `timeout_error`, `overloaded_error` for 529, `api_error`) | see below |
| GenerateContent | `{"error":{"code","message","status","details"?,"pluxy"?}}`; `status` is Google's name for the code (`INVALID_ARGUMENT`, `UNAUTHENTICATED`, `PERMISSION_DENIED`, `NOT_FOUND`, `RESOURCE_EXHAUSTED`, `UNAVAILABLE`, `INTERNAL`, ...) | see below |
| Responses WebSocket | `{"type":"error","status","error":{...}}` | see below |

- Status when the error has none: `invalid_request` and `unsupported` 400; `auth` 401; `permission` and
  `account_closed` 403; `not_found` 404; `rate_limit` and `quota` 429; `unavailable` 503;
  `internal` 500; anything else 502.
- An upstream's error keeps the fields its format shares with the client's: a Messages client keeps
  `request_id` and the text `error_code` of `details` from a Messages upstream; a Chat or Responses
  client keeps `param` from an OpenAI-format upstream; a GenerateContent client keeps `details`
  and the Google status name from a Google upstream. No field is copied into a format that does not
  have it.
- `Retry-After` is sent on `429`, `503` and `529` when the upstream said how long to wait.
  `X-Should-Retry: false` is sent when no account can take the request for more than 5 minutes, and
  for a rejected client key.
- An error after the stream started: Chat Completions writes `data: {"error":...}` and no
  `[DONE]` (so the client sees the reply is incomplete); Responses writes an `error` event; Messages
  and GenerateContent write `event: error`. An endpoint stream gets `event: error` with an OpenAI
  shaped body.
- A prompt that does not fit the model's context window: `400`, code `context_length_exceeded` on
  Chat Completions, Responses and the WebSocket; `invalid_request_error` with a message that
  contains `prompt is too long` on Messages; `INVALID_ARGUMENT` on GenerateContent. It fails that
  request only: no account or model is blocked and no other account is tried. The Claude, Codex,
  Gemini and OpenAI-compatible extensions recognise it (by the code or by the message list in
  `llm/overflow.go`, and Gemini also by a `500` that says `input context is too long`); Kimi, xAI,
  Meta and Devin do not.
- When routing settings turn on the usage-limit reply (`routing.quota_response_mode`
  `usage-limit`) and every account is out of quota for more than 5 minutes, the client gets `429`:
  Chat, Responses and GenerateContent clients an error with `type` `usage_limit_reached` and
  `code` `model_cooldown`; Messages clients `rate_limit_error` with the reset time. It carries
  `X-Should-Retry: false`, no `Retry-After`.

### Token counts and `token_count_estimate`

- Routes: `POST /v1/messages/count_tokens` (Messages body), `POST /v1beta/models/{model}:countTokens`
  (contents, or a `generateContentRequest`), `POST /v1/responses/input_tokens`. Chat Completions has
  no count route.
- Core sends a count request to the model's accounts like a generate request. The count is the
  upstream's own count when the account's extension can count:

| Provider | Counts upstream |
| --- | --- |
| Claude | only when `base_url` is `https://api.anthropic.com` (port 443, no user info) |
| Gemini | yes (Gemini API and Vertex AI) |
| Kimi | yes, with Kimi's Messages count endpoint |
| OpenAI-compatible | `wire_api` `messages` (`/messages/count_tokens`) and `gemini` (`:countTokens`); `chat` and `responses` cannot |
| Codex, xAI, Meta, Devin | cannot (no count operation) |

  For `/v1/responses/input_tokens`, Codex API-key accounts and OpenAI-compatible `responses` accounts count
  upstream; the others answer `unsupported`.
- An account that cannot count answers `unsupported` (scope `credential`, retryable) and routing
  tries the next account. When the count ends in an error, and any account tried could not count,
  core answers with its own estimate in the client's shape (`{"input_tokens":N}` for Messages;
  `{"totalTokens":N,"promptTokensDetails":[{"modality":"TEXT","tokenCount":N}]}` for GenerateContent;
  `{"object":"response.input_tokens","input_tokens":N}` for Responses). When every account tried
  could count, an upstream's error stays an error. A refusal under `drop_policy` `"reject"` is not
  a "cannot count" answer: it stays an error.
- `token_count_estimate` (top level, default `true`): `false` answers the error of the last account
  instead of an estimate (status `400`, a message such as `extension <id> does not count tokens`).
- A count from an upstream of the client's own format is passed on as the upstream sent it;
  from another format, core writes the number in the client's shape.
- The estimate is one function for every provider. It is a heuristic, not a tokenizer: it splits
  text the way OpenAI's `o200k_base` pre-tokenizer does and counts each piece by its length. In its
  tests it is within 1% of `o200k_base` on English prose and code, 2% on JSON, 3% on Chinese,
  Japanese and Korean, 4% on Russian, and within about 7% on short chat text. It counts the system
  text, each message's role and text, reasoning text, each tool call's ID, name and arguments, each
  tool result's call ID and output, each tool's name, description and parameters, and the tool
  choice. Images and files add nothing.
- Token counts take the same `drop_policy` and thinking suffix as a generate request.

### Legacy text completions

`POST /v1/completions` is read as a Chat Completions request: the prompt (a string or a list of one
string; an empty prompt becomes `Complete this:`) is one user message. `max_tokens`, `temperature`,
`top_p`, `frequency_penalty`, `presence_penalty`, `stop`, `stream`, `logprobs`, `top_logprobs` and
`echo` are carried; `n`, `suffix`, `best_of`, `user` and the rest are left out without a report. A
list of several prompts, or token arrays, is refused with `400`. The reply is written as
`text_completion` choices with `text`.

### Prompt caching

There is no cache setting in the model. A valid `cache_control` (`type` `ephemeral`, `ttl` `5m` or
`1h`) on a message or content part that came from another format is sent to a Messages upstream
(for example Chat Completions parts written for OpenRouter), up to 4 breakpoints in a request
(more are `dropped`). To other upstream formats `cache_control` and other cache fields are
`dropped`. Cached input tokens are carried in usage (see "Usage").

## Claude

The Claude extension (`pluxy/claude`) sends every request to Anthropic's Messages API, at
`{base_url}/v1/messages?beta=true`. Sign-in and account options are in [providers.md](providers.md#claude); only
the options that change how a request or reply is translated are listed here.

| Setting | Meaning |
| --- | --- |
| `base_url` | Where the Messages API is. Default `https://api.anthropic.com`. Whether it is Anthropic's own API (scheme `https`, host `api.anthropic.com`, port 443 or none, no user info) decides token counting, service tier, structured-output rules, and web search and code execution from Chat and Gemini clients (below). |
| `rebuild_mid_system_message` | `true` moves the text of each `system` message in the conversation into the top-level `system` prompt, after what it holds, and removes those messages, for a Claude-compatible upstream that refuses them there. Blank text and non-text blocks in them are left out. Default `false`. See "Mid-conversation system messages". |
| `model_level_cooling` | `true` keeps a used-up shared subscription window (5-hour or 7-day) on the model that hit it, so the account's other models stay in use. Default `false`: the whole account is blocked. |
| `cache_ttl` | Lifetime of the `cache_control` markers pluxy adds to rebuilt requests that set none: `"5m"` (default) or `"1h"`. Any other value makes the account refuse its requests. A Responses client's own `prompt_cache_retention` or `prompt_cache_options.ttl` takes precedence for that request. |

### Where a request goes

| Client format | Sent to | Body |
| --- | --- | --- |
| Anthropic Messages | `{base_url}/v1/messages?beta=true` | as written, with the repairs below |
| OpenAI Chat Completions, OpenAI Responses (HTTP and WebSocket), Gemini GenerateContent | the same | rebuilt as Messages |
| Responses compaction (`POST /v1/responses/compact`) | the same, not streamed | rebuilt, as compaction (below) |
| Token count, any format | `{base_url}/v1/messages/count_tokens?beta=true` | see "Token counting" |

A Responses WebSocket turn is sent like an HTTP request that carries the whole conversation.

**Headers.** The account's credential goes in `x-api-key`; a subscription's access token, and any key that contains
`sk-ant-oat`, goes as `Authorization: Bearer` (with the beta `oauth-2025-04-20`). The client's own credentials are
never sent on. A rebuilt request carries `Anthropic-Version: 2023-06-01`, the credential, and the betas it needs; no
client headers. A Messages client's request carries its own `anthropic-version` (else `2023-06-01`),
`anthropic-beta`, and these identification headers: `user-agent`, `x-app`, `anthropic-dangerous-direct-browser-access`,
`x-claude-code-session-id`, `x-client-request-id`, `x-client-app`, `x-anthropic-additional-protection` and the
`x-stainless-*` set of Anthropic's SDKs. Claude Code's subagent, prompt, request-class and remote-session headers
(`x-claude-code-agent-id`, `x-claude-code-prompt-id`, `x-claude-remote-session-id` and the others) go only from a
request that names itself as Claude Code: `x-app` is `cli` or `cli-bg`, the `user-agent` starts with `claude-cli/`,
and `anthropic-beta` has `claude-code-20250219`. The extension never changes how a client presents itself: a client
that is not Claude Code is not made to look like it, and no Claude Code betas are added.

### Changes to every body

These apply to a Messages client's body and to a rebuilt one.

| What | Change | Reported |
| --- | --- | --- |
| Empty `allowed_domains` or `blocked_domains` on a built-in `web_search_*` tool | Removed (Anthropic refuses an empty list; leaving it out means no restriction) | no |
| A `tool_use` `id` or `tool_result` `tool_use_id` that is not 1 to 64 letters, digits, `_` or `-` | Replaced by `cpa_tid_v1_` and the ID in unpadded URL-safe base64; if that is over 64 bytes, by `cpa_tid_v1_` and a SHA-256 digest of the ID. The same ID always becomes the same new ID, and two IDs never become one | no |
| Conversation (see "Conversation repairs") | Repaired | yes |
| Thinking settings (see "Thinking") | Fitted to the model | yes |
| Betas (see "The `anthropic-beta` header") | Added | no |

**Conversation repairs.** Claude answers some conversations with a 400, so these are sent in a form it takes.
Each change is reported.

- A tool call that the next user turn does not answer gets an error result (`is_error: true`, text `Tool call was
  interrupted before any output was recorded.`) in that turn, or in a user turn of its own when an assistant turn
  follows. Reported approximated.
- A tool result that answers no tool call in the assistant turn before it is sent as text instead (an empty one as
  `(empty tool result)`). Reported approximated.
- Answered results go first in their user turn. When a user turn needs any of these repairs, the consecutive user
  messages it was made of are sent as one message.
- Blank text blocks in a user turn are dropped (reported dropped). A user turn left with nothing is sent as
  `(empty message)` (reported approximated).
- For Claude 4.6 and later, `claude-mythos-preview` and any `claude-` name the extension cannot place as a version,
  trailing assistant turns are dropped (reported dropped), because those models refuse a conversation that ends on
  an assistant turn. A last turn that ends on a `server_tool_use` block (a paused web search) is kept. If nothing is
  left to send, the request is refused with a 400.
- On Claude 3.7 to 4.5, which take a prefilled assistant turn, trailing assistant turns are dropped while thinking is
  on (reported dropped); with thinking off, a trailing assistant turn loses the reasoning blocks it ends on, or the
  whole turn if only reasoning is in it (reported dropped).

**Messages clients.** A Messages request goes as written, so thinking blocks and signatures, `cache_control` markers,
`temperature`, `top_p`, `top_k`, document citations, text documents and every other field arrive intact, except for
the changes above, the thinking fit, a thinking suffix on the model name, `rebuild_mid_system_message` when set, and
the betas. `top_k` is left out where the model refuses it (reported dropped). Headers are the ones listed above.

**Rebuilt requests (Chat, Responses, Gemini).**

| Client field | Sent as |
| --- | --- |
| Output limit (`max_tokens`, `max_completion_tokens`, `max_output_tokens`, `maxOutputTokens`) | `max_tokens`. With none, the model's output limit from the extension's built-in model list (the name is read in lower case, without `[1m]`, a Vertex `@version` or a Bedrock `anthropic.` prefix, and matches a listed ID with or without its date), or `32000` for a model the list does not have |
| Stop sequences | `stop_sequences` |
| `temperature`, `top_p` | as written where the model takes them (see "Sampling") |
| System and developer messages | The `system` prompt; after the conversation has started see "Mid-conversation system messages" |
| Tools, tool choice, parallel tool calls | See "Tools" |
| Reasoning effort, summary | See "Thinking" |
| Response format | See "Structured output" |
| Service tier | See "Service tier and fast mode" |
| `cache_control`, `prompt_cache_retention` | See "Prompt caching" |

Anything else in the client's body that pluxy does not read is reported as dropped at its own path. Gemini
`safetySettings` is one such field; its `generationConfig` members other than `temperature`, `topP`,
`maxOutputTokens`, `stopSequences`, `candidateCount`, the response format and the thinking level (`topK` and `seed`
among them) are reported as one drop at `generationConfig`. Verbosity (Chat `verbosity`, Responses `text.verbosity`)
is reported dropped at every `base_url`.

### Tools

| Client sends | Claude gets |
| --- | --- |
| A function tool (Chat `function`, Responses `function`, Gemini function declaration) | `{name, description, input_schema}`, with `strict` when the client set it |
| Responses `web_search`, `web_search_preview*`; Chat `web_search_options`; Gemini `googleSearch` | See "Web search" |
| Responses `code_interpreter`; Gemini `codeExecution` | See "Code execution" |
| Responses `custom`, `namespace` and client-run `tool_search` | Functions (see Shared rules: Tools) |
| Any other tool: Responses `file_search`, `image_generation`, `mcp`, computer use, provider-run `tool_search`; Chat `custom`; other Gemini tools | Not sent; reported dropped at the tool's path (`Messages has no such tool`) |

Messages refuses two tools with one name. The client's functions win: a web search or code execution tool
translated from another format is not sent, and is reported dropped, when a function already has its name
(`web_search`, `code_execution`).

**Tool schema.** A rebuilt request's `parameters` become an object schema without `anyOf`, `oneOf` or `allOf` at its
root: the `properties` of each branch that can be an object move to the root (a property the root already has
wins), and the `required` names of `allOf` branches move too. `$ref`, `$defs` and patterns (`\p{...}` included) stay
as written. A missing or non-object schema becomes `{"type":"object","properties":{}}`. This is not reported.

**Tool names.** A rebuilt request's function names are rewritten as the Messages rows of Shared rules: Tools say,
and the reply's tool calls go back to the client under its own name, in every client format. A Messages client's
schemas and names are not touched.

**Tool choice.**

| Client | Sent |
| --- | --- |
| `auto`, `none`, `required` (Gemini `ANY`) | `{"type":"auto"}`, `{"type":"none"}`, `{"type":"any"}` |
| A named function | `{"type":"tool","name":...}`, with the sent name |
| An allowed list (Chat and Responses `allowed_tools`, Gemini `ANY` with several names or `VALIDATED`) | Only the function tools the list names are defined (see Shared rules: Tools). The mode (`auto` or `required`) is sent as above |
| Forced code interpreter | `{"type":"tool","name":"code_execution"}` when the code execution tool is sent; else reported dropped |
| A Responses choice naming a custom or namespaced tool | The function that tool is sent as |
| Any other choice (a hosted tool, Chat `custom`) | Reported dropped at `tool_choice` |

The choice is sent only when the request sends at least one tool. Some models refuse a forced choice; see
"Thinking". **Parallel tool calls:** `parallel_tool_calls: false` sends `disable_parallel_tool_use: true`, with
`{"type":"auto"}` added when there is no choice and none when the choice is `none`; `true` sends nothing.

**Custom tools (Responses clients).** A custom tool (Codex's `apply_patch`, for example) becomes a function with one
required string argument `input`; its description is the client's, then how to fill `input`, then its grammar.
`apply_patch` has its own instructions and schema.

### Thinking

Claude thinks in two forms. Models from 4.6 on think adaptively: `thinking: {"type":"adaptive"}` with
`output_config.effort`. Models from 3.7 to 4.5 take a budget: `thinking: {"type":"enabled","budget_tokens":N}`, at
least 1024 and below `max_tokens`. pluxy reads the model name, in lower case, without a Bedrock prefix (`anthropic.`,
or `us.`, `eu.`, `apac.` or `global.` before it), `[1m]` or a Vertex `@version`.

**Where an effort comes from.** A rebuilt request: Chat `reasoning_effort`, Responses `reasoning.effort`, Gemini
`thinkingConfig.thinkingLevel` or `thinkingBudget` (a budget is read as the effort it falls in: 0 `none`, up to 512
`minimal`, 1024 `low`, 8192 `medium`, 24576 `high`, above that `xhigh`; -1 is `auto`). A thinking suffix on the model
name (see Shared rules: Reasoning and the model-name thinking suffix) takes the place of the request's own settings.

**Effort to thinking** (rebuilt requests and suffixes):

| Model | Sent |
| --- | --- |
| Claude 3.7 to 4.5 | A budget: `minimal` 1024, `low` 1024, `medium` 8192, `high` 24576, `xhigh` 32768, `max` 128000, `auto` 1024; `none` is `thinking: {"type":"disabled"}`; a budget suffix is used as it is (at least 1024). A budget at or above `max_tokens` is set to `max_tokens - 1`; if that is under 1024, no thinking is sent and the effort is reported dropped |
| Claude 4.6 and later | Adaptive thinking with that effort; `minimal` is sent as `low` (reported approximated); `auto` sends adaptive thinking with no effort; a budget suffix is sent as the effort it falls in; `none` as in "Fit to the model" |
| Any name that is not Claude 3.7 or later, or has no version (`claude-mythos-preview`, `glm-4.6`) | No thinking; the effort is reported dropped |

**Fit to the model.** Every body's thinking settings are made ones its model takes, and each change is reported:

- Effort `xhigh` is sent as `xhigh` from Claude 4.7, and as `max` to 4.6 (approximated).
- Thinking off (`none`, or `thinking` of type `disabled` or `between_tools`) is sent as the model turns it off:
  Fable, Mythos (including `claude-mythos-preview`) and Opus 5.5 and later always think, so no `thinking` is sent, at
  effort `low` unless the body names one; Sonnet 5.5 and later take `between_tools` (no thinking before the answer,
  short notes between tool calls); every other model takes `disabled`. Where the model takes the off type only at
  effort `high` or below (Opus 5.0 to 5.4, Sonnet 5.5 and later, Haiku 5.5 and later), a higher effort is sent as
  `high`. An off type takes no other field: others are removed (reported dropped).
- A thinking budget goes to Claude 4.7 and later as adaptive thinking at the effort the budget falls in, unless the
  body names an effort.
- Adaptive thinking goes to a model that takes only a budget as a budget sized from `output_config.effort` (1024
  without one), below `max_tokens`; if there is no room, thinking is left out. Models that take no effort (every
  one from 3.7 to 4.5 except Opus 4.5) lose `output_config.effort`.
- A forced tool choice (`any` or a named tool) is sent as `auto` to Opus 5.5 and later, Sonnet 5.5 and later, and Fable
  and Mythos 5.1 and later, which refuse one (approximated). On any other model, a forced choice with budget thinking
  is sent without thinking (reported dropped).
- `top_k` is left out on Claude 4.7 and later and `claude-mythos-preview`, and on older models while thinking is on
  (reported dropped).
- With budget thinking, a final tool result whose assistant turn has no thinking block, in a conversation with no
  thinking block at all, goes with adaptive thinking on Claude 4.6 and without thinking on older models.

**Reasoning in the history.** A Responses client's reasoning item is replayed as a `thinking` block only when its
`encrypted_content` is a Claude signature (the writer puts Claude's signature there); every other reasoning is
reported dropped: Chat `reasoning_content`, Gemini thought parts, and Responses reasoning from another provider. A
Messages client's thinking blocks, signed or redacted, go as written.

**Reasoning in the reply.** A Responses client gets a `reasoning` item whose summary text is Claude's thinking and
whose `encrypted_content` is the signature. A Chat client gets the thinking text in `reasoning_content`, without the
signature. A Gemini client gets `thought` text parts, without the signature.

**Reasoning summary.** A Responses client's `reasoning.summary` (or `generate_summary`) becomes `thinking.display`:
`auto`, `concise` and `detailed` as `summarized` (the last two reported approximated), `none` as `omitted`. It acts
when the request has thinking on, or no thinking setting and a model that thinks by default (summarized only); else
the summary is reported dropped.

### Sampling

A rebuilt request's `temperature` and `top_p` are sent only where the model takes them, by the rules for
`anthropic-version` `2023-06-01`. Each one left out is reported dropped, so `drop_policy: "reject"` refuses the
request (or a token count sent to Anthropic's API).

| Model | Rule |
| --- | --- |
| Claude 4.7 and later | One alone is sent only at its default (`temperature` 1, `top_p` 0.99); both set: neither |
| A `claude-` name not read as a version (such as `claude-mythos-preview`) | Neither |
| Older Claude, thinking on | `temperature` only at 1; `top_p` only from 0.95 |
| Older Claude, either way | `top_p` is left out when `temperature` is sent too (Claude refuses both) |
| Any other name | Both as the client sent them |

`top_k`, seeds, and penalties from other formats are never sent (reported dropped). A Messages client's own
`temperature` and `top_p` go as written; only its `top_k` is fitted.

### Mid-conversation system messages

System and developer text before the first user or assistant turn is the `system` prompt. A Messages client's
`system` messages in the conversation stay where it put them (`rebuild_mid_system_message` off). Turn repair does not
look past them: a `system` message between a tool call and its result makes the call look unanswered and the result
look orphaned, so an error result is added and the real result is sent as text (reported approximated). For a
rebuilt request to a `claude-` model, system and developer messages after the conversation has started
stay in place, so the prefix Claude caches does not change:

- On models that take a `system` message in the conversation (Opus 4.8 and later, Fable and Mythos 5 and later,
  Sonnet 5.5 and later, Haiku 5.5 and later), the message goes after the user turn it follows (or the next one: it
  may not follow an assistant turn); runs separated only by user turns are put together after those user turns.
  Each move is reported approximated.
- On every other `claude-` model (Sonnet 5 included), its text goes in the user turn after it, in
  `<system-reminder>` tags, or in a user turn of its own when none follows (reported approximated).
- A model that is not a `claude-` name gets every system message in the `system` prompt.

`rebuild_mid_system_message: true` then moves every remaining `system` message into the `system` prompt. For a
Messages client this runs before turn repair, so a `system` message between a tool call and its result causes no
repair. For a rebuilt request it only affects models that take the message in the conversation; on the others the text
is already in a user turn.

### Media in

| Part | Sent |
| --- | --- |
| Image, `http(s)` URL (or any other URL string) | `image` block with a `url` source |
| Image, data URL | `image` block, base64. Only `image/jpeg`, `image/png`, `image/gif` and `image/webp` (the type is compared in lower case, parameters removed), with valid base64; any other is reported dropped |
| Image, file ID | `image` block with a `file` source; the ID is not checked |
| File, PDF data URL, URL or file ID | `document` block, with the client's file name as `title` |
| File, data URL with an image type | `image` block, checked as an image |
| File, any other type (text, Office, audio from Chat `input_audio` or Gemini `inlineData`) | Not sent; reported dropped |
| Chat image `detail` | Not sent; not reported |

A Messages client's blocks go as written.

### Web search, code execution and citations

**Web search**

- A Messages client's `web_search_*` tool goes as written (any version), after the empty-domain-list repair.
- A rebuilt request's web search tool becomes `web_search_20250305` with the name `web_search` (or the client's tool
  name). It never becomes a newer version.
- Responses `web_search`: carried on any `base_url` and any model, with `name`, `max_uses`, `filters.allowed_domains`
  and `user_location` as written. Anything else in the tool (`search_context_size`, `filters.blocked_domains`) is
  reported approximated, so `drop_policy: "reject"` accepts the request. `external_web_access: false` leaves the tool
  out, without a report.
- Responses `web_search_preview*`, Chat `web_search_options` and Gemini `googleSearch`: carried only when `base_url`
  is Anthropic's own API and the model is Claude 4.5 or later or `claude-mythos-preview`; otherwise reported dropped.
  `max_uses` and `allowed_domains` carry; `blocked_domains` carry only when there is no `allowed_domains` (Claude
  refuses both, so the other is reported dropped); `user_location` is sent as `type: approximate` with `city`,
  `region`, `timezone` and a `country` that is an uppercase ISO 3166-1 code (a different country is reported
  dropped); `search_context_size` is reported dropped. `external_web_access: false` leaves the tool out without a
  report.
- A Responses or Chat client gets Claude's search as a `web_search_call` item (Responses: one item, with the query,
  status and the results as Claude sent them, so `encrypted_content` survives; a search left without results when
  Claude paused the turn, `pause_turn`, stays `in_progress`, and the response has `end_turn: false`). A Chat client
  gets the text without the search blocks.
- A replayed `web_search_call` goes back as `server_tool_use` and `web_search_tool_result` blocks inside the
  assistant message around it. A paused search at the end of the history goes back as its `server_tool_use` alone, so
  Claude carries on. Anthropic encrypts each result for the credential that ran the search and refuses it from
  another, so the extension remembers (in memory, by hash, up to 16384 results) which account received each
  result; a replayed result that was recorded for another account is left out (reported dropped), and the result
  block keeps an empty list. A result this process has not seen is kept. A Messages client's history is not
  filtered.

**Code execution**

- A Messages client's `code_execution_*` tool goes as written.
- Responses `code_interpreter` and Gemini `codeExecution` become `code_execution_20250825` (no beta header) only when
  `base_url` is Anthropic's own API and the model is Claude 4.5 or later or `claude-mythos-preview`; otherwise the
  tool is reported dropped. Explicit containers, `file_ids`, `memory_limit`, `network_policy` and other settings are
  reported dropped; `{"container":{"type":"auto"}}` is not. Chat has no code execution tool.
- A forced choice of the tool (`{"type":"code_interpreter"}`) is sent where the tool is sent; else reported dropped.
- Results: a Gemini client gets `executableCode` and `codeExecutionResult` parts for the Python runs of Claude's
  `code_execution` tool, after the reply's last text part. Shell and file-editor runs, omitted files and images are
  reported dropped. A Messages client gets Claude's own blocks. Responses and Chat clients get no code blocks (they
  need an OpenAI `container_id`, or have no place for one); the results are reported dropped and the text is kept.

**Citations**

- Claude's `web_search_result_location` citations (on text blocks) and the sources of `web_search_tool_result`
  blocks carry to Responses, Chat and Gemini clients as Shared rules: Output media, web search, code execution and
  citations describes. Gemini `groundingMetadata` ranges are converted to UTF-8 byte offsets.
- A Gemini client's `groundingSupports` repeat each citation's cited text, at most 1 MiB in all; past that
  `segment.text` is left out (reported dropped).
- Claude's other citation types (`char_location`, `page_location`, `content_block_location`, from document citations)
  are not carried to other formats; a Messages client gets them as Claude sent them.
- A replayed assistant message's `annotations` are not sent back to Claude and are reported dropped. Claude does not
  need them: the replayed search blocks carry the results.

### Structured output

A Chat, Responses or Gemini JSON schema becomes `output_config.format` `{"type":"json_schema","schema":...}`. The
schema's `name`, `description` and `strict` are not sent and not reported. On Anthropic's own API the schema is
fitted to Anthropic's rules, and a schema or model that API refuses is left out (reported dropped):
- Models before Claude 4.5 (3.x, 4.0, 4.1): the format is dropped.
- Objects with `properties` get `additionalProperties: false` (approximated); `oneOf` is sent as `anyOf`
  (approximated).
- Constraints Anthropic refuses are removed (reported dropped): `minimum`, `maximum`, `exclusiveMinimum`,
  `exclusiveMaximum`, `multipleOf`, `minLength`, `maxLength`, `maxItems`, `uniqueItems`, `minContains`, `maxContains`,
  `contains`, `prefixItems`, `additionalItems`, `unevaluatedItems`, `minProperties`, `maxProperties`,
  `propertyNames`, `unevaluatedProperties`; a `minItems` other than 0 or 1; a string `format` Anthropic does not list;
  a `pattern` with backreferences, lookaround or word boundaries.
- A schema that cannot be made valid is dropped whole: recursive or external `$ref`; `not`, `if`/`then`/`else`,
  `patternProperties`, `dependentSchemas`, `dependencies`, `dependentRequired`; `anyOf` with `oneOf` or other
  keywords; objects or arrays in `enum` or `const`; open objects without `properties`; `additionalProperties` other
  than `false`; some `allOf` forms.

On another `base_url` the schema goes as it came. `json_object` (Chat, Responses, and Gemini `application/json`
without a schema) is reported dropped: Messages takes only `json_schema`. A Messages client's `output_config.format`
goes as written.

### Service tier and fast mode

| Client | Sent |
| --- | --- |
| Messages client's `speed` and `service_tier` | As written |
| `priority` or `fast` (Chat, Responses; Gemini `priority`) | `speed: "fast"`, with the fast-mode beta |
| `auto` | `service_tier: "auto"` |
| `default` (Gemini `standard`) | `service_tier: "standard_only"` (standard capacity only) |
| `flex`, `scale`, `ultrafast`, any other value | Not sent; reported dropped |

Chat, Gemini and Responses tiers other than `priority`/`fast` are sent only when `base_url` is Anthropic's own API;
on another host they are reported dropped. A Chat or Gemini priority tier is sent as fast mode only on Anthropic's own
API; a Responses client's priority tier is sent as fast mode on any `base_url`.

Failures of a fast-mode request on Anthropic's own API are the request's: they block neither the account nor the
model, and the request is not moved to another account, except a refused shared usage window (`quota`) or a closed
account. On any `base_url`, a 429 that says fast mode needs usage credits is the request's in the same way.

### The `anthropic-beta` header

A Messages client's own betas are kept, and never removed. Every request then gets the betas its body needs, after
the client's, without repeats:

| Beta | When |
| --- | --- |
| `fast-mode-2026-02-01` | The body's `speed` is `fast`, or the client names this beta (header or `betas`) |
| `oauth-2025-04-20` | The account's credential is an OAuth token |
| `advisor-tool-2026-03-01` | A tool whose type starts with `advisor_` |
| `extended-cache-ttl-2025-04-11` | A tool, system block or message block has `cache_control` with `ttl` `"1h"` (not on a token count) |
| `thinking-binding-controls-2026-08-01` | `thinking.block_binding` is set |
| `inline-tools-2026-09-15` | A `tool_addition` block defines a tool inline |
| `mid-conversation-tool-changes-2026-07-01` | `tool_addition` or `tool_removal` blocks without an inline definition |
| `compact-2026-09-04` | The request is a compaction |

A top-level `betas` list in the body, which Messages does not take there, is moved into `anthropic-beta`. A rebuilt
request has no client betas, so its header holds only the betas above.

### Prompt caching

- A Messages client's `cache_control` markers go as written; the extension adds none.
- A Chat client's valid markers (`{"type":"ephemeral"}` with `ttl` `"5m"` or `"1h"`) are carried: top-level, on
  tools, on `tool_calls`, on messages and parts. Any other `cache_control` is reported dropped. A marker on a Chat
  message that has no content block of its own goes on the last block of that turn, and is reported dropped if that
  block already has one or if four markers are already set (Anthropic's limit).
- When a rebuilt request has no marker anywhere, pluxy adds two: on the last system block (or, with no system prompt,
  the last tool), and on the last block of the last message that can hold one (not a `thinking` block), with the
  `cache_ttl` TTL.
- A Responses client's `prompt_cache_retention` sets the TTL of the added markers (`in_memory` as `5m`, `24h` as `1h`,
  reported approximated); `prompt_cache_options.ttl` does the same when no retention is set (`24h` approximated). Its
  other members are reported dropped. `prompt_cache_retention` is not reported dropped when it is `in_memory` or
  `24h`.

### Token counting

A token count goes to `{base_url}/v1/messages/count_tokens?beta=true` only when `base_url` is Anthropic's own API; the
reply's `input_tokens` is the count. A Messages client's body is sent without the fields only generation uses
(`max_tokens`, `stream`, `temperature`, `top_p`, `top_k`, `stop_sequences`, `metadata`, `service_tier`). A rebuilt
request keeps only `model`, `messages`, `system`, `tools`, `tool_choice`, `thinking`, `output_config`,
`cache_control`, `speed` and `mcp_servers`. The same repairs, thinking fit and betas apply.

### Compaction and other endpoints

`POST /v1/responses/compact` (a Responses client's request to shorten its conversation, which the Codex CLI sends) is
served with Anthropic's on-demand compaction. The body is rebuilt as for `/v1/responses` and sent once, not streamed,
with `"compaction": {"type": "summarize"}` and the `compact-2026-09-04` beta, and without what a compaction request
refuses: stop sequences, a response format, a forced tool choice (`any` or a named tool), `context_management`. A
conversation that ends on the model's turn is sent with that turn. The client's headers are not sent on.

The reply is OpenAI's compaction reply, one JSON object with `"object": "response.compaction"`, whose `output` is
Claude's summary as one user message that starts with the Codex CLI's summary prefix; `usage` is the summarization
call's (the sum of Anthropic's `usage.iterations`). Anthropic's signed compaction block is not passed on, so the next
request can go to any account. A reply with no summary (`stop_reason` other than `compaction`) is a 502 for that
request and blocks nothing. Every other endpoint call (embeddings, Images API) is answered as not served, so core
tries another account.

### Replies and stream repairs

| Claude says | Chat | Responses | Gemini |
| --- | --- | --- | --- |
| `end_turn`, `stop_sequence` | `stop` | `completed` | `STOP` |
| `max_tokens`, `model_context_window_exceeded` | `length` | `incomplete`, `max_output_tokens` | `MAX_TOKENS` |
| `tool_use` | `tool_calls` | `completed` | `STOP` |
| `refusal` | `content_filter` | `incomplete`, `content_filter` | `SAFETY` |
| any other (`pause_turn`) | the raw reason | `completed` with `end_turn: false` for `pause_turn` | `OTHER` |

Reasoning tokens come from `output_tokens_details.thinking_tokens`, never more than the output tokens. There are no
stream repairs: a stream that ends before `message_stop`, breaks, sends an `error` event, or sends one thinking
signature over 1 MiB ends in an error with the usage so far. A Messages client gets Claude's own events and body.

### Errors, rate limits and usage limits

The upstream's `error.message` is kept (else `upstream returned <status line>`), with its error type and, when it is
JSON, its body. The status decides what core does next:

| Upstream | Code | Scope | Retried on another account |
| --- | --- | --- | --- |
| 400, 413, 422 and other 4xx | `invalid_request` | the request | no; a message that says the prompt does not fit becomes `context_length_exceeded` |
| 401 | `auth` | the account | yes |
| 403 | `permission` | the account | yes |
| 400, 401, 403 that say the account or organization is disabled, suspended or deactivated | `account_closed` | the account | yes |
| 404 | `not_found` | the model | only when the message names the model |
| 429 | `rate_limit` or `quota` (below) | the model or the account | yes |
| 503, 529 | `unavailable` | the endpoint | yes, after `Retry-After` |
| other 5xx | `upstream` | the endpoint | no |
| 3xx | `upstream` (502) | the endpoint | no; a redirect to another host is not followed |

**429.** A shared 5-hour or 7-day window that Anthropic reports as rejected (`anthropic-ratelimit-unified-5h-status` or
`-7d-status` `rejected`, or the aggregate `-unified-status` `rejected` for a reason other than below) is a `quota`
refusal that blocks the whole account (only the model with `model_level_cooling`). A refusal of only a model's own
weekly window (`7d_oi`) or of overage, while the shared windows have room, and any other 429, is `rate_limit` and
blocks only that model on the account. The block lasts until the latest reset among the refused windows and
`Retry-After`, when that is in the future and at most 7 days and an hour away; else core backs off. A model's own
window or overage refusal waits for none of those resets, an aggregate reset equal to overage's billing reset is not
waited for, and a per-minute limit (one of Anthropic's exact per-minute messages, no quota status header, one
`Retry-After`) waits for its `Retry-After` alone. A `quota` refusal whose wait is the 5-hour or 7-day window carries
`quota_window_minutes` 300 or 10080.

**What the client sees.** The subscription's `anthropic-ratelimit-unified-*` headers are read by pluxy and never
forwarded. A Messages client gets Anthropic's other rate-limit headers as Shared rules: Rate-limit headers describes.

### Not supported

- Embeddings, the Images API, audio output and any other endpoint besides compaction (core tries another account).
- The `anthropic-beta-strip` and `anthropic-beta-append` settings, and a Claude Code beta profile.
- A newer web search tool version than `web_search_20250305` from another format.
- A provider-run `tool_search`, custom tool calls in Chat history, and `file_search`, `image_generation`, `mcp` and
  computer-use tools from other formats.
- A cache of web search results.

## Codex

The Codex extension (`pluxy/codex`) serves OpenAI's Codex models. See [`providers.md`](providers.md#codex-chatgpt)
for sign-in and account options.

An account is one of three kinds, and the kind decides where its requests go:

| Kind | Calls |
| --- | --- |
| OpenAI API key | `{base_url}/...`, default `https://api.openai.com/v1` |
| ChatGPT subscription | `https://chatgpt.com/backend-api/codex` |
| Sign in with ChatGPT (SIWC) | the OpenAI API (`https://api.openai.com/v1`) on the user's ChatGPT plan |

Every account speaks OpenAI Responses over HTTP with server-sent events. Every request is sent to the upstream as a
streamed request (`stream: true`), and core gathers the stream for a client that wants one reply.

Account settings that change how a request or reply is translated, all optional:

| Setting | Meaning |
| --- | --- |
| `base_url` | Where requests go (`{base_url}/responses`). Default `https://api.openai.com/v1` for an API key and SIWC, `https://chatgpt.com/backend-api/codex` for a subscription. |
| `codex_identity` | Default `true`: send the Codex terminal client's `User-Agent` and `Originator: codex-tui`. Off: the client's own `Originator` is sent when it has one (a subscription always sends one). |
| `codex_client_version` | Default `0.159.0`: the version in that `User-Agent`. It must look like a version (letters, digits, `.`, `_`, `+`, `-`). |
| `image_generation_tool` | Default `true`: add the hosted `image_generation` tool to every request. |
| `image_generation_off_models` | Default none: model names or patterns with `*` and `?` (any case, Go `path.Match`) that are not offered the tool. Matched against the model name sent upstream. A tool the client lists itself stays. |
| `prompt_cache_retention_passthrough` | Default `false`: send the client's `prompt_cache_retention` on, for an upstream that takes it. |
| `images` | Default `true`: serve the Images API on the account. |
| `signin_with_chatgpt_identifier` | Default `pluxy`: the start of a SIWC request's `User-Agent`. |
| `websockets` | Default `false`: hold an upstream WebSocket for each Responses WebSocket client (see "Responses WebSocket"). |
| `alpha_search` | Default `false`: let an API-key account serve Alpha Search (see "Alpha Search"). |

A setting that is not valid (a control character in the identifier, a malformed pattern, a bad version) refuses
the account's calls with an `invalid_request` error.

### Where a request goes

| Client format | Sent to | Body |
| --- | --- | --- |
| OpenAI Responses (HTTP and WebSocket) | `{base_url}/responses` | as written, with the changes in this section |
| OpenAI Chat Completions, Anthropic Messages, Gemini GenerateContent | `{base_url}/responses` | rebuilt as Responses |

A rebuilt request carries messages, function tools, tool choice, parallel calls, max output tokens, structured
output (`text.format`) and reasoning effort. It sends the system prompt as a `developer` message and `instructions`
as `""`. `stop` and every request field Responses has no place for (`top_k`, `frequency_penalty`,
`presence_penalty`, `seed`, `user`, `metadata`, Messages `cache_control` and `thinking.budget_tokens`, and so on)
are reported as dropped; `temperature` and `top_p` are carried by the rebuild and then removed without a report.
`n` above 1 is reported dropped. A tool result's `is_error` is reported dropped. A tool call whose arguments are
not a JSON object is sent as `{}` and reported. A tool of another type than function, web search or code
execution is reported dropped.
A user turn left with nothing to send after drops is refused with a 400 error, whatever the drop policy.

**Headers.** `Authorization`, `Accept: text/event-stream`, `Session-Id` (the prompt cache key, see "Prompt cache
key"), the Codex client's own `X-Codex-Beta-Features`, `Version`, `X-Codex-Turn-Metadata`, `X-Codex-Turn-State`,
`X-Client-Request-Id`, `X-Codex-Window-Id`, `Thread-Id`, `Session-Id` and `X-Openai-Internal-Codex-Responses-Lite`
as they came, the identity headers, and, for a subscription, `Chatgpt-Account-Id` and
`X-Codex-Routing-Hint: model=<model>[;tier=<service tier sent>]`. A redirect is followed only to the same scheme
and host.

#### Responses WebSocket

With `websockets: true` a Responses WebSocket client's turns go to `{base_url}/responses` over one upstream
WebSocket per client socket (`OpenAI-Beta: responses_websockets=2026-02-06`; session headers `session_id` and
`Conversation_id`; the same client and identity headers as HTTP). A turn is the request body with `type:
"response.create"`; `previous_response_id` and `generate` are kept, and `item_reference` items are left for the
upstream. Messages for a turn that no turn waits for are dropped. With `websockets: false` (the default) the
account takes no sessions and core sends each turn as an HTTP request that carries the whole conversation, which
core keeps.
- A turn that names a `previous_response_id` when the socket is gone gets a 409 `previous_response_not_found`
  error and must resend the whole conversation.
- A socket the upstream drops, or that is silent for 5 minutes, is not replaced mid-turn: the client gets
  `upstream_websocket_lost` (502) and resends the whole conversation. A close with code 1009 becomes
  `message_too_big` (413); another close code becomes an `upstream` 502.
- An upstream `error` message with a status of 400 to 599 is read as the HTTP reply with that status would be (see
  "Errors"; `websocket_connection_limit_reached` is such a message). Any error ends the upstream socket
  (except `previous_response_not_found`); the next turn opens a new one.
- A client interrupt is written to the upstream socket unchanged.

#### Responses-lite requests

A request is Responses-lite when its `X-Openai-Internal-Codex-Responses-Lite` header is `true` (any case) or its
`client_metadata` has `ws_request_header_x_openai_internal_codex_responses_lite` as `true` or `"true"` (a
WebSocket client sends it that way). Such a request gets no image generation tool, has `parallel_tool_calls: false`,
goes without `instructions` when it has none (a Responses client's), has replayed `web_search_call` items removed
(and no web search tool added), and skips the reply repairs described under "Replies".

#### Sign in with ChatGPT (SIWC) requests

A SIWC account's requests differ from a subscription's:
- They go to `{base_url}/responses` (default `https://api.openai.com/v1`) with the SIWC token and
  `User-Agent: <identifier> (<GOOS>; <GOARCH>)`, the session as `session_id` and `X-Client-Request-Id`, and none of
  the Codex client's headers, `Originator`, `Chatgpt-Account-Id` or routing hint. `codex_identity`,
  `codex_client_version`, `image_generation_tool`, `image_generation_off_models` and
  `prompt_cache_retention_passthrough` do not apply; no image tool is offered.
- `configuration_update` input items are removed and the last one's non-empty reasoning effort becomes the request's
  (a thinking suffix's effort wins); `temperature`, `prompt_cache_retention`, `prompt_cache_options` and
  `max_completion_tokens` are removed; blank `instructions` are left out; an `agent_message` is sent as a user
  message.
- Compaction goes to `{base_url}/responses/compact` under the same rules and without `store`; a WebSocket turn goes
  without `stream` and `background`.
- A request from a Codex client (a `User-Agent` containing `codex-desktop`, `codex desktop`, `codex-rs`,
  `codex_cli_rs`, `codex-tui` or `codex_exec`) is refused with unsupported, so core tries another account.
- Every endpoint but compaction (Images API, Alpha Search, input token count) is unsupported.

### Changes to every body

| Change | Detail |
| --- | --- |
| `model` | The model sent upstream, without a thinking suffix. |
| `stream`, `store` | `true` and `false` always. |
| `include` | `reasoning.encrypted_content`, plus `web_search_call.action.sources` if the client asked for it, plus `code_interpreter_call.outputs` for a rebuilt request that asked for it. Every other client value is removed. |
| Removed fields | `temperature`, `top_p`, `max_tokens`, `max_completion_tokens`, `truncation`, `user`, `metadata`, `prompt_cache_retention` (unless passed on), `prompt_cache_options`, `previous_response_id` and `generate` (the last two are kept over the upstream WebSocket), `safety_identifier`. None is reported. |
| `max_output_tokens` | Taken from `max_completion_tokens` or `max_tokens` when the client gave neither `max_output_tokens`; a whole number from 1 to 15 is raised to 16, which the endpoint requires. |
| `stream_options` | Only `reasoning_summary_delivery` is kept. |
| `instructions` | `""` when absent (left out for a Responses-lite request from a Responses client). |
| `input` | A string becomes one user message; a `system` message gets the role `developer`; a `function_call` with blank `arguments` gets `{}`. |
| `service_tier` | See "Service tier and verbosity". |
| `reasoning.effort` | See "Reasoning". |
| Image tool | `{"type":"image_generation","output_format":"png"}` is added unless the client lists an `image_generation` tool, the account turned it off, the plan is free, the model name ends in `spark`, the request is Responses-lite, or the account is SIWC. |
| Function schemas | See "Function schemas". |

**Item IDs.** Input item IDs are made on every request, a Responses client's included: an ID of a `message`,
`reasoning`, `function_call`, `custom_tool_call`, `custom_tool_call_output` or `tool_search_call` item that does not
start with `msg`, `rs`, `fc`, `ctc`, `ctco` or `tsc` gets `msg_`, `rs_`, `fc_`, `ctc_`, `ctco_` or `tsc_` in front;
other characters than letters, digits, `_` and `-` become `_`; an ID over 64 characters is cut to 47 and ends with `_`
and 16 hex characters of its SHA-256. An ID that needs no change is never changed and two different IDs never come
out the same. A `reasoning` item with encrypted content and an ID over 64 characters is left out and reported as
dropped, because the endpoint could not read its content under another ID.

### Tools

**Tool choice.** A rebuilt request's choice goes as `auto`, `none`, `required` or a named function. A list of
allowed tools becomes `{"type":"allowed_tools","mode":...,"tools":[...]}` of function names. Forcing code
execution without a code tool being sent is reported dropped, and so is forcing a tool when none is left. A
Messages or Gemini request that names no choice and has tools gets `tool_choice: "auto"`.

**Parallel tool calls.** `parallel_tool_calls` is `false` when the client said `false` (Messages
`disable_parallel_tool_use` too) and for a Responses-lite request, absent when the request has no tools, and `true`
otherwise.

**Custom and namespace tools** (a Responses client's, as the Codex client sends them) go as written. Function
tools inside a namespace get the schema rewrites. Function names and call IDs inside a Responses client's body are
never renamed.

**Function schemas.** Schema forms the Codex endpoint rejects, which MCP servers' tools often use, are rewritten
in every function tool's `parameters`, also inside namespace tools and in compaction requests, from any client:

| Form | Rewrite |
| --- | --- |
| Root `anyOf` or `oneOf` | Flattened into a root `object` schema: the object branches' properties are merged (root properties win, a property that differs between branches becomes an `anyOf`) and the `required` fields common to all branches are kept. Local `$defs` and `definitions` references are followed (16 levels). |
| `pattern` or `patternProperties` key with `\p{...}`, `\P{...}`, `\0`, or an unescaped lookahead `(?=` `(?!` outside a character class | Removed. |
| Numeric bound (`minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`) that is a whole number outside the 64-bit range | Removed. |
| A property of the root schema whose `oneOf` or `anyOf` has 8 or more branches that are each a distinct scalar `const` (a `title` or `description` beside it is fine) | Becomes an `enum`. |
| `array` schema with no `items` and no `$ref` | Gets `items: {"type":"string"}`. |
| `strict: true`: optional properties | Added to `required` and made nullable (`null` added to the `type`, `enum`, `anyOf` or `oneOf`, or the schema wrapped in an `anyOf` with a null branch). |
| `strict: true`: objects with `properties` | Get `additionalProperties: false`. |

A strict tool whose schema cannot be closed without changing what it accepts (`additionalProperties` true or a
schema, an `object` type with no `properties`, an `allOf` of more than one branch, `if`, `then`, `else`, `not`,
`dependentSchemas`, `dependencies`, or `properties` beside `$ref`, `allOf`, `anyOf` or `oneOf`) is sent with
`strict: false` and none of the strict rewrites, and the change is reported as approximated. A rebuilt function
tool says `strict: false` when the client did not set it. A Gemini function's parameters lose `$schema` and get
`additionalProperties: false` at the top.

**Names and IDs.** On a rebuilt request (not on a Responses client's body), function and custom tool names in the
tools, the tool choice and the calls of the conversation are made into at most 64 bytes of `a-zA-Z0-9_-`:
every other character becomes `_`; a name still over 64 bytes that starts with `mcp__` keeps `mcp__` and what
follows its last `__` (`mcp__server__tool` becomes `mcp__tool`); any other long name is cut to 64 bytes. A name that
is short and clean is not changed. Two names that come out the same get `_1`, `_2` and so on (names are counted in
the order tools, tool choice, conversation). The reply's tool calls give the client its own name back. A call ID
over 64 bytes is cut to 47 bytes and ends with `_` and 16 hex characters of its SHA-256; the same ID always comes
out the same, so a call and its result still pair.

### Reasoning

- A thinking suffix's level is applied as the effort and wins over anything the request says (see Shared rules:
  Reasoning and the model-name thinking suffix).
- A Chat client's `reasoning_effort` and a Gemini client's `thinkingConfig.thinkingLevel` go as the effort. A
  Gemini budget and a Messages `thinking.budget_tokens` become an effort by the same bounds (the budget itself is
  reported dropped); Gemini `-1` and Messages `enabled` without a budget or effort become `auto`; Messages
  `adaptive` without an effort becomes `xhigh`; Messages `disabled` becomes `none`; a Messages
  `output_config.effort` is used when the request has no `thinking`.
- A rebuilt request that names no effort gets `medium`. A Responses client's `reasoning` is left as written.
- A Chat client that names an effort also gets `reasoning.summary: "auto"` (if it set no summary), and the summary
  comes back as `reasoning_content`.
- The effort is then clamped to the levels the model lists in the Codex client catalog: an effort the model does
  not take becomes the highest level below it, else the lowest above it (`none` is raised to the model's lowest
  level, `ultra` lowered to `max` for a model without it), and the change is reported as approximated at
  `reasoning.effort`. A model the catalog does not list, and an effort the extension does not know (such as
  `auto`), keep the value as written.
- Encrypted reasoning is always requested. A Messages client gets each reasoning item's encrypted content as its
  thinking block's `signature`; replaying the block sends the reasoning back as a `reasoning` item (its summary
  text is not sent; reported as approximated), but only when the signature has the shape of OpenAI's encrypted
  reasoning (a token starting `gAAAAA`). A Responses client replays reasoning items as it received them. A Chat or
  Gemini client gets no encrypted reasoning and its replayed reasoning text is reported dropped.
- When Codex refuses replayed encrypted reasoning it cannot read (the error code `invalid_encrypted_content`, or a
  400 whose message says the encrypted content could not be verified or decrypted: another account or provider made
  it), the extension sends the request once more without every reasoning item that carries encrypted content and
  reports each as dropped. This retry happens over HTTP only, once, and not under `drop_policy: "reject"`.

### Images, files and audio in requests

An image becomes `input_image` with `detail: auto` unless the client gave one; an inline image the
Responses format cannot take (not a valid image data URL) is reported dropped. A file becomes `input_file` (file ID,
file data, file URL, filename); a file with no ID, data or URL, or whose data is neither base64 nor a URL, is
reported dropped. Inline audio becomes `input_audio`. A Messages plain-text document becomes text and is reported
as approximated. Any other part is reported dropped.

### Web search, code execution, citations, images out

- A Responses client's `web_search` and `code_interpreter` tools go as written.
- A Chat `web_search_options`, a Messages `web_search_*` tool or a Gemini `googleSearch` tool becomes
  `{"type":"web_search"}` (with `search_context_size`, `filters` and `user_location` where the client gave them
  and the upstream is `api.openai.com`; at `chatgpt.com` only the bare tool is sent and those settings, and
  Messages `max_uses` and `blocked_domains`, are reported dropped). At any other host the tool is not sent and is
  reported dropped.
- A Messages `code_execution_*` or Gemini `codeExecution` tool becomes
  `{"type":"code_interpreter","container":{"type":"auto"}}`, only at `api.openai.com` and only for a model on a
  fixed list of OpenAI models that document the tool. At `chatgpt.com` (the default for a subscription), at any
  other host and for any other model it is reported dropped.
- A Responses request that replays `web_search_call` items in `input` but declares no web search tool gets
  `{"type":"web_search"}` added to `tools` (also in compaction requests and over the upstream WebSocket), because the
  upstream refuses such a history. A Responses-lite request has those items removed instead (not reported).
- Reply `url_citation` annotations and web search sources reach a Chat client as annotations and a Gemini client as
  grounding metadata. A Messages client gets none (reported dropped).
- The hosted `image_generation` tool (see "Changes to every body") returns an image item. A Responses client gets it
  as it came; a Gemini client gets `inlineData`; a Chat or Messages client gets no image (reported dropped). A
  failed, empty or invalid image result is dropped and reported.

### Service tier and verbosity

A Responses client's `service_tier` goes through the endpoint's filter: `fast` and `priority` become `priority`,
`ultrafast` stays, anything else (`auto`, `default`, `flex`, `scale`) is removed without a report. Its `text.verbosity`
goes as written.

A Chat, Messages or Gemini client's tier and verbosity are sent only when the account's `base_url` host is
`api.openai.com` or `chatgpt.com`; at any other host both are reported dropped at the client's field. At those
hosts the routed model's entry in the Codex client catalog (the extension's `client_models.json`) decides:
a tier is sent only if the entry lists it (`fast` counts as `priority`; the catalog lists only `priority`, so
`ultrafast` is dropped), a verbosity only if the entry has `support_verbosity` and the value is `low`, `medium` or
`high`. A model the catalog does not list gets neither. `auto` and `default` ask for Codex's standard service and
are left off with no report. Every other tier or verbosity is reported dropped (so `drop_policy: "reject"`
refuses the request). A subscription's `X-Codex-Routing-Hint` names the tier that was sent.

### Prompt cache key

`prompt_cache_key` is the client's own, else nothing when the client sent a `Session-Id` header, else a UUID made
from the account, the model, the session and the agent for a Claude Code request (the session from the
`X-Claude-Code-Session-Id` header or a Messages `metadata.user_id` written as `..._session_<id>` or as a JSON object
with `session_id`; the agent from `X-Claude-Code-Agent-Id`, else `main`), else a UUID made from the account, the
model and the conversation's start (its developer messages and first user message) when the conversation has a
user message. The key is also sent as the `Session-Id` header.

### Token counts

`POST /v1/responses/input_tokens` goes, on an API-key account, to `{base_url}/responses/input_tokens` (the model
renamed for the account) and the upstream's reply comes back as it is. A subscription and a SIWC account answer
unsupported. For these, and for Messages `count_tokens` and Gemini `countTokens` (the extension has no count
handler), core estimates while `token_count_estimate` is on (the default).

### Compaction and other endpoints

#### Compaction

The Codex CLI shortens a long conversation with `POST /v1/responses/compact` (also
`/backend-api/codex/responses/compact`). Core routes it by `model` to an account's extension; it refuses a body
with `"stream": true` with a 400 (`Streaming not supported for compact responses`). The extension sends the body
to `{base_url}/responses/compact` as written with these changes only: `model` renamed for the account, `stream`
removed, `instructions` set to `""` when absent (not for a Responses-lite request), `parallel_tool_calls: false` for
a Responses-lite request, a thinking suffix's effort applied, the effort clamped and a small `max_output_tokens`
raised as on `/responses`, a `prompt_cache_key` (and `Session-Id`) made as for `/responses`, replayed web search
items handled as above, and the function schema rewrites applied. The fields removed from `/responses` requests
(such as `temperature`), the ID and name limits and the history repairs do not apply. The upstream's JSON reply
comes back as it is.

#### Alpha Search

`POST /v1/alpha/search` (also `/backend-api/codex/alpha/search`) is Codex's standalone search. Core routes it as an
endpoint call to a Codex account: a subscription always, an API-key account only with `alpha_search` on and a
`base_url` of its own; others answer unsupported and core tries another account. A request that names no model may
go to any such account. The extension sends the body to `{base_url}/alpha/search` as written, with `prompt_cache_key`
and `prompt_cache_retention` removed and `model` renamed for the account, with `Originator: codex_cli_rs`, the
bearer token, a subscription's `Chatgpt-Account-Id` and the client's `Version`, `User-Agent`, `Session_id` and
`X-Client-Request-Id`. The reply comes back whole with its content type. An upstream error becomes an error in
OpenAI's shape.

#### Images API

`POST /v1/images/generations` and `POST /v1/images/edits` go to `{base_url}/images/generations` or
`{base_url}/images/edits`, routed by `model` (default `gpt-image-2`). `model` is sent in the upstream's spelling,
`stream` is left out unless `true`, the Codex identity and client headers are added, and the reply (JSON, or
server-sent events for a stream) comes back as it is. An upstream that answers a streamed request with one JSON
document gets it turned into `image_generation.completed` or `image_edit.completed` events. An edit may be JSON
or `multipart/form-data`; core reads a form into JSON (each image file as a data URL in `images`, the first mask file
as `mask.image_url`). Core refuses a request without a `prompt`, and a form edit without an image file, with a 400.
The models are `gpt-image-1.5`, `gpt-image-2`, `gpt-image-2.5`, `gpt-image-2.5-flare` and `gpt-image-2.5-sunburst`;
every other model is refused with a 400. A free ChatGPT plan, an account with `"images": false` and a SIWC account
answer unsupported, and core tries another account.

### Repairs of the conversation

The Codex endpoint refuses some histories. Before sending, the extension repairs them and reports each repair at the
item's path in `input`:
- A tool call (`function_call`, `local_shell_call`, `custom_tool_call`, `tool_search_call`) with no output anywhere in
  the conversation gets an output right after it: `"Tool call was interrupted before any output was recorded."`
  (a `tool_search_call` gets an empty client-run search result). A `local_shell_call_output` answers a
  `local_shell_call`. Reported as approximated.
- An `item_reference` is dropped over HTTP (Codex stores no response) and reported dropped; over the WebSocket it
  is left for the upstream.
- An `agent_message` with no `author` or no `recipient`, or any `agent_message` on a SIWC account, becomes a user
  message with its readable content; its encrypted parts are dropped; one with nothing readable is dropped.
  Reported.

Under `drop_policy: "reject"` a repair that drops something refuses the request.

### Replies and errors

#### Replies

On HTTP and on the upstream WebSocket alike:
- An output item of a terminal `response.completed` or `response.incomplete` that has no `id` (missing, `null` or
  blank) gets the `id` of the item `response.output_item.done` finished at the same position.
- `response.done` counts as `response.completed`.
- A terminal event whose `output` is empty gets the items `response.output_item.done` finished.
- A `response.incomplete` that produced nothing (no output delta with text, no finished item, no output) and has
  `output_tokens` of exactly `0` fails with an `upstream` error (502, scope request, not retried on another account,
  no cooldown for the account).
- A Responses-lite client's stream, and a Responses-lite WebSocket turn, skip the ID repair; the lite WebSocket turn
  also keeps an empty `output` empty. A lite request that is not streamed gets the repairs.
- Reply parts the extension's decoder has to leave out are written to its log as `reply loss`; they are not in the
  client's reply.

#### Errors

| Codex answers | Error |
| --- | --- |
| `usage_limit_reached` (in the body's `type`; also in a stream) | `quota`, 429, account; retry time from `resets_at`, `resets_in_seconds`, `Retry-After`, else the reset of the used-up window named by the `x-codex-primary-*`/`x-codex-secondary-*` headers; the window length goes with it |
| Capacity refusal (below), any status | `rate_limit`, 429, model, retryable (in an open stream: not retryable) |
| 3xx | `upstream`, 502, not followed |
| `account_deactivated`, or a 400/401/403 message saying the account is deactivated, disabled or suspended | `account_closed`, account |
| 401 | `auth`, account |
| 403 | `permission`, account |
| 404 | `not_found`, model; retryable only for `model_not_found` or a message that names the model |
| 429 | `rate_limit`, model (the account for a call with no model); `quota` for `insufficient_quota`; `Retry-After` read |
| 503 | `unavailable`, endpoint, not retryable; `Retry-After` read |
| other 5xx | `upstream`, endpoint (`Retry-After` read for 529) |
| other 4xx | `invalid_request` |
| In a stream: `context_length_exceeded` | `invalid_request`, 400 |
| In a stream: `rate_limit_exceeded` code or `rate_limit_error` type | `rate_limit`, 429, model |

The message is the body's `error.message` (else `upstream returned <status line>`); valid JSON is kept as the raw
body. A capacity refusal is a refusal that says the turn or the model is full: the code `turn_rate_limit_exceeded`,
`model_at_capacity` or `model_is_at_capacity`, or, with no code at all, the message "at capacity" together with the
error type `rate_limit_error` or the word "model" (or naming one of the model codes). The first code found decides,
searching `error`, the top level, `body.error`, `response.error`; another explicit code keeps its own handling. A
`Retry-After` of `0` is no wait time, and core's backoff for a model starts at about a second. A 404 that does not
name a missing model on an endpoint call blocks no model.

### Not supported

- Embeddings: no endpoint handler; core tries another account.
- Audio output.
- Token counts for Messages `count_tokens` and Gemini `countTokens`: core estimates.
- A `number` schema type is not turned into `integer` for tools of the Codex app.
- Images API on a SIWC account.

## Gemini

The Gemini extension (`pluxy/gemini`) serves Google's Gemini models through the Gemini API (an API key) and through
Vertex AI (a service account key or an express mode API key). Sign-in and account options are in
[`providers.md#gemini`](providers.md#gemini). Two account settings change how requests are translated:

| Setting | Effect on translation |
| --- | --- |
| `base_url` | Where the API is. Default `https://generativelanguage.googleapis.com`: requests go to `{base_url}/v1beta/models/{model}:generateContent`, or `:streamGenerateContent?alt=sse` for a stream. On Vertex AI it replaces Google's host. |
| `interactions` | `true` serves Gemini's Interactions API to Interactions clients. Default `false`. |

Google's own hosts are `generativelanguage.googleapis.com` for the Gemini API, and for Vertex AI the default host,
`aiplatform.googleapis.com`, `aiplatform.us.rep.googleapis.com`, `aiplatform.eu.rep.googleapis.com` and
`{location}-aiplatform.googleapis.com` (HTTPS, no user info). Where the rules below say "Google's own hosts" they mean
these. A Vertex AI service account sends requests to
`{host}/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:{method}`; an express mode key sends
them to `{host}/v1/publishers/google/models/{model}:{method}`.

### Where a request goes

| Client format | Sent to | Body |
| --- | --- | --- |
| Gemini GenerateContent | `:generateContent` or `:streamGenerateContent?alt=sse` | as written, with the repairs below |
| OpenAI Chat Completions | same | rebuilt as GenerateContent |
| OpenAI Responses (HTTP and WebSocket) | same | rebuilt as GenerateContent |
| Anthropic Messages | same | rebuilt as GenerateContent |
| Gemini Interactions | `/v1beta/interactions` | as written, with IDs cleaned (see "Interactions API") |

Gemini clients may send the client key as `Authorization: Bearer`, `x-api-key`, `x-goog-api-key` or the `key` query
parameter.

### Changes to every GenerateContent request (Gemini clients included)

- **Tool schemas are cleaned.** Each function declaration's `parametersJsonSchema` (JSON Schema mode, used by rebuilt
  requests) and `parameters` (OpenAPI mode) is rewritten so Google accepts it:
  - Local `$ref` is inlined from `$defs` or `definitions` (at most 16 levels; a ref cycle, a deeper ref or one past the
    size budget of 4 MiB per request is replaced by a `See: <name>` hint in the description, keeping the target's short
    `type`, `nullable` and `description`). A ref that is not local is replaced the same way.
  - These keywords are removed: `$schema`, `$defs`, `definitions`, `$id`, `$ref` leftovers, `propertyNames`,
    `patternProperties`, `dependentRequired`, `dependentSchemas`, `not`, `unevaluatedProperties`, `additionalItems`,
    `contentSchema`, `contentMediaType`, `contentEncoding`, `readOnly`, `writeOnly`, `deprecated`, `discriminator`, and
    every `x-` key. `if`/`then`/`else` and `allOf` are merged into the parent object.
  - A union at the root (`anyOf` or `oneOf`) is flattened into the root's properties, keeping only the `required` names
    every branch shares. Elsewhere a union with one non-null branch collapses to that branch; a `null` branch becomes
    nullability.
  - `const` becomes a one-value `enum`. An `enum` of strings stays; in JSON Schema mode an enum of numbers keeps its
    numbers; any other enum becomes strings; a boolean `enum` is removed (with an `Allowed:` hint for one value).
  - `required` names that are not properties are removed; a schema with `properties` gets `type: object`; an `array`
    with no `items` gets string items; tuple `items` become `prefixItems` (JSON Schema mode) or an `anyOf` (OpenAPI mode).
  - In OpenAPI mode `additionalProperties` is removed (`false` adds a "No extra properties allowed" hint) and
    `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`, `uniqueItems`, `contains` and `examples` move into the
    description as hints.
  - The same cleaning applies inside a `countTokens` request's `generateContentRequest`. A schema that needs no change
    keeps its bytes.
- **Turns are repaired** for every request to the Gemini API or Vertex AI:
  - A first turn from the model gets an empty user turn (`{"role":"user","parts":[{"text":""}]}`) before it.
  - A last turn from the model gets the same turn after it, except a last turn of function responses, which the
    model answers. A function response that carries an image or file is not answered, so it gets the turn too. (This is
    how a Chat or Responses assistant prefill reaches Gemini: the message is kept and the user turn is added after it.)
  - On Vertex AI, trailing model turns made only of thoughts are dropped first (at least one turn is kept); so are turns
    that hold only a replayed thought signature (an empty text part with a signature) in a rebuilt request. In a stream
    the thought turns are dropped before the leading user turn is added; in a reply that is not streamed, after. This
    shows only for a request that is a single thought turn.
  - A request that needs no change is sent byte for byte.
- **Function responses are put in the order of the calls they answer.** Gemini matches them to calls by position. A
  response is matched to its call by ID, then by name (in turn, for several calls to one function), then, when it has no
  name, to the first call left; a nameless response takes its call's name. A rebuilt request is built this way.
- **Vertex AI call IDs.** On Vertex AI, requests from Chat and Responses clients are sent without `functionCall.id` and
  `functionResponse.id`; the response is matched to its call by function name. Messages clients' IDs, Gemini clients' own,
  and every ID sent to the Gemini API are kept.
- **A thinking suffix** on the model name (`(high)`, `(16000)`, below) replaces a Gemini client's `thinkingConfig`
  (an existing `includeThoughts` is kept). On a model not known as a thinking model it has no effect.
- **MALFORMED_FUNCTION_CALL.** When Gemini ends a reply with finish reason `MALFORMED_FUNCTION_CALL` (the model wrote a
  call Gemini could not parse) before the client has been sent any content (a thought, answer text or a call), the
  request is sent again once, for streamed and non-streamed replies alike. The client sees only the second reply. A
  stream is held back only until its first content event, so reasoning streams live. When the second reply ends the same
  way, or the first one failed after content went out, the client gets an `upstream` error that names
  `MALFORMED_FUNCTION_CALL` and Gemini's finish message (the resent case adds "it was asked for again once, and that
  reply ended the same way"). The dropped reply's thought signature never reaches the client.

### Rebuilt requests (Chat Completions, Responses, Messages clients)

**Messages and system prompt.** System and developer messages become `systemInstruction`. An assistant message is a
`model` turn; user messages and tool results are `user` turns; consecutive items of one role become one turn, and an
empty text part is skipped. A tool call becomes a `functionCall` part and its result a `functionResponse` part named
after the call (a result with no call before it is named after its call ID and reported approximated). A tool result
that failed is written under the response's `error` key, not `result`. A call's arguments that are not a JSON object are
sent as `{}` (approximated).

**Safety settings.** A rebuilt request sends `safetySettings` with `HARM_CATEGORY_HARASSMENT`, `HATE_SPEECH`,
`SEXUALLY_EXPLICIT` and `DANGEROUS_CONTENT` at `OFF` and `CIVIC_INTEGRITY` at `BLOCK_NONE`. No other client format can
name Gemini's safety settings. A Gemini client's own `safetySettings` go as written.

**Tools.** Function tools are sent as one `functionDeclarations` list, first among the tools.

| Feature | What happens |
| --- | --- |
| Tool names | A name GenerateContent takes (ASCII letters, digits, `_`, `.`, `:`, `-`, at most 128 bytes, starting with a letter or `_`) is sent as written. Any other name is changed by the rule in Shared rules: Tools (tool names), and gets a leading `_` if it starts with a digit or another character. The names of replayed tool calls are included. The reply's calls go back under the client's own name. A Gemini client's names are not changed. |
| Tool choice | `auto` is `AUTO`; `none` is `NONE`; `required` is `ANY`; one named function is `ANY` with that name in `allowedFunctionNames`. An allowed list (OpenAI `allowed_tools`) is `VALIDATED` with the names for `auto` and `ANY` with the names for `required`. |
| Parallel tool calls | Gemini cannot limit a reply to one call, so `parallel_tool_calls: false` (or Messages `disable_parallel_tool_use: true`) sends `mode: NONE`, which turns function calling off, over any other tool choice; reported approximated. `true` sends nothing. |
| Forced built-in tool | `toolConfig` is written only when there are function declarations. Forcing `codeExecution`, or forcing a tool when only built-in tools are sent, is reported dropped. |
| Custom, namespace and `tool_search` tools (Responses client) | Sent as plain functions; see Shared rules: Tools. |
| Other tool types | A custom Chat tool type, a Responses tool of another type and a replayed call to a tool Gemini does not have are reported dropped (`GenerateContent has no such tool`). A replayed custom call that is not lowered is reported dropped. |
| Function call signature | A function call that has no thought signature of its own and no replayed signature waiting gets `skip_thought_signature_validator`, the value Google documents for calls it did not issue (Gemini 3 refuses a call in the current turn without a signature). |

**Reasoning and thinking.** Gemini takes thinking as a level (`thinkingConfig.thinkingLevel`, Gemini 3 and later) or a
token budget (`thinkingBudget`, Gemini 2.5). The extension decides which from the model name (lower case, without a
`models/` prefix):

| Model name | Form |
| --- | --- |
| `gemini-2.5-pro` | budget 128 to 32768; cannot be turned off |
| `gemini-2.5-flash` | budget 0 to 24576; 0 turns thinking off |
| `gemini-2.5-flash-lite` | budget 512 to 24576; 0 turns thinking off |
| `gemini-pro-latest`, `gemini-flash-latest`, `gemini-flash-lite-latest` | the same ranges as 2.5 pro, flash, flash-lite |
| `gemini-robotics-er-1.6` | budget 0 to 24576; version 2 and later take levels `minimal`, `low`, `medium`, `high` |
| `gemini-3-*` pro (3.0) | levels `low`, `high` |
| Gemini 3.x pro, and 3.8 and later | levels `low`, `medium`, `high` |
| Gemini 3.x `-image` | levels `minimal`, `high` |
| other Gemini 3.x | levels `minimal`, `low`, `medium`, `high` |
| Gemini 4 | levels `low`, `medium`, `high`; the `-neon` and `-hydrogen` models also take `xhigh` |
| any other name (Gemini 2.0 and 1.x, Gemma, unknown names) | no thinking settings are sent |

The effort comes from the client (Chat `reasoning_effort`; Responses `reasoning.effort`; Messages `output_config.effort`
or `thinking`) or from a suffix on the model name, which wins. A Messages `thinking` becomes an effort: enabled with
`budget_tokens` is the effort the budget falls in (up to 512 `minimal`, 1024 `low`, 8192 `medium`, 24576 `high`, above
that `xhigh`; 0 is `none`), enabled with no budget is `auto`, adaptive with no effort is `xhigh`, disabled is `none`. The
exact `budget_tokens` is not sent; it stays in `thinking` and is reported dropped. Only a suffix with a number
(`(16000)`) sends an exact budget.

| Effort | Model with levels | Model with a budget |
| --- | --- | --- |
| `none` | the model's lowest level, `includeThoughts` not set (approximated: the model cannot turn thinking off) | 0 where allowed; otherwise the model's minimum (approximated) |
| `minimal`, `low`, `medium`, `high`, `xhigh` | that level; if the model has none, the next higher level it has, else its highest (approximated) | 512, 1024, 8192, 24576, 32768, kept within the model's range (approximated when changed) |
| `max` | `xhigh`, as above | the model's maximum |
| `auto` | no level; `includeThoughts: true` | -1 (dynamic) |
| a number (suffix) | the level the number falls in | that number, kept within the model's range (approximated when changed) |

An effort the extension does not know, or a model it does not know as a thinking model, sends no thinking settings and
the effort is reported dropped.

`includeThoughts: true` is set whenever an effort other than `none` is applied, so thought summaries come back as
reasoning (Chat `reasoning_content`, a Responses reasoning item, a Messages thinking block). A Messages client's
`thinking.display` (`summarized` true, `omitted` false) and a Responses client's `reasoning.summary` or
`generate_summary` (`auto` true; `concise` and `detailed` true, approximated; `none` or `null` false) override it. A
`true` is not sent beside a budget of 0.

*Replay.* A Gemini thought signature reaches a Messages client as a thinking block's `signature` and a Responses client as
a reasoning item's `encrypted_content` (a signature on a part that is not a thought becomes an empty reasoning item just
before that part). When the client replays the item, the signature goes back on the part that follows it, so Gemini 3's
function calls keep their reasoning between turns; the thought text itself is not replayed. A Messages signature counts
as Gemini's only when it has the shape of one (base64 of a protocol buffer starting with field 1); reasoning from another
provider (a Claude signature, OpenAI encrypted reasoning, a Chat `reasoning_content`) is reported dropped. A Chat client
has no field for a signature, so its replayed function calls carry `skip_thought_signature_validator`.

**Sampling and limits.**

| Client field | Sent as | What the client sees |
| --- | --- | --- |
| `temperature`, `top_p` | `temperature`, `topP` | works |
| Chat `max_tokens`/`max_completion_tokens`, Responses `max_output_tokens` | `maxOutputTokens` | works |
| Messages `max_tokens` | not sent: the model's own limit applies | reported dropped. Messages makes `max_tokens` mandatory, so under `drop_policy: "reject"` every Messages request to Gemini is refused |
| `stop`, `stop_sequences` | `stopSequences` | works |
| `top_k`, `seed`, `presence_penalty`, `frequency_penalty`, `logit_bias`, `logprobs`, `user`, `metadata` and every other field the model has no place for | not sent | reported dropped at the field's path |

**Images, files and audio in.** The rules are the GenerateContent column in Shared rules: Images, files, audio and
documents. A Gemini client's own parts are sent as written, `gs://` URIs and unchecked media types included.

**Web search.** A client's search tool (Chat `web_search_options`, Responses `web_search`, Messages
`web_search_20250305`) becomes `{"googleSearch":{}}` when all of these hold: the host is one of Google's own, the model is
in the documented list of Gemini families that take search (not embedding, TTS, `gemini-2.0-flash-lite`, or most image
models), the request has no function tools (Gemini does not combine `googleSearch` with `functionDeclarations`; the
function tools win and the search tool is reported dropped), and the model takes structured output beside it when the
request asks for it. Otherwise the tool is reported dropped. `external_web_access: false` drops the tool. Every setting of
the client's tool (`max_uses`, `allowed_domains`, `blocked_domains`, `user_location`, `search_context_size`) is reported
dropped.

**Code execution.** A client's code tool (Responses `code_interpreter`, Messages `code_execution_*`) becomes
`{"codeExecution":{}}` on Google's own hosts, on the documented model families (not embedding, TTS, image, Live,
native-audio or transcribe models, nor `gemini-2.0-flash-lite`), without function tools, and (for structured output) on
Gemini 3 and later. Beside `googleSearch` it is dropped on Vertex AI and on Gemini 2.0 models, and kept on the Gemini API
for other models. Settings of the client's tool (container, `file_ids`, `memory_limit`, `network_policy`,
`allowed_callers`, other fields) are reported dropped. A forced choice of the code tool is reported dropped, since
`toolConfig` cannot force `codeExecution`. A Chat Completions client has no code tool.

**Structured output.** `response_format` (Chat), `text.format` (Responses) and `output_config.format` (Messages) become
`responseMimeType: "application/json"`; a JSON schema also sets `responseJsonSchema` as the client wrote it (it is not
cleaned like tool schemas). The schema's name, description and `strict` are not sent and not reported. Plain text sets
nothing. On a model before Gemini 3, `googleSearch` and `codeExecution` are dropped (reported) when structured output is
requested.

**Service tier and verbosity.** `serviceTier` is sent only to `generativelanguage.googleapis.com` (never on Vertex AI or
a custom host): `default` is `standard`; `flex` and `priority` are themselves; `fast` (and a Messages `speed: "fast"`)
is `priority`; a Messages `standard_only` is `standard`. `auto`, `scale`, `ultrafast` and unknown values are reported
dropped, never moved to another tier. Verbosity has no place in GenerateContent and is reported dropped.

**Multiple candidates.** A Chat `n` above 1 is sent as `generationConfig.candidateCount` only when the host is Google's
own, for the model families and count limit in Shared rules: Multiple candidates, and, on Vertex AI, only when the
request is not streamed. Otherwise it is reported dropped. Candidates after the first reach a Chat client as extra
choices with text, reasoning and tool calls; their thought signatures, citations, code runs and files are reported
dropped, and a candidate that ends with `MALFORMED_FUNCTION_CALL` ends as `stop` with a reported loss. A candidate the
reply never finished ends as `length` with a reported loss. Responses and Messages have no `n`.

**Prompt caching.** There is no request-side cache field; cache hints such as `cache_control` and `prompt_cache_key` are
reported dropped. The reply's `cachedContentTokenCount` is reported as cached input tokens.

### Replies

- **Reasoning.** Thought parts become reasoning (see "Reasoning and thinking").
- **Function calls** arrive whole. A call that has no ID gets `call_<responseId>_<n>` (`responseId` reduced to letters,
  digits, `-` and `_`, at most 64 characters; random when Gemini sent none); a call in a candidate after the first gets
  `call_<prefix>_c<candidate>_<n>`. A call with no name in a later candidate is reported dropped.
- **Integer arguments.** Gemini's arguments are a protobuf Struct, so an integer parameter can come back as `2500.0`. In
  a rebuilt reply the extension writes it as `2500` where the request's tool schema declares that property `integer`
  (nested objects and array items included); a number under an unknown schema, a value with a fraction and a value beyond
  2^53 are left as they came. A Gemini client's reply is not changed.
- **Finish reasons.** `STOP` is a stop, or a tool-call stop when the reply has calls; `MAX_TOKENS` is length; `SAFETY`,
  `RECITATION`, `LANGUAGE`, `BLOCKLIST`, `PROHIBITED_CONTENT`, `SPII` and the `IMAGE_*` safety reasons are content filter;
  a prompt blocked before any candidate (`promptFeedback.blockReason`) is content filter too. Any other reason is
  "other": a Chat client gets Gemini's own word as `finish_reason`, a Messages client gets `end_turn`. A non-streamed
  reply with no finish reason counts as finished with "other"; a stream that ends before any finish reason is an
  `upstream` error ("upstream stream ended before the reply finished"), carrying the usage seen so far. An `error` object
  inside a stream chunk or a 200 reply ends the reply with an error mapped from its Google status.
- **Usage.** Input tokens are `promptTokenCount` plus `toolUsePromptTokenCount` (the prompt count already includes cached
  tokens); output tokens are `candidatesTokenCount` plus `thoughtsTokenCount`, with the thought tokens also reported as
  reasoning tokens. An empty or zero-filled `usageMetadata` after counts were reported does not overwrite them.
- **Web search sources and citations.** `groundingMetadata` (`groundingChunks` with `web.uri`, `groundingSupports`) is
  read; byte offsets (per part) become character offsets within the message item that holds the text. Only `http` and
  `https` URLs are kept. A Chat client gets citations only in a reply that is not streamed. A citation that cannot be
  placed on one message item, citations of later candidates, sources no annotation cites (Chat, Responses) and everything
  past 4 MiB of citations or sources are reported dropped.
- **Code execution results.** `executableCode` paired with `codeExecutionResult` is read as a run. A run with no readable
  result is left out and reported.
- **Generated images and audio** (`inlineData`): only safe, valid base64 parts of a known image or audio type within
  24 MiB per part and 96 MiB per reply are read; others are reported dropped.
- **Gemini clients** get every upstream chunk unchanged (the extension reads it only for usage and reports), citations,
  code runs, images and audio included.

### Token counts

`countTokens` is called for Messages `count_tokens` and Gemini `:countTokens`. A Gemini client's body is sent as written
(tool schemas cleaned, also inside `generateContentRequest`; a top-level `contents` that starts with a model turn gets
the empty user turn; a thinking suffix sets its thinking settings; a wrapped request is not otherwise repaired). A
Messages request is rebuilt as GenerateContent and counted whole: under `generateContentRequest` on the Gemini API, as
`contents`, `systemInstruction` and `tools` on Vertex AI (which takes no `generateContentRequest`; a Gemini client's
wrapped request is unwrapped to those fields). A request that the rebuild would drop parts of is refused under
`drop_policy: "reject"`. The reply's `totalTokens` is the count. The Responses `input_tokens` endpoint is not served by
Gemini (see Shared rules: Token counts and `token_count_estimate`).

### Interactions API

`POST /v1beta/interactions` is served only by accounts whose settings say `"interactions": true`; any other account
answers `unsupported` (so core tries another account). There is no translation: Interactions clients are not served by
other providers, and a request no opted-in account serves gets the usual error. An account with `interactions: true`
still serves Chat, Responses, Messages and Gemini clients through GenerateContent.

- Core refuses with 400 a request that names both a `model` and an `agent` or neither, a `stream` that is not a boolean,
  and a body that is not JSON; it removes a `models/` prefix from the model. A request with an `agent` (a Google agent
  such as Deep Research) goes to any account that serves the API. The client key may be sent in `x-goog-api-key` or `?key=`.
- The body goes upstream as written, with the model renamed for the account and the input cleaned: a `function_call` step
  with no `id` takes its `call_id` as `id` and loses `call_id`; every other step and every content part of a step that is
  not a thought loses its `id`; a `thought` step keeps only its signature (the first non-blank of `signature`,
  `thought_signature`, `thoughtSignature`, written at `signature`), and a thought step with no signature is left out.
  Signed thought steps are not checked.
- The client's `Api-Revision` header goes along (default `2026-05-20`).
- A Gemini API key posts to `{base_url}/v1beta/interactions`. A Vertex AI service account posts to
  `{host}/v1beta1/projects/{project}/locations/global/interactions` (always `global`); an express mode key posts to
  `{host}/v1beta1/interactions`, or to the project path when `vertex.project` is set. A stream adds `alt=sse` on Vertex AI.
- The reply comes back as Google sent it (one JSON object, or each server-sent event with its name as it arrives), except
  that Google project and account names in an error are replaced by `[redacted]`. Its `usage` counts toward usage
  statistics (`total_input_tokens`, `total_output_tokens`, `total_cached_tokens`, `total_thought_tokens`).
- A thinking suffix on the model name is refused with 400 `invalid_request`; set `generation_config.thinking_level`.

### Compaction and other endpoints

All of these are served on every Gemini account (the Gemini API, Vertex AI service accounts and express mode keys), without
`interactions`. A value the extension cannot serve is answered `unsupported` for that account, naming the field, with HTTP
status 400; core then tries another account on the same model, and when only Gemini accounts serve it the client gets the 400.

**Compaction (`POST /v1/responses/compact` and `/backend-api/codex/responses/compact`).** The Responses request is rebuilt
as non-streamed GenerateContent with the rules above, then stop sequences, structured output and `parallel_tool_calls`
are cleared, the tool choice is set to `none` (so, when function tools are sent, `toolConfig` mode `NONE`), and the Codex
CLI's compaction prompt is appended as a user turn. Thought parts are left out of the summary. The reply is OpenAI's
`response.compaction` object holding one user message: the Codex CLI's summary prefix, a newline, then the summary. An
empty summary, or a reply whose finish reason is not `STOP`, fails as a 502 `upstream` error for that request only.

**Embeddings (OpenAI `POST /v1/embeddings`).**
- Gemini API: one input is `:embedContent`; several are `:batchEmbedContents` in batches of at most 100, in order, with
  summed token usage.
- Vertex AI: `gemini-embedding-2` and its variants use `:embedContent` one input at a time; `gemini-embedding-001` uses
  `:predict` one input at a time; other text embedding models use `:predict` in batches of up to 250.
- `dimensions` becomes `outputDimensionality`; `encoding_format` is `float` (default) or `base64` (little-endian float32).
- Refused as `unsupported`: token-ID array inputs, more than 2048 inputs, a `dimensions` below 1, any other
  `encoding_format`, streaming, a thinking suffix. A usage total is reported only when every upstream call reported usage.
- Gemini's own `POST /v1beta/models/{model}:embedContent` and `:batchEmbedContents` are served. On the Gemini API the body
  and reply pass through (a model named in the body that is not the account's model is rewritten to it; a reply that is an
  error object, or lacks an embedding for any request, becomes an `upstream` error). On Vertex AI the same per-model rules
  as above apply (`embedContentConfig` for `gemini-embedding-2*`; `:predict` otherwise), and on `:predict` a non-text part
  or an `embedContentConfig` field other than `taskType`, `title` and `outputDimensionality` is answered `unsupported`.

**Speech (OpenAI `POST /v1/audio/speech`).** Sent as non-streamed `:generateContent` with `responseModalities: ["AUDIO"]`
and `speechConfig.voiceConfig.prebuiltVoiceConfig.voiceName`. OpenAI voices map to Gemini's: `alloy` Kore, `ash` Iapetus,
`ballad` Algieba, `cedar` Achird, `coral` Sulafat, `echo` Charon, `fable` Umbriel, `marin` Despina, `onyx` Orus, `sage`
Vindemiatrix, `shimmer` Achernar, `verse` Puck. `nova` has no mapping and is refused; any other voice name goes to Gemini
as written. Served formats are `pcm` (`audio/pcm`) and `wav` (`audio/wav`; raw PCM is wrapped in a 44-byte header using
the sample rate and channel count in the reply's `mimeType`, default 24000 Hz mono 16-bit). `mp3` (OpenAI's default when
`response_format` is omitted) and every other format, `stream`, `stream_format` other than `audio`, `speed` other than 1.0,
non-blank `instructions`, a custom voice object and a thinking suffix are refused as `unsupported`. A reply whose finish
reason is set and not `STOP` fails as a 502 `upstream` error instead of returning cut-off audio.

**Transcription (OpenAI `POST /v1/audio/transcriptions`).** The uploaded audio is sent as `inlineData` (the whole request
must stay within 20 MB) with a verbatim-transcript instruction, plus `language` and `prompt` as hints and `temperature`
(0 to 1). Thought parts are left out of the text. `json` (`{"text": ...}`) and `text` (`text/plain; charset=utf-8`) are
served. Refused as `unsupported`: other response formats (`verbose_json`, `srt`, `vtt`, `diarized_json`),
`timestamp_granularities`, `include`, speaker names and references, `chunking_strategy`, `stream`, a temperature outside
0 to 1, a thinking suffix, an audio type Gemini does not list (the type comes from the part's content type, else the file
extension), and a request over the 20 MB limit. A reply whose finish reason is not `STOP` fails as a 502 `upstream` error.
`POST /v1/audio/translations` is not served.

**Images (OpenAI `POST /v1/images/generations`).** One non-streamed `:generateContent` call with
`responseModalities: ["IMAGE"]`. `size` as `WIDTHxHEIGHT` becomes the nearest of Gemini's aspect ratios (`1:1`, `1:4`,
`4:1`, `1:8`, `8:1`, `2:3`, `3:2`, `3:4`, `4:3`, `4:5`, `5:4`, `9:16`, `16:9`, `21:9`); `auto` or no size leaves the choice to
Gemini. `n` above 1 and `stream: true` are refused with 400 `invalid_request`. The reply holds every image as `b64_json`;
no image is a 502 `upstream` error. Other fields (`quality`, `response_format`, and so on) are ignored without a report.
`POST /v1/images/edits` is not supported.

### Errors and limits

| Upstream answer | Result |
| --- | --- |
| 3xx | `upstream` (502), "upstream redirected to another host; not followed" (a redirect to another scheme or host is never followed) |
| 403 with ErrorInfo reason `CONSUMER_SUSPENDED`, or 401/403 whose message says the account is deactivated, disabled or suspended | `account_closed`, credential scope, another account may serve |
| 401; 400 with reason `API_KEY_INVALID` (Google answers a wrong key with 400) | `auth`, credential scope, another account may serve |
| other 403 | `permission`, credential scope, another account may serve |
| 404 | `not_found`, model scope; another account may serve only when the message names the model |
| 429 | `rate_limit`, model scope (credential scope when the call named no model); `quota` when the status is `RESOURCE_EXHAUSTED` and the reason is `QUOTA_EXHAUSTED`. The wait comes from `Retry-After`, else `RetryInfo.retryDelay`, else ErrorInfo `quotaResetDelay`. |
| 503 | `unavailable`, endpoint scope; not retry-safe (the model may have started) |
| other 5xx | `upstream`, endpoint scope; a 529 reads `Retry-After`; a 500 whose message says "input context is too long" becomes a 400 `context_length_exceeded` error for that request only |
| other 4xx | `invalid_request`, request scope; a message that says the prompt does not fit the context window becomes `context_length_exceeded` (400) |

The message is Google's `error.message` (else "upstream returned <status line>"); a JSON body is kept as the error's raw
data. Errors of generate, count and endpoint calls have Google project, organization, folder, billing account and person
names (and the account's own project and service account address) replaced by `[redacted]`, in the message and in details
(the details themselves, `RetryInfo` and `QuotaFailure` among them, stay). Core holds the model back on the account after
a 429, for the reset time when Google gave one.

### Not supported

- Interactions requests on an account without `interactions: true` (no translation onto GenerateContent), a thinking suffix
  on an Interactions model, and a check that drops a thought step whose signature does not look like Gemini's.
- `POST /v1/images/edits`, `POST /v1/audio/translations`, mp3 speech, streamed speech, and Imagen models (`predict`).
- Google tools other than `googleSearch` and `codeExecution` from a client that is not a Gemini client.

## OpenAI-compatible

The OpenAI-compatible extension (`pluxy/openai-compatible`) serves any upstream that speaks OpenAI Chat
Completions, OpenAI Responses, Anthropic Messages or Gemini GenerateContent with an API key. Sign-in and
options are in [`providers.md#openai-compatible`](providers.md#openai-compatible).

Settings that change translation:

| Setting | Meaning |
| --- | --- |
| `base_url` | The upstream's address, including its API path. Its host selects the presets and the carried fields below. |
| `wire_api` | The API the upstream speaks: `chat` (default), `responses`, `messages` or `gemini`. Any other value is refused. |
| `reasoning_levels` | The reasoning efforts a `chat` or `responses` upstream accepts, from `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`. Default `["low","medium","high"]`. |
| `developer_role`, `string_content`, `reasoning_passback` | Shape Chat bodies (see Chat shaping). |

### Where a request goes

A request in the upstream's own format is sent as written; one in another format is rebuilt in the
upstream's format. Each `wire_api` serves all four client formats.

| `wire_api` | Sent to | Key header |
| --- | --- | --- |
| `chat` | `{base_url}/chat/completions` | `Authorization: Bearer` |
| `responses` | `{base_url}/responses` | `Authorization: Bearer` |
| `messages` | `{base_url}/messages` | `x-api-key`, `anthropic-version` (the client's, else `2023-06-01`), the client's `anthropic-beta` |
| `gemini` | `{base_url}/models/{model}:generateContent` or `:streamGenerateContent?alt=sse` | `x-goog-api-key` |

A redirect to another scheme or host is not followed (the key would go with it). The sections below
describe the default `chat` wire; Where another wire differs says where the others differ.

### Changes to every body on a `chat` wire

Every body, as written or rebuilt, gets these:

| What | Rule |
| --- | --- |
| Reasoning effort | A model-name suffix, such as `gpt-5(high)`, wins over the body's `reasoning_effort`: core removes it from the name and the effort it comes to is written. An effort outside `reasoning_levels` is moved to the nearest listed level (the higher one on a tie; `auto` is taken as `medium`): by default `xhigh` and `max` become `high`; `minimal`, `none` become `low`. The move is reported as an approximated loss naming the effort asked and the effort sent. An unknown effort name is sent as written. |
| Developer role, string content, reasoning passback, assistant annotations | Chat shaping, below. |

If a body that was to go as written is changed or loses anything here, it is no longer reported as sent as
written.

**Chat client.** Sent as written, with the changes above.

**Responses, Messages and Gemini clients.** Rebuilt as a Chat request:

| Client feature | What happens |
| --- | --- |
| System and developer messages | Kept as messages with their role (`developer_role` may change it). |
| Function tools | Sent as Chat function tools. `tool_choice` and `parallel_tool_calls` are written only when tools are written. |
| Custom, namespace and `tool_search` tools of a Responses client | See Shared rules: Tools. |
| Max tokens, temperature, `top_p`, `stop` | Carried; max tokens is sent as `max_tokens`. |
| Streaming | A streamed rebuilt request sets `stream_options.include_usage`. |
| Structured output | Rebuilt as `response_format` (`text`, `json_object` or `json_schema` with name, description, schema, `strict`). |
| Tool-call arguments that are not a JSON object | A Chat client's own call keeps its bytes, and a Chat custom tool call keeps its free-form input. |

Service tier, verbosity, web search, code execution and candidate count from another client format are
sent only to the hosts below (see Shared rules: Service tier, verbosity and gated features). Any other host
gets none of them.

| Host | Wire | Carried |
| --- | --- | --- |
| `api.openai.com` | `chat` | service tier (`auto`, `default`, `flex`, `scale`, `priority`, `fast`), verbosity (`low`, `medium`, `high`), web search, candidate count |
| `api.openai.com` | `responses` | service tier (the same list and `ultrafast`), verbosity, web search, code execution (on documented models) |
| `api.anthropic.com` | `messages` | service tier `auto` and `default` (priority needs a beta header the extension does not add), web search, code execution, structured output rules |
| `generativelanguage.googleapis.com` | `gemini` | service tier, web search, code execution, candidate count |
| `openrouter.ai` | `chat` | replayed assistant `annotations` |

A Chat client's own `n` and `web_search_options` are sent as written to any host.

### Chat shaping (`chat` wire)

For upstreams stricter than OpenAI's own, applied to every Chat body, as written or rebuilt:

| Setting | Rule |
| --- | --- |
| `developer_role` | `developer` (default) or `system`; any other value is refused. A developer message is sent as that role. |
| `string_content` | `true` sends a message whose content is only text parts as one string. Default `false`. |
| `reasoning_passback` | `true`: when the request has tools and does not set `thinking.type` to `disabled`, an assistant message with no reasoning text gets a blank `reasoning_content` (one space, reported approximated), since the upstream answers 400 to a turn without it; what the client sent stays. `false`: `reasoning_content` is removed from every assistant message. Unset: left as the client sent it. |
| Assistant `annotations` | Removed from replayed assistant messages on every host but `openrouter.ai` (reported approximated). |

A setting left unset takes its host's preset (by `base_url` host), and a setting you give always wins:

| Host | Preset |
| --- | --- |
| `api.deepseek.com` | `developer_role` `system`, `string_content` `true`, `reasoning_passback` `true` |
| `api.mistral.ai` | `developer_role` `system`, `reasoning_passback` `false` |

Other hosts, OpenRouter and Groq included, have no preset. Shaping applies only to the `chat` wire.

### Reply on a `chat` wire

| Case | Rule |
| --- | --- |
| Refusal | A `refusal` that is not a string is ignored. |
| Tool calls | A call with no ID gets `call_<index>` (or the next free `call_<n>`); a name that arrives in a later chunk is waited for. |
| Usage | An empty or zero-filled usage chunk after a real one does not replace it. When a stream breaks or fails, the usage reported so far is kept. |
| Truncation | A stream that ends before every choice has a finish reason is an upstream error. An error object in the stream is an upstream error. |
| A loss found in a stream | The report has already gone out with the first event, so a loss found later (a dropped nameless call, a citation) is written to the extension's log, and only when the client does not get the upstream's body. |

### Endpoints

| Endpoint | Rule |
| --- | --- |
| Responses compaction (`POST /v1/responses/compact`, `POST /backend-api/codex/responses/compact`) | `chat` wire: the Responses conversation is rebuilt as one non-streamed Chat request with `tool_choice` `none`, `stop` and structured output cleared, `parallel_tool_calls` unset, and the Codex compaction prompt appended as a last user message. The reply is a `response.compaction` object with one user message (the Codex summary prefix, a newline, the summary), and the usage. Reasoning content is excluded; an empty summary, a tool call, or a finish other than a normal stop is a 502 error on the request. The request is shaped like any Chat request (effort clamp, presets, drop policy). `responses` wire: the client's body goes to `{base_url}/responses/compact` as written and the reply comes back as it came. `messages` and `gemini` wires: not served; core tries another account. |
| Responses input token count (`/v1/responses/input_tokens`) | `responses` wire only, to `{base_url}/responses/input_tokens`; other wires are not served. |
| Token counts (`/v1/messages/count_tokens`, `:countTokens`) | `messages` wire: `{base_url}/messages/count_tokens`; `gemini` wire: `{base_url}/models/{model}:countTokens`. `chat` and `responses` wires cannot count. |
| Embeddings (`/v1/embeddings`) | `chat` and `responses` wires: sent as written to `{base_url}/embeddings` with the routed model name. |
| Gemini `:embedContent` and `:batchEmbedContents` | `chat` and `responses` wires: rebuilt as `{base_url}/embeddings` (text parts only; `outputDimensionality` becomes `dimensions`); `taskType` (other than unspecified), `title`, non-text parts and other config are refused as unsupported (core tries another account, then answers 400). `gemini` wire: as written. `messages` wire: not served. |
| Images API (`/v1/images/generations`, `/v1/images/edits`) | `chat` and `responses` wires: to `{base_url}/images/generations` or `/edits` with the routed model name. A multipart edit is rebuilt as multipart with a new boundary; a JSON edit stays JSON. The reply body and content type come back as they came, including server-sent events. |
| Audio (`/v1/audio/speech`, `/transcriptions`, `/translations`) | Sent to `{base_url}/audio/...`; transcription and translation forms are rebuilt as multipart; the reply body and content type come back as they came. |

**Side endpoint 404.** A 404 from a side endpoint that does not say the model is unknown (compaction,
input token count, count tokens, embeddings, images, audio) fails that request only, with a message naming
the endpoint and path, and holds back no model on the account. A 404 that says the model does not exist
still holds the model back, except on embeddings, where it makes the account refuse that request (core tries
another account) without holding the model back. A 404 from the generation path always holds the model back.
On the count paths a 404 means the account cannot count, so core estimates.

### Errors

| Status | Result |
| --- | --- |
| 401 | authentication, can try another account |
| 403 | permission, can try another account |
| 404 | model error; can try another account only when it says the model is not served (`model_not_found`, or the message names the model) |
| 429 | rate limit on the model, keeps `Retry-After`; code `insufficient_quota` makes it a `quota` error |
| 503 | unavailable, keeps `Retry-After`, never retried on another account (a gateway may have run the request) |
| other 5xx | upstream error, not retried elsewhere; `529` keeps `Retry-After` |
| other 4xx | invalid request; a message that says the prompt does not fit gets `context_length_exceeded` and fails only that request |

The message is `error.message`, else the status line; valid JSON bodies are kept as raw. Upstream errors
from a Google host (or in Google's error shape) have the project and account names masked.

### Where another wire differs

| Wire | Differences from `chat` |
| --- | --- |
| `responses` | Responses client as written (custom, namespace and `tool_search` tools pass without lowering). Chat, Messages, Gemini clients are rebuilt as Responses: `stop`, `n` (Gemini `candidateCount`), `top_k`, seed, penalties reported dropped; service tier, verbosity, web search and code execution only to `api.openai.com`. The effort clamp applies to `reasoning.effort`; the suffix wins. Chat shaping does not apply. |
| `messages` | Messages client as written. Others rebuilt as Messages: `max_tokens` is 32000 when the client set none; `reasoning_effort` reported dropped unless a model-name suffix sets thinking; verbosity and `n` reported dropped; a custom tool call and reasoning from another provider reported dropped; at most four `cache_control` breakpoints. Effort clamp and Chat shaping do not apply. |
| `gemini` | Gemini client as written. Others rebuilt as GenerateContent: `reasoning_effort` reported dropped unless a suffix sets thinking; verbosity, function `strict` and custom tool calls reported dropped; `parallel_tool_calls` false turns function calling off (approximated); `googleSearch` and `codeExecution` are not combined with function declarations. Effort clamp and Chat shaping do not apply. |

### Not supported

Reasoning-effort checking against a model's own levels (only the account's `reasoning_levels` list),
WebSocket sessions (each turn is a plain request), tool-count limits, tool schema rewriting, rate-limit
headers on the `gemini` wire, and any endpoint not listed under Endpoints (core tries another account).

## xAI

The xAI extension (`pluxy/xai`) serves Grok models with an xAI API key or a Grok account signed in as the
Grok CLI signs in. Every client format goes to xAI's Responses API, `POST {base_url}/responses`. Sign-in
and options are in [`providers.md#xai-grok`](providers.md#xai-grok).

Setting that changes translation: `inject_x_search` (default `false`) adds xAI's `x_search` tool to
requests that do not have one (see X Search).

### Where a request goes

| Client format | Body |
| --- | --- |
| OpenAI Responses (HTTP and WebSocket) | as written, with the changes below |
| OpenAI Chat Completions, Anthropic Messages, Gemini GenerateContent | rebuilt as Responses, then the same changes |

A Responses WebSocket turn is sent like an HTTP request that carries the whole conversation, which core
keeps: the extension holds no sessions. The stream flag follows the client's: a request that is not
streamed is sent with `"stream": false` and its reply is read as one response.

**Headers.** An API key's requests carry a `pluxy-xai` User-Agent. A Grok login's requests carry the Grok
CLI's identity on every request: `User-Agent: xai-grok-workspace/<client_version>`,
`X-XAI-Token-Auth: xai-grok-cli`, `x-grok-client-version`, `x-grok-client-identifier: grok-shell` and
`x-authenticateresponse: authenticate-response`. When the body has a `prompt_cache_key`, the header
`x-grok-conv-id` is set to it.

### Changes to every body

| What | Rule |
| --- | --- |
| `stop`, `previous_response_id`, `prompt_cache_retention`, `safety_identifier`, `stream_options` | Removed, with no report. A Responses client that continues with `previous_response_id` gets no stored history: it must send the whole input. |
| `store` | `false` unless the client set it, so xAI does not keep the conversation. |
| `include` | `reasoning.encrypted_content` is added once to the client's list, so a reasoning model's thinking reaches the next turn without stored responses. |
| Function tool schemas | Local `$ref` is inlined from `$defs` or `definitions` (recursion stops after 16 levels), both keys removed. A function with no parameters gets `{"type":"object","properties":{}}`. |
| Tool count | At most 200 tools: the first 200 are kept after namespaces and injected search, and the rest are cut with no report. |
| `additional_tools` input items | Their tools move to the end of the tools list and the item is removed (xAI does not take that item), before the rules below. A function or custom tool whose name an earlier tool has is dropped: the tools list beats an item, and an earlier item beats a later one. Namespaces are all kept and merged by name. |
| Custom tool call and output items in history | Sent as `function_call` and `function_call_output` items. An item with no call ID or no name is dropped. |
| `automation_update` | The Codex app's tool of that name (in a `codex_app` or `codex_apps` namespace, with or without an `mcp__` prefix) is sent with parameters that accept any object and `strict: false`, because its own schema (a large `oneOf` with `$ref`) makes xAI accept the request and send no events. |

### Tools

**Custom tools.** A custom (freeform) tool is one the model fills with plain text, such as Codex's
`apply_patch`. Only a Responses client can declare one. Every custom tool the client declares, at the
top level, in a namespace or in an `additional_tools` item, is sent as a function with one required string
argument, `input`, holding the tool's text. The tool's `format` is removed and a grammar is added to the
description (after the tool's own description and a line telling the model to put the text in `input`;
a text format adds nothing). `apply_patch` has its own description. A history call to such a tool carries
its text in `input` even when the text is itself a JSON object; a call whose `input` is not a string is
refused with a 400 invalid-request error. A `tool_choice` or `allowed_tools` entry of type `custom` that
names such a tool names the function. When a name is declared more than once, the first declaration stays
(tools list before `additional_tools`, top level before a namespace child, then the first declared).

xAI's function call to such a tool goes back to a Responses client as a `custom_tool_call` with the text
as its `input`: the added item, the input events, the end of the input, the done item and the final
response all show it, with xAI's sequence numbers. A client function of the same kind stays a function,
and xAI's own `x_search` calls are left as they are. Arguments that are not exactly one `input` string, or
that are more than 16 MiB for one call or 32 MiB for one reply (white space counted), end the reply with a
502 upstream error that names the tool and does not repeat the arguments. A call with its arguments but no
done event is finished when the reply ends.

**Namespace tools.** A namespace tool, `{"type":"namespace","name":N,"tools":[...]}`, is how the Codex app
groups tools; xAI has none, so the extension sends its functions.

| Case | Rule |
| --- | --- |
| Up to 200 tools after flattening (the injected `x_search` counts, unless the request has its own) | Each child is a function named `N__child`. A name that already starts with `N__`, or with `mcp__`, keeps its name. `tool_search` children are left out. Codex's default `functions` namespace is not special: its children become `functions__child`. |
| More than 200 | Each namespace is folded into one function named `N` whose arguments are `{"name": child, "arguments": {...}}` and whose description lists the children with their schemas. Folded namespaces are kept first when the tools are cut to 200. |
| Same namespace declared twice | One namespace with the children of every declaration; the first declaration of each child stays. |
| History and `tool_choice` | A function call in the history, and a `tool_choice` that names a namespace, are written the way the tools were sent. |
| Reply | xAI's call comes back to a Responses client as the call to the child in its namespace (added item, done item, final response). A folded call is unwrapped when its arguments name one of the namespace's children, and stays a call to the namespace otherwise. |

A custom tool in a namespace is sent as a function (`N__tool`, or a child of the folded function) and comes
back as a `custom_tool_call` in its namespace. A top-level tool and a namespace's tool of one name are two
tools. For a folded namespace with a custom tool the child is known only from the call's arguments, so the
call's events are held until they name it; its custom call then shows the added item and the whole input at
the end, with no input fragments. A stream whose first event is such a call, before `response.created`,
ends with an upstream error.

**X Search.** xAI's native `{"type":"x_search"}` tool and its options go as written. With
`inject_x_search` the extension adds the tool once, and also allows it in an `allowed_tools` choice, in
every client format, with these exceptions: nothing is added when the list already holds 200 tools, and
nothing is added when the request forces a hosted `web_search` or `image_generation` tool (by choice, or
by an allowed list that names only it): then only that tool is kept and `tool_choice` becomes `required`
or `auto`. When the client has no tools, its `tool_choice` and `parallel_tool_calls` are removed.
For a request that uses X Search, xAI's internal `x_user_search`, `x_semantic_search`, `x_keyword_search`
and `x_thread_fetch` call traces are hidden from the reply (JSON and streamed, every client format), the
remaining output indexes are renumbered, and the answer, citations, hosted search items and usage are kept.
A client function of those names is kept unless its call ID starts with `xs_call`; calls inside a namespace
are kept.

### Reasoning and other fields of a rebuilt request

A Responses client's body goes as it came, so these apply to Chat, Messages and Gemini clients, whose
request is rebuilt:

| Client feature | What happens |
| --- | --- |
| `stop` | Reported dropped (Responses has no such field). |
| Reasoning effort | Sent as `reasoning.effort`. A Messages `thinking` budget becomes an effort (0 `none`, up to 512 `minimal`, 1024 `low`, 8192 `medium`, 24576 `high`, more `xhigh`); `thinking` enabled with no budget becomes `output_config.effort` if given, else `auto`; `adaptive` becomes that effort, else `xhigh`; `disabled` becomes `none`. A Gemini `thinkingLevel` is the effort; a Gemini `thinkingBudget` follows the same budget bands (negative is `auto`). The extension does not check the value against a model's levels. |
| Reasoning from another provider in the history | Reported dropped, except a Messages thinking block whose signature has the shape of OpenAI encrypted reasoning, which goes back as a reasoning item. |

### Compaction

`POST /v1/responses/compact` is served for API keys and Grok logins. It goes to `{base_url}/responses/compact`;
when the base URL is the Grok CLI chat endpoint (`https://cli-chat-proxy.grok.com/v1`, the default for a
Grok login), which has no compaction endpoint, the official API address is used instead, with the login's
token and the ordinary API headers (no CLI identity). The usual body changes apply, then `stream`, `tools`,
`tool_choice`, `max_output_tokens`, `temperature`, `top_p`, `top_k` and `stop` are removed,
`compaction_trigger` input items are removed, and a non-empty `previous_response_id` is kept. `store` and
`include` are left to the client. The JSON reply comes back whole with its usage recorded. A streamed
compaction request is refused with a 400 error. A 404 from the compaction path fails that request only and
holds nothing back; a 404 that names a missing model holds back that model.

### Errors

`401`, and a `403` whose body says the credential is bad, are authentication errors; other `403` replies
are permission errors; both let core try another account. A `429` that says free usage is used up is a
`quota` error that holds the model back on the account for 24 hours; any other `429` is a rate limit on the
model and keeps xAI's `Retry-After`. A `404` is a model error (core tries another account only when the
message names the model). `503` and `529` are unavailable (with `Retry-After`), any other 5xx is an
upstream error, and a redirect is an upstream error that is not followed. The message is `error.message`
(or a string `error`), else the status line; valid JSON bodies are kept as raw. xAI's rate-limit headers
are not forwarded.

### Not supported

Embeddings, the Images API, image and video generation, text to speech, token counting (core estimates it),
audio output, WebSocket sessions (each turn is a plain request), checking the reasoning effort against a
model's levels or setting a default effort, deriving a `prompt_cache_key` when the client sends none, and a
context-length error code (an overflowing prompt is a plain 400).

## Kimi

The Kimi extension (`pluxy/kimi`, folder `extensions/kimi`) serves Kimi Code, Moonshot AI's coding
subscription. For sign-in and account options see [providers.md](providers.md#kimi).

### Where a request goes

Each client format goes to the Kimi API that speaks it, and the client's body is sent as written, with
the changes below.

| Client format | Sent to | Body |
| --- | --- | --- |
| OpenAI Chat Completions | `{base_url}/v1/chat/completions` | as written |
| OpenAI Responses (HTTP and WebSocket) | `{base_url}/v1/responses` | as written |
| Anthropic Messages | `{base_url}/v1/messages?beta=true` | as written |
| Gemini GenerateContent | `{base_url}/v1/chat/completions` | rebuilt as Chat Completions |

A Responses WebSocket turn is sent like an HTTP request that carries the whole conversation, which core
keeps. A Chat, Responses or Messages client gets no report of anything Kimi's API does with its body:
the extension checks none of it. Only a Gemini client's request is rebuilt, so only a Gemini client has
fields reported as dropped.

**Headers.** Chat, Responses and Gemini requests carry Kimi's device headers: `X-Msh-Platform` (`pluxy`),
`X-Msh-Version`, `X-Msh-Device-Model` (operating system and architecture) and, for a login,
`X-Msh-Device-Id` (the ID made at login; a key has none). The device's host name, which Kimi's own CLI
sends, is left out. A Messages request carries the client's own Messages headers instead
(`anthropic-version`, else the default Messages version; `anthropic-beta`; the client's identification
headers such as `user-agent`, `x-app`, `x-claude-code-session-id` and the `x-stainless-*` set) and no
device headers.

### Changes to every body

| What | Chat | Responses | Messages |
| --- | --- | --- | --- |
| `model` | Kimi's name for the model (below) | same | same |
| `temperature` | when the body's `thinking.type` is `disabled` (after the effort step below, so effort `none` counts), only `0.6` is kept; otherwise only `1.0` is kept. Any other value is removed with no report | only `1.0` is kept (only `0.6` when the client's own body has `thinking.type` `disabled`); any other value is removed with no report | unchanged |
| Tool schemas | `tools[].function.parameters`: local `$ref` inlined from `$defs` or `definitions` (recursion stops after 16 levels), those two keys removed, `type: object` added when there is no `type` | the same on `tools[].parameters` | unchanged |
| Usage | `stream_options.include_usage: true` on every streamed request | none | none |
| Reasoning effort | `reasoning_effort` becomes `thinking` (below) | none | none |
| Assistant turns | empty assistant messages dropped; every assistant message with `tool_calls` and no `reasoning_content` gets one (below) | none | none |
| Stream | none | `stream` set to the client's value (a request that is not streamed gets `"stream": false`) | none |
| Tool outputs | none | the `function_call_output` items of a batch of consecutive `function_call` items are moved directly after the calls, and the items that sat between them move after the outputs; a batch with a missing output keeps its order | none |
| System prompt | none | none | Claude Code's billing block (a `system` text block that starts with `x-anthropic-billing-header:`) is removed, since Kimi would read it as prompt text; `system` is removed if nothing is left |
| Thinking | none | none | kept signed thinking is put back (see Thinking replay) |

A Gemini client's rebuilt Chat body gets every change in the Chat column, including the `temperature`
rule. Removing a `temperature` is never reported, for a Chat, Responses or Gemini client.

**Model names.** Spaces around the name are removed. The alias names in this paragraph are matched
without case, but the `[1m]` suffix and the `kimi-` prefix are removed only when written in lower case,
so `Kimi-K3` is sent as `Kimi-K3`. `kimi-k2.8`, `k2.8`, `kimi-k2.8-code`, `k2.8-code`,
`kimi-k2.8-preview`, `k2.8-preview`, `kimi-k2.7-code`, `k2.7-code`, `kimi-for-coding` and `for-coding`
become `kimi-for-coding`; `kimi-k2.7-code-highspeed`, `k2.7-code-highspeed`,
`kimi-for-coding-highspeed` and `for-coding-highspeed` become `kimi-for-coding-highspeed`. A `[1m]`
suffix is removed. Any other name loses a leading `kimi-` (`kimi-k3` is sent as `k3`).

### Reasoning and thinking

A thinking suffix on the model name, such as `(high)`, is taken off the name by core. For a Chat or
Gemini client the effort (from the suffix, `reasoning_effort`, or Gemini's `thinkingConfig`) is sent as
Kimi's `thinking` field and `reasoning_effort` is removed: `none` gives `{"type":"disabled"}`; anything
else gives `{"type":"enabled","effort":...}` where `minimal` and `low` send `low`, `xhigh` and `max`
send `max`, and every other value (`medium`, `high`, `auto`) sends `high`. A Chat client's
`reasoning_effort` is read as written: only the exact word `none` disables thinking, and any other word
(`off`, `disabled`, `None`) gives `enabled` with effort `high`. A body with no effort keeps whatever
`thinking` it has. The model `kimi-k2` takes no thinking settings: its `reasoning_effort` stays as
written. Whether the model is `kimi-k2` is decided on its name after the rules in Model names, so
`kimi-k2`, `k2` and `kimi-k2[1m]` all count; the match is case-sensitive, so `Kimi-K2` gets thinking
settings. For a Responses or Messages client the suffix only leaves the model name; the body's own
reasoning settings go as written.

For a Chat body, and for a Gemini client's rebuilt Chat body, an assistant message with `tool_calls` and
no `reasoning_content` gets one: the most recent `reasoning_content` earlier in the conversation that
the client sent itself (a value filled in by this rule is not reused), else the message's own text,
else `[reasoning unavailable]`. A Gemini client's thought parts are dropped by the rebuild, so there
its value is the message's own text, else `[reasoning unavailable]`.

**Thinking replay (Messages clients).** Kimi's Messages API wants an assistant tool-use turn back with
its signed thinking, and some Messages clients resend earlier turns without it. The extension keeps
each Messages reply that has a signed `thinking` block and a `tool_use` block with an ID. When a later
request resends an assistant turn that has a `tool_use` block, no `thinking` or `redacted_thinking`
block and otherwise exactly the content that was kept (compared without thinking blocks), the kept
content replaces that turn.
At most one turn is restored per request: the newest assistant turn that has a kept match.
- A kept turn belongs to one client key (the request's `caller`), one conversation (the
  `X-Claude-Code-Session-Id` header, else the session in `metadata.user_id`, written either as
  `..._session_<id>`, where the ID is lower-case hexadecimal digits and `-`, or as a JSON object with
  `session_id`) and one model family (`k3` and `k3-256k`
  are one family, `k3.1-pro` and `k3.1-pro-highspeed` another). A request with no client key or no
  session is sent as written.
- A kept turn expires an hour after its last use. At most 10,240 turns and 256 MiB (keys included) are
  kept, least recently used first out. A turn over 8 MiB (key included) or over 512 blocks is not kept.
- Kimi refusing a request whose thinking was put back (status 400 or 422, or an error event in the
  stream) drops that kept turn.
- Turns are kept in the extension's memory: core stopping the idle extension (after 10 minutes without
  requests) or restarting it loses them.
- Chat clients are not covered; their tool calls get `reasoning_content` as above.

### Gemini clients (rebuilt as Chat Completions)

The Chat encoder shared with the OpenAI-compatible extension builds the body.
- Carried: system instruction, messages, function declarations as function tools, `toolConfig`
  (`AUTO`, `NONE`, `ANY` as `auto`, `none`, `required`; one allowed name as a forced function; several
  names or `VALIDATED` as `allowed_tools`), `temperature` (then removed with no report unless it passes
  the `temperature` rule above), `topP`, `maxOutputTokens` as `max_tokens`,
  `stopSequences`, `responseMimeType` and schema as `response_format`, thinking settings as an
  effort, inline and URL images as `image_url` parts, inline audio as `input_audio`, other inline files
  as `file` parts.
- Function call `thoughtSignature` goes on the tool call as `extra_content.google.thought_signature`.
- Tool names Chat does not take (characters outside `a-zA-Z0-9_-`, or over 64 bytes) are sanitized:
  each other character becomes `_`, the name is cut to 64 bytes, and a name that would collide gets `_`
  and 10 hexadecimal characters from a hash of the original name, after the name is cut to 53 bytes. The reply's tool calls go back to the client under its own name. A body sent
  as written keeps its names.
- Reported as dropped: `serviceTier`; `candidateCount` above 1; the `googleSearch`, `codeExecution` and
  any other native tool; a `thought` part, executable code and other Gemini-only parts of the history;
  a function response's error flag; a file given by `fileData` URI that is not an image; an inline
  image or file Chat cannot take; `safetySettings` and any other top-level request field the encoder
  has no place for, each under its own name; and whatever is left in `generationConfig` (`topK`,
  `presencePenalty`, `seed`, `includeThoughts`, a thinking budget) as the single path
  `generationConfig`. A `thinkingLevel` is not reported; a thinking budget is turned into an effort, and
  the budget itself is still reported.
- A tool result's images and files go in a user message after the tool messages (reported as
  approximated).
- A user turn left with nothing to send after drops is refused with a 400 error, whatever the drop
  policy.

### Token counts

`POST /v1/messages/count_tokens` and Gemini's `countTokens` go to
`{base_url}/v1/messages/count_tokens?beta=true`, for every account. A Messages client's own count body
goes with Kimi's model name, no billing block and its Messages headers. A Gemini request is rebuilt as
Messages with the fields a count takes. The `anthropic-beta` header gets `token-counting-2024-11-01`
added. A Messages client gets Kimi's count body as it is; for a Gemini client core builds Gemini's reply
from the number. An error from Kimi reaches the client and core does not estimate in its place, except
when an earlier account tried for the same request answered that it cannot count. Under `drop_policy: "reject"` a count is refused when generating the same
request would be, which for a Gemini client means whatever the Chat rebuild drops.

### Errors

Kimi's error body supplies the message (`error.message`, else `upstream returned <status>`), the
provider code (`error.type`, else `error.code`) and, when it is valid JSON, the raw body. Core removes
whole copies and pieces of the account's secrets from the message and raw body.

| Kimi answers | Error |
| --- | --- |
| 401 | `auth`, account (retry another account) |
| 403 | `permission`, account |
| 404 | `not_found`, model; retryable only when the message names the model |
| 429 | `rate_limit`, only the model is held back (the account, for a call with no model); `quota` instead when the provider code holds lower-case `quota` or the message holds "quota" in any case; `Retry-After` is read |
| 503, 529 | `unavailable`, endpoint; `Retry-After` is read |
| other 5xx | `upstream`, endpoint |
| 3xx | `upstream`, 502: a redirect to another host is not followed |
| other 4xx | `invalid_request`, request |

A reply Kimi sends that the decoder has to leave out (for example a Chat tool call with no name, or a
Responses image generation call with no result) is dropped and written to the extension's log as
`reply loss`; it is not in the client's reply.

### Not supported

- Embeddings, compaction (Responses `compact`) and the Images API: the extension has no endpoint
  handler, so core skips its accounts for those calls.
- Upstream rate-limit headers are not forwarded.

## Muse (Meta)

The Meta extension (`pluxy/meta`, folder `extensions/meta`) serves Meta's Muse models on `api.meta.ai`.
For sign-in and account options see [providers.md](providers.md#muse-meta).

### Where a request goes

Every client format goes to Meta's Responses API at `{base_url}/responses`.

| Client format | Body |
| --- | --- |
| OpenAI Responses (HTTP and WebSocket) | as written, with the changes below |
| OpenAI Chat Completions, Anthropic Messages, Gemini | rebuilt as a Responses body, with the changes below |

A Responses WebSocket turn is sent like an HTTP request that carries the whole conversation, which
core keeps.

### Changes to every body

- `stream` is always `true`: the extension always streams, whether or not the client did.
- `generate`, `prompt_cache_retention`, `safety_identifier`, `stream_options` and `client_metadata`
  are removed.
- `store` is `false` unless the body sets it.
- `reasoning.encrypted_content` is added to `include`.
- A `web_search` tool (also inside a namespace) loses `search_content_types`.
- A body with at least one tool gets Muse Code's own `muse` tool namespace, unless it already carries a
  namespace named `muse`.
- Requests carry Muse Code's client identity: its `User-Agent`, `X-Client-Id`, and a new session ID
  (`X-Tbh-Session-Id` and related headers) for every request.

### Responses clients (as written)

Nothing is checked or reported: service tier, verbosity, custom and namespace tools, hosted tools such
as `web_search` and `code_interpreter`, and reasoning items go as the client wrote them, and Meta's API
accepts or refuses them. A thinking suffix on the model name only leaves the model name; a Responses
body keeps its own `reasoning`.

### Chat, Messages and Gemini clients (rebuilt)

The Responses encoder shared with the Codex extension builds the body.
- Carried: system and developer messages as messages with their role, text, images as `input_image`
  (with `detail`, `auto` when none), files as `input_file`, inline audio as `input_audio`, function
  tools with `strict`, function calls and their outputs, `tool_choice`, `parallel_tool_calls`,
  `temperature`, `top_p`, max tokens as `max_output_tokens`, the response format as `text.format`, the
  effort as `reasoning.effort` as the client named it.
- Tool choice: `auto`, `none`, `required`, a forced function, or an allowed list as
  `allowed_tools`.
- Reported as dropped: service tier and verbosity (always, whatever the value); `stop`; a
  candidate count above 1; web search and code execution tools; tools of other types (the Chat and
  Messages hosted tools); an `is_error` flag on a tool result; reasoning from another provider (a
  Messages thinking block whose signature is a Responses encrypted reasoning goes back as that
  reasoning item, summary text not sent); items Responses has no place for (a Gemini thought part);
  every request field kept beside the model (such as `top_k`, `seed`, penalties, `metadata`,
  `cache_control`).
- Tool arguments that are not a JSON object are sent as `{}` (reported as approximated). A Messages
  plain-text document is carried as text (approximated).
- A user turn left with nothing to send after drops is refused with a 400 error.

### Replies

Meta's `response.subscription_usage` events, which are not part of the Responses stream, are ignored.
Reasoning's `encrypted_content` reaches the client as the reasoning's opaque data (a Responses client's
`encrypted_content`, a Messages client's thinking `signature`).

### Errors

Meta's error body is an OpenAI-style error object or a string error with a top-level `code`. The
message is `error.message`, else `upstream returned <status>`; valid JSON is kept as raw; core removes
the account's secrets.

| Meta answers | Error |
| --- | --- |
| 401, 403 | `auth` or `permission`, account |
| 404 | `not_found`, model; the model is held back until `resets_at` when that time is in the future, else five minutes; retryable only when the message names the model |
| 429 | `rate_limit`, model; the wait comes from `Retry-After`, and from `resets_at` instead when that time is in the future; `quota` for the whole account instead, when either (1) the message, in any case, contains "subscription quota" or "quota exhausted", or (2) the code, in any case, is `rate_limit_exceeded` or contains "quota", and `error.resets_at` is greater than 0 (even a time already past). The code is `error.code`, else the top-level `code`; `error.type` is not used |
| 503, 529 | `unavailable`, endpoint |
| other 5xx | `upstream`, endpoint |
| 3xx | `upstream`, 502 |
| other 4xx | `invalid_request`, request |

An `error` event or a `response.failed` event in the stream is classified by the status it names
(`error.code` as a number or a string; 502 when none). Once the reply began, it is not retry-safe.

### Not supported

- Compaction, embeddings and the Images API: no endpoint handler; core skips the account for those
  calls.
- Tool schema and tool name rewriting.
- Meta does not add `instructions`, a reasoning effort, `tool_choice`, `parallel_tool_calls` or
  `strict` that the client did not send. It does not rewrite `apply_patch` tools or calls. It does not
  remove encrypted reasoning from the input, whoever issued it.
- Upstream rate-limit headers are not forwarded.

## Devin

The Devin extension (`pluxy/devin`, folder `extensions/devin`) serves the models a Devin account gets
through Devin's API server, as Devin's CLI talks to it. For sign-in and account options see
[providers.md](providers.md#devin).

Account settings: `base_url` changes where requests go (below). `device_seed` is the only one that
changes what a request carries: it makes the device fingerprint the same every time; without it the
fingerprint is random per request.

### Where a request goes

Every client format is rebuilt as Devin's chat request, a protobuf message sent over Connect to
`{base_url}/exa.api_server_pb.ApiServerService/GetChatMessage`. Each request is streamed and starts a
new session (a new session ID); a Responses WebSocket turn is sent the same way with the whole
conversation. Headers are the CLI's: `Authorization: Basic <token>-<token>`, `Connect-Protocol-Version:
1`, a Sentry trace, no `User-Agent`, and no compression. The body holds the CLI's client metadata with
the token and a device fingerprint, the system prompt, the turns, the completion settings, the function
tools and the model ID.

### What is carried

- System and developer messages: joined by a line break into the system prompt.
- Turns: user, assistant (text, reasoning with its signature, tool calls) and tool results. Several
  text parts of a message are joined by a line break. A tool result for a call ID the conversation
  has not seen is sent as the user's text.
- Images: only base64 `data:` URLs in user turns (the scheme and `;base64` match without case; no media
  type means `image/png`). Each is sent as base64 data and named in the turn's text as Devin's CLI
  names pasted images (`[Image 1: pasted_image_1.png]`, `.jpg`, `.webp` or `.gif` by media type, put
  first unless the text already has `[Image `).
- Tool results: text only. An image or other part and the `is_error` flag are reported as dropped. A
  result the client gave as blocks keeps its text blocks, joined by a line break.
- Completion settings: max tokens (the request's value when it is above 0 and below 64000, else 64000;
  a larger value is lowered to 64000 with no report) and
  `temperature` (1 when the request has none). Other settings are Devin's fixed values.
- Function tools with name, description and JSON Schema as written.
- Reasoning replay: a replayed reasoning item is sent as thinking text and, when the client kept it, its
  signature, as given by whoever issued it: a Messages thinking block's `signature` or a Responses
  reasoning item's `encrypted_content`. A signature that starts with `sealed.v1.` is also marked type
  `sealed`; no other type is sent. A Chat client's reasoning text has no signature. A Gemini client's
  thought part cannot be replayed (see below).

### Reported as dropped

Refused under `drop_policy: "reject"`:
- every client field kept beside the model, at its path: for example a Chat message's `name` or `seed`,
  Responses `store` and `metadata`, Messages `cache_control` and `top_k`, Gemini `safetySettings`;
- `top_p` and `stop` (Devin sends a fixed top-p and takes no stop sequences);
- `tool_choice` other than plain `auto`, and an `auto` choice narrowed to an allowed list;
- `response_format` other than text;
- `parallel_tool_calls` set to `false`, and a candidate count above 1;
- tools that are not function tools (web search, code execution, namespace and custom tools such as
  `apply_patch`, any other hosted tool), and `strict: true` on a function tool;
- image parts that are not base64 data in a user turn, and every other part type (files, audio);
- a tool result's non-text parts and its error flag;
- items with no place: a Gemini thought part, executable code, and a function call's `thoughtSignature`.

Service tier and verbosity are neither sent nor reported, with one caveat: the format parser reads only
some values into them (a string `service_tier` or `verbosity` from a Chat client; a string
`service_tier` or `text.verbosity` from a Responses client; from a Messages client `speed: "fast"`, else
`service_tier` `auto` or `standard_only`; from a Gemini client `serviceTier` `standard`, `flex`,
`priority` or `unspecified`). Any other value stays among the request's kept fields and is reported
dropped like any of them, under its own name.

A request `max_tokens` of 64000 or more, or of 0 or less, is sent as 64000, with no report.

### Refused

A run of user turns (between assistant turns) in which every turn lost all its content to dropped media
is refused with a 400 error, whatever the drop policy: "Devin cannot take the <type> content of a user
turn, and the turn has nothing else". When another turn in the run has content, only the emptied turn
is removed.

### Model IDs

Devin names a model and its reasoning effort as one model ID (`claude-opus-5-5-medium`). A client names
the model as `devin/claude-opus-5-5` or `claude-opus-5-5` (the `devin/` prefix is removed without case;
an empty name is `swe-2`). The extension adds the effort:
- A name that already ends in an effort (`-none`, `-minimal`, `-low`, `-medium`, `-high`, `-xhigh`,
  `-max`, or `_` before it, optionally followed by `-fast`, `-priority`, `-lightning` or `-1m`) or in
  `-fast`, `-slow`, `-priority`, `-thinking` or `-thinking-1m` is sent as given.
- The effort is the request's reasoning effort (`off` and `disabled` mean `none`; `auto` and `adaptive`
  mean `high`; a thinking suffix or a Messages or Gemini thinking budget arrives as an effort first), or
  the model's default.
- A model in the extension's table (the efforts of Devin's catalog) gets the nearest effort it has (the
  higher on a tie). `none` on a model without `none`, and any word that is not an effort, give the
  model's default. A model with no efforts is sent as named. A lightning model keeps the effort before
  the suffix (`swe-2-high-lightning`).
- The default is `high` for SWE-2 models and for Gemini, Grok, GLM, DeepSeek, Kimi and Nemotron
  models that have `high`; `low` for a GPT-5 model that has `none` and `low`; otherwise `medium`,
  `high` or `low` in that order, else the model's first effort.
- A model not in the table is sent as named, with `-<effort>` when the request has an effort. A word
  such as `high-fast` is not an effort, so it is added as it is (`-high-fast`) to such a model, and
  for a model in the table gives the model's default.
- Variants: SWE-1.7 is sent bare unless the effort is `medium` (`swe-1-7-medium`); SWE-1.6 is bare
  unless the effort is `fast` (`swe-1-6-fast`); GLM-5.2 is bare but for `none` and `max`
  (`glm-5-2-max`, `glm-5-2-max-1m` for the long-context model); Claude Opus 4.6 and Sonnet 4.6 are
  `-thinking` (`-thinking-1m` for the 1M models) with any effort but `none`, else bare.

### Replies

The reply's frames become events as they arrive:
- Reasoning stays open until the reply ends or new reasoning follows other output, so Devin's
  signature, which can come after the answer began, reaches the reply as the reasoning's opaque
  data. A signature with no reasoning text gets a reasoning item of its own.
- Each tool call is found by its ID, so a call can continue after another one started. A call starts
  once its name is known; a call that never gets a name ends the reply with an `upstream` error. A
  piece with no ID continues the last call, or starts one with a made-up ID.
- Protobuf field 4 of a tool-call piece holds the arguments Devin could not parse as JSON. They are
  passed on as text, as the call's arguments, when the call has no ordinary arguments (field 3).
- A character split across frames is passed on whole.
- The finish reason is `length` for Devin's stop reasons 1 and 3, `content_filter` for 11, `tool_calls`
  when there are calls, else `stop`.
- Usage: input tokens include cache reads and writes; the cache read and write counts are reported too.
  A reply with no usage message has none. Usage comes only from Devin's usage message.
- A data frame whose message cannot be read is skipped. A frame with invalid flags, a frame over 16 MiB,
  or a compressed frame that does not decompress or inflates past 64 MiB ends the reply with an
  `upstream` error, as does a reply that ends without its end frame. An end frame that is not JSON with
  an error is a clean end.
- The signature reaches clients as follows: a Responses client gets it as `encrypted_content`, a Messages
  client as the thinking block's `signature` and a streamed Gemini reply as a thought part's
  `thoughtSignature` (an unstreamed Gemini reply drops it and reports the loss). A Chat client gets
  none. A Gemini client cannot replay it, because its thought part is dropped when it comes back.

### Errors

An error in Devin's end frame is classified by its Connect code; an HTTP error refusal by its status.

| Connect code | Status |
| --- | --- |
| `invalid_argument` | 400; 502 when the message holds "internal error" |
| `unauthenticated` | 401 |
| `permission_denied` | 403; 429 when the message holds "high demand" |
| `resource_exhausted` | 429 |
| `failed_precondition` | 429 when the message holds "quota", "credit", "acu", "exhausted" or "limit"; else 400 |
| `not_found` | 404 |
| `unavailable` | 503 |
| `deadline_exceeded` | 504 |
| `canceled` | 499, `cancelled` |
| any other | 502 |

Status to error: 401 `auth` and 403 `permission` (account, retry another account); 404 `not_found`
(model); 429 `rate_limit`, which holds back only that model, except `quota` for the account when the
cleaned message, in lower case, contains "quota" and "exhausted" and either "daily" or "weekly" (parts
of words count; "monthly ACU quota exhausted" and "model quota exhausted" stay `rate_limit` on the
model; the wait comes from `Retry-After` only, no reset time is read, and an end-frame error has no
`Retry-After`; an empty or unreadable body gives the message `Devin answered 429 Too Many Requests`,
which never matches); 503 `unavailable`; other 5xx `upstream`; 3xx 502 (not followed); 499 `cancelled`
(request); other statuses `invalid_request`. An error after Devin sent any data frame is not
retry-safe.

**Error text.** A refusal with an HTTP error status takes its message from a Connect error body's
`message`; an end-frame error takes the frame's. The message and, when it is not one of Connect's
codes, the code are cleaned; the error's raw body is rebuilt from the cleaned code and message only, so
the body's other fields are dropped. Any other body becomes the message only, cleaned, when the
extension read it whole (under 1 MiB and not broken off); a body that broke off, or fills the 1 MiB
limit, gives only the status (`Devin answered 500 Internal Server Error`). Cleaning:
- the text becomes one line, cut after 1000 characters with `…`;
- every run of token characters (letters, digits, `_-.+/=~`) that holds 4 or more consecutive
  characters of one of the account's secrets (8 or more for a long token) is replaced by
  `[redacted]`: the session token after the public `devin-session-token$` prefix, secret header
  values, the proxy's user name and password, and secrets in the settings. A run is also read with
  its escapes decoded (JSON `\/` and `\uXXXX`, `%XX`);
- `Bearer` and `Basic` values, and the values of fields named like `access_token`, `refresh_token`,
  `id_token`, `session_token`, `token`, `device_code`, `authorization_code`, `user_code`,
  `code_verifier`, `client_secret`, `api_key`, `password` or `secret`, are replaced;
- any other run of 24 or more token characters is replaced.

Core removes whole copies of the account's secrets, and pieces of them by the same rule, from every
extension's errors, but not the other rules or the bound, so the extension keeps its own cleaning. The
piece rule can also replace a word that shares 4 characters (8 for a long token) with a secret, such as
`proxy` for a proxy user name `proxy-user-1`.

### Not supported

- Removing Claude Code's and Codex's attribution lines from the system prompt, and `devin.sensitive-words`:
  the system prompt is sent as the client wrote it.
- Rewriting tool descriptions, and encoding tool call IDs: both are sent as given.
- A session's turn count: every request starts a new session.
- Devin's names for older models (`claude-haiku-4-5`, `gpt-4-1`, Sonnet 4.5, Claude 4.5 Opus), and the
  `-fast` variants built from a `<effort>-fast` request: these names are not renamed. A name that is not
  in the table is sent as named, with `-<effort>` added when the request has an effort.
- Images in tool results (reported dropped); signature types other than `sealed`.
- Namespace and custom (freeform) tools such as `apply_patch` (reported dropped), and checks or
  recovery of a malformed `apply_patch` call.
- Embeddings, compaction and the Images API: no endpoint handler; core skips the account for those
  calls.
- Upstream rate-limit headers are not forwarded.

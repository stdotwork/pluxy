# Point a coding tool at pluxy

This page shows how to make a coding tool send its requests through pluxy. A client is any tool
that does: a coding agent such as Claude Code or Codex, or anything else that speaks the OpenAI,
Anthropic or Gemini API.

You need a running pluxy with at least one account ([setup](setup.md)).

## Set up a tool

1. **Get a client key.** A client key is the secret a tool sends to prove it may use pluxy. The
   first one is in `client_keys` in pluxy's `config.json`. `pluxy key add` makes another and
   prints it once. `pluxy key list` shows the keys masked, with their IDs.
2. **Print the settings.** Run `pluxy client show <tool>`, with `<tool>` one of `claude-code`,
   `codex`, `opencode`, `pi` or `omp`. It prints each file to change and what goes in it, with
   core's address and a starting model already filled in. It needs core to be running.
   - A coding tool asks for a model of its own unless told which one, and your accounts may not
     serve that one, so every tool's output names the model it starts with. When pluxy serves
     models, `pluxy client show <tool>` fills in the best match for the tool (a Claude model for
     Claude Code, a `codex` or `gpt-` one for Codex, else the first chat model); when pluxy serves
     no model yet, it puts `<model-id>` in its place.
   - `pluxy client show <tool> --model <name>` names another model from `pluxy model list`.
   - `pluxy client show <tool> --key-id <id>` names the key with that ID in the output, still
     masked: pluxy never prints a stored key.
3. **Paste them.** In the printed text, replace `<client-key>` with the key from step 1 (and
   `<model-id>`, if core served no model yet, with a name from `pluxy model list`). If the file
   already exists, add these keys to it; do not replace the file. In Codex's `config.toml`, keep
   `model_provider` and `model` above the first `[table]` of the file: TOML puts a key written
   below a table line inside that table. `pluxy client show` only prints: it changes no file.
4. **Start the tool again** so it reads the new settings.

The panel's **Tools** page shows the same settings under each tool's **Set up**, as numbered steps
with a **Copy** button and a model picker when core serves models. Its **Make key** button makes a
new client key and puts it into the text, so you do not paste it yourself. The panel's guided setup
(the **Set up pluxy** page, called **Setup** once first-run setup is done) shows the same in its
second step, "Your coding tool". The desktop app (`app.md`) can also write a tool's files itself.

Claude Desktop, Grok Build and Hermes Agent are set up from the desktop app only; `pluxy client
show` does not know them.

An unknown tool name fails with `pluxy: unknown client tool "<name>" (supported: claude-code,
codex, opencode, pi, omp)` and exit code 2. The command also takes these names for a tool: `claude`
for Claude Code, `codex-cli` for Codex, and `oh-my-pi` for Oh My Pi.

The examples below show core on its default address, `http://127.0.0.1:8317`, serving demo models
(`claude-demo-haiku`, `gpt-demo-large`, `alpha-model`). Your output has the address your core
listens on and the model chosen from your accounts.

## What each tool gets

### Claude Code

`pluxy client show claude-code` sets `model` and the three `ANTHROPIC_DEFAULT_*_MODEL` variables so
Claude Code's Opus, Sonnet, Haiku and background choices resolve to a model pluxy serves:

```text
Client: Claude Code
Address: http://127.0.0.1:8317

~/.claude/settings.json:
{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "<client-key>",
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-demo-haiku",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-demo-haiku",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-demo-haiku",
    "CLAUDE_CODE_AUTO_MODE_SERVER": "0",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  },
  "model": "claude-demo-haiku"
}
```

### Codex CLI

`pluxy client show codex` prints:

```text
Client: Codex CLI
Address: http://127.0.0.1:8317

~/.codex/config.toml:
model_provider = "pluxy"
model = "gpt-demo-large"

[model_providers.pluxy]
name = "pluxy"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"
experimental_bearer_token = "<client-key>"

If config.toml already has tables (lines such as [name]), put its first two lines, model_provider and model, above the first table: a key written below a table line belongs to that table, not to the file. The [model_providers.pluxy] block can go after them.
```

### OpenCode

`pluxy client show opencode` prints:

```text
Client: OpenCode
Address: http://127.0.0.1:8317

~/.config/opencode/opencode.jsonc:
{
  "$schema": "https://opencode.ai/config.json",
  "model": "pluxy/alpha-model",
  "provider": {
    "pluxy": {
      "models": {
        "alpha-model": {
          "name": "alpha-model"
        }
      },
      "name": "pluxy",
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "apiKey": "<client-key>",
        "baseURL": "http://127.0.0.1:8317/v1"
      }
    }
  }
}
```

### Pi

`pluxy client show pi` prints:

```text
Client: Pi
Address: http://127.0.0.1:8317

~/.pi/agent/cliproxyapi.json:
{
  "apiKey": "<client-key>",
  "baseUrl": "http://127.0.0.1:8317"
}

~/.pi/agent/settings.json:
{
  "defaultModel": "alpha-model",
  "defaultProvider": "cliproxyapi",
  "packages": [
    "./pluxy-cliproxyapi-provider"
  ]
}

Pi also needs pluxy's adapter in ~/.pi/agent/pluxy-cliproxyapi-provider, which the desktop app installs.
```

### Oh My Pi

`pluxy client show omp` prints:

```text
Client: Oh My Pi
Address: http://127.0.0.1:8317

~/.omp/agent/models.yml:
providers:
  pluxy:
    baseUrl: "http://127.0.0.1:8317/v1"
    apiKey: "<client-key>"
    api: openai-responses
    discovery:
      type: openai-models-list

~/.omp/agent/config.yml:
modelRoles:
  default: "pluxy/alpha-model"
```

## Any other client

A client that lets you set a base URL and an API key needs no entry above. Give it one of pluxy's
client keys as the API key, and the base URL of the API it speaks:

| The client speaks | Base URL | Main paths |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:8317/v1` | `/v1/chat/completions`, `/v1/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/images/generations`, `/v1/images/edits`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/translations` |
| Anthropic | `http://127.0.0.1:8317` | `/v1/messages`, `/v1/messages/count_tokens` |
| Gemini | `http://127.0.0.1:8317` | `/v1beta/models/<model>:generateContent`, `:streamGenerateContent`, `:countTokens`, `:embedContent`, `:batchEmbedContents` |

Every route reads the client key from `Authorization: Bearer <client-key>` or from `x-api-key:
<client-key>`. Gemini's routes also read it from `x-goog-api-key: <client-key>` or from the `key`
query parameter.

`GET /v1/models` lists the models pluxy serves now (every client key sees the same list), in OpenAI's shape, or in Anthropic's
for a client that sends an `anthropic-version` header or whose user agent starts with `claude-cli`. `GET /v1beta/models` lists them in Gemini's shape.

### Other routes

| Route | What it is |
| --- | --- |
| `/backend-api/codex/responses` and `/responses` | The same route as `/v1/responses`, for `POST` and for the WebSocket `GET`: the same handlers and the same client-key check. The Codex CLI uses the first when its `chatgpt_base_url` points at pluxy; some clients post to the bare `/responses`. |
| `GET /healthz` | Answers 200 with the text `ok`, for a container or load balancer. It takes no client key and writes no request log line. `HEAD` gets the same answer and any other method a 405. With `tls.enable` it is served over HTTPS like the rest. |
| `GET /api/hello`, `POST /api/event_logging/batch` | Claude Code probes its configured Anthropic address with these before it sends a message. Core answers `{"status":"ok"}` itself, with no client key and no account, and drops the event batch. |

**Responses WebSocket.** `GET /v1/responses` (or either alias above) upgrades to a WebSocket that
speaks OpenAI's Responses WebSocket mode. The client sends a `response.create` message for each turn,
and gets the turn's stream events back as text messages. When the account's extension holds
sessions, core keeps one upstream session for the socket, so a turn can continue the previous one
with `previous_response_id`; the Codex extension does this when the account's `websockets` setting
is on ([Codex](providers.md#codex-chatgpt)). On any other account each turn is a separate request
that carries the whole conversation, which core keeps for the socket. `response.append`,
`response.interrupt` and a warmup (`generate: false`) are served too.

**Keep-alive.** Once a streamed reply has started, core writes the SSE comment `: keep-alive` after
every 15 seconds without a byte, so a client or proxy does not drop a model that thinks silently. A
comment line starts with a colon, and readers of server-sent events skip it. It goes on
`/v1/chat/completions`, `/v1/completions`, `/v1/responses` and its aliases, `/v1/messages`, and on
the streamed replies of the other routes that stream (image generation, for example). There is none on the Gemini routes (Google's
clients read every line as JSON) or on `/v1beta/interactions`, nor on a reply that is not streamed
or on a WebSocket.

## Check that it works

```bash
curl http://127.0.0.1:8317/v1/models -H "Authorization: Bearer <client-key>"
```

A JSON list means the key and the address are right. With no accounts the list is empty. For an
error from pluxy, see [Troubleshooting](troubleshooting.md#a-client-gets-an-error).

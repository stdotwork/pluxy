# Point a coding tool at pluxy

This page shows how to make a coding tool send its requests through pluxy. A client is any tool
that does: a coding agent such as Claude Code or Codex, or anything else that speaks the OpenAI,
Anthropic or Gemini API.

You need a running pluxy with at least one account ([setup](setup.md)).

## Set up a tool

1. **Get a client key.** A client key is the secret a tool sends to prove it may use pluxy. The
   first one is in `client_keys` in pluxy's `config.json`. `pluxy key add` makes another and
   prints it once. `pluxy key list` shows the keys masked, with their IDs.
2. **Get a model name.** Run `pluxy model list` and pick a name from its `MODEL` column. The
   settings for OpenCode, Pi and Oh My Pi have a `<model-id>` to fill in; those for Claude Code and
   Codex do not.
3. **Print the settings.** Run `pluxy client show <tool>`, with `<tool>` one of `claude-code`,
   `codex`, `opencode`, `pi` or `omp`. It prints each file to change and what goes in it, with
   core's address already filled in. It needs core to be running. `pluxy client show <tool>
   --key-id <id>` names the key with that ID in the output, still masked: pluxy never prints a
   stored key.
4. **Paste them.** In the printed text, replace `<client-key>` with the key from step 1 and
   `<model-id>` with the name from step 2. If the file already exists, add these keys to it; do
   not replace the file. `pluxy client show` only prints: it changes no file.
5. **Start the tool again** so it reads the new settings.

The panel's **Tools** page shows the same settings under each tool's **Set up**, as numbered steps
with a **Copy** button. Its **Make key** button makes a new client key and puts it into the text, so
you do not paste it yourself. The panel's guided setup (the **Set up pluxy** page, called **Setup**
once first-run setup is done) shows the same in its second step, "Your coding tool". The
desktop app (`app.md`) can also write a tool's files itself.

Claude Desktop, Grok Build and Hermes Agent are set up from the desktop app only; `pluxy client
show` does not know them.

An unknown tool name fails with `pluxy: unknown client tool "<name>" (supported: claude-code,
codex, opencode, pi, omp)` and exit code 2. The command also takes these names for a tool: `claude`
for Claude Code, `codex-cli` for Codex, and `oh-my-pi` for Oh My Pi.

The examples below show core on its default address, `http://127.0.0.1:8317`. Your output has the
address your core listens on.

## What each tool gets

### Claude Code

`pluxy client show claude-code` prints:

```text
Client: Claude Code
Address: http://127.0.0.1:8317

~/.claude/settings.json:
{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "<client-key>",
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "CLAUDE_CODE_AUTO_MODE_SERVER": "0",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  }
}
```

### Codex CLI

`pluxy client show codex` prints:

```text
Client: Codex CLI
Address: http://127.0.0.1:8317

~/.codex/config.toml:
model_provider = "pluxy"

[model_providers.pluxy]
name = "pluxy"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"
experimental_bearer_token = "<client-key>"
```

### OpenCode

`pluxy client show opencode` prints:

```text
Client: OpenCode
Address: http://127.0.0.1:8317

~/.config/opencode/opencode.jsonc:
{
  "$schema": "https://opencode.ai/config.json",
  "model": "pluxy/<model-id>",
  "provider": {
    "pluxy": {
      "models": {
        "<model-id>": {
          "name": "<model-id>"
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
  "defaultModel": "<model-id>",
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
  default: "pluxy/<model-id>"
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

## Check that it works

```bash
curl http://127.0.0.1:8317/v1/models -H "Authorization: Bearer <client-key>"
```

A JSON list means the key and the address are right. With no accounts the list is empty. For an
error from pluxy, see [Troubleshooting](troubleshooting.md#a-client-gets-an-error).

# Point a client at pluxy

A client is the tool that sends requests through pluxy: a coding agent such as Claude Code or
Codex, or anything else that speaks the OpenAI, Anthropic or Gemini API. This page lists what to
put in each client's config. It assumes pluxy is running and has an account
([quick start](../README.md#quick-start)).

pluxy never edits a client's files. `pluxy client show <tool>` prints the settings, and you paste
them. The panel's **Set up pluxy** page shows the same text under **Point a client at pluxy**.

Every block below has two placeholders:

- `<client-key>` is one of pluxy's client keys. The first one is in `client_keys` in pluxy's
  `config.json`; `pluxy key add` makes another and prints it once. `pluxy key list` shows the
  keys masked, with their IDs, and `pluxy client show <tool> --key-id <id>` names the key to use
  in the output (still masked: pluxy never prints a stored key).
- `<model-id>` is a model name from `pluxy model list`.

The address in the blocks, `http://127.0.0.1:8317`, is pluxy's default. Your output has the
address your core listens on.

The settings are shown as the whole file for a client with no config yet. If the file exists, add
these keys to it; do not replace it.

## Claude Code

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

## Codex CLI

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

## OpenCode

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

## Pi

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

## Oh My Pi

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

A client that lets you set a base URL and an API key works without an entry here. Give it one of
pluxy's client keys as the API key, and the base URL for the API it speaks:

| The client speaks | Base URL | pluxy reads the key from |
| --- | --- | --- |
| OpenAI Chat Completions or Responses | `http://127.0.0.1:8317/v1` | `Authorization: Bearer <client-key>` |
| Anthropic Messages | `http://127.0.0.1:8317` | `x-api-key: <client-key>`, or `Authorization: Bearer <client-key>` |
| Gemini | `http://127.0.0.1:8317` (requests go to `/v1beta/...`) | `x-goog-api-key: <client-key>`, or `?key=<client-key>` |

`GET /v1/models` (and `GET /v1beta/models` for Gemini clients) lists the models the key can ask
for.

## Keeping this page current

Each tool's block is the unedited output of `pluxy client show <tool>` on a core listening on
`127.0.0.1:8317`. When that output changes, run the command for each of `claude-code`, `codex`,
`opencode`, `pi` and `omp` and paste the result over the block. `pluxy client show` with an
unknown tool name lists the tools it knows.

# Point a client at pluxy

A client is the tool that sends requests through pluxy: a coding agent such as Claude Code or
Codex, or anything else that speaks the OpenAI, Anthropic or Gemini API. This page lists what to
put in each client's config. It assumes pluxy is running and has an account
([quick start](../README.md#quick-start)).

pluxy never edits a client's files. `pluxy client show <tool>` prints the settings, and you paste
them. The panel's **Tools** page, and step 2 of its guided setup, show the same text under each tool's
**Set up**.

Every block below has two placeholders:

- `<client-key>` is one of pluxy's client keys. The first one is in `client_keys` in pluxy's
  `config.json`; `pluxy key add` makes another and prints it once. `pluxy key list` shows the
  keys masked, with their IDs, and `pluxy client show <tool> --key-id <id>` names the key to use
  in the output (still masked: pluxy never prints a stored key).
- `<model-id>` is a model name from `pluxy model list`. A coding tool asks for a model of its own
  unless it is told which one, and your accounts may not serve that one, so each block names the
  model the tool starts with. When pluxy serves models, `pluxy client show <tool>` fills in the
  best match for the tool (a Claude model for Claude Code, a `codex` or `gpt-` one for Codex, else
  the first chat model) and `pluxy client show <tool> --model <name>` names another. The blocks
  below show `<model-id>` because they were printed on a pluxy with no account.

The address in the blocks, `http://127.0.0.1:8317`, is pluxy's default. Your output has the
address your core listens on.

The settings are shown as the whole file for a client with no config yet. If the file exists, add
these keys to it; do not replace it. In Codex's `config.toml`, keep `model_provider` and `model`
above the first `[table]` of the file: TOML puts a key written below a table line inside that
table.

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
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "<model-id>",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "<model-id>",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "<model-id>",
    "CLAUDE_CODE_AUTO_MODE_SERVER": "0",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  },
  "model": "<model-id>"
}
```

## Codex CLI

`pluxy client show codex` prints:

```text
Client: Codex CLI
Address: http://127.0.0.1:8317

~/.codex/config.toml:
model_provider = "pluxy"
model = "<model-id>"

[model_providers.pluxy]
name = "pluxy"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"
experimental_bearer_token = "<client-key>"

If config.toml already has tables (lines such as [name]), put its first two lines, model_provider and model, above the first table: a key written below a table line belongs to that table, not to the file. The [model_providers.pluxy] block can go after them.
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
`127.0.0.1:8317` that serves no model yet. When that output changes, run the command for each of `claude-code`, `codex`,
`opencode`, `pi` and `omp` and paste the result over the block. `pluxy client show` with an
unknown tool name lists the tools it knows.

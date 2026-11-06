# next: the new pluxy core

A new core for pluxy, built from scratch beside today's `core/`. Core receives client requests and
hands them to **provider extensions**: separate programs, one per kind of upstream, that core
starts and talks to over a local socket. Today it does one thing end to end: it serves OpenAI Chat
Completions (streamed and not) through the OpenAI-compatible API-key provider.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/pluxy` | The core program. |
| `protocol/` | The extension protocol: [README](protocol/README.md), [tutorial](protocol/TUTORIAL.md), wire types and framing. |
| `llm/` | The internal model of a request and its streamed reply, shared by core and extensions. |
| `sdk/` | Go SDK for writing extensions. |
| `conformance/` | The conformance kit extensions run in their tests. |
| `format/` | Client and upstream formats as libraries extensions build in: `sse`, `openaichat`. |
| `extensions/openai-compatible` | Provider extension for OpenAI-compatible APIs with an API key. |
| `examples/scripted` | The tutorial's example extension. |
| `internal/host`, `internal/server` | Core's extension host and HTTP API. |
| `e2e/` | Black-box tests that drive the built core over HTTP. |
| `corpus/` | Differential test corpus: cases replayed against this core and today's `core/`, with listed exceptions ([README](corpus/README.md)). |

## Run it

```bash
go build -o pluxy ./cmd/pluxy
go build -o extensions/openai-compatible/openai-compatible ./extensions/openai-compatible
```

`config.json`:

```json
{
  "listen": "127.0.0.1:8400",
  "client_keys": ["choose-a-client-key"],
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

An account may list `models` it serves; without a list it serves any model. Version 0 runs one
extension; requests go to the first account that serves the model, with no retries.

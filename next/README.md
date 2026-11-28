# next

`next/` holds pluxy: core (the proxy, a Go program), the provider extensions core starts, the
extension protocol and SDK, the UI kit with the web panel, and the desktop app. Core receives client
requests in the OpenAI, Anthropic, Gemini and Codex APIs and hands each to a **provider extension**:
a separate program, one per kind of upstream, that core starts and talks to over a local socket.

The Go code is one module, `github.com/stdotwork/pluxy/next`. To use pluxy, start with
[docs/setup.md](../docs/setup.md); [docs/README.md](../docs/README.md) lists every page.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/pluxy` | The core program, and the `pluxy` command that manages it ([CLI](../docs/cli.md)). |
| `cmd/pluxy-pack` | Builds each extension's release archives and the bundled registry index; packs the desktop app's native tools with `-app-tools` ([Pack extensions](../docs/extensions.md#pack-extensions)). |
| `cmd/pluxy-bench` | Measures the time and memory pluxy adds between a client and its upstream, against a mock upstream, and checks them against budgets in CI ([docs/design/perf.md](../docs/design/perf.md)). |
| `protocol/` | The extension protocol: [README](protocol/README.md), [tutorial](protocol/TUTORIAL.md), wire types and framing. |
| `llm/` | The internal model of a request and its streamed reply, shared by core and extensions. |
| `sdk/` | Go SDK for writing extensions. |
| `conformance/` | The conformance kit extensions run in their tests. |
| `format/` | Client and upstream formats as libraries extensions build in: `sse`, `openaichat`, `openairesponses`, `anthropicmessages`, `geminigenerate`. |
| `extensions/openai-compatible` | Provider extension for OpenAI-compatible APIs with an API key. |
| `extensions/codex` | Provider extension for OpenAI's Codex models: an OpenAI API key, a ChatGPT subscription or a Sign in with ChatGPT (SIWC) sign-in; `codextest` is a fake OpenAI login server and Codex endpoint for its tests. |
| `extensions/claude` | Provider extension for Anthropic's Claude, with an API key or a Claude subscription sign-in; `claudetest` is a fake Anthropic for its tests. |
| `extensions/gemini` | Provider extension for Google's Gemini: the Gemini API with an API key, and Vertex AI with a service account or an express mode key; `geminitest` is a fake Google for its tests. |
| `extensions/kimi` | Provider extension for Kimi Code, Moonshot AI's coding subscription, signed in with a device code; `kimitest` is a fake Kimi for its tests. |
| `extensions/xai` | Provider extension for xAI's Grok models: an API key, or a Grok account signed in as the Grok CLI signs in; `xaitest` is a fake xAI for its tests. |
| `extensions/meta` | Provider extension for Meta's Muse models: a Muse Code subscription signed in with a device code, or a Meta API key; `metatest` is a fake Meta for its tests. |
| `extensions/devin` | Provider extension for Devin, signed in as Devin's CLI signs in or with a session token, over Devin's protobuf API; `devintest` is a fake Devin for its tests. |
| `archive/` | Extensions pluxy no longer ships, each a Go module of its own ([README](archive/README.md)). |
| `examples/scripted` | The tutorial's example extension. |
| `examples/oauth` | An example provider that signs in by device code, browser (PKCE) or API key and renews its tokens, with a fake OAuth provider for tests (`oauthtest`). |
| `management` | The management API's request and reply types and a client, which the `pluxy` command uses ([Management API](../docs/management-api.md)). |
| `internal/core` | Core's live state: the config in use, the extensions it runs, and reloads. |
| `internal/route` | Routing: picks an account for each request, retries, blocks and fallback chains ([routing](../docs/routing.md)). |
| `internal/host`, `internal/server` | Core's extension host and HTTP API (client formats and the management API). |
| `internal/legacy` | Reads a CLIProxyAPI `config.yaml` and auth folder ([Moving from CLIProxyAPI](../docs/moving-from-cpa.md)). |
| `internal/catalogue` | Embedded `models.dev` snapshot of model limits and capabilities, refreshed daily into `state/catalogue.json`. |
| `internal/cred` | Core's credential store and renewal of accounts added by sign-in. |
| `internal/private` | Core's private folders and files (credentials, extensions' settings, logs): 0700 and 0600 on Linux and macOS, an owner-only access list on Windows. |
| `internal/packages` | Registries, release archives and the record of installed packages. |
| `internal/panel` | Serves the web panel at `/panel/`, embedded in core's binary. |
| `internal/pages` | Serves extension pages on a site of their own ([extension pages](ui/extension-pages/README.md)). |
| `internal/logfile` | Core's log files: `main.log`, its rotation, the folder's size cap, reading lines back ([Usage and logs](../docs/usage-and-logs.md)). |
| `internal/usage` | The usage record: one event per request, kept in memory and written asynchronously to daily JSONL files. |
| `internal/extconfig` | Extensions' settings: the schema from the manifest, checking values, and the store core keeps them in. |
| `ui/` | The UI kit the panel and the desktop app share, the panel's browser build, and the [theme contract](ui/themes/README.md) ([README](ui/README.md)). |
| `app/` | The desktop app: the kit's second build in a Tauri window, with a tray icon and three ways to reach core ([README](app/README.md)). Linux only for now. |
| `app-tools/` | The desktop app's optional native tools and the libraries they share: a cargo workspace of its own ([README](app-tools/README.md)). |
| `e2e/` | Black-box tests that drive the built core over HTTP. |
| `corpus/` | Differential test corpus: cases replayed against core and compared with recorded replies, with listed exceptions ([README](corpus/README.md)). |

## Build and test

[AGENTS.md](../AGENTS.md#build-and-test) has the commands CI runs, and `make` in this folder lists
targets that run them (`make check`, `make test`, `make ui`, `make app`). The short version, from
here:

```bash
go generate ./internal/panel     # builds the web panel core embeds; needs Bun
go build ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
```

[docs/map.md](../docs/map.md) says where to start in the code for a kind of change, and which test
fakes exist.

## Docs

[docs/README.md](../docs/README.md) lists the user guide, references, extension docs and design notes.

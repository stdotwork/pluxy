# Code map

Start here before searching the tree. **Core** is the Go proxy in `next/` that serves client APIs
and manages accounts; a **provider extension** is a separate program core starts over a local socket
to talk to one upstream. Commands below run from `next/` unless another folder is named.

## Path of one request

1. **Client API handler**: `next/internal/server/server.go` (`NewHandler`, `serve`) receives HTTP
   requests (WebSocket Responses enter `next/internal/server/responses_ws.go`) and parses the body
   into the **internal model** (`llm.Request` in `next/llm/llm.go`) via `ParseRequest` in
   `next/format/openaichat/request.go`, `next/format/openairesponses/request.go`,
   `next/format/anthropicmessages/request.go`, or `next/format/geminigenerate/request.go`.
2. **Client key check**: `next/internal/server/server.go` (`refused`, `authorized`, `clientToken`)
   validates the caller's key and checks client-IP and lockout rules.
3. **Routing and account choice**: `next/internal/server/route.go` (`routeRequest`) hands the
   request to `next/internal/route/route.go` (`Router.Generate`), which picks an account through
   `next/internal/route/pick.go` (`picker.pick`), tracks cooldowns, and decides retries.
4. **Extension host**: `next/internal/host/managed.go` (`Managed.Generate`) ensures the extension
   process (`next/internal/host/host.go`, `Extension`) is running and sends a `protocol.Call` frame
   (`next/protocol/protocol.go`, `WriteFrame`) over the local socket.
5. **Extension**: `next/sdk/sdk.go` (`Serve`, `server.generate`) decodes the frame and invokes the
   provider handler (for example `generate` and `upstreamBody` in
   `next/extensions/openai-compatible/main.go`).
6. **Upstream**: the extension sends the HTTP request through `sdk.Upstream` (`next/sdk/upstream.go`),
   which applies account proxy, extra headers, payload rules, and optional capture.
7. **Reply and format translation**: the extension decodes upstream events with `NewStreamDecoder`
   (for example `next/format/openaichat/decode.go`) into `llm.Event` frames; `next/internal/host`
   redacts secrets and forwards events to `next/internal/server/server.go`, which writes the client
   reply via `NewStreamWriter` or `NewAggregator` (for example `next/format/openaichat/render.go`).

## Where to start by task

### Provider extension
- **Entry**: `next/extensions/openai-compatible/main.go` and
  `next/extensions/openai-compatible/pluxy-extension.json` (same pattern in `next/extensions/claude`,
  `next/extensions/codex`, `next/extensions/devin`, `next/extensions/gemini`, `next/extensions/kimi`,
  `next/extensions/meta`, `next/extensions/xai`); wire up with `sdk.Extension` and `sdk.Serve` in
  `next/sdk/sdk.go`, and wrap every HTTP transport with `sdk.Upstream` in `next/sdk/upstream.go`.
- **Tests**: `go test -count=1 -run TestBuildBodyTranslatesEveryWire ./extensions/openai-compatible`
  and `go test -count=1 -run TestExtensionUpstreamClients ./sdk`; full extension contract in
  `next/conformance/conformance.go` (`Run`, `BuildExtension`).
- **Fakes and helpers**: `next/sdk/testhost.go` and the per-provider `*test` packages in the table below.
- **Handbook**: `next/protocol/README.md` ("Overview", "Package manifest", "Calls") and
  `next/protocol/TUTORIAL.md`.

### Wire format request or reply field
- **Entry**: add or update the internal model in `next/llm/llm.go` (`Request`, `Item`, `Part`,
  `Event`), then update `ParseRequest`, `EncodeRequest`, `NewStreamDecoder`, `NewStreamWriter`, and
  `NewAggregator` in all four packages: `next/format/openaichat`, `next/format/openairesponses`,
  `next/format/anthropicmessages`, and `next/format/geminigenerate`. Shared URL and media-type
  helpers live in `next/format/internal/media/media.go`.
- **Cross-format check**: every format must either translate the new field or report an `llm.Loss`;
  otherwise cross-format calls silently drop data. If any `ParseRequest` output changes on existing
  bodies, bump `ParserVersion` in `next/format/format.go` (checked by `TestParserVersion`) and check
  `next/conformance/conformance.go`.
- **Tests**: `go test -count=1 -run TestParserVersion ./format` and
  `go test -count=1 -run TestInlineImage ./format/internal/media`.
- **Fakes and helpers**: `next/format/formattest/formattest.go` (`CorpusRequests`, `LiteralRequests`).
- **Handbook**: `docs/design.md` ("Inside core") and `next/README.md` ("Layout").

### Config key
- **Entry**: parse and validate in `next/internal/server/config.go` (`Config`, `ParseConfig`),
  YAML/JSON round-trip editing in `next/internal/configdoc/edit.go` (`Doc.ApplySettings`,
  `SettingsPatch`), secret redaction in `next/internal/configdoc/redact.go` (`Redactor`,
  `Redactor.Scrub`), management read/write in `next/internal/core/manageconfig.go`
  (`Core.WriteConfig`, `Core.handleConfig`, `settings`) and `next/management/types.go`, panel form
  in `next/ui/kit/pages/configSettings.tsx`.
- **Tests**: `go test -count=1 -run TestParseConfig ./internal/server`,
  `go test -count=1 -run TestApplySettings ./internal/configdoc`, and
  `go test -count=1 -run TestWriteConfig ./internal/core`.
- **Handbook**: `next/README.md` ("config.yaml with today's key names", "Reloading config and
  swapping extensions").

### Management API endpoint, CLI command, and panel page
- **Entry**: request/reply types and Go client in `next/management/types.go` and
  `next/management/client.go` (`Client.Call`); HTTP routes in `next/internal/server/manage.go`
  (`Management.Handle`) and `next/internal/core/management.go` (`Core.Management`); CLI subcommands
  in `next/cmd/pluxy/cli.go` (account, logs, and setup commands in `next/cmd/pluxy/account.go`,
  `next/cmd/pluxy/logs.go`, `next/cmd/pluxy/setupcli.go`); panel API wrapper in `next/ui/kit/api`
  (`next/ui/kit/api/index.ts`) and page in `next/ui/kit/pages`.
- **Tests**: `go test -count=1 -run TestManagement ./internal/core` and
  `go test -count=1 -run TestEveryRouteHasACommand ./cmd/pluxy`.
- **Handbook**: `next/management/README.md` ("Resources", "The `pluxy` command").

### Panel page or UI kit component
- **Entry**: shared primitives in `next/ui/kit/ui.tsx` (`Card`, `List`, `Button`, `PageHead`) and
  `next/ui/kit/ui.css`; shell and navigation in `next/ui/kit/shell.tsx`; strings in
  `next/ui/kit/strings.ts`; screen-share masks in `next/ui/kit/privacy.ts`; register a core page in
  `next/ui/kit/pages/core.ts` (`corePages`). Browser host wiring is in
  `next/ui/panel/browserHost.ts` (`browserHost`) and `next/ui/panel/main.tsx`.
- **Tests** (from `next/ui`): `bun test tests/browserHost.test.ts` and `bun run check`.
- **Fakes and helpers**: `next/e2e/panelcore/main.go` (starts a real core with fake extensions for
  Playwright and app tests).
- **Handbook**: `next/ui/README.md` ("How a host build uses the kit", "Adding a page").

### Theme or token
- **Entry**: token, part, and layout definitions in `next/ui/kit/theme/contract.ts` (`TOKENS`,
  `PARTS`, `LAYOUT`); resolution and DOM application in `next/ui/kit/theme/engine.ts` (`resolve`,
  `ThemeApplier`); state and persistence in `next/ui/kit/theme/state.ts` (`ThemeState`); validation
  in `next/ui/kit/theme/validate.ts`; built-in files in `next/ui/themes/default.json`,
  `next/ui/themes/paper.json`, `next/ui/themes/terminal.json`; core storage in
  `next/internal/core/managethemes.go`.
- **Tests**: from `next/ui`, `bun test tests/theme.test.ts tests/theme-state.test.ts`; from `next/`,
  `go test -count=1 -run TestHandleThemesHTTP ./internal/core`.
- **Handbook**: `next/ui/themes/README.md` ("Tokens", "Layout options", "Changes to this contract").

### Desktop app behaviour
- **Entry (UI)**: `next/app/src/main.tsx` mounts the shared kit with `appHost`
  (`next/app/src/host.ts`), connection UI (`next/app/src/connection.tsx`), and device extensions
  (`next/app/src/devicePage.tsx`).
- **Entry (Tauri Rust in `next/app/src-tauri/src`)**:
  - `lib.rs`: Tauri command registration, tray, and window wiring.
  - `conn.rs`, `managed.rs`, `cores.rs`: managed/local/remote connection modes, managed core
    supervision, and installed core versions.
  - `device.rs`, `program.rs`, `route.rs`: on-device package catalog, native tool process host, and
    call router with capability grants.
  - `spawn.rs`, `confirmation.rs`: confirmed child launches and native effect-review dialogs.
  - `pages.rs`, `access_broker.rs`: sandboxed extension-page webviews and Unix file-access grants.
  - `vault.rs`, `instance.rs`, `release.rs`: host-private secret/backup vault, single-instance
    profile lock, and signed release feed.
- **Tests**: from `next/app`, `bun test tests/host.test.ts`; Rust unit tests in
  `next/app/src-tauri` via `cargo test --locked` (see `AGENTS.md`).
- **Handbook**: `next/app/README.md` ("How the app reaches core", "Commands", "Extensions on this
  device") and `next/app/app-host/README.md`.

### Native tool
- **Definition**: a **native tool** is a standalone Rust binary the desktop app starts on demand
  over private pipes (never linked into the app).
- **Entry**: workspace `next/app-tools/Cargo.toml`; shared crates in `next/app-tools/lib/program`,
  `next/app-tools/lib/files`, `next/app-tools/lib/transactions`, `next/app-tools/lib/usage`,
  `next/app-tools/lib/sqlite-ro`, `next/app-tools/lib/pi-sessions`; tool crates such as
  `next/app-tools/claude-code`, `next/app-tools/codex`, `next/app-tools/codex-sessions`, and
  `next/app-tools/agent-usage`. Packed by `next/cmd/pluxy-pack` (`-app-tools`).
- **Tests**: `cargo test --locked` in `next/app-tools` (see `AGENTS.md`).
- **Handbook**: `next/app-tools/README.md` and `next/app/app-host/README.md` ("App program contract v0").

### Usage records
- **Entry**: in-memory ring and aggregation in `next/internal/usage/usage.go` (`Store`, `Event`,
  `Store.Add`, `Store.Summarize`); daily JSONL files and closed-day summaries in
  `next/internal/usage/disk.go` (`Open`); request metering in `next/internal/server/usage.go`
  (`meter`, `meterStream`); management endpoints in `next/internal/core/manageusage.go`
  (`Core.handleUsage`); panel view in `next/ui/kit/pages/usage.tsx`.
- **Tests**: `go test -count=1 -run TestStore ./internal/usage` and
  `go test -count=1 -run TestManageUsageRangesByKeyAndBefore ./internal/core`.
- **Handbook**: `next/README.md` ("Usage statistics").

### Logs and request logs
- **Entry**: `main.log` rotation and folder size cap in `next/internal/logfile/logfile.go` (`Files`,
  `Files.Configure`); per-request capture and error-log retention in
  `next/internal/server/requestlog.go` (`RequestLog`, `logged`, `ErrorLogs`, `FindRequestLog`);
  upstream capture in `next/internal/server/upstreamlog.go` and `next/internal/host/capture.go`;
  management endpoints in `next/internal/core/managelogs.go` (`Core.handleLogs`); CLI in
  `next/cmd/pluxy/logs.go`; panel in `next/ui/kit/pages/logs.tsx`.
- **Tests**: `go test -count=1 -run TestRotateAndTrim ./internal/logfile`,
  `go test -count=1 -run TestRequestLog ./internal/server`, and
  `go test -count=1 -run TestDeleteLogs ./internal/core`.
- **Handbook**: `next/README.md` ("Log files", "Request logs").

### Secret redaction
- **Entry**: wire and error redaction in `next/protocol/redact.go` (`Redactor`, `NewRedactor`,
  `SecretParts`) and `next/protocol/redact_longest.go`; config document redaction in
  `next/internal/configdoc/redact.go` (`Redactor`, `Redactor.Scrub`). Both share one secret-shape
  table in `next/internal/secretshapes/shapes.go` (`Rows`, `URLRows`, `NumberRows`).
- **Tests**: `go test -count=1 -run TestSecretShapes ./protocol ./internal/configdoc` and
  `go test -count=1 -run TestRedact ./protocol`.
- **Handbook**: `next/protocol/README.md` ("Errors", "Upstream capture").

### Credentials and token renewal
- **Entry**: versioned credential store in `next/internal/cred/store.go`; single-flight renewal and
  rejection handling in `next/internal/cred/manager.go` (`Manager`, `Manager.Credential`,
  `Manager.Refresh`, `Manager.Rejected`); interactive logins in `next/internal/cred/login.go`; host
  credential handoff in `next/internal/host/credentials.go`.
- **Tests**: `go test -count=1 -run TestConcurrentRequestsShareOneRenewal ./internal/cred` and
  `go test -count=1 -run TestLoginPromptIsRedacted ./internal/host`.
- **Handbook**: `next/README.md` ("Accounts added by login") and `next/protocol/README.md` ("Login
  and refresh").

### Differential test corpus
- **Definition**: the **corpus** (`next/corpus`) replays recorded HTTP/WebSocket cases against core
  and compares them with the removed old core's `recorded_old_core` reference. An **exception** is a
  case entry (or global rule in `next/corpus/cases/corpus.json`) with `path`, `reason`, and `kind`
  (`intended` for an agreed difference, `gap` for an open one), optionally pinning `want` or
  `absent`. Unused exceptions fail the run; never edit `recorded_old_core`.
- **Entry**: `next/corpus/case.go` (`Case`, `Exception`), `next/corpus/diff.go`,
  `next/corpus/cores.go`, and case files under `next/corpus/cases`.
- **Tests**: `go test -count=1 -run TestUsesManagement ./corpus` (unit check on case files); narrow
  replay with
  `PLUXY_CORPUS_CASE=gemini-interactions/interactions/models-prefix go test -count=1 -v -run TestCorpus ./corpus`.
- **Handbook**: `next/corpus/README.md` ("Case files", "Adding cases") and `docs/corpus.md` ("The bar").

## Test fakes

| Package | What it stands in for |
| --- | --- |
| `next/extensions/claude/claudetest` | Fake Anthropic Messages, models, profile/usage, and OAuth endpoints |
| `next/extensions/codex/codextest` | Fake OpenAI PKCE/device/SIWC login, Codex Responses, and usage endpoints |
| `next/extensions/devin/devintest` | Fake Devin CLI sign-in, token/self API, and protobuf chat endpoint |
| `next/extensions/gemini/geminitest` | Fake Gemini API, Vertex AI, and Google service-account OAuth endpoints |
| `next/extensions/kimi/kimitest` | Fake Kimi device-code OAuth and Kimi Code API (`/coding/v1`) |
| `next/extensions/meta/metatest` | Fake Meta device-code login, key minting, and Muse Responses API |
| `next/extensions/xai/xaitest` | Fake xAI Responses, models, billing/profile, and Grok CLI sign-in |
| `next/examples/oauth/oauthtest` | Fake OAuth 2.0 (device, PKCE, refresh rotation) and chat/quota API |
| `next/format/formattest` | Loader for corpus and literal JSON client requests used by format tests |
| `next/e2e/panelcore` | Runs a real core with fake extensions for panel Playwright and app tests |
| `next/e2e/testext` | No-upstream extension that reports version/PID and supports gated/silent streams |
| `next/conformance` | Extension contract suite (`Run`) exercising handshake, streaming, media, and errors |

## Things fixes keep teaching

- Every new error, log, or rate-limit header path that can carry upstream text needs a redaction
  test against `next/internal/secretshapes/shapes.go` (`next/protocol/secret_shapes_test.go`,
  `next/protocol/redact_ratelimit_test.go`).
- Adding or changing a field in one format's parser, encoder, or renderer requires checking the
  other three formats and `next/conformance`, plus bumping `ParserVersion` in `next/format/format.go`
  when parser output changes (`TestParserVersion`).
- Wrap every extension HTTP transport—including login, refresh, and WebSocket handshakes—with
  `sdk.Upstream` (`next/sdk/upstream.go`, enforced by `TestExtensionUpstreamClients` in
  `next/sdk/upstream_clients_test.go`).
- Match URL schemes, data URLs, and media types case-insensitively through
  `next/format/internal/media/media.go` (`IsDataURL`, `DataURL`, `Type`, `InlineImage`) instead of
  custom prefix checks.
- Escape every foreign or upstream string printed to a terminal in `next/cmd/pluxy` with
  `writeEscaped` or `writeEscapedText` (`next/cmd/pluxy/cli.go`, checked by
  `TestForeignTextSitesTerminalEscaping` in `next/cmd/pluxy/escape_sites_test.go`).
- Any change touching files, permissions, processes, or paths needs Windows and macOS runs: label
  the PR `full-ci` (`AGENTS.md`, `next/internal/private`).

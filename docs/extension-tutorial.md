# Tutorial: write a provider extension

A provider extension is a separate program that pluxy starts to reach one kind of upstream. This
tutorial builds a small one in Go, from an empty folder to an extension that pluxy starts and
streams a reply through, and that passes the conformance kit (pluxy's test suite for extensions).

The extension is called `scripted`: it answers with whatever its account's settings say and calls
no upstream, so nothing but the protocol is in view. A real provider makes an HTTP call where this
one reads its settings. The finished code is in [`examples/scripted`](../next/examples/scripted);
[`extensions/openai-compatible`](../next/extensions/openai-compatible) is a real provider.

You can write an extension in any language that can listen on a Unix socket and read and write
JSON. This tutorial uses pluxy's Go SDK, which does the handshake, framing, cancellation and
shutdown for you. The [protocol reference](protocol.md) has the rules behind each step.

You need Go and `curl`. pluxy's Go module needs Go 1.26.11 or later; a `go` command from 1.21 on
downloads that version by itself when it is older.

## 1. Make a module

```bash
mkdir scripted
cd scripted
go mod init example.com/scripted
```

`example.com/scripted` is your module's name: use your own.

## 2. Write the manifest

pluxy finds an extension by its manifest. Create `pluxy-extension.json`:

```json
{
  "manifest_version": 0,
  "id": "example/scripted",
  "version": "0.1.0",
  "protocol": [0],
  "programs": {
    "linux/amd64": "scripted",
    "linux/arm64": "scripted",
    "darwin/amd64": "scripted",
    "darwin/arm64": "scripted"
  }
}
```

`id` is yours: a publisher name, a slash and the extension name. `programs` names the program to
run on each platform, relative to the manifest.

## 3. Write the program

Create `main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// settings is what an account's settings hold for this extension.
type settings struct {
	Reply []string   `json:"reply"`
	Usage *llm.Usage `json:"usage"`
	Error *llm.Error `json:"error"`
	// Hang keeps the reply open after the text until the call is cancelled.
	Hang bool `json:"hang"`
}

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:       "example/scripted",
		Version:  "0.1.0",
		Generate: generate,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "scripted:", err)
		os.Exit(1)
	}
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	var s settings
	if err := json.Unmarshal(call.Account.Settings, &s); err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad account settings", Scope: llm.ScopeCredential}
	}
	if s.Error != nil {
		return s.Error // sent to core as a structured error
	}
	if err := out.Start(llm.ResponseInfo{ID: "scripted-1", Model: call.Request.Model}); err != nil {
		return err
	}
	item, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
	if err != nil {
		return err
	}
	for _, piece := range s.Reply {
		if err := out.Text(item, piece); err != nil {
			return err
		}
	}
	if s.Hang { // for the conformance kit's cancellation check
		<-ctx.Done()
		return ctx.Err()
	}
	if err := out.EndItem(item); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: "stop"}, s.Usage)
}
```

Fetch the SDK and build the program next to the manifest:

```bash
go mod tidy
go build -o scripted .
./scripted
```

The program prints "scripted: this program is a pluxy extension; pluxy starts it" and exits:
`sdk.Serve` reads the environment pluxy sets when it starts an extension, checks the protocol
version, listens on the socket, prints the handshake line and serves calls until pluxy closes its
standard input.

`generate` gets the account, the request and a stream to write events to. The rules the SDK holds
you to:

- Call `Start` first, open an item with `StartItem` before writing to it, and close it with
  `EndItem` before `Finish`. Breaking the order returns an error instead of sending a bad stream.
- End with `Finish`, or return an error. Return an `*llm.Error` to tell core what went wrong: its
  code, whether retrying is safe (`Retryable`), what it is about (`Scope`) and when to try again
  (`RetryAfterMS`). Any other error is sent as code `cancelled` when `ctx` has ended, and as code
  `internal` otherwise. To fill `RetryAfterMS` from an
  upstream's `Retry-After` header, call `sdk.RetryAfter(value, time.Now())`: it reads seconds or
  an HTTP date and returns milliseconds from now, or 0 when the header is absent, malformed or not
  in the future. A provider's own hint (a body field, a vendor header) is yours to read.
- Watch `ctx`. When the client goes away, pluxy closes the connection and the SDK cancels `ctx`.
  Pass it to your upstream HTTP request so that request stops too.
- Do not retry inside the extension: core decides retries (see [`generate`](protocol.md#generate)).
- Write each event when you have it. Writes block while the client is slow; let them.

A real provider also follows these:

- Wrap the transport of every upstream HTTP client with `sdk.Upstream(base)`, including the
  clients for login, token refresh and WebSocket handshakes. An account's
  [proxy](protocol.md#proxy) is used only by clients wrapped this way; any other client, such as
  `http.DefaultClient`, connects directly. An extension that wraps none gets an error reply, not a
  call, when an account has a proxy.
- Create each wrapped client before `sdk.Serve` runs, for example as a package-level variable. The
  SDK refuses a call with a proxy while no `sdk.Upstream` transport exists in the process, so a
  client first created inside a handler never gets its first call.
- If the request carries `native` in the format your upstream speaks, send `native.body` upstream
  as it is instead of building a body from `items`: that keeps fields pluxy does not model.

## 4. Run it with pluxy

Install pluxy itself, if you do not have it yet:

```bash
go install github.com/stdotwork/pluxy/next/cmd/pluxy@latest
```

This puts `pluxy` in Go's program folder (`$(go env GOPATH)/bin`, unless `GOBIN` is set); add that
folder to your `PATH` if your shell does not find it. A pluxy built this way has no web panel and says so when it
starts, which does not matter here.

Create `config.json` in the same folder. Put the full path of your manifest in `manifest`:

```json
{
  "listen": "127.0.0.1:8317",
  "client_keys": ["local-test-key"],
  "management_key": "local-management-key",
  "extension": {
    "manifest": "/path/to/scripted/pluxy-extension.json",
    "accounts": [
      {"id": "demo", "settings": {"reply": ["Hello", " there."]}, "credential": {"kind": "api_key", "api_key": "unused"}}
    ]
  }
}
```

- `listen` is the address pluxy serves on. If another pluxy already uses port 8317 (the desktop
  app's, for example), pick another port here and in the `curl` command below.
- `client_keys` are the keys clients send. `management_key` is the key of pluxy's management API;
  pluxy refuses to start without one. Use long random secrets for both anywhere but a test.
- `extension.accounts` are the accounts pluxy routes requests to. `settings` is what your
  extension reads in `generate`.

Start pluxy:

```bash
pluxy -config config.json
```

It logs `extension example/scripted 0.1.0 started` and the address it listens on. In a second
terminal, send a request:

```bash
curl -N http://127.0.0.1:8317/v1/chat/completions \
  -H 'Authorization: Bearer local-test-key' \
  -d '{"model":"any","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

The reply streams back as OpenAI Chat Completions chunks, carrying `Hello` and ` there.`, then
`data: [DONE]`. Stop pluxy with Ctrl+C.

## 5. Run the conformance kit

The kit starts your extension the way pluxy does and checks it: the handshake, a streamed and an
unstreamed request, request features such as tool results and service tiers, cancellation, a
crash, shutdown, structured errors, and the login, refresh, models, check and quota operations. It cannot know how to make your upstream fail, so you give it a
fixture: for each scenario, the account that makes your extension behave that way.

Create `conformance_test.go`:

```go
package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest: manifest,
		Prepare: func(t *testing.T, s conformance.Scenario) conformance.Setup {
			return conformance.Setup{Account: accountFor(s)}
		},
	})
}

// accountFor returns account settings that make this extension act out a scenario.
func accountFor(s conformance.Scenario) protocol.Account {
	var set settings
	switch s {
	case conformance.ScenarioText:
		set.Reply = []string{conformance.ReplyText[:6], conformance.ReplyText[6:]}
		set.Usage = &llm.Usage{InputTokens: llm.Int64(conformance.InputTokens), OutputTokens: llm.Int64(conformance.OutputTokens)}
	case conformance.ScenarioHang:
		set.Reply, set.Hang = []string{"Hold on"}, true
	case conformance.ScenarioRateLimited:
		set.Error = &llm.Error{Code: llm.CodeRateLimit, Message: "slow down", Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 20000}
	case conformance.ScenarioAuthFailed:
		set.Error = &llm.Error{Code: llm.CodeAuth, Message: "bad key", Retryable: true, Scope: llm.ScopeCredential}
	}
	b, _ := json.Marshal(set)
	return protocol.Account{ID: "scripted", Settings: b}
}
```

Run it:

```bash
go mod tidy
go test -v .
```

`conformance.BuildExtension` builds the program into a temporary folder next to a copy of the
manifest. Each check is a named subtest, so a failure tells you which rule broke. For each of
those operations `scripted` does not serve, the kit checks that it answers with code
`unsupported`, which the SDK does for you. The `alive` check is skipped unless the fixture
arranges `conformance.ScenarioQuiet` and sets `Target.Quiet`. Each scenario's doc comment on `conformance.Scenario` says what the upstream does in it
and what the extension must report. For the cancellation scenario, a fixture that controls a fake
upstream can also set `Setup.UpstreamCancelled`, a channel that closes when the upstream sees the
request cancelled; the kit then checks that cancellation reaches the upstream, not only the
extension.

The rest of this tutorial adds optional operations. Each is a field of `sdk.Extension`; the SDK
lists the operation in its describe reply when the field is set, and core uses it only then. The
protocol version in each heading is the one that added the operation.

## 6. Sign in to accounts (optional)

A provider whose accounts sign in, instead of taking a key from the config, adds login and refresh
handlers. Core keeps the credential; your extension only knows how to get one and how to renew it:

```go
sdk.Serve(sdk.Extension{
	ID: "example/oauth", Version: "0.1.0", Generate: generate,
	LoginMethods: []protocol.LoginMethod{{Method: "device", Kind: protocol.LoginDevice, Label: "Device code"}},
	Login: func(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
		// Ask the provider for a device code, show it, poll until the user approves.
		s.Prompt(protocol.Prompt{Kind: protocol.PromptDevice, UserCode: code, VerificationURI: uri})
		...
		return &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialOAuth,
			AccessToken: at, RefreshToken: rt, ExpiresAt: exp}, Label: email}, nil
	},
	Refresh: func(ctx context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
		// Trade call.Account.Credential.RefreshToken for a new credential.
		...
	},
})
```

A browser login prompts with the sign-in URL and waits for the redirect URL with `s.Input(ctx)`.
From Refresh, report a refused grant (OAuth `invalid_grant`) as code `auth`, scope `credential`,
not retryable: the account then needs a new login. Any other failure is retryable only when the
request never reached the token endpoint, or when the endpoint rate limited it (HTTP 429); a 5xx
answer, or a request that failed after it was sent, is not retryable, because the endpoint may
have run it. [`examples/oauth`](../next/examples/oauth)
is a complete example with all three login kinds, and its `oauthtest` package is a fake OAuth
provider for tests. Give the conformance kit a `PrepareLogin` fixture and it checks each login
method and refresh outcome too. The rules are in [Login and refresh](protocol.md#login-and-refresh).

## 7. Hold sessions (optional, protocol 0.1)

An upstream that keeps state on one connection between requests, such as a Responses WebSocket,
is served through a session: core opens one per client conversation and sends its turns one at a
time on the same local connection. Add a session handler that returns a value that answers turns:

```go
sdk.Serve(sdk.Extension{
	ID: "example/ws", Version: "0.1.0", Generate: generate,
	Session: func(ctx context.Context, call sdk.SessionCall) (sdk.Session, error) {
		if !wantsSessions(call.Account) {
			// Core then sends this account's turns as separate generate calls.
			return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Message: "sessions are off for this account"}
		}
		return &wsSession{account: call.Account}, nil // connect upstream now or at the first turn
	},
})
```

`sdk.Session` has three methods:

- `Turn(ctx, call, out)` answers one turn exactly as a generate handler answers a call, on the
  session's upstream connection; `ctx` ends if core cancels the turn.
- `Interrupt(raw)` is called from another goroutine while a turn runs, with the client's control
  message to pass upstream.
- `Close()` is called once when the session ends: close the upstream connection there.

The rules, including what to send when the upstream connection is lost, are in
[Sessions](protocol.md#sessions); the Codex extension (`extensions/codex`) is a complete example.

Core's [payload rules](protocol.md#payload-rules) (fields to set or remove in upstream bodies) reach a
turn on its frame, and `sdk.Upstream` cannot apply them to a message that is not an HTTP request.
So for each turn, pass the message you write upstream to `sdk.ApplyPayload(ctx, body)` once, with
the turn's `ctx`, just before writing it and after everything else you do to it
(`extensions/codex/websocket.go` does this). Leave out control messages such as the client's
interrupt. Do not pass a message that goes out as an HTTP request through `sdk.Upstream` to
`sdk.ApplyPayload` as well: `sdk.Upstream` already applies the rules when it sends it.

## 8. List models (optional, protocol 0.3)

Core answers clients' model lists (`GET /v1/models`) with the models each account serves, and
asks each account's extension for them. An account whose config names its `models` serves those
names, with the details (such as the context length) the extension gives; any other account
serves the models its extension lists. Add a models handler that returns the account's models by
their upstream names:

```go
sdk.Serve(sdk.Extension{
	ID: "example/echo", Version: "0.1.0", Generate: generate,
	Models: func(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
		return &protocol.ModelList{OwnedBy: "example", Models: []protocol.Model{
			{ID: "echo-1", DisplayName: "Echo 1", ContextLength: 8192},
		}}, nil
	},
})
```

Read the list from the upstream when it has one (with `call.Account`'s credential), or return a
list the extension carries. Core renames, leaves out and prefixes the models by the account's own
rules, and keeps the list: it asks again every 30 minutes (plus up to 3 minutes, so accounts do not
all ask at once) and after a login or a credential renewal. A read that fails, takes longer than
15 seconds or comes back empty keeps the last good list. So the handler needs no cache of its own.
Without a models handler, core lists only the models accounts' configs name. The fields are in
[`models`](protocol.md#models).

## 9. Check accounts (optional, protocol 0.4)

When core starts, and when a reload or a login adds or changes an account, it asks the account's
extension whether the account works, and shows the answer in its account listing and log. Add a
check handler that makes one free call upstream, such as the provider's model list or its own
call that tells whether a token is valid. Never generate:

```go
sdk.Serve(sdk.Extension{
	ID: "example/echo", Version: "0.1.0", Generate: generate,
	Check: func(ctx context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
		resp, err := getModels(ctx, call.Account) // GET {base_url}/models with the account's key
		if err != nil {
			return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the upstream", Scope: llm.ScopeEndpoint}
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, &llm.Error{Code: llm.CodeAuth, Message: "the upstream refused the key", Scope: llm.ScopeCredential}
		}
		return &protocol.CheckResult{Detail: "the upstream accepted the key"}, nil
	},
})
```

Return code `unsupported` for an account that cannot be checked without generating: core then
shows it as not checked. The rules are in [`check`](protocol.md#check); the OAuth example
(`examples/oauth`) checks a token with its provider.

## 10. Report quota and who signed in (optional, protocol 0.5)

The management API shows an account's plan and usage when someone asks for it
(`GET /v0/management/accounts/{id}/quota`). Add a quota handler that reads them from the
provider's own usage endpoint, with the account's credential and the same client identity the
extension uses for its requests:

```go
sdk.Serve(sdk.Extension{
	ID: "example/echo", Version: "0.1.0", Generate: generate,
	Quota: func(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
		used := 42.0
		return &protocol.Quota{Plan: "pro", Limits: []protocol.QuotaLimit{
			{ID: "five_hour", Label: "5-hour limit", UsedPercent: &used, ResetsAt: 1794268800, WindowSeconds: 18000},
		}}, nil
	},
})
```

An account with no plan to report, such as one with an API key, returns code `unsupported` with
scope `credential`. Never read quota with a request that spends from the plan.

A login handler should also say who signed in, in the grant's `Identity`: the fields that tell two
upstream accounts apart, such as the e-mail address and the account or workspace ID. Core keeps
them and refuses a later login to the same account that signs in as someone else. The OAuth
example (`examples/oauth`) does both. The fields are in [`quota`](protocol.md#quota) and
[`login`](protocol.md#login).

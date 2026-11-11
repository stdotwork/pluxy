# Tutorial: write a provider extension

This walks through a small provider extension in Go, from an empty folder to an extension that
pluxy starts, streams through and that passes the conformance kit. The finished code is in
[`examples/scripted`](../examples/scripted): a provider that answers with whatever its account
settings say. It calls no upstream, which keeps the protocol in view; a real provider puts an
HTTP call where this one reads its settings. [`extensions/openai-compatible`](../extensions/openai-compatible)
is a real one.

The [protocol README](README.md) has the details behind each step. You can write an extension in
any language that can listen on a Unix socket and read and write JSON; this tutorial uses the Go
SDK, which does the handshake, framing, cancellation and shutdown for you.

## 1. The manifest

Create `pluxy-extension.json`:

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
run for each platform, relative to this file.

## 2. The program

```go
package main

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
```

`sdk.Serve` reads the environment pluxy sets, checks the protocol version, listens on the socket,
prints the handshake line and serves calls until pluxy closes its stdin. Run the program by hand
and it tells you it must be started by pluxy.

## 3. Answer a call

`Generate` gets the account, the request and a stream to write events to:

```go
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

For upstream HTTP clients, wrap the configured transport with `sdk.Upstream(base)`, including clients for login, token refresh and WebSocket handshakes. An account's proxy is honoured only by clients wrapped this way: a client that is not (`http.DefaultClient` is one) sends direct, so wrap every client. An extension that wraps none gets an error reply, not a call, when an account carries a proxy.

The rules the SDK holds you to:

- Start with `Start`, open items before writing to them, close them before `Finish`. Breaking the
  order returns an error rather than sending a bad stream.
- End with `Finish`, or return an error. Return an `*llm.Error` to tell core exactly what went
  wrong: its code, whether retrying is safe (`Retryable`), what it is about (`Scope`) and when to
  try again (`RetryAfterMS`). Any other error is sent as `internal`.
- Watch `ctx`. When the client goes away, pluxy closes the connection and the SDK cancels `ctx`.
  Pass it to your upstream HTTP request so that request stops too.
- Do not retry inside the extension. Core owns retries.
- Write each event when you have it. Writes block while the client is slow; let them.

If the request carries `native` in the format your upstream speaks, send `native.body` upstream
as it is instead of building a body from `items`; that keeps fields pluxy does not model.

## 4. Run it with pluxy

Build the program next to the manifest and point a core config at the manifest:

```json
{
  "listen": "127.0.0.1:8400",
  "client_keys": ["local-test-key"],
  "extension": {
    "manifest": "/path/to/scripted/pluxy-extension.json",
    "accounts": [
      {"id": "demo", "settings": {"reply": ["Hello", " there."]}, "credential": {"kind": "api_key", "api_key": "unused"}}
    ]
  }
}
```

```bash
go build -o scripted .
pluxy -config config.json
curl -N http://127.0.0.1:8400/v1/chat/completions \
  -H 'Authorization: Bearer local-test-key' \
  -d '{"model":"any","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

The reply streams back as OpenAI Chat Completions chunks.

## 5. Run the conformance kit

The kit starts your extension the way core does and checks the handshake, a streamed and an
unstreamed request, cancellation, a crash, and structured errors. It cannot know how to make your
upstream fail, so you give it a fixture: for each scenario, the account that makes your extension
behave that way.

```go
func TestConformance(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	conformance.Run(t, conformance.Target{
		Manifest: manifest,
		Prepare: func(t *testing.T, s conformance.Scenario) conformance.Setup {
			return conformance.Setup{Account: accountFor(s)}
		},
	})
}
```

`conformance.BuildExtension` builds the program into a temporary folder next to a copy of the
manifest. The scenarios and what each expects are documented on `conformance.Scenario`. For the
cancellation scenario, a fixture that controls a fake upstream can also return a channel that
closes when the upstream sees the request cancelled; the kit then checks cancellation reaches the
upstream, not only the extension.

Run it with `go test`. Each check is a named subtest, so a failure tells you which rule broke.

## 6. Sign in to accounts (optional)

A provider whose accounts sign in, rather than taking a key from the config, adds login and
refresh handlers. Core keeps the credential; your extension only knows how to get one and how to
renew it:

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
Report a refused grant (OAuth `invalid_grant`) from Refresh as code `auth`, scope `credential`,
not retryable; anything that may pass as retryable. [`examples/oauth`](../examples/oauth) is a
complete example with all three login kinds, and its `oauthtest` package is a fake OAuth provider
for tests. Give the conformance kit a `PrepareLogin` fixture and it checks each login method and
refresh outcome too. The rules are in [Login and refresh](README.md#login-and-refresh).

## 7. Hold sessions (optional, protocol 0.1)

An upstream that keeps state on one connection between requests, such as a Responses WebSocket,
is served through a session: core opens one per client conversation and sends its turns one at a
time on the same local connection. Add a session handler; it returns a value that answers turns:

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

`sdk.Session` has three methods. `Turn(ctx, call, out)` answers one turn exactly as a generate
handler answers a call, on the session's upstream connection; `ctx` ends if core cancels the turn.
`Interrupt(raw)` is called from another goroutine while a turn runs, with the client's control
message to pass upstream. `Close()` is called once when the session ends: close the upstream
connection there. The SDK lists `session` in describe when `Session` is set. The rules,
including what to send when the upstream connection is lost, are in
[Sessions](README.md#sessions); the Codex extension (`extensions/codex`) is a complete example.

## 8. List models (optional, protocol 0.3)

Core answers clients' model lists (`GET /v1/models`) with the models each account serves. An
account whose config names its `models` lists those; for any other account core asks its
extension. Add a models handler that returns the account's models by their upstream names:

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
catalog the extension carries. Core renames, leaves out and prefixes the models by the account's
own rules, and keeps the answer until the account's settings or credential change, so the handler
needs no cache of its own. The SDK reports its `protocol_minor` (3 or more) in describe, and lists
`models` when `Models` is set; without it, core lists only the models accounts' configs name. The fields are in
[`models`](README.md#models).

## 9. Check accounts (optional, protocol 0.4)

When core starts, and when a reload or a login adds or changes an account, it asks the account's
extension whether the account works, and shows the answer in its account listing and log. Add a
check handler that makes one free call upstream: the provider's model list, or its own call that
tells whether a token is valid. Never generate:

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

Return code `unsupported` for an account that cannot be checked without a generation: core then
shows it as not checked. The SDK lists `check` in describe when `Check` is set. The rules are in
[`check`](README.md#check); the OAuth example (`examples/oauth`) checks a token with its provider.

## 10. Report quota and who signed in (optional, protocol 0.5)

The management API shows each account's plan and usage when someone asks
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

An account with no plan to report, such as an API key, returns code `unsupported` with scope
`credential`. Never read quota with a request that spends from the plan.

A login handler should also say who signed in, in the grant's `Identity`: the fields that tell
two upstream accounts apart, such as the e-mail address and the account or workspace ID. Core
keeps them and refuses a later login to the same account that signs in as someone else. The
example OAuth extension (`examples/oauth`) does both. The fields are in [`quota`](README.md#quota)
and [`login`](README.md#login).

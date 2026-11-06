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

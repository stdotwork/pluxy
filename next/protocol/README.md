# protocol

Go package `protocol` holds the wire types and framing of pluxy's extension protocol:

- `protocol.go`: wire types for manifests, handshakes, calls, events and errors.
- `frame.go`, `finish.go`: length-prefixed framing and streamed reply decoding.
- `pieces.go`, `redact.go`, `redact_longest.go`: secret collection and redaction from error replies and upstream captures.
- `nativeonly/`: rules for native-only `generate` calls.
- `testdata/app-manifests/`: shared manifest test cases ([README](testdata/app-manifests/README.md)).

The extension protocol specification is in [docs/protocol.md](../../docs/protocol.md), and the step-by-step guide to writing an extension is in [docs/extension-tutorial.md](../../docs/extension-tutorial.md).

Run the package's tests from `next/`:

```bash
go test ./protocol/...
```

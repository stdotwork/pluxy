# management

The Go package for pluxy's management API: the request and reply types of every call, and a client
for it. Core's handlers write these types and the clients read them, so the two cannot drift.

- `types.go`: the types, and `APIVersion`, the number `GET /status` reports as `management_api`.
- `client.go`: a client that calls a core's management API and turns its error replies into
  `APIError` values.

The `pluxy` command, the web panel and the desktop app are clients of the API. Every call, field and
error code is described in [`docs/management-api.md`](../../docs/management-api.md); the commands are
in [`docs/cli.md`](../../docs/cli.md).

The package's tests run with the rest of the module: `go test ./management` from `next/`.

# Antigravity (archived)

Provider extension for Google Antigravity (`pluxy/antigravity`), serving Gemini and Claude models through a Google account signed in to Antigravity via Google's Cloud Code API.

This extension is archived: pluxy does not ship it. It is not packed or bundled by default, and `go build ./...`, `go vet ./...` and `go test ./...` in `next/` skip it, because it is a Go module of its own. It still builds, passes the extension conformance kit and speaks the current extension protocol, and you can build and install it by hand.

- Last built at commit: `f1db49e1531341c4ed46712736ba39e0f932386d`
- Corpus cases: the 41 recorded differential corpus cases are kept beside it in `corpus/` and are not run.

## Build and test

```bash
cd next/archive/antigravity
go build .          # writes ./antigravity, which `.gitignore` leaves out
go vet ./... && go test -count=1 ./...
```

`go test` includes the conformance kit (`next/conformance`), so it checks the extension against the protocol this `next/` speaks. Nothing in CI runs it.

## Pack and install it

`pluxy-pack` packs it as a release archive when asked with `-archived`. Run it from `next/`:

```bash
cd next
go run ./cmd/pluxy-pack -out ~/pluxy-antigravity -archived -extensions antigravity
```

This builds the program in this folder's own module for the host platform (`-platforms linux/amd64,darwin/arm64` for others) and writes `~/pluxy-antigravity/bundled/index.json` and the archive beside it. The index lists the package with the tier `unverified`: pluxy no longer reviews it.

Then give core that index as a registry of its own, in core's config file (the path must be absolute), and install the package:

```json
"packages": {
  "registries": [{"name": "antigravity", "url": "/home/you/pluxy-antigravity/bundled/index.json"}]
}
```

```bash
pluxy ext install pluxy/antigravity
```

Core reads its registries from its config when it installs, so this works on a running core once the config is saved. It installs and enables the package; its accounts need no other setting.

To pack it together with the shipped extensions instead, add `-archived` and leave out `-extensions`: `go run ./cmd/pluxy-pack -out dist -archived` writes one bundled index that lists every extension and this one, which core installs from like any other (`-bundled dist/bundled/index.json`).

## Accounts

- Add one by signing in: `pluxy account add pluxy/antigravity` (a browser sign-in with Google).
- Or bring them from CLIProxyAPI: `pluxy import cpa <config.yaml>` imports each `antigravity` auth file as a `pluxy/antigravity` account, with `oauth.model-alias.antigravity` and `oauth.excluded-models.antigravity` as the package's `model_alias` and `excluded_models`. It does so whether or not this package is installed; until it is, the accounts do not serve, and the import's **Needs** list names `pluxy/antigravity`. See [Moving from CLIProxyAPI](../../../docs/moving-from-cpa.md).

The Antigravity desktop app's OAuth client ID and secret are built into `login.go`, as the app ships them; no account's credential is in this folder.

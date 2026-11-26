# Common manifest cases

`cases.json` lists package-root manifests shared by Go installer checks and future Rust app
checks. `manifest_valid` describes the common format; `core_error` is the expected substring of
core's install error, or empty for successful provider install and serving. Valid app-only
manifests are refused by core. `provider_index: true` tests an app-only manifest when the registry
advertises provider protocol 0; otherwise app-only releases omit registry protocol. Core must
refuse both routes without running a program.

Fixtures use `linux/amd64` keys. Tests on other platforms replace only those keys. `bin/codex`
is a real provider executable supplied by the e2e builder. `bin/app-canary` writes an external
temporary marker if executed. The builder supplies `app-pages/index.html`. No fixture contains
a private key or signed artifact. `app-in-v0.json` proves schema-0 still ignores unknown app
metadata.

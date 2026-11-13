# Write an app package manifest

This walkthrough prepares two package folders for the [app package contract](README.md).
The desktop app installs the page-only package on this device. It installs the native package
only from a curated registry, signed by a curated first-party key, and does not run it yet: the
native program host is later work.

## Make a page-only web package

Create `package/pluxy-extension.json`:

```json
{
  "manifest_version": 1,
  "id": "example/notes",
  "version": "0.1.0",
  "app": {
    "version": 0,
    "tier": "web",
    "host_versions": [0],
    "ui": {
      "version": 0,
      "dir": "pages",
      "pages": [{"id":"notes","title":"Notes","short":"Notes","path":"index.html"}]
    }
  }
}
```

Create `package/pages/index.html` containing `<!doctype html><title>Notes</title><p>My notes page</p>`.
It needs no provider `protocol` or `programs` and requests no resources. The page receives no
Tauri commands. A non-curated source is Unverified. Core refuses this manifest with instructions
to install it in the desktop app.

To install it, pack the folder as a release archive (`tar -czf notes-0.1.0.tar.gz -C package .`),
open **Device extensions** in the desktop app and choose **Install from file…**. The page
appears in the navigation, labelled Unverified on the Device extensions page.

For settings, add versioned `config` inside `app`, using the existing
[settings schema](../../ui/extension-pages/README.md#config). To request a file later, declare
`"resources":[{"id":"notes","kind":"file","operations":["read"]}]` inside `app`. This grants
no access: signer approval and native confirmation are required. Keep this first package page-only
until the broker is available.

## Describe a native package

Create a second folder with this `pluxy-extension.json`:

```json
{
  "manifest_version": 1,
  "id": "pluxy/example-tool",
  "version": "0.1.0",
  "app": {
    "version": 0,
    "tier": "native",
    "host_versions": [0],
    "programs": {"linux/amd64":"bin/example-tool"},
    "operations": [{
      "id":"inspect",
      "description":"Read the selected tool state",
      "input_schema":{"type":"object"},
      "output_schema":{"type":"object"}
    }],
    "state":{"version":0,"readable_versions":[0]}
  }
}
```

This is a curated first-party example: a local source cannot claim reserved publisher `pluxy`.
Only curated first-party native packages are admitted. Put the executable at `bin/example-tool`.
It must implement the README's hello/describe/call/cancel pipe contract; simply printing JSON
is insufficient. Native tools have **full access to your user account** and declare no folder
grants. Optional `app.ui` pages still have no native IPC permissions.

Pack each platform folder using the [archive rules](../../protocol/README.md#release-archives).
Compute exact archive size and SHA-256. Write a UTF-8 descriptor with ID, version, tier and
platform, then sign those exact bytes with the publisher's Ed25519 key. Preserve the original
bytes beside the detached signature. The app's verifier checks both before extraction; changing
an archive byte requires a new digest and signature.

For a mixed package, add provider `protocol` and `programs` at the top level, with optional
top-level provider `ui` and `config`. Leave the app contribution inside `app`. Core runs only
the provider executable and exposes only provider pages/settings. Device installation is separate.

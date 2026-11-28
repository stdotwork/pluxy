# Write an app package

In this tutorial you make a page-only web package, install it in the desktop app and see its page,
then write the manifest of a native tool. The rules behind each step are in the
[app package contract](README.md).

You need the desktop app on Linux: installing a package from a file works only there.

## Make a page-only web package

1. Make a folder `package` and in it `pluxy-extension.json`:

   ```json
   {
     "manifest_version": 1,
     "id": "example/notes",
     "version": "1.0.0",
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

   The package has no provider `protocol` or `programs`, so it is app-only: core refuses it with
   `app-only package: install it in the desktop app`.

2. Make `package/pages/index.html`:

   ```html
   <!doctype html><title>Notes</title><p>My notes page</p>
   ```

3. Pack the folder as a release archive, with the manifest at the top:

   ```bash
   tar -czf notes-1.0.0.tar.gz -C package .
   ```

4. In the desktop app, open **Add-ons** and choose **Install from file…**, then pick
   `notes-1.0.0.tar.gz`.

The **Notes** page now shows in the navigation. On Add-ons the package is labelled **Unverified**,
since it does not come from a curated registry. Its page gets no Tauri commands.

A package installed on this device has no settings: the app serves none, and a page's settings
calls answer `unavailable`. To ask for a file later, add
`"resources":[{"id":"notes","kind":"file","operations":["read"]}]` inside `app`. Declaring it grants
nothing: a grant needs a release signed by its publisher and listed in a registry (a package
installed from a file never gets one), the person's approval of the signer and a native
confirmation ([Access for web packages](README.md#access-for-web-packages)).

## Describe a native tool

A native tool runs a program with full access to your user account, so the app installs one only
from a curated registry, signed by a curated first-party key. You can write its manifest and
program, but you can install it only through such a registry.

1. Make a second folder with this `pluxy-extension.json`:

   ```json
   {
     "manifest_version": 1,
     "id": "pluxy/example-tool",
     "version": "1.0.0",
     "app": {
       "version": 0,
       "tier": "native",
       "host_versions": [0],
       "programs": {"linux/amd64":"bin/example-tool"},
       "operations": [{
         "id":"inspect",
         "description":"Read the selected tool state",
         "effect":"read",
         "input_schema":{"type":"object"},
         "output_schema":{"type":"object"}
       }],
       "state":{"version":0,"readable_versions":[0]}
     }
   }
   ```

   `pluxy` is a reserved publisher: only a curated registry may list it. `state` is required for a
   native tool. A native tool cannot declare `resources`.

2. Put the program at `bin/example-tool`. It must be a native executable, not a script, and speak
   the [app program contract](README.md#app-program-contract-v0): send hello, answer describe with
   exactly the manifest's operations and state version, then answer calls. The Rust library
   [`lib/program`](../../app-tools/README.md#libprogram) implements the program side.

3. Pack one archive per platform under the [archive rules](../../protocol/README.md#release-archives).
   For each, write the descriptor with the archive's exact size and SHA-256
   ([Signed descriptor bytes](README.md#signed-descriptor-bytes)) and sign those exact bytes with the
   publisher's Ed25519 key. Keep the descriptor bytes as they are: the app verifies the signature over
   them, then the archive's size and SHA-256, before it unpacks anything. Changing one byte of the
   archive needs a new descriptor and signature.

Once the registry lists it, the tool installs disabled. Enabling it asks a native confirmation that
says it has full access to your user account; the app then starts its program when a call needs it.

## Make a mixed package

To ship a provider extension and an app part in one package, put the provider `protocol` and
`programs` (and optional `ui` and `config`) at the top level of the manifest and keep the app part
inside `app`. Core runs only the provider program and shows only the provider pages and settings;
the desktop app installs the app part on its own.

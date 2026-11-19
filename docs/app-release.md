# Releasing the desktop app

The desktop app in `next/app` has not been released yet. Until it is, every package is built
with test trust, and the release gate refuses anything that carries it. This page is the
maintainers' checklist for the first release: what stays as it is until then, and the steps that
need the maintainers' own accounts and keys. How the packages and the gate work is in
[`next/app/README.md`](../next/app/README.md) ("Packages", "Validate release inputs" and "The
core the app comes with").

## Until a release

- Test packages (`bun run package:test`) are built with the `artifact-test-trust` feature, an
  updater key made for each build, and the test identity (`dev.pluxy.desktop.test`, product
  `pluxy Test`). Each comes with a core released and signed for that build with a key made in
  memory.
- Windows: no Authenticode signing.
- macOS: no signing identity, so nothing is signed beyond the linker's ad-hoc signature, and
  nothing is notarized.
- The release gate (`bun run package:release [artifact...]`) refuses any test trust, test
  feature, test identity or test file name, and a package whose core does not match the manifest
  built into the app.

## Steps at release

Each step uses the maintainers' accounts. None of their keys, certificates or passwords goes into
the repository.

1. **Windows code signing.** Get an Authenticode certificate and set it as
   `bundle.windows.certificateThumbprint`, or set a `bundle.windows.signCommand`. It must sign
   the app's program, the core's program (`pluxy.exe`), the installer and its uninstaller, all
   with the same certificate.
2. **macOS signing and notarization.** Set a Developer ID Application identity
   (`APPLE_SIGNING_IDENTITY`). The bundler signs with the hardened runtime by default, and signs
   both programs in the app bundle, `pluxy-app` and `pluxy`. Give the bundler notarization
   credentials so it notarizes the app, then notarize and staple the disk image.
3. **The updater's key.** Make a minisign key pair. Put the public key in
   `plugins.updater.pubkey` for release builds, and configure production updater trust so that
   `trust::updater_gate` passes.
4. **The release feed's trust root.** Configure the production root for the https feed and its
   signed descriptors.
5. **Core publisher keys.** Configure production publisher keys for `pluxy/core`, so a core
   source can be trusted. Sign the core release that `next/app/core-release.json` names with that
   key, and run `bundle-core` with it when packaging, so the core the app comes with is checked
   against a production signature. `bundle-core` trusts the key it is given, and the release gate
   checks the core's files against the manifest, not which key signed the release, so this step
   is what keeps a test-signed core out of a release.
6. **The release gate.** The gate still requires the trust files to be empty, because production
   trust is not configured yet. Once steps 3 to 5 add production keys, change the gate so it
   accepts production trust and still refuses test trust. Build the release with
   `PLUXY_BUNDLED_CORE` naming the manifest that `bundle-core` wrote, and run
   `bun run package:release` with every package. It must say `release-package: valid`.
7. **A manual check on a clean Mac and a clean Windows computer**, with each package downloaded
   in a browser:
   - Gatekeeper's first open, and Open Anyway if the app is still not notarized;
   - App Translocation ending after the app is moved in Finder;
   - SmartScreen's prompt for an unsigned or new Windows installer (nobody has checked yet what it
     shows);
   - the first update after each of these;
   - on the Mac, with the notarized build: an update and a rollback from `/Applications` by an
     admin account, and the refusal for a standard account. CI's ad-hoc signed bundle cannot show
     how macOS App Management treats a notarized app.
8. **WebView2.** Keep `bundle.windows.webviewInstallMode` at its default,
   `downloadBootstrapper`.

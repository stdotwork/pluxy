# app-host

The app package contract and tutorial have moved into `docs/`:

- [docs/app-packages.md](../../../docs/app-packages.md): reference for app manifests, archives, signatures, web package grants and app program contract v0.
- [docs/app-package-tutorial.md](../../../docs/app-package-tutorial.md): guide to building a first app package.

The desktop app's host implementation lives in [`../src-tauri/src/`](../src-tauri/src/) (`artifact.rs`, `device.rs`, `program.rs`, `route.rs`, `spawn.rs`), and shared manifest test cases live in [`../../protocol/testdata/app-manifests/`](../../protocol/testdata/app-manifests/).

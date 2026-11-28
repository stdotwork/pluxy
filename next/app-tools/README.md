# app-tools

Native programs that pluxy's desktop app runs on demand, and the shared libraries they use. This folder is a Cargo workspace of its own (its own `Cargo.lock`) and is never linked into the desktop app:

- Setup tools: `claude-code`, `codex`, `claude-desktop`, `opencode`, `omp-setup`, `grok-setup`, `hermes-setup`, `pi-setup`.
- Session, launch, usage and WSL tools: `execution`, `codex-sessions`, `agent-usage` ([README](agent-usage/README.md)), `usage-*`, `wsl`.
- Shared libraries in `lib/`: `files`, `program`, `transactions`, `usage`, `sqlite-ro`, `pi-sessions`.

The reference for every tool and library is in [docs/app-tools.md](../../docs/app-tools.md), and the contract between the app and a tool is in [docs/app-packages.md](../../docs/app-packages.md).

Run checks and builds from `next/app-tools/` (`make tools` from `next/` runs the same checks):

```bash
cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
cargo build --release --locked -p <crate>
```

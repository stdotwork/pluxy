- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Added

- Added an **Approval model** setting to Codex configuration management. Choose Codex default or a specific model, save it to the managed catalog, and let newly discovered models inherit the setting. The selected model must be callable through the current proxy.
- Added an experimental **Remote compaction** toggle for Codex, off by default, to enable server-side context compaction when supported by the upstream service.
- Added an option to use the `openai` provider identity in Codex while continuing to route through CPA, with optional backup and synchronization of local session history when switching.

## Improved

- Quota cards now support model groups with separate usage windows and reset countdowns, with updated plan information and refresh button placement.
- The app now restores its previous window position after restarting.
- Plugin installation requests can now be cancelled using Cancel, Close, or Esc while installation is in progress.

## Fixed

- Usage cost calculation now prefers automatic pricing for the actual original model before falling back to other prices, preventing manual prices from taking precedence unexpectedly.
- Fixed reasoning token counts being squeezed or cut off when the cost column is enabled.
- Fixed input controls squeezing setting labels and descriptions on configuration pages.
- Fixed detection of the DeepSeek Harness desktop app and its bundled CLI on macOS.

## Usage Notes

- Restart the relevant Codex CLI service or desktop app after changing these settings. The remote compaction toggle changes the provider name; if historical sessions disappear, resynchronize them in Session management.

## Selected Previous Updates

## Fixed

- Fixed an issue on Windows where checking the installation status and version of VSCode Copilot could unexpectedly launch VS Code, restore multiple windows, and forcibly terminate those processes when the check timed out. Repeated checks could cause the editor to keep opening and closing.
- Detection now uses the command-line entry point from the matching installation and distinguishes between Stable and Insiders. If the matching CLI is unavailable, detection no longer attempts to launch the editor and reports the version as unknown.

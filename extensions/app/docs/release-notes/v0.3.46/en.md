- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Improved

- Restored the native Windows title bar and window borders, removing the custom title bar and its compatibility patches. Window dragging, minimizing, maximizing, and closing now use system controls.
- Refined the category bar and search field in Advanced settings with shorter controls and more vertical spacing. Selected categories now use highlighted text and a thin underline instead of a filled background.
- Updated the default core version to **8.0.34**.

## Fixed

- Fixed plugin pages shifting left, overlapping the sidebar, or leaving a large blank area at the top when scaling differs. Plugin placement now uses the main WebView's actual client-area dimensions and position.
- Fixed plugin placement not updating after monitor or DPI changes. Plugin bounds are reapplied even when the page's layout dimensions remain unchanged.
- VS Code / Copilot detection on Windows, macOS, and Linux now reads installation and extension metadata directly without running the editor or its command-line entry point, preventing status checks from unexpectedly launching the editor or terminating related processes after a timeout.

## Selected Previous Updates

### Added

- Added an **Approval model** setting to Codex configuration management. Choose Codex default or a specific model, save it to the managed catalog, and let newly discovered models inherit the setting. The selected model must be callable through the current proxy.
- Added an experimental **Remote compaction** toggle for Codex, off by default, to enable server-side context compaction when supported by the upstream service.
- Added an option to use the `openai` provider identity in Codex while continuing to route through CPA, with optional backup and synchronization of local session history when switching.

### Improved

- Quota cards now support model groups with separate usage windows and reset countdowns, with updated plan information and refresh button placement.
- The app now restores its previous window position after restarting.
- Plugin installation requests can now be cancelled using Cancel, Close, or Esc while installation is in progress.

### Fixed

- Usage cost calculation now prefers automatic pricing for the actual original model before falling back to other prices, preventing manual prices from taking precedence unexpectedly.
- Fixed reasoning token counts being squeezed or cut off when the cost column is enabled.
- Fixed input controls squeezing setting labels and descriptions on configuration pages.
- Fixed detection of the DeepSeek Harness desktop app and its bundled CLI on macOS.

### Usage Notes

- Restart the relevant Codex CLI service or desktop app after changing these settings. The remote compaction toggle changes the provider name; if historical sessions disappear, resynchronize them in Session management.

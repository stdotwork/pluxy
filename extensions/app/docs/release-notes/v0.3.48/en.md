- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Fixed

- Fixed a stack overflow error (`Maximum call stack size exceeded`) when opening the log folder from Advanced settings.
- Restored an opaque main window on Linux to address partial blank or transparent content after hovering, as reported on AMD / KDE and NVIDIA / Hyprland. Native translucent themes on Windows and macOS remain supported. [#491](https://github.com/router-for-me/EasyCLIProxyAPI/issues/491)

## Improved

- Reduced background UI work: silent startup, minimizing, and hiding to the tray now synchronize main and plugin WebView visibility and pause UI polling and countdowns. Restoring the window refreshes current data; the proxy core and usage collection continue running. [#493](https://github.com/router-for-me/EasyCLIProxyAPI/issues/493)
- Usage updates now coalesce events, with fallback polling reduced from once per second to once every 10 seconds. Usage update events are no longer sent to the hidden UI, reducing background queries and rendering work.
- Restored default browser background throttling. UI refresh scheduling uses native window visibility and minimized state, so losing focus alone does not pause updates.
- Updated Tauri to stable version 2.12.4 and refreshed related runtime and build dependencies.

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

## Improved

- Models connected to ZCode through CPA now enable image input by default by generating `inputFormat.supportsImage: true`, including models without vision metadata. Existing manual settings are preserved.
- Plugins are now enabled by default for new installations and configurations without an explicit setting. An explicitly disabled plugin configuration remains disabled.
- Refreshed the README screenshots to show the current interface and client configuration pages.

## Usage Notes

- After updating, reapply the ZCode configuration and restart ZCode to use the default image input setting.
- Image input settings allow the client to send images; actual vision support still depends on the selected model and upstream service.

## Fixed

- Fixed inconsistent default plugin states between the generated configuration and the settings page.

## Selected Previous Updates

- v0.3.57 included Pi model catalog synchronization, default image input for DeepSeek Harness, and ZCode reasoning-level configuration fixes.

- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

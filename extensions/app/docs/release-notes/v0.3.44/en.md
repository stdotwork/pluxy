- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Fixed

- Fixed an issue on Windows where checking the installation status and version of VSCode Copilot could unexpectedly launch VS Code, restore multiple windows, and forcibly terminate those processes when the check timed out. Repeated checks could cause the editor to keep opening and closing.
- Detection now uses the command-line entry point from the matching installation and distinguishes between Stable and Insiders. If the matching CLI is unavailable, detection no longer attempts to launch the editor and reports the version as unknown.

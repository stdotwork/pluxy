- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Added

- Added the "渐离の梦境" theme with purple-pink gradients and translucent frosted panels. Choose Starlight, Sakura Sky, or a plain gradient background, or upload and clear your own wallpaper.
- The theme selector now includes a custom theme creation guide. Copy a prompt containing the rules, a complete example, and JSON Schema, then ask an AI to create a theme from your preferences or a reference image.
- The Codex session delete dialog now offers a "Back up before deleting" option, enabled by default. Turn it off to delete directly, with a warning that the deletion cannot be recovered.

## Improved

- Refined desktop cards, borders, shadows, and navigation. Empty API provider categories are less prominent, OAuth account labels have status dots, and Usage statistics cards have a clearer visual hierarchy.
- Kimi quotas now show monthly total and monthly coding quotas with their reset times, plus the plan name when available.
- Reset times and refresh buttons are aligned in quota card footers for a more compact layout.
- Updated the core version to v8.0.48.

## Fixed

- Fixed Kimi quota queries choosing the wrong domain. Queries now select kimi.com or kimi.ai from the credential information, matching Management Center behavior.
- Fixed pages retaining the previous page's scroll position after navigation. Newly opened pages now start at the top.
- Fixed wallpaper changes being applied even when saving failed. Image loading or local storage failures now show an error and preserve the previous display settings.
- Fixed long titles or large selections pushing the Codex session delete dialog outside the window. The content scrolls while the action buttons remain visible.

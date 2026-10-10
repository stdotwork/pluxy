## Added

- If you have any questions or suggestions about the software, please contact us on [Discord](https://discord.gg/PxvX4D9kgs) or submit them through [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues), and we will address them as soon as possible.
- Agents with both a command-line program and a desktop app—Codex, OpenCode, and DeepSeek Harness—can now use separate paths. When automatic detection fails, launch and detection use the matching path.
- A model alias can now be shared by multiple sources. Identical aliases can be edited and deleted independently without overwriting one another.

## Improved

- Redesigned the sidebar with more compact navigation and a clearer current page. Beginner mode and contact are now plain entries, and the language and theme controls share one toolbar.
- Adjusted the model alias list so reasoning levels and Fast badges no longer shift the edit and delete buttons.
- The agent configuration page keeps the selected model. If a refresh temporarily omits it, the selection is not replaced by the first item, and the page notes that it is unavailable.
- The app update progress now shows only the current stage, so the progress text no longer repeats or jumps.
- Renamed session recovery to “Sync Historical Sessions,” and clarified that ChatGPT keeps each API’s conversations separate. Older conversations can be synced from Session Management.

## Fixed

- The API access page now remembers the last selected provider category.
- Saving a model alias now preserves unrelated configuration changes.

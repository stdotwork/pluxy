- If you have any questions or suggestions about the app, please contact us through our [QQ group](https://qm.qq.com/q/3queDaIG) or [GitHub Issues](https://github.com/router-for-me/EasyCLIProxyAPI/issues). We will respond as soon as possible.

## Improvements

- Updated gateway configuration for newer Claude Desktop versions. CPA now explicitly uses static credentials and a specified model list to prevent legacy authentication, automatic configuration, or session service settings from overriding the CPA connection.
- Updating Claude Desktop configuration now correctly switches to the local CPA profile. Restoring a backup also restores the previous configuration selection.
- Added recognition of the Fable and Mythos model families in Claude Desktop configuration.

## Fixes

- Fixed incomplete Claude Desktop model alias handling for xAI, Meta, Vertex, and Interactions API key providers. API key models are no longer incorrectly mapped to OAuth channels, which could make models unavailable.
- Fixed aliases still in use being removed when saving configuration again. Valid existing routes are preserved when multiple providers offer the same model ID, preventing unintended provider changes.
- Fixed disabled or excluded API key models being incorrectly accepted through stale OAuth mappings.
- Improved Claude Desktop configuration checks to detect authentication and configuration source conflicts, as well as incomplete model lists.
- Improved credential protection and rollback handling when saving model mappings, preserving provider keys and connection settings.

## Release Workflow

- Restored automatic Miaomiao preview builds and publication. Non-draft PRs authored by Roomen and targeting `miaomiao` are automatically merged after all six platform builds pass. The merged commit is then built and the rolling preview is updated.

## Usage Notes

- After upgrading, save the Claude Desktop configuration again in CPA, then fully quit and reopen Claude Desktop.
- Model aliases must point to an available source model. If the source model is disabled or excluded, adjust its access configuration or select another available model.

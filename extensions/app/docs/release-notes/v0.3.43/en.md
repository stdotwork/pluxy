## Added

- Added an “Allow Tool Search” toggle to the Codex model editor for per-model configuration. Settings persist across model catalog updates, synchronization, and restarts. This feature requires support from both the model and the upstream provider; disable it if incompatible.

## Fixed

- Fixed official model template matching for model IDs with provider or multi-level path prefixes, preserving the full routing ID while inheriting the corresponding model capabilities.
- Fixed an issue where checking the installation status and version of ZCode could unexpectedly launch the application.

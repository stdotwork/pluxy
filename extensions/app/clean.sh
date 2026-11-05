#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

BIN_DIR="$ROOT_DIR/bin-work"

rm -rf dist
rm -rf src-tauri/target
rm -rf src-tauri/gen

# bin-work/ is also the working folder of the app started by run.sh: it holds that
# app's config.toml, oauth/ credentials, databases, logs and installed cores. Remove
# only the files build.sh (scripts/portable.mjs) copies there.
if [ -d "$BIN_DIR" ]; then
  rm -f -- \
    "$BIN_DIR/EasyCLIProxyAPI" \
    "$BIN_DIR/EasyCLIProxyAPI.exe" \
    "$BIN_DIR/cpa-gui" \
    "$BIN_DIR/cpa-gui.exe" \
    "$BIN_DIR/core-version.txt" \
    "$BIN_DIR/portable-app.json"
  if [ -d "$BIN_DIR/cpa-core" ]; then
    find "$BIN_DIR/cpa-core" -mindepth 1 -maxdepth 1 -type f \
      \( -name 'CLIProxyAPI_*.tar.gz' -o -name 'CLIProxyAPI_*.zip' \) -delete
  fi
fi

echo "Cleaned build output. Kept app data in bin-work/ (config, credentials, databases, logs)."

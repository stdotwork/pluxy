#!/usr/bin/env bash
#
# build.sh - Linux/macOS Build Script
#
# This script automates the process of building and running the Docker container
# with version information dynamically injected at build time.

# Exit immediately if a command exits with a non-zero status.
set -euo pipefail

# --- Step 1: Get Version Information ---
# Get the latest git tag or commit hash as the version string.
VERSION="$(git describe --tags --always --dirty)"

# Get the short commit hash.
COMMIT="$(git rev-parse --short HEAD)"

# Get the current UTC date and time in ISO 8601 format.
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "--- Building with the following info ---"
echo "Version: ${VERSION}"
echo "Commit: ${COMMIT}"
echo "Build Date: ${BUILD_DATE}"
echo "----------------------------------------"

# --- Step 2: Build the Docker Image ---
# Pass the version information as build arguments to 'docker compose build'.
# These arguments are then used by the Dockerfile to inject them into the Go binary.
docker compose build \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  --build-arg BUILD_DATE="${BUILD_DATE}"

# --- Step 3: Start the Services ---
# Start the services in detached mode using the newly built image.
# '--remove-orphans' cleans up any containers for services that are no longer defined.
docker compose up -d --remove-orphans

echo "Build complete. Services are starting."
echo "Run 'docker compose logs -f' to see the logs."
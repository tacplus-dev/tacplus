#!/usr/bin/env bash
# package-macos-sdk.sh — Extract macOS SDK from Xcode, package it, and upload
# to the GitLab package registry for use by the tacplus base image (osxcross).
#
# Run on a Mac with Xcode installed.
#
# Usage:
#   ./scripts/package-macos-sdk.sh
#
# Requires:
#   - Xcode installed
#   - GITLAB_TOKEN env var set (personal access token with api scope)
#     OR glab CLI authenticated

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUT_DIR="$REPO_ROOT/cross-toolchain"
OSXCROSS_DIR="$(mktemp -d)"
trap 'rm -rf "$OSXCROSS_DIR"' EXIT

# --- Preflight ---------------------------------------------------------------

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "error: this script must be run on macOS" >&2
    exit 1
fi

XCODE_DEV="/Applications/Xcode.app/Contents/Developer"
if [[ ! -d "$XCODE_DEV" ]]; then
    # Fall back to xcode-select path
    XCODE_DEV="$(xcode-select -p 2>/dev/null || true)"
    if [[ ! -d "$XCODE_DEV" ]]; then
        echo "error: Xcode not found. Install Xcode from the App Store." >&2
        exit 1
    fi
fi

SDK_DIR="$XCODE_DEV/Platforms/MacOSX.platform/Developer/SDKs"
SDK_PATH="$(ls -d "$SDK_DIR"/MacOSX*.sdk 2>/dev/null | grep -v '.sdk/' | sort -V | tail -1)"
if [[ -z "$SDK_PATH" ]]; then
    echo "error: no macOS SDK found in $SDK_DIR" >&2
    exit 1
fi

SDK_NAME="$(basename "$SDK_PATH")"
SDK_VERSION="${SDK_NAME%.sdk}"
SDK_VERSION="${SDK_VERSION#MacOSX}"
echo "Found SDK: $SDK_NAME (version $SDK_VERSION)"

# --- Clone osxcross (packaging script only) ----------------------------------

echo "Cloning osxcross packaging tools..."
git clone --depth 1 --quiet https://github.com/tpoechtrager/osxcross "$OSXCROSS_DIR"

# --- Package the SDK ---------------------------------------------------------

echo "Packaging SDK (this may take a minute)..."
cd "$OSXCROSS_DIR"
XCODEDIR="$XCODE_DEV" tools/gen_sdk_package.sh

# Find the generated tarball
TARBALL="$(ls "$OSXCROSS_DIR"/MacOSX*.sdk.tar.* 2>/dev/null | head -1)"
if [[ -z "$TARBALL" ]]; then
    echo "error: gen_sdk_package.sh did not produce a tarball" >&2
    exit 1
fi

# --- Move to output ----------------------------------------------------------

mkdir -p "$OUT_DIR"
mv "$TARBALL" "$OUT_DIR/"
FINAL="$OUT_DIR/$(basename "$TARBALL")"

echo ""
echo "SDK packaged: $FINAL"
echo "Size: $(du -h "$FINAL" | cut -f1)"

# --- Upload to GitLab package registry --------------------------------------

GITLAB_PROJECT_ID="${GITLAB_PROJECT_ID:-81965435}"
SDK_FILENAME="$(basename "$FINAL")"

# Resolve token: env var > glab config
if [[ -z "${GITLAB_TOKEN:-}" ]]; then
    GLAB_CONFIG="${HOME}/Library/Application Support/glab-cli/config.yml"
    if [[ -f "$GLAB_CONFIG" ]]; then
        GITLAB_TOKEN="$(grep 'token:' "$GLAB_CONFIG" | head -1 | awk '{print $2}')"
    fi
fi

if [[ -z "${GITLAB_TOKEN:-}" ]]; then
    echo "error: no GitLab token found. Set GITLAB_TOKEN or authenticate with glab." >&2
    exit 1
fi

echo "Uploading $SDK_FILENAME to GitLab package registry..."
HTTP_STATUS=$(curl --silent --output /dev/stderr --write-out "%{http_code}" \
    --header "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
    --upload-file "$FINAL" \
    "https://gitlab.com/api/v4/projects/${GITLAB_PROJECT_ID}/packages/generic/macos-sdk/1/${SDK_FILENAME}")

if [[ "$HTTP_STATUS" == "201" ]]; then
    echo "Uploaded successfully."
elif [[ "$HTTP_STATUS" == "409" ]]; then
    echo "Package version already exists (409). Delete the old version first to re-upload."
else
    echo "error: upload failed with HTTP $HTTP_STATUS" >&2
    exit 1
fi

echo ""
echo "Next: rebuild the tacplus base image to pick up the new SDK."

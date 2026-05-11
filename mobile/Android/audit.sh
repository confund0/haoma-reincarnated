#!/bin/bash
# Privacy audit — runs inside the alpsec bubble via the Makefile.
# Usage: bash audit.sh <apk-path>
set -eu

APK="${1:?missing apk path}"
SDK_ROOT="${SDK_ROOT:-$HOME/sdk}"
export PATH="$SDK_ROOT/jdk/bin:$SDK_ROOT/android/platform-tools:$SDK_ROOT/android/build-tools/35.0.0:$PATH"

echo "==> manifest"
aapt2 dump xmltree --file AndroidManifest.xml "$APK" \
    | grep -E 'uses-permission|provider|receiver' || true

echo "==> telemetry-flagged entries in APK (should be empty)"
unzip -l "$APK" \
    | grep -iE 'firebase|gms|google|analytic|crash|telemetr' \
    || echo "(none — clean)"

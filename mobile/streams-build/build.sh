#!/usr/bin/env bash
#
# Cross-compile libopus + libsodium static archives for the call
# streamers (haoma-mic, haoma-spk). Reads version pins from
# streams-versions.json, drives the Makefile recipe, stages the output
# under prebuilt/<abi>/.
#
# Designed to run inside the alpsec Debian chroot via the bubble. Caller
# (the root Makefile's streams-deps target) handles the bubble wrapper.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
WORK_DIR="$SCRIPT_DIR/work"
PREBUILT_DIR="$SCRIPT_DIR/prebuilt"
VERSIONS_FILE="$SCRIPT_DIR/streams-versions.json"
MAKEFILE_SRC="$SCRIPT_DIR/Makefile"

ABI="${ABI:-arm64-v8a}"
NDK_HOME="${ANDROID_NDK_HOME:-$HOME/sdk/android/ndk/r29}"

j() { python3 -c "import json,sys; d=json.load(open(sys.argv[1])); k=sys.argv[2:]; v=d
for x in k:
    v=v[x]
print(v)" "$VERSIONS_FILE" "$@"; }

pick_version() {
    python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
for k in d:
    if not k.startswith("_"):
        print(k); break
' "$VERSIONS_FILE"
}

STREAMS_VERSION="$(pick_version)"
echo "==> Building streams deps $STREAMS_VERSION for $ABI"

# --- Verify NDK ----------------------------------------------------------
if [ ! -d "$NDK_HOME" ]; then
    echo "FATAL: NDK not found at $NDK_HOME" >&2
    echo "       Set ANDROID_NDK_HOME or unpack NDK r29 there." >&2
    exit 1
fi
EXPECTED_NDK_REV="$(j "$STREAMS_VERSION" ndk revision)"
ACTUAL_NDK_REV="$(sed -n 's,^Pkg.Revision *= *\([^ ]*\),\1,p' "$NDK_HOME/source.properties" 2>/dev/null || true)"
if [ -z "$ACTUAL_NDK_REV" ]; then
    echo "FATAL: $NDK_HOME/source.properties not readable" >&2
    exit 1
fi
if [ "$EXPECTED_NDK_REV" != "$ACTUAL_NDK_REV" ]; then
    echo "FATAL: NDK revision mismatch" >&2
    echo "       expected $EXPECTED_NDK_REV (per streams-versions.json)" >&2
    echo "       got      $ACTUAL_NDK_REV (at $NDK_HOME)" >&2
    exit 1
fi
echo "    NDK: $NDK_HOME (rev $ACTUAL_NDK_REV)"

# --- Stage work/ tree ----------------------------------------------------
mkdir -p "$WORK_DIR" "$PREBUILT_DIR/$ABI"
cp "$MAKEFILE_SRC" "$WORK_DIR/Makefile"

clone_or_fetch() {
    local name="$1" url="$2" commit="$3"
    local dest="$WORK_DIR/$name"
    if [ ! -d "$dest/.git" ]; then
        echo "==> Cloning $name @ $commit (shallow, no submodules)"
        git clone --depth 1 --branch "$commit" --no-recurse-submodules "$url" "$dest"
    else
        echo "==> Fetching $name @ $commit (refresh)"
        git -C "$dest" fetch --depth 1 origin "tag" "$commit" 2>/dev/null \
            || git -C "$dest" fetch --depth 1 origin "$commit"
        git -C "$dest" checkout -f "$commit"
    fi
    git -C "$dest" reset --hard >/dev/null
    git -C "$dest" clean -dffx >/dev/null
}

for component in opus libsodium; do
    URL="$(j "$STREAMS_VERSION" "$component" url)"
    COMMIT="$(j "$STREAMS_VERSION" "$component" commit)"
    clone_or_fetch "$component" "$URL" "$COMMIT"
done

# --- Build ---------------------------------------------------------------
echo "==> Running make all"
export ANDROID_NDK_HOME="$NDK_HOME"
export APP_ABI="$ABI"
export TZ=UTC
export LC_ALL=C.UTF-8
export SOURCE_DATE_EPOCH=1234567890

make -C "$WORK_DIR" all

# --- Stage output --------------------------------------------------------
INSTALL_PREFIX="$WORK_DIR/install/$ABI"
for lib in libopus.a libsodium.a; do
    SRC="$INSTALL_PREFIX/lib/$lib"
    if [ ! -f "$SRC" ]; then
        echo "FATAL: $lib not produced at $SRC" >&2
        exit 1
    fi
done

# Copy headers + archives into prebuilt/<abi>/. Headers needed at
# compile-time by the C++ streamers; archives consumed by the CMake
# Android toolchain in chunk A.2.
rm -rf "$PREBUILT_DIR/$ABI"
mkdir -p "$PREBUILT_DIR/$ABI/lib" "$PREBUILT_DIR/$ABI/include"
cp "$INSTALL_PREFIX/lib/libopus.a"    "$PREBUILT_DIR/$ABI/lib/"
cp "$INSTALL_PREFIX/lib/libsodium.a"  "$PREBUILT_DIR/$ABI/lib/"
cp -r "$INSTALL_PREFIX/include/opus"   "$PREBUILT_DIR/$ABI/include/"
cp    "$INSTALL_PREFIX/include/sodium.h"  "$PREBUILT_DIR/$ABI/include/"
cp -r "$INSTALL_PREFIX/include/sodium"    "$PREBUILT_DIR/$ABI/include/"

SHA_OPUS="$(sha256sum "$PREBUILT_DIR/$ABI/lib/libopus.a"   | awk '{print $1}')"
SHA_SODIUM="$(sha256sum "$PREBUILT_DIR/$ABI/lib/libsodium.a" | awk '{print $1}')"
SIZE_OPUS="$(stat -c%s "$PREBUILT_DIR/$ABI/lib/libopus.a")"
SIZE_SODIUM="$(stat -c%s "$PREBUILT_DIR/$ABI/lib/libsodium.a")"

echo
echo "==> Done."
echo "    libopus.a:   $SIZE_OPUS bytes, sha256 $SHA_OPUS"
echo "    libsodium.a: $SIZE_SODIUM bytes, sha256 $SHA_SODIUM"

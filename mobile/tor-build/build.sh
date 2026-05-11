#!/usr/bin/env bash
#
# Cross-compile Tor + zlib + OpenSSL + libevent for Android.
# Reads version pins from tor-versions.json, drives the vendored
# Briar/Guardian Project Makefile, stages the output binary as
# output/<abi>/libtor.so.
#
# Designed to run inside the alpsec Debian chroot via the bubble. Caller
# (the root Makefile's tor-rebuild target) handles the bubble wrapper.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
WORK_DIR="$SCRIPT_DIR/work"
OUTPUT_DIR="$SCRIPT_DIR/output"
VERSIONS_FILE="$SCRIPT_DIR/tor-versions.json"
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

TOR_VERSION="$(pick_version)"
echo "==> Building Tor $TOR_VERSION for $ABI"

# --- Verify NDK ----------------------------------------------------------
if [ ! -d "$NDK_HOME" ]; then
    echo "FATAL: NDK not found at $NDK_HOME" >&2
    echo "       Set ANDROID_NDK_HOME or unpack NDK r29 there." >&2
    exit 1
fi
EXPECTED_NDK_REV="$(j "$TOR_VERSION" ndk revision)"
ACTUAL_NDK_REV="$(sed -n 's,^Pkg.Revision *= *\([^ ]*\),\1,p' "$NDK_HOME/source.properties" 2>/dev/null || true)"
if [ -z "$ACTUAL_NDK_REV" ]; then
    echo "FATAL: $NDK_HOME/source.properties not readable" >&2
    exit 1
fi
if [ "$EXPECTED_NDK_REV" != "$ACTUAL_NDK_REV" ]; then
    echo "FATAL: NDK revision mismatch" >&2
    echo "       expected $EXPECTED_NDK_REV (per tor-versions.json)" >&2
    echo "       got      $ACTUAL_NDK_REV (at $NDK_HOME)" >&2
    exit 1
fi
echo "    NDK: $NDK_HOME (rev $ACTUAL_NDK_REV)"

# --- Stage work/ tree ----------------------------------------------------
mkdir -p "$WORK_DIR" "$OUTPUT_DIR/$ABI"
cp "$MAKEFILE_SRC" "$WORK_DIR/Makefile"

clone_or_fetch() {
    local name="$1" url="$2" commit="$3"
    local dest="$WORK_DIR/$name"
    if [ ! -d "$dest/.git" ]; then
        # Shallow clone of just the target tag/commit. Briar's recipe
        # full-clones + recursive-submodule-inits, which for openssl
        # 3.x drags in cloudflare-quiche/krb5/pyca-cryptography/etc —
        # 100s of MB of submodules our `no-*` Configure never touches,
        # and a single transient github.com timeout aborts the whole
        # build. We carry only what compiles.
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

for component in zlib openssl libevent tor; do
    URL="$(j "$TOR_VERSION" "$component" url)"
    COMMIT="$(j "$TOR_VERSION" "$component" commit)"
    clone_or_fetch "$component" "$URL" "$COMMIT"
done

# --- Build ---------------------------------------------------------------
echo "==> Running make tor"
export ANDROID_NDK_HOME="$NDK_HOME"
export APP_ABI="$ABI"
export TZ=UTC
export LC_ALL=C.UTF-8
export SOURCE_DATE_EPOCH=1234567890

make -C "$WORK_DIR" tor

# --- Stage output --------------------------------------------------------
TOR_BIN="$WORK_DIR/tor/src/app/tor"
if [ ! -x "$TOR_BIN" ]; then
    echo "FATAL: Tor binary not found at $TOR_BIN" >&2
    exit 1
fi
cp "$TOR_BIN" "$OUTPUT_DIR/$ABI/libtor.so"
SHA="$(sha256sum "$OUTPUT_DIR/$ABI/libtor.so" | awk '{print $1}')"
SIZE="$(stat -c%s "$OUTPUT_DIR/$ABI/libtor.so")"

echo
echo "==> Done."
echo "    File:   $OUTPUT_DIR/$ABI/libtor.so"
echo "    Size:   $SIZE bytes"
echo "    SHA256: $SHA"

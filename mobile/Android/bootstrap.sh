#!/bin/bash
# Userland Android toolchain bootstrap.
#
# Runs every step inside the alpsec Debian chroot via bwrap so that all
# downloaded binaries (JDK, sdkmanager, build-tools, AGP's aapt2 daemon)
# are native Debian glibc — no patchelf, no glibc rootfs, no LD shims.
#
# Idempotent. Re-run after JDK / SDK / Gradle version bumps, or just to
# repair the toolchain dir.
#
# After running, build via `make android` from repo root or
# `cd mobile/Android && make build`.

set -eu

# Project scope: ignore any host proxy env. We talk to dl.google.com,
# adoptium.net, services.gradle.org, maven repos directly.
unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy NO_PROXY no_proxy

ANDROID_DIR="$(cd "$(dirname "$0")" && pwd)"
SDK_ROOT="${SDK_ROOT:-$HOME/sdk}"
mkdir -p "$SDK_ROOT" "$HOME/.gradle" "$HOME/.android"

BUBBLE_LIB="/usr/local/lib/alpsec/bubble.sh"
if [ ! -f "$BUBBLE_LIB" ]; then
    echo "bootstrap: $BUBBLE_LIB not found." >&2
    echo "On Alpine (musl) hosts the build chain runs inside the alpsec Debian chroot." >&2
    echo "On a glibc host you can run this script with USE_BUBBLE=0 to install directly." >&2
    : "${USE_BUBBLE:=1}"
fi

USE_BUBBLE="${USE_BUBBLE:-1}"

# in_chroot CMD ARGS...
# Runs the given command either inside the bubble or directly, with
# $SDK_ROOT, $HOME/.gradle, $HOME/.android and the project root bound in.
in_chroot() {
    if [ "$USE_BUBBLE" = "1" ]; then
        # shellcheck disable=SC1090
        BUBBLE_BINDS=("$SDK_ROOT" "$HOME/.gradle" "$HOME/.android" "$ANDROID_DIR")
        # bubble.sh is bash; we re-exec ourselves through bash to source it.
        BUBBLE_LIB="$BUBBLE_LIB" SDK_ROOT="$SDK_ROOT" ANDROID_DIR="$ANDROID_DIR" \
        bash -c '
            set -eu
            BUBBLE_BINDS=("$SDK_ROOT" "$HOME/.gradle" "$HOME/.android" "$ANDROID_DIR")
            # bubble defaults: no audio, no camera, host net (gradle needs maven).
            . "$BUBBLE_LIB"
            bubble "$@"
        ' bash "$@"
    else
        "$@"
    fi
}

# 1. JDK 21 (Adoptium Temurin Linux x64 — glibc).
JDK_URL="https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.5%2B11/OpenJDK21U-jdk_x64_linux_hotspot_21.0.5_11.tar.gz"
if [ ! -x "$SDK_ROOT/jdk/bin/java" ]; then
    echo "==> JDK"
    in_chroot bash -c "
        set -eu
        cd '$SDK_ROOT'
        curl -fsSLo jdk.tar.gz '$JDK_URL'
        rm -rf jdk
        mkdir -p jdk
        tar xzf jdk.tar.gz -C jdk --strip-components=1
        rm jdk.tar.gz
    "
fi

# 2. Android cmdline-tools + platform-tools + platform-35 + build-tools-35.0.0.
CMDLINE_URL="https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip"
if [ ! -x "$SDK_ROOT/android/cmdline-tools/latest/bin/sdkmanager" ]; then
    echo "==> Android cmdline-tools"
    in_chroot bash -c "
        set -eu
        cd '$SDK_ROOT'
        curl -fsSLo cmdline-tools.zip '$CMDLINE_URL'
        rm -rf android/cmdline-tools
        mkdir -p android/cmdline-tools
        unzip -q cmdline-tools.zip -d android/cmdline-tools
        mv android/cmdline-tools/cmdline-tools android/cmdline-tools/latest
        rm cmdline-tools.zip
    "
fi

if [ ! -d "$SDK_ROOT/android/platforms/android-35" ]; then
    echo "==> Android platform-35 + build-tools + platform-tools"
    in_chroot bash -c "
        set -eu
        export JAVA_HOME=$SDK_ROOT/jdk
        export PATH=\$JAVA_HOME/bin:$SDK_ROOT/android/cmdline-tools/latest/bin:\$PATH
        yes | sdkmanager --licenses >/dev/null 2>&1 || true
        sdkmanager 'platform-tools' 'platforms;android-35' 'build-tools;35.0.0'
    "
fi

# 3. Gradle 8.10.2 (JVM-portable; just used to generate the wrapper jar).
if [ ! -x "$SDK_ROOT/gradle-8.10.2/bin/gradle" ]; then
    echo "==> Gradle"
    in_chroot bash -c "
        set -eu
        cd '$SDK_ROOT'
        curl -fsSLo gradle.zip 'https://services.gradle.org/distributions/gradle-8.10.2-bin.zip'
        unzip -q gradle.zip
        rm gradle.zip
    "
fi

# 4. Regenerate gradle-wrapper.jar — kept out of git, materialised here.
WRAPPER_JAR="$ANDROID_DIR/gradle/wrapper/gradle-wrapper.jar"
if [ ! -f "$WRAPPER_JAR" ]; then
    echo "==> gradle-wrapper.jar"
    in_chroot bash -c "
        set -eu
        export JAVA_HOME=$SDK_ROOT/jdk
        cd $ANDROID_DIR
        $SDK_ROOT/gradle-8.10.2/bin/gradle wrapper --gradle-version=8.10.2 --quiet
    "
fi

echo "Done. Run 'make android' from repo root, or 'cd mobile/Android && make build'."

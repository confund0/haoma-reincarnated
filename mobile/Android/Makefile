.PHONY: bootstrap build release clean install audit test help

SDK_ROOT     ?= $(HOME)/sdk
JAVA_HOME    := $(SDK_ROOT)/jdk
ANDROID_HOME := $(SDK_ROOT)/android
BUILD_TOOLS  := $(ANDROID_HOME)/build-tools/35.0.0
APK          := app/build/outputs/apk/debug/app-debug.apk

# Telemetry kill-switches. See docs/MOBILE-PRIVACY.md.
# Proxy: project scope ignores host HTTP_PROXY/HTTPS_PROXY — we talk to
# dl.google.com / maven repos directly (host proxy is corp/cache-flavoured
# and resolves to a name unreachable inside the chroot anyway).
ENV := JAVA_HOME=$(JAVA_HOME) \
       ANDROID_HOME=$(ANDROID_HOME) \
       ANDROID_SDK_ROOT=$(ANDROID_HOME) \
       PATH=$(JAVA_HOME)/bin:$(ANDROID_HOME)/platform-tools:$(BUILD_TOOLS):$$PATH \
       DO_NOT_TRACK=1 \
       NO_TELEMETRY=1 \
       JAVA_TOOL_OPTIONS=-XX:-FlightRecorder \
       HTTP_PROXY= HTTPS_PROXY= http_proxy= https_proxy= NO_PROXY= no_proxy=

# Detect alpsec bubble. If present, every gradle invocation runs inside the
# Debian chroot — Android binaries run native glibc, no patchelf needed.
# On a glibc host, fall back to direct exec.
BUBBLE_LIB := /usr/local/lib/alpsec/bubble.sh
ANDROID_DIR := $(CURDIR)
ifneq (,$(wildcard $(BUBBLE_LIB)))
    # Use bash -c so we can source bubble.sh and call bubble().
    # BUBBLE_BINDS scopes filesystem reach inside the sandbox.
    IN_BUBBLE = SDK_ROOT="$(SDK_ROOT)" ANDROID_DIR="$(ANDROID_DIR)" bash -c '\
        BUBBLE_BINDS=("$$SDK_ROOT" "$$HOME/.gradle" "$$HOME/.android" "$$HOME/.code-signing" "$$ANDROID_DIR"); \
        . $(BUBBLE_LIB); \
        bubble "$$@"' bash
else
    IN_BUBBLE =
endif

help:
	@echo "Targets:"
	@echo "  bootstrap - install/refresh JDK + Android SDK + Gradle (idempotent)"
	@echo "  build     - debug APK at $(APK)"
	@echo "  release   - unsigned release APK"
	@echo "  install   - adb install -r the debug APK (host-side adb)"
	@echo "  clean     - gradle clean"
	@echo "  audit     - check APK for analytics/telemetry leaks"
	@echo "  test      - run unit tests across all modules"

bootstrap:
	./bootstrap.sh

build:
	$(IN_BUBBLE) sh -c '$(ENV) ./gradlew --no-scan --no-daemon assembleDebug'
	@ls -la $(APK)

release:
	$(IN_BUBBLE) sh -c '$(ENV) ./gradlew --no-scan --no-daemon assembleRelease'

clean:
	$(IN_BUBBLE) sh -c '$(ENV) ./gradlew clean'

install: build
	$(IN_BUBBLE) sh -c '$(ENV) adb install -r $(APK)'

audit:
	@$(IN_BUBBLE) bash audit.sh $(APK)

test:
	$(IN_BUBBLE) sh -c '$(ENV) ./gradlew --no-scan --no-daemon testDebugUnitTest'

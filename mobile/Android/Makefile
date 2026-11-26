.PHONY: bootstrap build release clean install audit test verify-apk help

SDK_ROOT     ?= $(HOME)/sdk
JAVA_HOME    := $(SDK_ROOT)/jdk
ANDROID_HOME := $(SDK_ROOT)/android
BUILD_TOOLS  := $(ANDROID_HOME)/build-tools/35.0.0
APK          := app/build/outputs/apk/debug/app-debug.apk
# The JVM temp dir, the same path as java.io.tmpdir in gradle.properties.
# /tmp is read-only inside some sandboxes, and the JVM does not make the
# dir itself.
JVM_TMP      := build/jvm-tmp

# tools/release.sh passes VERSION=$TAG so the APK's versionName matches
# the git tag stamped into the Go binaries' ldflags. Bare dev builds
# omit it; build.gradle.kts falls back to "dev". Empty + non-blank
# guard so `-PversionName=` (with nothing after the =) never lands.
ifneq ($(strip $(VERSION)),)
    GRADLE_PROPS := -PversionName=$(VERSION)
else
    GRADLE_PROPS :=
endif

# Telemetry kill-switches.
# The build ignores the host's HTTP(S) proxy and reaches dl.google.com and
# the Maven repositories directly.
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
    # verify-apk reads an APK outside this dir (the release artifacts),
    # so it binds the APK's own dir instead of the gradle dirs.
    VERIFY_BUBBLE = SDK_ROOT="$(SDK_ROOT)" APK_DIR="$(dir $(abspath $(APK)))" bash -c '\
        BUBBLE_BINDS=("$$SDK_ROOT" "$$APK_DIR"); \
        . $(BUBBLE_LIB); \
        bubble "$$@"' bash
else
    IN_BUBBLE =
    VERIFY_BUBBLE =
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
	@echo "  verify-apk APK=<absolute path> - apksigner check; the certificate must be in HOWTO-VERIFY.md"

bootstrap:
	./bootstrap.sh

# build and test are quiet like the root targets: one line on success,
# the error lines on failure, the full output in
# tmp/logs/android-<target>.log. V=1 streams it.
V ?= 0
QUIET = $(if $(filter 1,$(V)),,$(ANDROID_DIR)/../../tools/quiet.sh android-$@)

build:
	@mkdir -p $(JVM_TMP)
	@$(QUIET) env $(IN_BUBBLE) sh -c '$(ENV) ./gradlew --no-scan --no-daemon $(GRADLE_PROPS) assembleDebug'

release:
	@mkdir -p $(JVM_TMP)
	$(IN_BUBBLE) sh -c '$(ENV) ./gradlew --no-scan --no-daemon $(GRADLE_PROPS) assembleRelease'

clean:
	@mkdir -p $(JVM_TMP)
	$(IN_BUBBLE) sh -c '$(ENV) ./gradlew clean'

install: build
	$(IN_BUBBLE) sh -c '$(ENV) adb install -r $(APK)'

audit:
	@$(IN_BUBBLE) bash audit.sh $(APK)

test:
	@mkdir -p $(JVM_TMP)
	@$(QUIET) env $(IN_BUBBLE) sh -c '$(ENV) ./gradlew --no-scan --no-daemon testDebugUnitTest'

# Release check: apksigner verifies the APK, then its certificate
# SHA-256 must appear in HOWTO-VERIFY.md (public-only/docs here, docs/
# in the public tree). Full apksigner output in build/verify-apk.txt.
VERIFY_OUT := build/verify-apk.txt
verify-apk:
	@test -f "$(APK)" || { echo "FATAL: $(APK) not found"; exit 1; }
	@mkdir -p build
	@$(VERIFY_BUBBLE) sh -c '$(ENV) apksigner verify --verbose --print-certs "$(abspath $(APK))"' > $(VERIFY_OUT) 2>&1 \
	  || { grep -v '^alpsec:' $(VERIFY_OUT) | head -20; echo "FATAL: apksigner failed (full output: $(VERIFY_OUT))"; exit 1; }
	@D=$$(awk -F': ' '/^Signer #1 certificate SHA-256 digest/{print $$2}' $(VERIFY_OUT)); \
	 test -n "$$D" || { echo "FATAL: no certificate digest in $(VERIFY_OUT)"; exit 1; }; \
	 H=$$(ls ../../public-only/docs/HOWTO-VERIFY.md ../../docs/HOWTO-VERIFY.md 2>/dev/null | head -1); \
	 test -n "$$H" || { echo "FATAL: HOWTO-VERIFY.md not found"; exit 1; }; \
	 grep -q "$$D" "$$H" || { echo "FATAL: certificate $$D is not in $$H"; exit 1; }; \
	 echo "verify-apk: ok (certificate $$D is in $$H)"

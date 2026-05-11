.PHONY: linux linux-release android android-bins android-release tor-rebuild tor-verify clean help

ROOT      := $(CURDIR)
BINS      := $(ROOT)/tmp/bins
ANDROID   := $(ROOT)/mobile/Android
APK_DEBUG   := $(ANDROID)/app/build/outputs/apk/debug/app-debug.apk
APK_RELEASE := $(ANDROID)/app/build/outputs/apk/release/app-release.apk

# Tor cross-compile recipe + the committed binary it produces.
TOR_BUILD     := $(ROOT)/mobile/tor-build
TOR_JNILIBS   := $(ANDROID)/app/src/main/jniLibs
TOR_LOCKFILE  := $(ANDROID)/app/tor-prebuilt.lock
TOR_BUBBLE    := /usr/local/lib/alpsec/bubble.sh

# jniLibs staging dir for cross-compiled Go binaries. Gradle's jniLibs
# srcDir is wired to this path in mobile/Android/app/build.gradle.kts.
# Lives under build/ so it's gitignored + cleaned by `gradle clean`.
# Filenames are libhaoma.so / libhaomad.so even though they're ELF
# executables — Android's PackageManager only extracts files matching
# lib*.so onto disk at applicationInfo.nativeLibraryDir, where SELinux
# permits exec (vs app-private writable storage which is W^X-blocked
# from API 29 onward).
ANDROID_GOBINS := $(ANDROID)/app/build/go-bins/arm64-v8a

GO_LDFLAGS    := -s -w
GO_BUILD_FLAGS = -ldflags="$(GO_LDFLAGS)" -trimpath
export CGO_ENABLED = 0

help:
	@echo "Targets:"
	@echo "  linux         - build all Go binaries into tmp/bins/"
	@echo "  android       - cross-compile haoma + haomad for android-arm64,"
	@echo "                  build APK, stage at tmp/bins/haoma-debug.apk"
	@echo "  android-bins  - cross-compile only (skip APK build)"
	@echo "  android-release - signed release APK (needs HAOMA_RELEASE_* env vars)"
	@echo "  linux-release - shipping subset of linux binaries (used by tools/release.sh)"
	@echo "  tor-rebuild   - cross-compile Tor + deps to libtor.so (slow, ~15 min)"
	@echo "  tor-verify    - re-hash committed libtor.so, fail on lockfile mismatch"
	@echo "  clean         - remove tmp/bins and per-republic build outputs"

linux:
	mkdir -p $(BINS)
	@echo "==> backend binaries"
	cd backend  && go build $(GO_BUILD_FLAGS) -o $(BINS)/haomad        ./cmd/haomad
	cd backend  && go build $(GO_BUILD_FLAGS) -o $(BINS)/haomactl-pair ./cmd/haomactl-pair
	@echo "==> frontend binaries"
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma          ./cmd/haoma
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-text     ./cmd/haoma-text
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-dev-stub ./cmd/haoma-dev-stub
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-vault    ./cmd/haoma-vault
	@echo "==> C++ call streamers"
	$(MAKE) -C streams build
	@ls -la $(BINS)

linux-release:
	mkdir -p $(BINS)
	cd backend  && go build $(GO_BUILD_FLAGS) -o $(BINS)/haomad     ./cmd/haomad
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma      ./cmd/haoma
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-text ./cmd/haoma-text
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-vault ./cmd/haoma-vault
	@echo "==> C++ call streamers"
	$(MAKE) -C streams build
	strip $(BINS)/haoma-mic $(BINS)/haoma-spk
	@ls -la $(BINS)

android-bins:
	mkdir -p $(ANDROID_GOBINS)
	@echo "==> cross-compile haomad → libhaomad.so (android/arm64)"
	cd backend  && GOOS=android GOARCH=arm64 \
	  go build $(GO_BUILD_FLAGS) -o $(ANDROID_GOBINS)/libhaomad.so ./cmd/haomad
	@echo "==> cross-compile haoma → libhaoma.so (android/arm64)"
	cd frontend && GOOS=android GOARCH=arm64 \
	  go build $(GO_BUILD_FLAGS) -o $(ANDROID_GOBINS)/libhaoma.so ./cmd/haoma
	@echo "==> cross-compile haoma-vault → libhaoma-vault.so (android/arm64)"
	cd frontend && GOOS=android GOARCH=arm64 \
	  go build $(GO_BUILD_FLAGS) -o $(ANDROID_GOBINS)/libhaoma-vault.so ./cmd/haoma-vault
	@ls -la $(ANDROID_GOBINS)

android: android-bins
	mkdir -p $(BINS)
	$(MAKE) -C $(ANDROID) build
	cp $(APK_DEBUG) $(BINS)/haoma-debug.apk
	@ls -la $(BINS)/haoma-debug.apk

# Signed release APK. Requires HAOMA_RELEASE_KEYSTORE +
# HAOMA_RELEASE_STORE_PASSWORD + HAOMA_RELEASE_KEY_PASSWORD env vars set
# (gradle's signingConfig in app/build.gradle.kts reads them). Used by
# tools/release.sh; not part of the daily dev loop.
android-release: android-bins
	@test -n "$$HAOMA_RELEASE_KEYSTORE" || (echo "FATAL: HAOMA_RELEASE_KEYSTORE not set"; exit 1)
	mkdir -p $(BINS)
	$(MAKE) -C $(ANDROID) release
	cp $(APK_RELEASE) $(BINS)/haoma-release.apk
	@ls -la $(BINS)/haoma-release.apk

# Cross-compile Tor for arm64-v8a from upstream sources via the
# vendored Briar/Guardian-Project recipe in mobile/tor-build/. Runs
# inside the alpsec Debian chroot (NDK is a glibc-linked toolchain).
# After build success, copies output into the committed jniLibs dir
# and writes the SHA-256 to tor-prebuilt.lock.
tor-rebuild:
	@test -f $(TOR_BUBBLE) || (echo "FATAL: alpsec bubble missing at $(TOR_BUBBLE)"; exit 1)
	@SDK_ROOT="$(HOME)/sdk" REPO_DIR="$(ROOT)" bash -c '\
	    BUBBLE_BINDS=("$$SDK_ROOT" "$$REPO_DIR"); \
	    . $(TOR_BUBBLE); \
	    bubble env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u NO_PROXY -u no_proxy \
	      bash $(TOR_BUILD)/build.sh'
	@mkdir -p $(TOR_JNILIBS)/arm64-v8a
	@cp $(TOR_BUILD)/output/arm64-v8a/libtor.so $(TOR_JNILIBS)/arm64-v8a/libtor.so
	@SHA=$$(sha256sum $(TOR_JNILIBS)/arm64-v8a/libtor.so | awk '{print $$1}'); \
	 NDK=$$(grep -E '^Pkg.Revision' $(HOME)/sdk/android/ndk/r29/source.properties | awk -F'= ' '{print $$2}'); \
	 TOR_VER=$$(python3 -c 'import json; d=json.load(open("$(TOR_BUILD)/tor-versions.json")); print(next(k for k in d if not k.startswith("_")))'); \
	 printf '# Auto-written by `make tor-rebuild`. Do not hand-edit.\n# Verified by `make tor-verify` and CI.\nabi=arm64-v8a\ntor_version=%s\nndk_revision=%s\nlibtor_so_sha256=%s\n' \
	   "$$TOR_VER" "$$NDK" "$$SHA" > $(TOR_LOCKFILE); \
	 cat $(TOR_LOCKFILE)

# Recompute libtor.so SHA-256 and assert it matches tor-prebuilt.lock.
# Cheap CI guard against accidental or malicious replacement of the
# committed binary without going through the rebuild path.
tor-verify:
	@test -f $(TOR_JNILIBS)/arm64-v8a/libtor.so || (echo "FATAL: $(TOR_JNILIBS)/arm64-v8a/libtor.so missing — run 'make tor-rebuild'"; exit 1)
	@test -f $(TOR_LOCKFILE) || (echo "FATAL: $(TOR_LOCKFILE) missing — run 'make tor-rebuild'"; exit 1)
	@EXPECTED=$$(awk -F= '$$1=="libtor_so_sha256"{print $$2}' $(TOR_LOCKFILE)); \
	 ACTUAL=$$(sha256sum $(TOR_JNILIBS)/arm64-v8a/libtor.so | awk '{print $$1}'); \
	 if [ "$$EXPECTED" != "$$ACTUAL" ]; then \
	   echo "FATAL: libtor.so SHA mismatch"; \
	   echo "       expected $$EXPECTED (per tor-prebuilt.lock)"; \
	   echo "       actual   $$ACTUAL"; \
	   exit 1; \
	 fi; \
	 echo "tor-verify: ok ($$ACTUAL)"

clean:
	rm -rf $(BINS)
	-$(MAKE) -C backend clean
	-$(MAKE) -C streams clean
	-$(MAKE) -C $(ANDROID) clean

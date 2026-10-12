.DEFAULT_GOAL := help

.PHONY: linux linux-release android android-bins android-release android-streams tor-rebuild tor-verify streams-deps streams-verify cross-verify clean help test lint fmt check rig wire-corpus

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

# Call-streamer native deps (libopus + libsodium) cross-compile recipe
# + the committed static archives it produces. NOT runtime artifacts;
# linked into haoma-mic / haoma-spk at compile-time.
STREAMS_BUILD     := $(ROOT)/mobile/streams-build
STREAMS_PREBUILT  := $(STREAMS_BUILD)/prebuilt
STREAMS_LOCKFILE  := $(STREAMS_BUILD)/streams-prebuilt.lock

# jniLibs staging dir for cross-compiled Go binaries. Gradle's jniLibs
# srcDir is wired to this path in mobile/Android/app/build.gradle.kts.
# Lives under build/ so it's gitignored + cleaned by `gradle clean`.
# Filenames are libhaoma.so / libhaomad.so even though they're ELF
# executables — Android's PackageManager only extracts files matching
# lib*.so onto disk at applicationInfo.nativeLibraryDir, where SELinux
# permits exec (vs app-private writable storage which is W^X-blocked
# from API 29 onward).
ANDROID_GOBINS := $(ANDROID)/app/build/go-bins/arm64-v8a

# Version stamp injected into every Go binary as main.version via
# -ldflags "-X". Dev builds get `git describe --tags --dirty --always`
# (e.g. v0.0.20-beta-3-g9d2fa11 or v0.0.20-beta-3-g9d2fa11-dirty);
# tools/release.sh overrides with the clean tag (VERSION=$TAG).
# Fallback "dev" keeps things working in a non-git tree (rsync'd
# public tree before tagging, throwaway docker builds, etc).
VERSION       ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
GO_LDFLAGS    := -s -w -X main.version=$(VERSION)
GO_BUILD_FLAGS = -ldflags="$(GO_LDFLAGS)" -trimpath
export CGO_ENABLED = 0

help: ## print this index
	@grep -hE '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

# Everything that has to pass before a commit, in one run. End-to-end
# runs and tests on real devices are not part of it.
check: fmt lint test cross-verify ## run every check a commit must pass

test: ## run the unit tests: backend, frontend, mobile
	$(MAKE) -C backend test
	cd frontend && go test ./... && go test -tags faults ./cmd/haoma
	$(MAKE) -C $(ANDROID) test

lint: ## vet + staticcheck, both Go modules, with and without the faults tag
	cd backend  && go vet ./... && staticcheck ./... && go vet -tags faults ./... && staticcheck -tags faults ./...
	cd frontend && go vet ./... && staticcheck ./... && go vet -tags faults ./... && staticcheck -tags faults ./...

fmt: ## fail when any Go file is unformatted
	@out=$$(gofmt -l backend frontend); \
	if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

# The wire fixture corpus the mobile tests read. A Go test marshals it
# from the daemon's own payload structs. Every ordinary test run only
# checks the committed files and fails on drift; this target is the only
# thing that rewrites them, so a payload change is regenerated on purpose
# and the diff is reviewed.
wire-corpus: ## regenerate the wire fixture corpus the mobile tests read
	cd frontend && go test ./cmd/haoma -run TestWireCorpusIsCurrent -wire.corpus.write

# SCENARIO picks the end-to-end test; it prints its log directory on the
# last line. CASES picks groups inside a scenario (comma-separated).
# CHUTNEY is the nodes dir of a private Tor test network. Either one left
# unset passes no flag at all, and the run is the full one on public Tor.
#
# CASES and CHUTNEY are read from the command line only. One left behind
# in the shell's environment would quietly narrow, or move to another
# network, every later run.
RIG_CASES   = $(if $(filter command line,$(origin CASES)),$(CASES))
RIG_CHUTNEY = $(if $(filter command line,$(origin CHUTNEY)),$(CHUTNEY))

rig: ## run an end-to-end test scenario (SCENARIO, CASES, CHUTNEY)
	cd frontend && go run ./cmd/haoma-rig -scenario $(or $(SCENARIO),sibling)$(if $(RIG_CASES), -cases '$(RIG_CASES)')$(if $(RIG_CHUTNEY), -chutney '$(RIG_CHUTNEY)')

linux: ## build every Go binary into tmp/bins/
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

linux-release: ## the shipping subset of the linux binaries
	mkdir -p $(BINS)
	cd backend  && go build $(GO_BUILD_FLAGS) -o $(BINS)/haomad     ./cmd/haomad
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma      ./cmd/haoma
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-text ./cmd/haoma-text
	cd frontend && go build $(GO_BUILD_FLAGS) -o $(BINS)/haoma-vault ./cmd/haoma-vault
	@echo "==> C++ call streamers"
	$(MAKE) -C streams build
	strip $(BINS)/haoma-mic $(BINS)/haoma-spk $(BINS)/haoma-cam $(BINS)/haoma-vid
	@ls -la $(BINS)

android-bins: ## cross-compile haoma + haomad for android-arm64 only
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

android: android-bins ## cross-compile, build the APK, stage it in tmp/bins/
	mkdir -p $(BINS)
	$(MAKE) -C $(ANDROID) VERSION=$(VERSION) build
	cp $(APK_DEBUG) $(BINS)/haoma-debug.apk
	@ls -la $(BINS)/haoma-debug.apk

# Signed release APK. Requires HAOMA_RELEASE_KEYSTORE +
# HAOMA_RELEASE_STORE_PASSWORD + HAOMA_RELEASE_KEY_PASSWORD env vars set
# (gradle's signingConfig in app/build.gradle.kts reads them). Used by
# tools/release.sh; not part of the daily dev loop.
android-release: android-bins ## signed release APK (needs HAOMA_RELEASE_* in the environment)
	@test -n "$$HAOMA_RELEASE_KEYSTORE" || (echo "FATAL: HAOMA_RELEASE_KEYSTORE not set"; exit 1)
	mkdir -p $(BINS)
	$(MAKE) -C $(ANDROID) VERSION=$(VERSION) release
	cp $(APK_RELEASE) $(BINS)/haoma-release.apk
	@ls -la $(BINS)/haoma-release.apk

# Cross-compile Tor for arm64-v8a from upstream sources via the
# vendored Briar/Guardian-Project recipe in mobile/tor-build/. Runs
# inside the alpsec Debian chroot (NDK is a glibc-linked toolchain).
# After build success, copies output into the committed jniLibs dir
# and writes the SHA-256 to tor-prebuilt.lock.
tor-rebuild: ## cross-compile Tor and its dependencies to libtor.so (~15 min)
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
tor-verify: ## re-hash the committed libtor.so against its lockfile
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

# Cross-compile libopus + libsodium static archives for arm64-v8a from
# upstream sources via mobile/streams-build/. Runs inside the alpsec
# Debian chroot (NDK is a glibc-linked toolchain; autotools needs
# native glibc make / autoconf / clang). After build success, the
# committed archives + headers are at mobile/streams-build/prebuilt/
# and the SHA-256 lockfile is rewritten.
streams-deps: ## cross-compile libopus, libsodium and libvpx for arm64-v8a
	@test -f $(TOR_BUBBLE) || (echo "FATAL: alpsec bubble missing at $(TOR_BUBBLE)"; exit 1)
	@SDK_ROOT="$(HOME)/sdk" REPO_DIR="$(ROOT)" bash -c '\
	    BUBBLE_BINDS=("$$SDK_ROOT" "$$REPO_DIR"); \
	    . $(TOR_BUBBLE); \
	    bubble env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u NO_PROXY -u no_proxy \
	      bash $(STREAMS_BUILD)/build.sh'
	@SHA_OPUS=$$(sha256sum $(STREAMS_PREBUILT)/arm64-v8a/lib/libopus.a | awk '{print $$1}'); \
	 SHA_SODIUM=$$(sha256sum $(STREAMS_PREBUILT)/arm64-v8a/lib/libsodium.a | awk '{print $$1}'); \
	 SHA_VPX=$$(sha256sum $(STREAMS_PREBUILT)/arm64-v8a/lib/libvpx.a | awk '{print $$1}'); \
	 NDK=$$(grep -E '^Pkg.Revision' $(HOME)/sdk/android/ndk/r29/source.properties | awk -F'= ' '{print $$2}'); \
	 OPUS_VER=$$(python3 -c 'import json; d=json.load(open("$(STREAMS_BUILD)/streams-versions.json")); k=next(k for k in d if not k.startswith("_")); print(d[k]["opus"]["commit"])'); \
	 SODIUM_VER=$$(python3 -c 'import json; d=json.load(open("$(STREAMS_BUILD)/streams-versions.json")); k=next(k for k in d if not k.startswith("_")); print(d[k]["libsodium"]["commit"])'); \
	 VPX_VER=$$(python3 -c 'import json; d=json.load(open("$(STREAMS_BUILD)/streams-versions.json")); k=next(k for k in d if not k.startswith("_")); print(d[k]["libvpx"]["commit"])'); \
	 printf '# Auto-written by `make streams-deps`. Do not hand-edit.\n# Verified by `make streams-verify` and CI.\nabi=arm64-v8a\nopus_version=%s\nlibsodium_version=%s\nlibvpx_version=%s\nndk_revision=%s\nlibopus_a_sha256=%s\nlibsodium_a_sha256=%s\nlibvpx_a_sha256=%s\n' \
	   "$$OPUS_VER" "$$SODIUM_VER" "$$VPX_VER" "$$NDK" "$$SHA_OPUS" "$$SHA_SODIUM" "$$SHA_VPX" > $(STREAMS_LOCKFILE); \
	 cat $(STREAMS_LOCKFILE)

# Re-hash committed archives and assert match against streams-prebuilt.lock.
# Cheap CI guard against accidental or malicious replacement.
streams-verify: ## re-hash the committed streams archives against their lockfile
	@test -f $(STREAMS_PREBUILT)/arm64-v8a/lib/libopus.a || (echo "FATAL: libopus.a missing — run 'make streams-deps'"; exit 1)
	@test -f $(STREAMS_PREBUILT)/arm64-v8a/lib/libsodium.a || (echo "FATAL: libsodium.a missing — run 'make streams-deps'"; exit 1)
	@test -f $(STREAMS_PREBUILT)/arm64-v8a/lib/libvpx.a || (echo "FATAL: libvpx.a missing — run 'make streams-deps'"; exit 1)
	@test -f $(STREAMS_LOCKFILE) || (echo "FATAL: $(STREAMS_LOCKFILE) missing — run 'make streams-deps'"; exit 1)
	@EXPECTED_OPUS=$$(awk -F= '$$1=="libopus_a_sha256"{print $$2}' $(STREAMS_LOCKFILE)); \
	 ACTUAL_OPUS=$$(sha256sum $(STREAMS_PREBUILT)/arm64-v8a/lib/libopus.a | awk '{print $$1}'); \
	 EXPECTED_SODIUM=$$(awk -F= '$$1=="libsodium_a_sha256"{print $$2}' $(STREAMS_LOCKFILE)); \
	 ACTUAL_SODIUM=$$(sha256sum $(STREAMS_PREBUILT)/arm64-v8a/lib/libsodium.a | awk '{print $$1}'); \
	 EXPECTED_VPX=$$(awk -F= '$$1=="libvpx_a_sha256"{print $$2}' $(STREAMS_LOCKFILE)); \
	 ACTUAL_VPX=$$(sha256sum $(STREAMS_PREBUILT)/arm64-v8a/lib/libvpx.a | awk '{print $$1}'); \
	 if [ "$$EXPECTED_OPUS" != "$$ACTUAL_OPUS" ]; then \
	   echo "FATAL: libopus.a SHA mismatch"; \
	   echo "       expected $$EXPECTED_OPUS (per streams-prebuilt.lock)"; \
	   echo "       actual   $$ACTUAL_OPUS"; \
	   exit 1; \
	 fi; \
	 if [ "$$EXPECTED_SODIUM" != "$$ACTUAL_SODIUM" ]; then \
	   echo "FATAL: libsodium.a SHA mismatch"; \
	   echo "       expected $$EXPECTED_SODIUM (per streams-prebuilt.lock)"; \
	   echo "       actual   $$ACTUAL_SODIUM"; \
	   exit 1; \
	 fi; \
	 if [ "$$EXPECTED_VPX" != "$$ACTUAL_VPX" ]; then \
	   echo "FATAL: libvpx.a SHA mismatch"; \
	   echo "       expected $$EXPECTED_VPX (per streams-prebuilt.lock)"; \
	   echo "       actual   $$ACTUAL_VPX"; \
	   exit 1; \
	 fi; \
	 echo "streams-verify: ok (opus=$$ACTUAL_OPUS sodium=$$ACTUAL_SODIUM vpx=$$ACTUAL_VPX)"

# Cross-compile the C++ call streamers (haoma-mic + haoma-spk) for
# arm64-v8a, linking against the vendored libopus + libsodium static
# archives from `make streams-deps`. Output: libhaoma-mic.so +
# libhaoma-spk.so under mobile/Android/app/src/main/jniLibs/arm64-v8a/.
# These are PIE ELF executables — the lib*.so naming is what makes
# Android's PackageManager extract them to nativeLibraryDir, where
# exec is permitted (W^X is enforced everywhere else from API 29).
# Runs inside the alpsec bubble: the NDK is glibc-linked. The NDK
# builds with debug info even in Release, so llvm-strip removes it and
# the symbol table (with them the build paths); .dynsym stays.
android-streams: ## cross-compile the four call libraries (mic, spk, cam, vid) into jniLibs
	@test -f $(TOR_BUBBLE) || (echo "FATAL: alpsec bubble missing at $(TOR_BUBBLE)"; exit 1)
	@$(MAKE) streams-verify
	@SDK_ROOT="$(HOME)/sdk" REPO_DIR="$(ROOT)" bash -c '\
	    BUBBLE_BINDS=("$$SDK_ROOT" "$$REPO_DIR"); \
	    . $(TOR_BUBBLE); \
	    bubble env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u NO_PROXY -u no_proxy \
	      bash -c "cd $(ROOT)/streams && \
	        cmake -S . -B build-android \
	          -DCMAKE_TOOLCHAIN_FILE=$$HOME/sdk/android/ndk/r29/build/cmake/android.toolchain.cmake \
	          -DANDROID_ABI=arm64-v8a \
	          -DANDROID_PLATFORM=android-26 \
	          -DCMAKE_BUILD_TYPE=Release && \
	        cmake --build build-android --parallel && \
	        $$HOME/sdk/android/ndk/r29/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip --strip-all \
	          $(addprefix $(ANDROID)/app/src/main/jniLibs/arm64-v8a/libhaoma-,mic.so spk.so cam.so vid.so)"'
	@ls -la $(addprefix $(ANDROID)/app/src/main/jniLibs/arm64-v8a/libhaoma-,mic.so spk.so cam.so vid.so)

# Platforms every Go command has to build for. The gates all run on
# linux/amd64, so a unix-only syscall reaching shared code is invisible
# until somebody builds for macOS or Windows. This target is what makes
# it visible.
CROSS_PLATFORMS := linux/amd64 darwin/amd64 darwin/arm64 windows/amd64

# Build every main package in both Go modules for each platform above.
# Output goes to /dev/null, so nothing lands in the tree. On failure it
# names the platform and the package and keeps going, so one run lists
# every break rather than the first one.
cross-verify: ## build every Go command for linux, macOS and Windows; writes nothing
	@fail=0; \
	 for plat in $(CROSS_PLATFORMS); do \
	   os=$${plat%%/*}; arch=$${plat##*/}; \
	   for mod in backend frontend; do \
	     pkgs=$$(cd $(ROOT)/$$mod && GOOS=$$os GOARCH=$$arch \
	       go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./cmd/...) || exit 1; \
	     for pkg in $$pkgs; do \
	       if ! (cd $(ROOT)/$$mod && GOOS=$$os GOARCH=$$arch go build -o /dev/null $$pkg); then \
	         echo "FATAL: $$os/$$arch does not build $$pkg"; \
	         fail=1; \
	       fi; \
	     done; \
	   done; \
	 done; \
	 if [ $$fail -ne 0 ]; then echo "cross-verify: FAILED"; exit 1; fi; \
	 echo "cross-verify: ok ($(CROSS_PLATFORMS))"

clean: ## remove tmp/bins and the per-module build outputs
	rm -rf $(BINS)
	-$(MAKE) -C backend clean
	-$(MAKE) -C streams clean
	-$(MAKE) -C $(ANDROID) clean

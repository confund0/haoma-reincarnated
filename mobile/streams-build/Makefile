# Cross-compile libopus + libsodium to static archives for the call
# streamers (haoma-mic, haoma-spk). Output is .a archives + headers,
# linked into the C++ streamer binaries at compile-time. NOT runtime
# artifacts — they do NOT go in jniLibs.
#
# Toolchain selection mirrors mobile/tor-build/Makefile (vendored from
# Guardian Project / Briar). Same NDK pin, same ABI shape, same
# reproducible-build env discipline.

EXTERNAL_ROOT := $(shell pwd)

CPU := $(shell uname -m)
ifeq ($(CPU),x86_64)
 NDK_PROCESSOR=x86_64
else
 NDK_PROCESSOR=x86
endif

ifeq ($(APP_ABI), )
 $(error APP_ABI must be set to an NDK-supported ABI for this to run!)
endif

NDK_REVISION := $(shell sed -n 's,^Pkg.Revision *= *\([^ ]*\),\1,p' $(ANDROID_NDK_HOME)/source.properties)
NDK_REQUIRED_REVISION := 29.0.14206865
MIN_NDK_VERSION := 29
ifeq ($(shell test $(MIN_NDK_VERSION) -gt $(firstword $(subst ., ,$(NDK_REVISION))); echo $$?),0)
 $(error NDK r$(MIN_NDK_VERSION) or newer required! r$(NDK_REVISION) is installed at $(ANDROID_NDK_HOME).)
endif
ifneq ($(NDK_REQUIRED_REVISION), $(NDK_REVISION))
 $(warning WARNING: NDK $(NDK_REQUIRED_REVISION) required for reproducible builds, $(NDK_REVISION) is installed.)
endif

# arm64-v8a only for v1; other ABIs would need their own
# `ifneq ($(filter ...),)` blocks (see tor-build/Makefile for the
# template). minSdk on the app is 26, but android-21 is used here to
# match tor-build's pin and keep one toolchain across both recipes.
ifneq ($(filter arm64-v8a, $(APP_ABI)),)
 HOST := aarch64-linux-android
 ALTHOST := $(HOST)
 NDK_ABI := arm64
 NDK_PLATFORM_LEVEL := 21
endif
ifeq ($(HOST), )
 $(error APP_ABI=$(APP_ABI) is not supported by this recipe — see tor-build/Makefile for the multi-ABI template)
endif

NDK_UNAME := $(shell uname -s | tr '[A-Z]' '[a-z]')
NDK_TOOLCHAIN_BASE := $(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/$(NDK_UNAME)-$(NDK_PROCESSOR)

export CC := $(NDK_TOOLCHAIN_BASE)/bin/$(HOST)$(NDK_PLATFORM_LEVEL)-clang
export AR := $(NDK_TOOLCHAIN_BASE)/bin/llvm-ar
export RANLIB := $(NDK_TOOLCHAIN_BASE)/bin/llvm-ranlib
export STRIP := $(NDK_TOOLCHAIN_BASE)/bin/llvm-strip

# Make autotools see the NDK clang and helpers.
PATH := $(NDK_TOOLCHAIN_BASE)/bin:$(PATH)

export TZ := UTC
export LC_ALL := C.UTF-8
export SOURCE_DATE_EPOCH := 1234567890

REPRODUCIBLE_CFLAGS := \
  -fbuild-session-timestamp=$(SOURCE_DATE_EPOCH) \
  -fdebug-compilation-dir . \
  -no-canonical-prefixes \

ALL_CFLAGS := $(REPRODUCIBLE_CFLAGS) $(CFLAGS) -Os -fPIC
export CFLAGS := $(ALL_CFLAGS)

INSTALL_PREFIX := $(EXTERNAL_ROOT)/install/$(APP_ABI)

MAKEFLAGS_PARALLEL := -j$(shell nproc)

.PHONY: all test-setup opus libsodium libvpx clean opus-clean libsodium-clean libvpx-clean showsetup

all: test-setup opus libsodium libvpx

test-setup:
	@test -x $(CC) || (echo "FATAL: $(CC) not executable"; exit 1)
	@printf 'int main(){return 0;}\n' > .test.c
	@$(CC) $(CFLAGS) .test.c -o .test.out
	@rm -f .test.c .test.out

#---------------------------------------------------------------------#
# libopus — Xiph reference Opus codec. Fixed-point build (smaller +
# faster on ARM, opus_encode_float still works against fixed-point
# internals). Extra programs disabled (the streamers don't ship opusdec
# or opusenc CLIs). Doc generation disabled.
opus-build-stamp:
	cd opus && ./autogen.sh
	cd opus && CFLAGS="$(CFLAGS)" ./configure \
	    --host=$(ALTHOST) \
	    --prefix=$(INSTALL_PREFIX) \
	    --disable-shared \
	    --enable-static \
	    --disable-doc \
	    --disable-extra-programs \
	    --enable-fixed-point
	$(MAKE) $(MAKEFLAGS_PARALLEL) -C opus install
	@touch $@

opus: opus-build-stamp

opus-clean:
	-rm -f opus-build-stamp
	-$(MAKE) -C opus uninstall || true
	-$(MAKE) -C opus clean || true
	-cd opus && git clean -fdx > /dev/null 2>&1 || true

#---------------------------------------------------------------------#
# libsodium — modern crypto primitives. Minimal build (drops legacy /
# deprecated algorithms; ChaCha20-Poly1305 IETF — what the streamers'
# common/aead.cpp uses — stays in). PIE off here since the archive is
# linked into binaries that set their own PIE policy via CMake.
libsodium-build-stamp:
	cd libsodium && ./autogen.sh -s
	cd libsodium && CFLAGS="$(CFLAGS)" ./configure \
	    --host=$(ALTHOST) \
	    --prefix=$(INSTALL_PREFIX) \
	    --disable-shared \
	    --enable-static \
	    --enable-minimal \
	    --disable-pie
	$(MAKE) $(MAKEFLAGS_PARALLEL) -C libsodium install
	@touch $@

libsodium: libsodium-build-stamp

libsodium-clean:
	-rm -f libsodium-build-stamp
	-$(MAKE) -C libsodium uninstall || true
	-$(MAKE) -C libsodium clean || true
	-cd libsodium && git clean -fdx > /dev/null 2>&1 || true

#---------------------------------------------------------------------#
# libvpx — WebM project VP8/VP9 reference codec. VP8 only (VP9 deeper
# CPU cost; mobile target favors latency over quality). Realtime build
# drops the slow-but-better quality modes. NDK toolchain picked up via
# CC/AR/RANLIB env exports above; CXX + LD scoped below because libvpx's
# configure runs C++ + linker probes that default to host g++ (opus +
# libsodium don't, hence those blocks don't need this). webm_io + libyuv
# subdirs disabled — both are C++ and we don't use them (raw VP8 frames
# ship over our own AEAD wire; no WebM container, no in-codec YUV
# conversion).
libvpx-build-stamp:
	cd libvpx && \
	    CXX="$(NDK_TOOLCHAIN_BASE)/bin/$(HOST)$(NDK_PLATFORM_LEVEL)-clang++" \
	    LD="$(NDK_TOOLCHAIN_BASE)/bin/$(HOST)$(NDK_PLATFORM_LEVEL)-clang++" \
	    CFLAGS="$(CFLAGS)" \
	    CXXFLAGS="$(CFLAGS)" \
	    ./configure \
	    --target=arm64-android-gcc \
	    --prefix=$(INSTALL_PREFIX) \
	    --disable-shared \
	    --enable-static \
	    --enable-vp8 \
	    --disable-vp9 \
	    --enable-realtime-only \
	    --disable-examples \
	    --disable-tools \
	    --disable-docs \
	    --disable-unit-tests \
	    --disable-webm-io \
	    --disable-libyuv \
	    --extra-cflags="$(CFLAGS)"
	$(MAKE) $(MAKEFLAGS_PARALLEL) -C libvpx install
	@touch $@

libvpx: libvpx-build-stamp

libvpx-clean:
	-rm -f libvpx-build-stamp
	-$(MAKE) -C libvpx clean || true
	-cd libvpx && git clean -fdx > /dev/null 2>&1 || true

clean: opus-clean libsodium-clean libvpx-clean
	rm -rf $(INSTALL_PREFIX)

showsetup:
	@echo "APP_ABI:            $(APP_ABI)"
	@echo "HOST:               $(HOST)"
	@echo "NDK_PLATFORM_LEVEL: $(NDK_PLATFORM_LEVEL)"
	@echo "CC:                 $$CC"
	@echo "AR:                 $$AR"
	@echo "CFLAGS:             $$CFLAGS"
	@echo "INSTALL_PREFIX:     $(INSTALL_PREFIX)"

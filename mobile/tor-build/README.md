# Tor cross-compile recipe (Android)

Builds Tor + its static dependencies (zlib, OpenSSL, libevent) for
Android, producing a single statically-linked `libtor.so` that the
mobile app launches as a child process.

## Why we own this

Avoids a binary-blob runtime dependency on a third-party prebuilt.
Reproducibility comes from the version pins in `tor-versions.json` plus
the (verbatim, BSD-3-attributed) `Makefile` borrowed from Guardian
Project / Briar. We rebuild from upstream sources ourselves.

## Source acquisition

- `Makefile` is **vendored verbatim** from Briar Project's
  [tor-reproducer](https://code.briarproject.org/briar/tor-reproducer)
  (originally Guardian Project's tor-android external/Makefile,
  3-clause BSD). Copyright header preserved.
- `build.sh` is ours — minimal shell orchestration (clone-or-fetch the
  pinned commits, dispatch `make tor`, stage output).
- `tor-versions.json` is a slim single-entry version pin file, same
  schema as Briar's.

## Output

`output/<abi>/libtor.so` — the Tor binary, ELF format, named with the
`lib*.so` prefix so Android's PackageManager extracts it to
`applicationInfo.nativeLibraryDir` on install (the only on-disk path
SELinux permits exec from on API 29+ — see `feedback_android_no_logcat`
project memory).

The committed binary lives in
`mobile/Android/app/src/main/jniLibs/<abi>/libtor.so`. SHA-256 is
recorded in `mobile/Android/app/tor-prebuilt.lock`. CI verifies the
checked-in SHA against the lockfile.

## Bumping versions

1. Edit `tor-versions.json` — bump the `commit` field for whichever
   component(s) move.
2. From repo root: `make tor-rebuild`.
3. Inspect the diff in `mobile/Android/app/src/main/jniLibs/`. Verify
   the new SHA written to `tor-prebuilt.lock`.
4. Commit.

## Pre-reqs

- `~/sdk/android/ndk/r29/` — NDK r29, revision `29.0.14206865`. The
  build script auto-downloads if absent (verifies SHA-256 against the
  pin before unpacking).
- alpsec bubble at `/usr/local/lib/alpsec/bubble.sh` (we run inside
  the Debian chroot for native-glibc autotools + clang).
- Inside chroot: `gcc make pkg-config build-essential autoconf
  automake libtool git curl wget unzip patch perl python3`.

No Docker. No host-side root.

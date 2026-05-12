# Call-streamer deps cross-compile recipe (Android)

Builds the two native libraries the call streamers (`haoma-mic`,
`haoma-spk`) link against — **libopus** (Opus audio codec) and
**libsodium** (ChaCha20-Poly1305 AEAD) — as static archives for
Android arm64-v8a.

## Output shape

`prebuilt/<abi>/`:

- `lib/libopus.a` + `lib/libsodium.a` — static archives the C++
  streamers link against at compile-time.
- `include/opus/*` + `include/sodium.h` + `include/sodium/*` —
  matching headers.

These are **link-time only**. They are NOT runtime artifacts and do
NOT go in `jniLibs/`. The C++ streamers consume them via CMake in
chunk A.2 of M-CALLS-A.

The committed archives' SHA-256 is recorded in `streams-prebuilt.lock`
(committed alongside the binaries). CI verifies it via
`make streams-verify`.

## Why we own this

Same posture as `mobile/tor-build/`: no runtime dependency on a
third-party prebuilt, reproducibility via the version pins in
`streams-versions.json`. NDK pin matches tor-build's so one NDK
satisfies both recipes.

## Bumping versions

1. Edit `streams-versions.json` — bump the `commit` field for
   whichever component(s) move.
2. From repo root: `make streams-deps`.
3. Inspect the diff in `mobile/streams-build/prebuilt/`. Verify the
   new SHA written to `streams-prebuilt.lock`.
4. Commit the lockfile + the updated archives together.

## Pre-reqs

- `~/sdk/android/ndk/r29/` — NDK r29, revision `29.0.14206865`
  (shared with `mobile/tor-build/`).
- alpsec bubble at `/usr/local/lib/alpsec/bubble.sh` (build runs
  inside the Debian chroot for native-glibc autotools + clang).
- Inside chroot: `gcc make pkg-config build-essential autoconf
  automake libtool git python3`.

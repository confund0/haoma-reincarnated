# Call-streamer deps cross-compile recipe (Android)

Builds the native libraries the call streamers (`haoma-mic`,
`haoma-spk`, `haoma-cam`, `haoma-vid`) link against — **libopus**
(Opus audio codec), **libsodium** (ChaCha20-Poly1305 AEAD), and
**libvpx** (VP8 video codec; V-1 of M-CALLS-VIDEO) — as static
archives for Android arm64-v8a.

## Output shape

`prebuilt/<abi>/`:

- `lib/libopus.a` + `lib/libsodium.a` + `lib/libvpx.a` — static
  archives the C++ streamers link against at compile-time.
- `include/opus/*` + `include/sodium.h` + `include/sodium/*` +
  `include/vpx/*` — matching headers.

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

## Linux host build deps (sibling concern — building `streams/` on the host)

The Android cross-compile above stages the libs into `prebuilt/`. The
host-side Linux build of the same streamers (used for dev + tests,
output at `tmp/bins/`) instead pulls deps via system `pkg-config`.
Install once per host:

| Distro | Packages |
|---|---|
| Alpine | `apk add libopus-dev libsodium-dev libvpx-dev pipewire-dev pkgconf socat` |
| Debian / Ubuntu | `apt install libopus-dev libsodium-dev libvpx-dev libpipewire-0.3-dev pkg-config socat` |

`socat` is only needed if you want to drive `streams/smoke.sh` (audio
loopback) or do a cam→vid loopback through a TCP bridge — both are
manual smoke aids, not unit-test deps.

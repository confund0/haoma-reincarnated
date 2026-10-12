# Haoma

A private messenger over Tor.

## Status — BETA

This is a beta cut for an inner-circle test group. The crypto and
transport are well-trodden patterns (Signal Protocol via libsignal,
Tor onion routing) but the application around them has not faced any
external audit. Issues + Discussions on this repo are intentionally
disabled during beta.

Do not use this for high-stakes adversaries yet.

## What it is

- 1:1 chats, end-to-end encrypted (Signal Protocol), over Tor onion
  services. No server in the middle.
- One onion address and its own Tor circuits per contact.
- Pairing with 7 words said out of band, or as a QR code.
- Linked devices: one more phone or desktop on the same identity.
  Messages, edits, reactions, read state, files and the calls you
  made show on both; an incoming call rings on both.
- In a chat: replies, edits and deletes (24 hours), reactions, read
  receipts, disappearing messages, search, presence.
- Files up to 10 MB. Images are compressed without EXIF data unless
  sent as "original".
- Voice calls on Android and the Linux desktop. Video calls on
  Android; the desktop shows video but sends none.
- Backup and restore of a whole device in one archive file.
- Encrypted store at rest (Argon2id, AEAD).
- Android: Tor built in, a calculator disguise, lock levels, PIN,
  idle timeout, notifications that hide the sender and the text.
- Desktop: `haoma-text`, a terminal client for Linux. A desktop app
  with windows, for Linux, Windows and macOS, is being built.

## What it is not

- An account. No phone number, no servers.
- Audited. See **Status** above.
- A group messenger. Chats are 1:1 only.

## Install

A release has these files:

- `haoma-vX.Y.Z-beta-android-arm64.apk` — signed Android app.
- `haoma-vX.Y.Z-beta-linux-amd64.tar.gz` — Linux desktop binaries,
  built on Alpine 3.23.
- `SHA256SUMS` and `SHA256SUMS.minisig` — checksums and their
  signature.

Verify before you install: `docs/HOWTO-VERIFY.md`.

### Android

Needs an arm64 phone, Android 8.0 or newer.

1. Open the GH release page in the phone's browser and download
   `haoma-vX.Y.Z-beta-android-arm64.apk`.
2. Open the downloaded file and install it. Android asks to allow
   installs from the browser once.

   Alternatively, download the APK on a computer, verify it (see
   `docs/HOWTO-VERIFY.md`), and install it with `adb install` or copy
   it to the phone and open it there.
3. Launch — the app presents itself as a calculator. To reveal the
   messenger, hold `[5]` for about 2 seconds until a green dot shows.
   Then, without lifting, slide `7 → 8 → 9 → 6 → 3` and lift.
   No slide? Hold `[1]` until a blue dot shows, tap `7 8 9 6 3`, then
   hold `[=]` for 2 seconds.
4. The passphrase page opens. Tap **Use default passphrase** (the
   default is `good-girls-go-to-heaven`).
5. Change the passphrase and the reveal code in Settings → Security
   once you're in. The keys for the reveal gesture can be changed
   there too (**Unlock keys…**).

Per-GUI manual: `docs/HAOMA-ANDROID.md`.

### Linux desktop (haoma-text), built on Alpine 3.23

`haoma-text`, `haoma`, `haomad` and `haoma-vault` are static and run on
any x86-64 Linux. The call binaries link musl and the libsodium,
libopus, libvpx and PipeWire packages of Alpine 3.23, so they run only
there. On another distro, run them in an Alpine 3.23 container, or
build from source (below).

1. Download `haoma-vX.Y.Z-beta-linux-amd64.tar.gz` from the GH release page.
2. Verify (`docs/HOWTO-VERIFY.md`).
3. Untar, drop the binaries somewhere in `$PATH`. The tarball has
   `haoma-text` (the terminal client), `haoma` and `haomad` (the two
   daemons), `haoma-vault`, and the call binaries `haoma-mic`,
   `haoma-spk`, `haoma-cam`, `haoma-vid`.
4. Install `tor`. `haomad` starts a `tor` of its own; no system tor
   service is needed. To use a running system tor instead, start
   `haoma-text --system-tor`.
5. Run `haoma-text`. On first run it asks for a new master passphrase.
   Empty = the insecure default; change it with `/change-pass`.

Per-GUI manual: `docs/HAOMA-TEXT.md`.

## Build from source

```
make linux            # all binaries → tmp/bins/ (Go + C++ call binaries)
make linux-release    # only the Linux binaries a release ships
make android-streams  # Android call binaries into jniLibs (needed for calls)
make android          # Android debug APK → tmp/bins/haoma-debug.apk
make tor-verify       # check libtor.so against tor-prebuilt.lock
make tor-rebuild      # rebuild libtor.so from upstream sources (slow)
make streams-deps     # rebuild libopus + libsodium + libvpx for arm64 (slow)
make help             # all targets
```

The Linux build needs Go, CMake, pkg-config and the development
packages of libopus, libsodium, libvpx and libpipewire-0.3.

The Android build needs a glibc Linux host (tested on Debian) with
NDK r29 at `~/sdk/android/ndk/r29`. Today `make android-streams`,
`make tor-rebuild` and `make streams-deps` stop unless the maintainer's
build sandbox is installed; running them on a plain glibc host is open
work. See `mobile/tor-build/README.md` for the Tor build recipe and
`mobile/streams-build/README.md` for the call libraries.

## License

AGPL-3.0-or-later. See `LICENSE`.

Embedded Tor is upstream Tor (BSD-3-Clause). The build recipe under
`mobile/tor-build/Makefile` is borrowed verbatim from Briar's
`tor-reproducer` (originally Guardian Project's `tor-android`,
BSD-3-Clause); attribution preserved in the file header.

Other third-party parts:

- `backend/internal/bencode/` is copied from anacrolix/torrent
  (MPL-2.0; see the `LICENSE` file there).
- The pairing words are the EFF Short Wordlist (CC-BY-3.0; see
  `backend/internal/pair/effshort_LICENSE.md`).
- The Android call binaries link static libopus (BSD-3-Clause),
  libsodium (ISC) and libvpx (BSD-3-Clause), built from upstream
  sources under `mobile/streams-build/`.

## Security disclosure

See `SECURITY.md`.

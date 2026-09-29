# Haoma

A private messenger over Tor.

## Status — BETA

This is a beta cut for an inner-circle test group. The crypto and
transport are well-trodden patterns (Signal Protocol via libsignal,
Tor onion routing) but the application around them has not faced any
external audit. Issues + Discussions on this repo are intentionally
disabled during beta — feedback flows through the tester channel.

Do not use this for high-stakes adversaries yet.

## What it is

- 1:1 messages, end-to-end encrypted (Signal Protocol Double Ratchet),
  sent over Tor onion services. No server in the middle.
- One onion address per pairing. No shared global address. Each
  pairing also gets its own Tor circuits. `/rotate-tor` moves one
  chat to a new onion address.
- Pairing with 7 EFF-short words, said out-of-band. The phone can
  scan the words as a QR code; the inviter's phone or desktop shows it.
- In a chat: replies, edits and deletes of your own messages (last
  24 hours), reactions, read receipts (can be turned off per chat),
  disappearing messages (a timer per chat), search, presence
  (available / away / busy).
- Files up to 10 MB, with an optional caption. Images are sent
  compressed by default, and compressing drops the EXIF data;
  "original" sends the file as it is.
- Calls: voice calls on Android and on the Linux desktop. Video calls
  on Android. The desktop can receive video (shown with `ffplay`),
  but it has no camera support yet.
- Backup and restore: one archive file with all data of a device.
  Restore needs the passphrase of the device that made the backup.
- Encrypted store at rest. Argon2id key derivation; AEAD block-based
  encryption.
- Android app with a calculator disguise. A reveal gesture on the
  keypad strips the disguise and shows the messenger. Lock levels
  (soft / safe / hard), a PIN, an idle timeout, and notifications
  that can hide the sender and the text.
- Tor is built into the Android APK — no Termux, no Orbot, no system
  tor needed on the phone.

## What it is not

- A phone-number-based messenger. There are no accounts, no servers,
  no metadata you don't carry.
- Audited. See **Status** above.
- A group messenger. Chats are 1:1 only.
- A drop-in Signal/WhatsApp replacement. The threat model is closer
  to Briar's: real-world adversaries you can name + identity hygiene
  you can practice.

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
   `haoma-text` (the terminal UI), `haoma` and `haomad` (the two daemons),
   `haoma-vault`, and the call binaries `haoma-mic`, `haoma-spk`,
   `haoma-cam`, `haoma-vid`.
4. A system `tor` must be running: SOCKS on `127.0.0.1:9050`, control
   port on `127.0.0.1:9051`. Cookie auth works as is; for a control
   password use Settings → Tor or `/set-tor-password`.
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

The Android build needs the alpsec bubble + Debian chroot OR a glibc
host with NDK r29 at `~/sdk/android/ndk/r29`. `make android-streams`,
`make tor-rebuild` and `make streams-deps` run only in the alpsec
bubble. See `mobile/tor-build/README.md` for the Tor build recipe and
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

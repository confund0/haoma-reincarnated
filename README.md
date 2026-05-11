# Haoma

A private messenger over Tor.

## Status — BETA

This is a beta cut for an inner-circle test group. The crypto and
transport are well-trodden patterns (Signal Protocol via libsignal,
Tor onion routing) but the application around them has not faced any
external review. Issues + Discussions on this repo are intentionally
disabled during beta — feedback flows through the tester channel.

Do not use this for high-stakes adversaries yet.

## What it is

- 1:1 messages, file attachments, voice calls (desktop), all routed
  end-to-end-encrypted (Signal Protocol Double Ratchet) over Tor
  ephemeral onion services.
- Per-peer dedicated onion identities. No shared global address.
- Pairing via 7 EFF-short words exchanged out-of-band.
- Vault-encrypted at-rest store. Argon2id key derivation; AEAD
  block-based encryption.
- Disguise-skin Android app with a calculator cover surface; reveal
  gesture strips the disguise to expose the messenger.
- Native Tor binary embedded in the Android APK — no Termux, no
  Orbot, no system-tor required.

## What it is not

- A phone-number-based messenger. There are no accounts, no servers,
  no metadata you don't carry.
- Audited. See **Status** above.
- A drop-in Signal/WhatsApp replacement. The threat model is closer
  to Briar's: real-world adversaries you can name + identity hygiene
  you can practice.

## Install

### Android

1. Pull `haoma-vX.Y.Z-beta-android-arm64.apk` from the GH release page.
2. Verify (see `docs/HOWTO-VERIFY.md`).
3. `adb install` (or sideload via your phone's package installer).
4. Launch — the app presents itself as a calculator. Slide pattern
   `78963` starting on `[5]` to reveal the messenger surface.
5. First-launch passphrase: `good-girls-go-to-heaven`. Change it in
   Settings → Lock once you're in.

Per-GUI manual: `docs/HAOMA-ANDROID.md`.

### Linux desktop (haoma-text)

1. Pull `haoma-vX.Y.Z-beta-linux-amd64.tar.gz` from the GH release page.
2. Verify (`docs/HOWTO-VERIFY.md`).
3. Untar, drop the binaries somewhere in `$PATH`.
4. `tor` daemon must be running locally (system tor — Briar/Termux/
   stock distro all work).
5. Run `haoma-text` to launch the TUI supervisor.

Per-GUI manual: `docs/HAOMA-TEXT.md`.

## Build from source

```
make linux         # host binaries → tmp/bins/
make android       # Android debug APK → tmp/bins/haoma-debug.apk
make tor-rebuild   # rebuild libtor.so from upstream sources (slow)
```

The Android build needs the alpsec bubble + Debian chroot OR a glibc
host with NDK r29 at `~/sdk/android/ndk/r29`. See `mobile/tor-build/README.md`
for the Tor build recipe.

## License

AGPL-3.0-or-later. See `LICENSE`.

Embedded Tor is upstream Tor (BSD-3-Clause). The build recipe under
`mobile/tor-build/Makefile` is borrowed verbatim from Briar's
`tor-reproducer` (originally Guardian Project's `tor-android`,
BSD-3-Clause); attribution preserved in the file header.

## Security disclosure

See `SECURITY.md`.

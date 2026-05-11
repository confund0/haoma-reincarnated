# Security

## Reporting

This is a beta. If you find a vulnerability:

- **Do not file a public issue.** Issues are disabled on this repo
  during beta anyway, but for everyone's sake — including the inner
  circle running this build — please use the private channel.
- Reach the maintainer at the address listed on the
  `confund0` GitHub profile or in the tester channel.
- For non-trivial findings, embargo the public disclosure until
  there's a fix in the next release.

## Scope

- The Go core: `backend/`, `frontend/`.
- The Android app: `mobile/Android/`.
- The Tor cross-compile recipe: `mobile/tor-build/`.

Out of scope for now:
- Cryptographic protocol design (Signal Protocol via libsignal, Tor
  ephemeral onion services). Both have established threat models +
  separate disclosure channels.
- The vendored upstream Tor binary (`mobile/Android/app/src/main/jniLibs/<abi>/libtor.so`).
  Reproducible from source via `make tor-rebuild`; report Tor-specific
  issues to the Tor Project.

## What this beta does NOT defend against

- Adversaries with code-execution on your device. The vault is
  encrypted at rest; once unlocked, secrets live in process memory.
- Network adversaries who can correlate Tor circuits at scale (i.e.
  global passive observers). Tor's anonymity guarantees apply.
- Forensic analysis of an unlocked device. The disguise skin is layer
  1 (casual inspection). It does not stand up to a sophisticated
  examiner with `adb` access.

## Threat model snapshot

Two-layer:

- **Layer 1** — physical-inspection of the device (partner / clergy /
  customs glance). Calculator disguise + reveal pattern.
- **Layer 2** — network-grade adversary trying to learn who you talk
  to. Per-peer Tor onions, no metadata server, no shared address.

Activist-grade panic + retention enforcement is a planned post-beta
slice; not in this build.

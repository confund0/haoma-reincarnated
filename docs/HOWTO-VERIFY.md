# Verifying a release artifact

Each Haoma release is signed with a [minisign](https://jedisct1.github.io/minisign/)
keypair held outside the repo. The public key is in this file (below)
and pinned in the tester channel. Two checks:

1. **Integrity** (the file you downloaded matches what the maintainer
   listed):

   ```
   sha256sum -c SHA256SUMS
   ```

   Drop `haoma-vX.Y.Z-beta-android-arm64.apk` and `SHA256SUMS` in the
   same directory. Output should be `OK` per file. If a file is
   missing, `sha256sum -c` prints `FAILED open or read` for it; that's
   fine if you only downloaded a subset — focus on the lines that say
   the artifact you grabbed.

2. **Authenticity** (the listing itself was published by the
   maintainer, not someone replaying a tampered SHA256SUMS):

   ```
   minisign -Vm SHA256SUMS -P RWQ...the_real_key_above
   ```

   `Signature and comment signature verified` = good.

If either check fails: do not install. Re-fetch from the canonical
source and re-verify. If it still fails, ping the tester channel.

## Public key (minisign)

```
untrusted comment: minisign public key AF4BEAE2C16E57CB
RWTLV27B4upLrxQoPpqVNFf19XhDzKpOdi7KnCEdKluiXdqFH7WfF2Hk
```

The same key signs every beta release until rotated. Rotation events
will be announced in the tester channel + a `KEY-ROTATION-YYYY-MM-DD.md`
file added at repo root.

## Why both?

`SHA256SUMS` alone proves the file matches the listing. It does not
prove the listing is what the maintainer wrote — anyone who modifies
both files in transit defeats it. The minisign signature is the trust
anchor on the listing itself.

## On Android

The APK is also signed by the Android app-signing key, separately
from the minisign signature. Android verifies that signature on
install — if the APK was tampered with, the install fails. The
minisign sig adds a "and the maintainer publishes only this APK
under this version tag" guarantee on top.

Verify the APK's signing cert against the published fingerprint:

```
apksigner verify --print-certs haoma-vX.Y.Z-beta-android-arm64.apk
```

(`apksigner` ships with the Android build-tools; on Debian/Ubuntu
`apt install apksigner`.) The output's `Signer #1 certificate SHA-256
digest` must match:

```
B7:5F:BF:7E:D4:DD:F3:01:BA:A7:1E:21:CE:58:59:33:16:97:56:9B:F3:78:9D:93:52:48:49:F2:1B:A5:0B:E2
```

This fingerprint is the Android trust anchor — it stays the same
across every Haoma release. Once your device has installed an APK
signed by this key, Android refuses to upgrade with any APK signed
by a different key. **Mismatch = do not install.**

## Tools

- Debian / Ubuntu: `apt install minisign coreutils`
- Alpine: `apk add minisign coreutils`
- macOS: `brew install minisign`
- Windows: prebuilt binary on the minisign GitHub releases page.

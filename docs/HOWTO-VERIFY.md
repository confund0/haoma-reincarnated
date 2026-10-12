# Verifying a release artifact

Each Haoma release is signed with a [minisign](https://jedisct1.github.io/minisign/)
keypair held outside the repo. The public key is in this file (below).
Two checks:

1. **Integrity** (the file you downloaded matches what the maintainer
   listed):

   ```
   sha256sum -c SHA256SUMS
   ```

   Drop the file you downloaded (`haoma-vX.Y.Z-beta-android-arm64.apk`
   or `haoma-vX.Y.Z-beta-linux-amd64.tar.gz`) and `SHA256SUMS` in the
   same directory. Output should be `OK` per file. If a file is
   missing, `sha256sum -c` prints `FAILED open or read` for it; that's
   fine if you only downloaded a subset — focus on the lines that say
   the artifact you grabbed.

2. **Authenticity** (the listing itself was published by the
   maintainer, not someone replaying a tampered SHA256SUMS):

   ```
   minisign -Vm SHA256SUMS -P RWRSYy7KY3S9eib8l8aONF2AeyqP1vJLgtoqHdz2FPUARhz1xMMBlG6d
   ```

   `Signature and comment signature verified` = good.

If either check fails: do not install. Re-fetch from the canonical
source and re-verify.

## Public key (minisign)

```
untrusted comment: minisign public key 7ABD7463CA2E6352
RWRSYy7KY3S9eib8l8aONF2AeyqP1vJLgtoqHdz2FPUARhz1xMMBlG6d
```

This key signs every release from v0.1.0-beta on.

## Keys up to v0.0.35-beta

The signing keys changed with v0.1.0-beta. Releases up to
v0.0.35-beta verify against the earlier keys:

- minisign: `RWTLV27B4upLrxQoPpqVNFf19XhDzKpOdi7KnCEdKluiXdqFH7WfF2Hk`
- Android certificate SHA-256:
  `b75fbf7ed4ddf301baa71e21ce5859331697569bf3789d93524849f21ba50be2`
  (`B7:5F:BF:…:0B:E2` in the colon form)

Android installs an update only when it is signed with the same key
as the installed app. So v0.1.0-beta cannot be installed over an
older release. To move over: in the app, make a backup; uninstall the
app; install v0.1.0-beta; restore the backup. After the restore,
delete the backup file.

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
170942d15e341bc3bd1c622e71647302eb322e62ce65a25574aa022b649361b2
```

The same value in the colon form that `keytool` prints:

```
17:09:42:D1:5E:34:1B:C3:BD:1C:62:2E:71:64:73:02:EB:32:2E:62:CE:65:A2:55:74:AA:02:2B:64:93:61:B2
```

This fingerprint is the Android trust anchor from v0.1.0-beta on. It
stays the same across every later release. Once your device has
installed an APK signed by this key, Android refuses to upgrade with
any APK signed by a different key. **Mismatch = do not install.**

## Tools

- Debian / Ubuntu: `apt install minisign coreutils`
- Alpine: `apk add minisign coreutils`
- macOS: `brew install minisign`
- Windows: prebuilt binary on the minisign GitHub releases page.

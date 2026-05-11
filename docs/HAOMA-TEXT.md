# haoma-text — TUI manual

`haoma-text` is the desktop client. Terminal UI, supervises both
daemons (`haomad` backend + `haoma` frontend), opens the encrypted
vault, exposes a windowed chat surface.

## Runtime dependencies

The Linux tarball ships **six** binaries: the four messenger ones
(`haomad`, `haoma`, `haoma-text`, `haoma-vault`) plus two C++ call
streamers (`haoma-mic`, `haoma-spk`) that `haoma-text` exec's during
voice calls.

The streamers dynamically link against `libsodium`, `libopus`, and
`libpipewire-0.3`. Install per your distro:

- Alpine: `apk add libsodium opus pipewire`
- Debian / Ubuntu: `apt install libsodium23 libopus0 libpipewire-0.3-0`
- Fedora / RHEL: `dnf install libsodium opus pipewire-libs`

The binaries are built against **musl libc** (Alpine). On glibc
distros they need either `gcompat`/`apk-tools` shims or a fresh build
from source (`make linux-release` in the repo). The messenger
binaries themselves are pure Go and run anywhere.

## First launch

1. `tor` daemon must be running on `127.0.0.1:9050` (SOCKS) and
   `127.0.0.1:9051` (control). Most distros ship a
   `tor.service` you `systemctl start`.
2. `haoma-text` will mint a fresh vault on first run. You'll be
   prompted for a passphrase. Pick something memorable but real —
   no recovery if you forget.
3. After unlock the TUI lands on the system status window.

## Slash commands

Type `/help` in the input pill for the live list. Highlights:

- `/invite-tor` — start a pairing-via-onion exchange. Prints 7
  EFF-short words. Read them out-of-band (Signal, voice, paper) to
  the peer who's accepting.
- `/accept-tor` — accept a peer's 7-word invite.
- `/contacts` — open the contacts pane.
- `/chats` — open the conversations pane.
- `/files` — receiver-side picker for incoming attachments.
- `/attach` — sender-side picker.
- `/rotate-tor` — kick a per-peer onion rotation.
- `/nick <text>` — set your displayed name.
- `/lock` — soft-lock the UI (idle-action shape; daemons stay alive).
- `/quit` — clean shutdown.

## Window navigation

- `Esc N` — switch to window `N` (0–9).
- `Tab` / `Shift-Tab` — cycle within forms.
- `Enter` — submit / activate.
- `Ctrl-C` — clean exit (same as `/quit`).

## Daemons

`haoma-text` spawns and supervises:

- `haoma` — UI-attached frontend. Owns Signal sessions, timeline
  cache, IPC server. Dies with the supervisor.
- `haomad` — detached backend. Owns Tor onions, message routing,
  encrypted store. Survives a UI restart so messages keep landing.
  Reaped on hard-lock or panic.

Logs land at `~/.haoma/{frontend,backend}/*.log`. Set
`--log-level debug` to make them chatty.

## Notable settings

- **Threat profile** — Domestic / Privacy / Activist (the last is
  planned, not active in this build). Drives the idle-action,
  retention, and panic-action defaults.
- **Idle action** — `soft-lock` (UI hidden, daemons alive),
  `safe-lock` (haoma dies, haomad keeps draining queue),
  `hard-lock` (both daemons die, vault re-seal required).
- **Tor password** — set in Settings → Tor if your local Tor uses
  HASHEDPASSWORD auth. Cookie-auth is auto-detected; no setting
  needed there.

## Pairing recipe

On Alice (inviter):

```
/invite-tor alice
```

`haoma-text` shows 7 words, e.g. `crisp · rather · vague · solar · goblin · pearl · hatred`.
Read them to Bob over a separate trusted channel.

On Bob (joiner):

```
/accept-tor crisp rather vague solar goblin pearl hatred bob
```

Both ends now share a peer-pair onion + libsignal Double Ratchet
session. Subsequent messages route directly over Tor — no shared
server, no common address.

## Wire format

`haoma-text` ↔ `haoma` over a loopback TLS-1.3 socket pinned via
self-signed cert + bearer token. Both files at
`~/.haoma/frontend/{cert.pem,token}`, 0600. `haoma-text` mints them
on first run.

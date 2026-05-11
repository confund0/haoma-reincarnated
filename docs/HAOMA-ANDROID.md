# Haoma — Android manual

The Android build presents itself as a calculator. There is no
launcher icon labelled "Haoma" — that's by design. Casual inspection
opens an actual working calculator. Slide a pattern to reveal the
messenger.

## First launch

1. Sideload the APK (verify it first — see `HOWTO-VERIFY.md`).
2. Open the app — you'll see a gruvbox-dark calculator. Try `2+2=`
   to confirm it works as advertised.
3. To reveal the messenger:
   - Press and hold `[5]` for ~2 seconds. A small green dot lights
     up in the display = "armed".
   - Without lifting, slide across the keypad in the pattern
     `7 → 8 → 9 → 6 → 3` (the default). Lift = submit.
4. The disguise strips. You'll be prompted for a passphrase. The
   shipped default is `good-girls-go-to-heaven`. Tap **Use default
   passphrase** to one-tap it.
5. Change the passphrase + the unlock pattern from Settings → Lock
   once you're in.

If you fat-finger the slide, the calc silently reverts. No counter,
no error. Re-do the 2-second hold to retry.

## Tabs

Bottom navigation, five tabs, icon-only:

- **Conversations** — chat-row list, tap to open the chat window,
  tap **Edit** for per-chat settings.
- **Contacts** — paired peers + presence + Edit link to the contact
  detail page.
- **Invites** — start or accept a pairing exchange.
- **Status** — system status chips + scrolling event log + a slash
  CLI for ops commands.
- **Settings** — seven sub-sections: Profile, Notifications, Chat
  defaults, Tor, Files, Lock, Advanced.

## Pairing

Inviter:
1. Invites tab → optionally set an alias → **Tor**.
2. Wait for the 7-word block to appear (a "publishing" pill spins
   while Tor's onion gets circulated). Read the words aloud to the
   joiner — Signal, voice, paper, whatever your trusted side-channel
   is.

Joiner:
1. Invites tab → **Accept** → **Tor**.
2. Type the 7 words + your alias for them → **Submit**.
3. Round-trips through Tor; you'll see the new chat appear in
   Conversations within 30s typically.

## Lock postures

- **Soft-lock** — screen-off / app-swiped-away. Cover surface returns;
  both daemons keep running so messages keep arriving in the
  background. Re-reveal = slide pattern.
- **Safe-lock** — same as soft-lock on this build (mobile collapses
  the two; full safe-lock partial-teardown is post-beta wishlist).
- **Hard-lock** — both daemons die. No notifications until you
  passphrase-unlock again. Triggered by panic-action,
  hard-lock-on-idle (off by default), or boot.

When you cold-boot the phone, the app starts in hard-lock state.
First reveal slide → passphrase page.

## Notifications

Plain title + body, no peer name in metadata. Per-app channel called
"Haoma messages" (visible in system Settings → Notifications).
Visibility is `SECRET` so they don't show on the lock-screen.

If you set Settings → Notifications → **Disguise mode** on AND turn
both Show toggles off, the notification banner shows a math tip
instead of the real message body. Tapping the banner opens the calc
surface and overlays the tip; another tap dismisses.

## Tor

Embedded. The APK ships a cross-compiled Tor binary at
`lib/<abi>/libtor.so`. `haomad` spawns it with auto-allocated control
+ SOCKS ports per launch (no clash with Orbot or a Termux-side Tor
you may also run). Cookie auth, no password required.

The Tor data directory lives at
`/data/user/0/io.haoma.calculator/files/haoma/tor/`. If you want to
pull the Tor log for diagnostics:

```
adb shell run-as io.haoma.calculator cat files/haoma/tor/tor.log
```

## Logs

App-side logs at:
- Debug build: `/sdcard/Android/data/io.haoma.calculator/files/logs/`
- Release build: `getFilesDir()/logs/` (gated by Settings → Diagnostics).

Per-component:
- `haoma-gui.log` — the Compose UI + supervisor.
- `haoma.log` — the frontend daemon (Signal sessions + IPC).
- `haomad.log` — the backend daemon (Tor + outbox).
- `haoma-vault.log` — vault open/reseal subprocess.

## Files / attachments

- Send: chat input → tap `+` → system file picker.
- Receive: chat window burger menu → **View files** → action modal
  per row (Save as / Open / Delete).
- Maximum 10 MB per file (v0 cap).
- Sent images are EXIF-stripped before upload (re-encoded on the
  device).

## Settings reference

- **Profile** → self-nick. Empty = peer sees a default placeholder.
- **Notifications** → notify-on / show-sender / show-body /
  on-lock-screen / disguise mode.
- **Chat defaults** → default retention, default send-receipts. Per-
  chat overrides via Conversations → Edit.
- **Tor** → presence indicator + change Tor password (only relevant
  when not using the embedded Tor — i.e., never on Android today).
- **Files** → informational read-only on Android. The system file
  picker handles the WHERE.
- **Lock** → threat-preset bundle (Domestic / Privacy) +
  individually-tunable idle-action, idle-timeout, PIN-validity,
  panic-action. Change unlock pattern + passphrase live here too.
- **Advanced** → security warnings (currently always empty by
  design; producer ships post-beta).

# Haoma — Android manual

The Android app presents itself as a calculator. There is no
launcher icon labelled "Haoma". Casual inspection opens an actual
working calculator. A hold-and-slide gesture reveals the messenger.

## First launch

1. Install the APK (see the main README for how to verify and
   install it).
2. Open the app. You see a calculator. Try `2+2=` to check that it
   works.
3. To reveal the messenger, use either path below. Both check the
   same secret, which is `78963` until you change it.

   **Slide path** — press and hold `5` for about 2 seconds. A small
   green dot lights up in the display. Without lifting your finger,
   slide across the keypad in the pattern `7 → 8 → 9 → 6 → 3`. Lift
   to submit.

   **Tap-PIN path** — press and hold `1` for about 2 seconds. A
   small blue dot lights up. Lift, then tap the digits one by one
   (`7`, `8`, `9`, `6`, `3`), then press and hold `=` for 2 seconds
   to submit. A short tap of `=` cancels, and so does touching any
   operator key (`+`, `−`, `×`, `÷`, `C`).

4. The calculator gives way to a passphrase screen. The passphrase
   is `good-girls-go-to-heaven` until you change it. Tap **Use
   default passphrase** to fill it in.
5. Open **Settings → Security** and change the passphrase and the
   reveal code. You can also change which calculator keys trigger
   the slide and tap-PIN paths under **Unlock keys…**.

If you get a gesture wrong, the calculator just stays a calculator.
There is no error message. Start the hold again to retry.

## Tabs

Bottom navigation, five tabs:

- **Chats** — your conversations. Tap one to open it.
- **Contacts** — the people you are paired with, with their
  presence next to their name.
- **Add contact** — start or accept a pairing exchange.
- **Settings** — the sections listed below.
- **Status** — system status and an event log.

## Pairing

Open **Add contact**.

To invite someone:
1. Open the **Invite a friend** tab, optionally set an alias for
   them, and tap **Invite**.
2. Wait for a 7-word phrase to appear. Read the words to the other
   person over a channel you both trust, such as a voice call or a
   messaging app you already use, or tap **Show QR** and let them
   scan it.

To accept an invite:
1. Open the **Accept an invite** tab.
2. Type the 7 words, or tap **Scan QR** and scan the code the other
   person shows you.
3. Set an alias for them and tap **Accept**.
4. The new contact appears in Chats once the exchange completes.

## Linked devices

You can add one more device — a phone or a desktop — to your own
identity. The two devices then hold the same contacts and chats,
and keep the content listed below in step. Either device can act
for you.

Open **Settings → Linked devices**.

- **Invite** — on the device you already use, open the Invite tab,
  set a short PIN for each device, and start the link. A 7-word
  phrase appears; read it to yourself at the other device, or show
  the QR code there.
- **Join** — on a new, empty device, open the Join tab, enter the
  same PINs and the 7 words (or scan the QR code), and confirm. A
  device that already holds contacts cannot join as a second
  device; unlink or wipe it first.

What syncs between your two devices: messages, edits, deletes,
reactions, read state, files, and the history of calls you placed.
An incoming call rings on both devices; the first one to answer
takes it, and the other stops ringing.

Your own presence (available, away, busy) does not sync between
your devices. It goes out to your contacts, not between your own
devices.

Once linked, the Linked devices screen shows the other device and
how long it has been linked, and lets you set or verify a destroy
PIN. Two further actions ask you to tick a confirmation box first:
**Unlink** (stop treating it as your device) and **Destroy** (erase
the other device from here).

## Chats and messages

- Open a chat from the **Chats** tab.
- Type and send a message from the input bar at the bottom.
- **Reply** — long-press a message, tap **Reply**.
- **Edit** — long-press your own message, tap **Edit**. Only within
  24 hours of sending.
- **Delete** — long-press your own message, tap **Delete**, then
  confirm. Only within 24 hours of sending; it deletes the message
  for both sides.
- **React** — long-press a message and tap **React** to pick an
  emoji, or tap an existing reaction under a message to add or
  remove your own.
- **Search** — tap the menu (⋮) in the chat header, then
  **Search…**. A search bar opens with a match counter and buttons
  to step through matches.
- **Mute** — tap the menu (⋮), then **Mute** or **Unmute**.
- **Disappearing messages for this chat** — tap the menu (⋮), then
  **Details…**, and set the retention there. Settings → Chat
  defaults sets the default used for new chats.

The chat menu (⋮) also has **View files**, **New Tor circuit** (a
fresh Tor circuit for this chat, same contact) and **Rotate Tor**
(a new onion address for this chat).

## Files and images

- **Send** — tap **+** in the chat input, then **Gallery** (photos
  and videos) or **Files** (anything else).
- Sending a photo or video over about 500 KB shows a toggle before
  it sends: **compressed** (smaller, EXIF data removed) or
  **original** (full size, EXIF data kept). Compressed is the
  default.
- **Receive** — open the chat menu (⋮) → **View files**. Each file
  has **Save as**, **Open** and **Delete**.
- Files up to 10 MB.

## Calls

- Tap the phone icon in the chat header to start a call, or to hang
  up one in progress.
- Hold the phone icon to choose **Voice call** or **Video call**.
  Video needs the camera permission.
- An incoming call shows an **Answer** / **Decline** screen, naming
  the caller and whether it is a voice or video call.
- Voice and video calls both work on Android.

## Presence

A contact's presence (available, away, busy) shows next to their
name in **Contacts** and in the chat header.

## Settings

- **Profile** — your self-nick. Leave it empty and your contacts
  see a default placeholder.
- **Chat defaults** — the default disappearing-messages retention
  and whether to send read receipts, used for new chats. Change a
  single chat's setting from that chat's menu → Details.
- **Notifications** — notify on new messages, show the sender, show
  the message text, show on the lock screen, and a disguise mode.
  With disguise mode on, a notification banner shows a math fact
  instead of the real message; tapping it opens the calculator with
  the fact shown on screen.
- **Security** —
  - A threat preset ("Who is after you?") that sets the options
    below to sensible values for your situation.
  - A toggle for whether Haoma appears in the system share sheet.
  - Idle action and idle timeout: what happens, and after how long
    idle, when you leave the app.
  - PIN validity: how long a short PIN keeps working before the app
    asks for the full passphrase again (0 means it never asks
    again on its own).
  - Panic action: disabled, safe-lock, hard-lock, or self-destruct.
  - **Unlock keys…** — which calculator keys trigger the slide
    gesture and the tap-PIN gesture.
  - **Change PIN / pattern…** — the digits used for both reveal
    paths.
  - **Change passphrase…** — the master passphrase. There is no
    recovery if you forget it.
- **Appearance** — chat font size.
- **Files** — informational only on Android; the system file
  picker handles where files are saved.
- **Tor** — a control-port password. The app runs its own Tor with
  automatic authentication, so you only need this if you point the
  app at a Tor process you run yourself.
- **Linked devices** — see "Linked devices" above.
- **Advanced** — a toggle for whether links open Haoma from other
  apps' share sheets, a read-only list of security warnings, backup
  (below), and wipe (below).

## Lock levels

- **Soft-lock** — the calculator cover returns; the app keeps
  running in the background, so messages keep arriving. Reveal with
  the slide or tap-PIN gesture to get back in.
- **Safe-lock** — the cover returns and the messaging part stops;
  the part that talks to Tor keeps running and queues anything that
  arrives. Getting back in needs the full passphrase.
- **Hard-lock** — the app stops completely. Nothing arrives until
  you unlock with the passphrase again.

A freshly started phone always begins hard-locked: reveal gesture,
then the passphrase screen.

## Backup and restore

**Backup** — Settings → Advanced → **Back up to file…**. This saves
an encrypted archive of every chat, contact, file and setting,
sealed with your master passphrase. Pick where to save it (cloud,
SD card, a USB drive). The app locks itself once the backup is
done. Delete the backup file once you no longer need it.

**Restore** — only on an empty device, before you set a passphrase.
On first launch, the passphrase screen has a **Restore from
backup** button. Pick the archive file, then enter the passphrase
from the device that made it. A device that already holds any data
refuses to restore; wipe it first if you want to restore onto it.

## Wipe this device

Settings → Advanced, at the bottom of the screen. Enter the unlock
answer and a confirmation word, then confirm. This sends an erase
request; it can take up to ninety seconds to complete. The fields
clear themselves if you leave the screen.

To erase your other linked device instead, use **Destroy** under
Settings → Linked devices.

## Tor

Built in. The app runs its own Tor process, with its own ports, so
it does not clash with another Tor you may have running on the same
phone. The Tor data directory lives at
`/data/user/0/io.haoma.calculator/files/haoma/tor/`. To pull the Tor
log for diagnostics:

```
adb shell run-as io.haoma.calculator cat files/haoma/tor/tor.log
```

## Logs

App-side logs:
- Debug build: `/sdcard/Android/data/io.haoma.calculator/files/logs/`
- Release build: the app's private files directory, under `logs/`.

Per component:
- `haoma-gui.log` — the app's screens and the supervisor that starts
  the other two.
- `haoma.log` — the daemon that holds your chats and keys.
- `haomad.log` — the daemon that talks to Tor and sends and
  receives over the network.
- `haoma-vault.log` — the process that opens and reseals your
  encrypted store.

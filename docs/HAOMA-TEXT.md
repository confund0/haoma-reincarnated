# haoma-text manual

`haoma-text` is the Linux terminal client. It starts and supervises
the two daemons (`haomad`, the backend; `haoma`, the frontend), opens
the encrypted vault, and gives you a windowed chat screen in your
terminal.

## What ships

The Linux tarball has eight binaries:

- `haoma-text` — the terminal client. Start this one.
- `haoma`, `haomad` — the two daemons `haoma-text` starts for you.
- `haoma-vault` — used internally for vault writes.
- `haoma-mic`, `haoma-spk` — voice call streamers (microphone in,
  speaker out).
- `haoma-cam`, `haoma-vid` — video call streamers (camera in, video
  out). The Linux desktop shows incoming video but has no camera
  support, so it does not use `haoma-cam`.

`haoma-text`, `haoma`, `haomad` and `haoma-vault` are plain Go
binaries and run on any x86-64 Linux. The call streamers link
`libsodium`, `libopus`, `libvpx` and PipeWire, built against Alpine
3.23. Run them on Alpine 3.23, in an Alpine 3.23 container, or build
from source on your distro.

## Install and first run

1. Install `tor`. `haomad` starts its own `tor` process, with its own
   data directory, and stops it when it stops. You do not need the
   system `tor` service running, and no tor password is needed for
   this default path. `haoma-text` looks for `tor` on `PATH`; point it
   at another binary with `--tor-bin <path>`.

   The first start downloads the Tor network directory, which takes a
   minute or more. Later starts take seconds.

   To use a `tor` that is already running on your system instead
   (SOCKS on `127.0.0.1:9050`, control on `127.0.0.1:9051`), pass
   `--system-tor`. If that `tor` needs a control password, set it with
   `/set-tor-password` or in Settings → Tor.

2. Run `haoma-text`. On first run it creates a new vault and asks for
   a master passphrase. Pick one you can remember — there is no way to
   recover a forgotten passphrase.

3. After unlock, haoma-text opens on the status window.

Data, logs and the vault live under `~/.haoma` by default (choose a
different root with `--cfg-dir`).

## Window navigation

- `Esc` then a digit — switch window: `1` status, `2` contacts, `3`
  chats, `4`–`9` an open chat.
- `F6` — move focus between the input bar and the active window's main
  pane (table, list or scroll view).
- `Tab` / `Shift-Tab` — move between fields inside a form.
- `Enter` — submit or activate.
- `Ctrl-C` or `Ctrl-D` — quit.

Each window keeps its own draft text, so switching windows never
mixes up what you were typing.

In the contacts pane, `Enter` opens a chat and `e` edits the contact
(alias, unpair, delete). In the chats pane, `Enter` opens a chat and
`e` edits the conversation (disappearing messages, clear or delete).
On an open chat's scroll view, `f` opens the files list for that chat.

## Commands

Type `/help` at any time for the full, current list. The sections
below describe what each one does.

### Pairing with a person

Pairing connects you to someone else. Tor onion rendezvous is the
default and the one to use:

- `/invite-tor` — set your name with `/nick` first. This opens an
  onion address and, once it is reachable, prints 7 short words. Read
  them to the other person over a separate channel you both trust
  (voice call, a messaging app you already use, in person) — never
  paste them into the same chat you are setting up.
- `/accept-tor <7 words>` — the other person's side: enter the 7 words
  you were given.
- `/cancel-tor <handle-id>` — withdraw an invite you started that has
  not been accepted yet.

Two fallback methods exist. Both are weaker than Tor onion
rendezvous and are meant for cases where it does not work:

- `/invite-dht` / `/accept-dht` / `/cancel-dht` — rendezvous through a
  public DHT (BitTorrent's). This leaks your IP address to the DHT
  nodes it talks to. Use the onion method unless you have a specific
  reason not to.
- `/invite-paste` / `/accept-paste`, `/invite-file` / `/accept-file` —
  no rendezvous at all: you exchange a block of invite data yourself,
  by any channel you like, then both sides accept. Meant for testing
  and for cases where neither of the above will work.

### Your own devices (linking)

Linking adds a second device — phone or desktop — to your own
identity. This is not how you connect to another person; see Pairing
above for that.

- `/link-device` — offer to link one of your own devices. This hands
  your identity key to the device that accepts, so only run it toward
  a device that is brand new (no contacts, no chats of its own) and
  that you control. You can have at most one linked device.
- `/link-join <7 words>` — run this on the brand-new device, with the
  words `/link-device` printed on the first device. The new device
  takes on the first device's identity.
- `/link-cancel <handle-id>` — withdraw a `/link-device` offer that
  has not been joined yet. No argument cancels the most recent one.
- `/linked-devices` — open Settings → Linked devices: the current set
  and every action on it.
- `/sync [all]` — ask your other devices for anything this one missed.

What syncs between linked devices: messages, edits, deletes,
reactions, read state, files, and your own outgoing call history. An
incoming call rings on every linked device at once. Your status
(available / away / busy) does not sync — each device sets its own.

A device you link inherits your contacts, but starts with no pairing
of its own to any of them (a pairing is tied to one device). Use
`/re-pair` to reach a contact your device inherited this way:

- `/re-pair <contact>` — offer that contact a fresh pairing. It is
  carried to them through one of your devices that can still reach
  them.
- `/invites` — every pairing invitation open in either direction.
  Press `Enter` on one offered to you to accept it.

### Messaging

- `/msg [peer-id] <text>` — send a text message. Inside a chat window
  the peer-id can be left out.
- `/send-file [peer-id] <path>` — send a file, up to 10 MB. Peer-id
  can be left out inside a chat window.
- `/attach` — pick a file to send with a dialog, instead of typing a
  path.
- `/files` (or `f` on a chat's scroll view) — list the files sent and
  received in this chat; `Enter` on a row opens a save dialog.
- `/retry` — retry every file download in this chat that got stuck.
- `/edit` — edit one of your own messages, up to 24 hours old.
- `/delete` — delete one of your own messages for both sides, up to
  24 hours old.
- `/react` — react to a message in this chat; sending an empty
  reaction removes it.
- `/search <text>` — search this chat's history.
- `/contacts` (or `/peers`) — open the contacts pane.
- `/chats` — open the chats (conversations) pane.
- `/close` — close the active chat window; its history stays, and
  reopening the chat shows it again.
- Disappearing messages are set per chat from the chats pane (`e` on a
  chat, then its settings), not from a slash command.

### Calls

Voice calls work on the Linux desktop. Video calls work on Android;
the desktop shows incoming video but sends none.

- `/call` — start a voice call with the active chat's peer.
- `/answer` — accept an incoming call.
- `/decline` — decline an incoming call.
- `/hangup` — end the active call, whichever state it is in.

### Presence, identity and the connection

- `/status [available|away|busy]` — set how you appear to others. No
  argument resets to available. In a chat window this targets only
  that chat's peer; anywhere else it broadcasts to everyone you are
  paired with.
- `/nick [name]` — set the name sent along with your invites. No
  argument shows your current one.
- `/verify [peer-id] [on|off]` — mark a contact as verified. The mark
  is kept and synced to your linked device; the screen shows no badge
  for it.
- `/rotate-tor` — rotate the onion address used for the active chat.
  Refused while a call with that peer is in progress.
- `/new-circuit` (or `/renew-circuit`) — rebuild the Tor circuit for
  the active chat without changing the peer or the onion address.
- `/tor-info` — show your published onion addresses and Tor's status.
- `/about` — show the versions and uptime of `haoma-text`, `haoma`,
  `haomad`.

### Locking the device

- The idle action fires after a period of no use. Two settings exist:
  `soft-lock` (the screen locks, both daemons stay running) and
  `safe-lock` (`haoma` stops, `haomad` keeps delivering messages in
  the background).
- `/set-idle-action <soft-lock|safe-lock>` — pick which one fires.
- `/set-idle-timeout <seconds>` — how long idle before it fires.
- `/set-pin-validity <seconds>` — how long a soft-lock accepts your
  PIN before it escalates; `0` turns escalation off.
- `/change-pin <old> <new>` — change the soft-lock PIN. If the current
  PIN is still the insecure default, `<old>` is not checked.

### Vault and account

- `/change-pass <old> <new>` — re-seal the vault under a new master
  passphrase. Passphrases cannot contain spaces.
- `/set-tor-password <password>` — the control-port password for a
  system `tor`, used only with `--system-tor`.
- `/settings` — open the settings dialog: one place for every
  preference above and more (chat defaults, notifications, Tor,
  advanced).
- `/backup [path]` — stop both daemons and write the whole vault
  directory to a single archive file. Default path is a timestamped
  file in your home directory. `haoma-text` exits once the backup is
  written; start it again to keep using it.
- To restore a backup, start `haoma-text` with
  `--restore-backup <archive-path>` on a brand-new device (no
  contacts, no chats, no linked device of its own yet). It asks for
  the backed-up device's passphrase, then replaces the new device's
  empty vault with the one from the archive.
- `/wipe-device` — open the block that erases this device. Asks for
  your unlock PIN and the word `ERASE`; this cannot be undone.

### Other

- `/help` (or `/h`) — the full command list.
- `/quit` (or `/q`, `/panic`) — exit.
- `Tab` — complete a slash command you are typing. A second `Tab` on
  an ambiguous prefix lists every match.

### For testing only

- `/inspect <msg_id>` — dump one stored message row. Not meant for
  normal use.

## Threat profile and retention

Settings → Lock asks one question, "Who is after you?", with four
answers: No one, People at home, People with power, People with
force. Picking one sets the idle action, retention and panic-action
values that fit it, in one step; each can still be changed on its own
afterward.

## Logs

`haoma-text` logs to `<cfg-dir>/haoma-text.log` by default (so
nothing prints over the terminal screen while it is in use); `haoma`
and `haomad` each log to their own file under the same directory. Pass
`--log-level debug` for more detail, or `--log-file -` to send
`haoma-text`'s own log to the terminal instead (only useful without
the terminal screen active).

## Wire format

`haoma-text` talks to `haoma` over a loopback TLS 1.3 connection,
authenticated with a self-signed certificate and a bearer token.
Both are generated on first run and stored at
`<cfg-dir>/{cert.pem,token}` with file permissions `0600`.

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

var writeWireCorpus = flag.Bool("wire.corpus.write", false,
	"rewrite the committed wire fixture corpus instead of checking it; the wire-corpus Makefile target passes this")

const wireCorpusDir = "mobile/Android/app/src/test/resources/wire-corpus"

const wireCorpusManifest = "manifest.json"
const wireCorpusLadder = "delivery-ladder.json"
const wireCorpusReadme = "README"

var wireCorpusRootMarkers = []string{
	"frontend/go.mod",
	"mobile/Android/app/build.gradle.kts",
}

func wireCorpusRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		found := true
		for _, marker := range wireCorpusRootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
				found = false
				break
			}
		}
		if found {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked to the filesystem root without finding %s — this gate read nothing",
				strings.Join(wireCorpusRootMarkers, " and "))
		}
		dir = parent
	}
}

const (
	chatMara = "7f3c1a94e60b45d28c1f0ab35d92e471"
	chatToma = "1d84b0f7c25e49a3b6d70f1c8a34e5b9"

	chatJoinerNew  = "3b9e5d07a1c84f26b0d3e7a9c15f8d42"
	chatInviterNew = "c60a2f8e4b1d47a3958e0d6b2f7c1a93"
	chatStarted    = "5e17c3a9d08b4f62a4c9e1b70d3f8a56"
	chatCrew       = "3a6f0c85b91d47e2a05c8f3d6b21947c"
	peerMara1      = "a41c7e59d0b3486fa2e15c9037bd48e6"
	peerMara2      = "c95f28d17a604b3e81ca6d0f452937ab"
	peerToma       = "e0b7419c53d84a26bf9d1e7038c5a2f4"

	siblingDevice = "5b2e94a07c1d43f8ae60b3d519f78c24"

	linkHandle = "8e40c7b12df9463a05e8b1c73fa0d629"

	fingerprintMara = "05a7d3b81f0c46e29d75b3ea18f40c6d92b5710e83fa2c4d67b019e5a83cf2d41b"
	fingerprintToma = "05c1e84f27b30a95d6e1483fbc70a2d59e63817f04ab52c9d381e7f6a04b3c95e2"

	fingerprintSelf = "05e93b17c0da42f85b6e1904c7d3a28f16054bde9273ca81f6e0d4b5928c7a30f4"
)

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture body: %v", err)
	}
	return b
}

type wireFixture struct {
	Name string

	Frame ipc.Frame
}

func wireCorpusFixtures(t *testing.T) []wireFixture {
	t.Helper()

	frame := func(name string, typ ipc.FrameType, id string, payload any) wireFixture {
		f, err := ipc.NewFrame(typ, id, payload)
		if err != nil {
			t.Fatalf("build fixture %s: %v", name, err)
		}
		return wireFixture{Name: name, Frame: f}
	}

	event := func(name string, ev events.Event) wireFixture {
		return frame(name, ipc.FrameTimelineEvent, "",
			ipc.TimelineEventPayload{Event: mustRaw(t, ev)})
	}

	textOutboundRead := events.Event{
		RecvSeq:       4187,
		ChatID:        chat.ChatID(chatMara),
		Direction:     events.DirOut,
		Kind:          events.KindText,
		DisplayTs:     1754902811,
		SenderTs:      1754902811,
		RecvTs:        1754902811,
		SenderSeq:     612,
		EnvelopeID:    "2f60a8c94b17d3e05a8c61f2790db34e",
		EnvelopeIDs:   []string{"2f60a8c94b17d3e05a8c61f2790db34e", "8d17c04a6e2b95f31c70ad8b625e493f"},
		MsgID:         "9c1e4d7a3f80426bb52c8e0f19a73d65",
		Body:          mustRaw(t, events.TextBody{Text: "the ferry leaves at six and I will be on it"}),
		DeliveryState: events.DeliveryRead,
		ExpireSeconds: 604800,
	}

	textInboundRead := events.Event{
		RecvSeq:           4188,
		ChatID:            chat.ChatID(chatMara),
		Direction:         events.DirIn,
		Kind:              events.KindText,
		DisplayTs:         1754902874,
		SenderTs:          1754902871,
		RecvTs:            1754902875,
		SenderSeq:         344,
		ArrivedFromPeerID: peerMara2,
		EnvelopeID:        "b3927e51d08a4c6f21be7395d06fa48c",
		MsgID:             "6ad3910e75c2483bb0f9e6172c8d5401",
		DecryptStatus:     events.DecryptOK,
		Body: mustRaw(t, events.TextBody{
			Text: "I will wait by the ticket office",
			ReplyTo: &events.ReplyToSnapshot{
				MsgID: "9c1e4d7a3f80426bb52c8e0f19a73d65",
				Text:  "the ferry leaves at six and I will be on it",
			},
		}),
		ExpireSeconds:          604800,
		ReadAt:                 1754902930,
		ReadReceiptSentAt:      1754902931,
		ReceiptClaimOrigin:     true,
		ReceiptClaimGeneration: 3,
	}

	return []wireFixture{

		frame("welcome", ipc.FrameWelcome, "handshake", ipc.WelcomePayload{
			DaemonVersion: "v0.0.31-beta-7-g4c9a13d",

			ProtocolVersion: ipc.ProtocolVersion,

			SelfNick:            defaultSelfNick,
			SelfNickIsDefault:   true,
			ChatFontScale:       1.15,
			DefaultRetentionSec: 604800,
			DefaultSendReceipts: true,
		}),

		frame("peers-listed", ipc.FramePeersListed, "c-2", ipc.PeersListedResponse{
			Peers: []ipc.PeerEntry{
				{
					ID:            peerMara1,
					ChatID:        chatMara,
					Nick:          "mara",
					Alias:         "Mara from the ferry",
					Label:         "Mara from the ferry",
					LastActiveAt:  1754902874,
					LastPassiveAt: 1754903102,
					RetiredAt:     0,
					Accepting:     true,
					Sending:       true,
					Chatty:        "available",
					Effective:     "available",
					Verified:      true,
					Blocked:       false,

					Unreachable: true,
				},

				{
					ID:           siblingDevice,
					ChatID:       "",
					Nick:         "",
					Label:        "my other device",
					LastActiveAt: 1754903120,
					Accepting:    true,
					Chatty:       "available",
					Effective:    "available",
					Sibling:      true,
				},
				{
					ID:            peerToma,
					ChatID:        chatToma,
					Nick:          "toma",
					Alias:         "",
					Label:         "toma",
					LastActiveAt:  1754812440,
					LastPassiveAt: 1754899301,
					RetiredAt:     1754900000,
					Accepting:     false,
					Chatty:        "busy",
					Effective:     "busy",
					Verified:      false,
					Blocked:       true,
				},
			},
		}),

		frame("chats-listed", ipc.FrameChatsListed, "c-3", ipc.ChatsListedResponse{
			Chats: []ipc.ChatEntry{
				{
					ChatID:              chatMara,
					Kind:                ipc.ChatKindDirect,
					PeerID:              peerMara1,
					Fingerprint:         fingerprintMara,
					Label:               "Mara from the ferry",
					RetentionTTL:        604800,
					DisableReadReceipts: false,
					NotificationsMuted:  false,
					NickOverride:        "kestrel at sea",
					Members:             []string{peerMara1, peerMara2},
					CreatedAt:           1754099200,
					LastActivityAt:      1754902874,
					UnreadCount:         2,
					LastReadAt:          1754902930,
					LastTimerChangeTs:   1754880000,
					Accepting:           true,
					Sending:             true,
					Chatty:              "available",
					Effective:           "available",
				},
				{
					ChatID:              chatToma,
					Kind:                ipc.ChatKindDirect,
					PeerID:              "",
					Fingerprint:         fingerprintToma,
					Label:               "toma",
					RetentionTTL:        86400,
					DisableReadReceipts: true,
					NotificationsMuted:  true,
					Members:             []string{},
					CreatedAt:           1753812440,
					LastActivityAt:      1754812440,
					UnreadCount:         0,
					LastReadAt:          1754812500,
					Accepting:           false,
					Chatty:              "busy",
					Effective:           "busy",
				},
				{
					ChatID:         chatCrew,
					Kind:           ipc.ChatKindGroup,
					GroupName:      "harbour crew",
					GroupAlias:     "the crew",
					Label:          "the crew",
					RetentionTTL:   0,
					Members:        []string{peerMara1, peerToma},
					CreatedAt:      1753500000,
					LastActivityAt: 1754890000,
					UnreadCount:    7,
				},

				{
					ChatID:            chatJoinerNew,
					Kind:              ipc.ChatKindDirect,
					PeerID:            peerToma,
					Label:             "joined a moment ago",
					Members:           []string{peerToma},
					CreatedAt:         1754903000,
					WaitingForSession: true,
				},
				{
					ChatID:            chatInviterNew,
					Kind:              ipc.ChatKindDirect,
					PeerID:            peerMara2,
					Label:             "invited a moment ago",
					Members:           []string{peerMara2},
					CreatedAt:         1754903100,
					WaitingForContact: true,
				},
				{
					ChatID:        chatStarted,
					Kind:          ipc.ChatKindDirect,
					PeerID:        peerMara1,
					Label:         "started",
					Members:       []string{peerMara1},
					CreatedAt:     1754903200,
					ChatStartedAt: 1754903217,
				},
			},
		}),

		event("timeline-event.text-outbound-read", textOutboundRead),

		event("timeline-event.text-inbound-read", textInboundRead),

		event("timeline-event.text-outbound-mirrored", events.Event{
			RecvSeq:             4191,
			ChatID:              chat.ChatID(chatMara),
			Direction:           events.DirOut,
			Kind:                events.KindText,
			DisplayTs:           1754899140,
			SenderTs:            1754899140,
			RecvTs:              1754902400,
			SenderSeq:           610,
			SenderPeerID:        siblingDevice,
			MsgID:               "d47f0b6912ae4c53810b7e2f9d6a4c38",
			Body:                mustRaw(t, events.TextBody{Text: "sent this one from the other phone"}),
			DeliveryState:       events.DeliverySiblingFloor,
			MirroredFromSibling: true,
			ExpireSeconds:       604800,
		}),

		event("timeline-event.text-outbound-failed", events.Event{
			RecvSeq:       4192,
			ChatID:        chat.ChatID(chatToma),
			Direction:     events.DirOut,
			Kind:          events.KindText,
			DisplayTs:     1754901000,
			SenderTs:      1754901000,
			RecvTs:        1754901000,
			SenderSeq:     91,
			EnvelopeID:    "70c3b1e9a24d85f06b3c9e1740da285b",
			EnvelopeIDs:   []string{"70c3b1e9a24d85f06b3c9e1740da285b"},
			MsgID:         "1fb60d3892c74ae5a03b6e19d75c284f",
			Body:          mustRaw(t, events.TextBody{Text: "are you still coming on Thursday"}),
			DeliveryState: events.DeliveryFailed,
			ExpireSeconds: 86400,
		}),

		event("timeline-event.text-edited", events.Event{
			RecvSeq:       4193,
			ChatID:        chat.ChatID(chatMara),
			Direction:     events.DirOut,
			Kind:          events.KindText,
			DisplayTs:     1754903001,
			SenderTs:      1754903001,
			RecvTs:        1754903001,
			SenderSeq:     613,
			EnvelopeID:    "5e08fa73c1b6429d0af35c8b17e26d40",
			EnvelopeIDs:   []string{"5e08fa73c1b6429d0af35c8b17e26d40"},
			MsgID:         "83b1e5f06d2a4739c1085ba7e394f2d6",
			Body:          mustRaw(t, events.TextBody{Text: "the six twenty, not the six"}),
			DeliveryState: events.DeliveryDelivered,
			ExpireSeconds: 604800,
			EditedAt:      1754903044,
		}),

		event("timeline-event.text-tombstoned", events.Event{
			RecvSeq:       4194,
			ChatID:        chat.ChatID(chatMara),
			Direction:     events.DirOut,
			Kind:          events.KindText,
			DisplayTs:     1754903120,
			SenderTs:      1754903120,
			RecvTs:        1754903120,
			SenderSeq:     614,
			EnvelopeID:    "94d2c60b7e185af3026d9b41c73e50a8",
			EnvelopeIDs:   []string{"94d2c60b7e185af3026d9b41c73e50a8"},
			MsgID:         "2c74a91f8b035d6ea19c04f72b8d6531",
			DeliveryState: events.DeliverySent,
			ExpireSeconds: 604800,
			DeletedAt:     1754903180,
		}),

		event("timeline-event.text-decrypt-failed", events.Event{
			RecvSeq:       4195,
			ChatID:        chat.ChatID(chatToma),
			Direction:     events.DirIn,
			Kind:          events.KindText,
			DisplayTs:     1754903200,
			SenderTs:      1754903198,
			RecvTs:        1754903201,
			EnvelopeID:    "af71305c9b2e64d80a5c31f9e7b04268",
			DecryptStatus: events.DecryptFailed,
			FailReason:    events.DecryptReasonVersionMismatch,
			RawBlob:       []byte{0x33, 0x0a, 0x21, 0x05, 0x9f, 0x4c, 0xe1, 0x77, 0x2b, 0x08, 0xd0, 0x16},
		}),

		event("timeline-event.timer-change", events.Event{
			RecvSeq:      4196,
			ChatID:       chat.ChatID(chatMara),
			Direction:    events.DirIn,
			Kind:         events.KindTimerChange,
			DisplayTs:    1754880000,
			RecvTs:       1754880002,
			SenderPeerID: peerMara1,
			Body: mustRaw(t, events.TimerChangeBody{
				From:      86400,
				To:        604800,
				ChangedBy: peerMara1,
			}),
			ExpireSeconds: 604800,
		}),

		event("timeline-event.reaction", events.Event{
			RecvSeq:      4197,
			ChatID:       chat.ChatID(chatMara),
			Direction:    events.DirIn,
			Kind:         events.KindReaction,
			DisplayTs:    1754902811,
			RecvTs:       1754903260,
			SenderPeerID: peerMara2,
			MsgID:        "0e5b83c716df42a9805c1e7b64a3f298",
			Body: mustRaw(t, events.ReactionBody{
				TargetMsgID: "9c1e4d7a3f80426bb52c8e0f19a73d65",
				Emoji:       "\U0001F44D",
				At:          1754903259,
			}),
			ExpireSeconds: 604800,
		}),

		event("timeline-event.file-outbound-delivered", events.Event{
			RecvSeq:     4198,
			ChatID:      chat.ChatID(chatMara),
			Direction:   events.DirOut,
			Kind:        events.KindFile,
			DisplayTs:   1754903300,
			SenderTs:    1754903300,
			RecvTs:      1754903300,
			SenderSeq:   615,
			EnvelopeID:  "6b19d0f437ac82e5019b6d3c48f72a10",
			EnvelopeIDs: []string{"6b19d0f437ac82e5019b6d3c48f72a10"},
			MsgID:       "7d40b92e1c6a4358af02e75b9c163d84",
			Body: mustRaw(t, FileEventBody{
				Token:            "4c8e7a01b53d69f2807a1e4c93b508d7",
				UrlPath:          "/files/4c8e7a01b53d69f2807a1e4c93b508d7",
				Name:             "harbour-timetable.pdf",
				Size:             184320,
				Mime:             "application/pdf",
				Sha256Ciphertext: "9f2b70dc4a18e6350bc9d27f4a613e85c0d78b192f6a4e35c81d0b7a962e4f13",
				State:            "ready",
				Caption:          "the winter timetable",
			}),
			DeliveryState: events.DeliveryDelivered,
			ExpireSeconds: 604800,
		}),

		event("timeline-event.file-inbound-downloading", events.Event{
			RecvSeq:           4199,
			ChatID:            chat.ChatID(chatMara),
			Direction:         events.DirIn,
			Kind:              events.KindFile,
			DisplayTs:         1754903400,
			SenderTs:          1754903398,
			RecvTs:            1754903401,
			SenderSeq:         345,
			ArrivedFromPeerID: peerMara1,
			EnvelopeID:        "c802e14b7d3956fa07b1c4e26d95038f",
			MsgID:             "b58c1e703da24f96820b7c1e5d436a09",
			DecryptStatus:     events.DecryptOK,
			Body: mustRaw(t, FileEventBody{
				Token:            "e37c05b98a1d426f803e5c7b19d0a462",
				UrlPath:          "/files/e37c05b98a1d426f803e5c7b19d0a462",
				Name:             "quay-plan.png",
				Size:             742880,
				Mime:             "image/png",
				Sha256Ciphertext: "31c0b7f5a92e4d68013b7c5f9e2a406d8b71c39e05f4a2d61b8c07e394f5a2d0",
				State:            "downloading",
				BytesReceived:    311296,
				Caption:          "where the gate is",
			}),
			DeliveryState: "downloading",
			ExpireSeconds: 604800,
		}),

		event("timeline-event.call-summary", events.Event{
			RecvSeq:      4200,
			ChatID:       chat.ChatID(chatMara),
			Direction:    events.DirOut,
			Kind:         events.KindCallSummary,
			DisplayTs:    1754903500,
			RecvTs:       1754903500,
			SenderPeerID: peerMara1,
			Body: mustRaw(t, events.CallSummaryBody{
				CallID:          "a90f4c7e2b1d58630ac74e1b8d295f36",
				Direction:       "out",
				Outcome:         events.CallOutcomeCompleted,
				DurationSeconds: 372,
				Modalities:      []string{"audio", "video"},
			}),
			ExpireSeconds: 604800,
		}),

		event("timeline-event.onion-rotation", events.Event{
			RecvSeq:      4201,
			ChatID:       chat.ChatID(chatToma),
			Direction:    events.DirOut,
			Kind:         events.KindOnionRotation,
			DisplayTs:    1754903600,
			RecvTs:       1754903600,
			SenderPeerID: peerToma,
			Body: mustRaw(t, events.OnionRotationBody{
				RotationID: "f10a83d6b47c2e59018d3f6a95c274be",
				Stage:      events.OnionRotationStageFailed,
				Reason:     "cooldown",
			}),
			ExpireSeconds: 86400,
		}),

		event("timeline-event.invitation", events.Event{
			RecvSeq:      4202,
			ChatID:       chat.ChatID(chatToma),
			Direction:    events.DirIn,
			Kind:         events.KindInvitation,
			DisplayTs:    1754903700,
			RecvTs:       1754903700,
			SenderPeerID: siblingDevice,
			MsgID:        "c6d1074f3b8e2a5901c7e4b36d802f95",
			Body: mustRaw(t, events.InvitationBody{
				ExpiresAt: 1755508500,
				State:     events.InvitationLive,
			}),
		}),

		frame("inherited-contacts-listed", ipc.FrameInheritedContactsListed, "c-19",
			ipc.InheritedContactsListedResponse{
				Contacts: []ipc.InheritedContactEntry{
					{
						Fingerprint: fingerprintToma,
						Nick:        "toma",
						Alias:       "Toma at the boatyard",
						Label:       "Toma at the boatyard",
					},
				},
				Linked: true,
			}),

		frame("inherited-contacts.changed", ipc.FrameInheritedContactsChanged, "",
			ipc.InheritedContactsChangedPayload{
				Contacts: []ipc.InheritedContactEntry{
					{
						Fingerprint: fingerprintToma,
						Nick:        "toma",
						Label:       "toma",
					},
				},
				Linked: false,
			}),

		frame("invitation-offered", ipc.FrameInvitationOffered, "c-20",
			ipc.InvitationOfferedResponse{
				MsgID:     "e51b07c93a2d4f86b0c7d1e4a93f5268",
				ExpiresAt: 1755508620,
				Siblings:  1,
			}),

		frame("invitations-listed", ipc.FrameInvitationsListed, "c-21",
			ipc.InvitationsListedResponse{
				Invitations: []ipc.InvitationEntry{
					{
						MsgID:     "4a9d2e61b07c4f35a8e1d3c7b62f9054",
						Label:     "Mara from the ferry",
						Direction: ipc.InvitationOfferedToMe,
						ExpiresAt: 1755420000,
						State:     events.InvitationLive,
					},
					{
						MsgID:       "e51b07c93a2d4f86b0c7d1e4a93f5268",
						Label:       "Toma at the boatyard",
						Direction:   ipc.InvitationOfferedByMe,
						ExpiresAt:   1755508620,
						State:       events.InvitationLive,
						Fingerprint: fingerprintToma,
					},
					{
						MsgID:     "b83f16d0c95e4a27a1d6e08f3c724b91",
						Label:     "Mara from the ferry",
						Direction: ipc.InvitationOfferedToMe,
						ExpiresAt: 1754990000,
						State:     events.InvitationSuperseded,
					},
				},
			}),

		frame("timeline-page", ipc.FrameTimelinePage, "c-9", ipc.TimelinePageResponse{
			PeerID:  peerMara1,
			Events:  []json.RawMessage{mustRaw(t, textOutboundRead), mustRaw(t, textInboundRead)},
			HasMore: true,
		}),

		frame("timeline-event-deleted", ipc.FrameTimelineEventDeleted, "",
			ipc.TimelineEventDeletedPayload{
				ChatID:  chatMara,
				RecvSeq: 4194,
			}),

		frame("delivery-state-changed.sent", ipc.FrameDeliveryStatus, "",
			ipc.DeliveryStatusPayload{
				EnvelopeID: "2f60a8c94b17d3e05a8c61f2790db34e",
				State:      events.DeliverySent,
				At:         1754902812903117000,
				Attempts:   1,
			}),

		frame("delivery-state-changed.failed", ipc.FrameDeliveryStatus, "",
			ipc.DeliveryStatusPayload{
				EnvelopeID: "70c3b1e9a24d85f06b3c9e1740da285b",
				State:      events.DeliveryFailed,
				At:         1754901337408225000,
				Attempts:   6,
				LastError:  "dial onion: SOCKS connect: host unreachable",
			}),

		frame("read-receipt-sent", ipc.FrameReadSent, "",
			ipc.ReadSentPayload{
				ChatID:     chatMara,
				EnvelopeID: "d59b03c8a17e42f6805b1c9e73d04a26",
				MsgID:      "0b3e79c14fa8256d90c1b7e438f05a92",
				SenderSeq:  346,
				Targets: []string{
					"6ad3910e75c2483bb0f9e6172c8d5401",
					"b58c1e703da24f96820b7c1e5d436a09",
				},
			}),

		frame("devicelink-started", ipc.FrameDeviceLinkStarted, "c-9",
			ipc.DeviceLinkStartedResponse{
				HandleID:  linkHandle,
				Words:     []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"},
				ExpiresAt: 1754903400,
			}),

		frame("devicelink-probe.waiting", ipc.FrameDeviceLinkProbe, "",
			ipc.DeviceLinkProbePush{
				HandleID: linkHandle,
				Attempt:  2,
				Ready:    false,
				Error:    "unreachable",
				At:       1754902820,
			}),

		frame("devicelink-probe.ready-unverified", ipc.FrameDeviceLinkProbe, "",
			ipc.DeviceLinkProbePush{
				HandleID: linkHandle,
				Attempt:  6,
				Ready:    true,
				Error:    "probe-timeout",
				At:       1754902884,
			}),

		frame("devicelink-completed", ipc.FrameDeviceLinkCompleted, "",
			ipc.DeviceLinkCompletedPush{
				HandleID: linkHandle,
				PeerID:   siblingDevice,
			}),

		frame("devicelink-accepted", ipc.FrameDeviceLinkAccepted, "c-10",
			ipc.DeviceLinkAcceptedResponse{
				PeerID:             siblingDevice,
				AdoptedFingerprint: fingerprintSelf,
				ContactsCleared:    true,
			}),

		frame("devicelink-failed", ipc.FrameDeviceLinkFailed, "",
			ipc.DeviceLinkFailedPush{
				HandleID: linkHandle,
				Reason:   "joiner_not_empty",
				Detail:   "this device holds 2 contacts",
			}),

		frame("devicesync-sent", ipc.FrameDeviceSyncSent, "c-22",
			ipc.DeviceSyncSentResponse{
				Siblings: 1,
				Full:     true,
			}),

		frame("devicesync-sent.already-running", ipc.FrameDeviceSyncSent, "c-23",
			ipc.DeviceSyncSentResponse{
				Siblings:       1,
				Full:           true,
				AlreadyRunning: true,
			}),

		frame("devicesync.finished", ipc.FrameDeviceSyncFinished, "",
			ipc.DeviceSyncFinishedPush{
				Full:            true,
				Siblings:        1,
				Answered:        1,
				RowsApplied:     3,
				SnapshotApplied: 2,
				Held:            1,
			}),

		frame("devicesync.finished.unreachable", ipc.FrameDeviceSyncFinished, "",
			ipc.DeviceSyncFinishedPush{
				Siblings:    1,
				Unreachable: 1,
			}),

		frame("devicesync.finished.timed-out", ipc.FrameDeviceSyncFinished, "",
			ipc.DeviceSyncFinishedPush{
				Siblings: 1,
				Answered: 0,
				TimedOut: true,
			}),

		frame("devicelinks-listed", ipc.FrameDeviceLinksListed, "c-11",
			ipc.DeviceLinksListedResponse{
				Devices: []ipc.DeviceLinkEntry{
					{PeerID: siblingDevice, LinkedAt: 1754902890},
				},
			}),

		frame("destroy-pin-listed", ipc.FrameDestroyPINListed, "c-12",
			ipc.DestroyPINListedResponse{
				Set:   true,
				SetAt: 1754902895,
				Siblings: []ipc.DestroyPINSiblingEntry{
					{
						PeerID:     siblingDevice,
						SaltHeld:   true,
						ReceivedAt: 1754902934,
					},
				},
			}),

		frame("destroy-pin-set.accepted", ipc.FrameDestroyPINSet, "c-13",
			ipc.DestroyPINSetResponse{
				Accepted:   true,
				SaltPushed: 1,
				Siblings:   1,
			}),

		frame("destroy-pin-set.refused", ipc.FrameDestroyPINSet, "c-14",
			ipc.DestroyPINSetResponse{
				Accepted: false,
				Reason:   ipc.DestroyPINRefusedAlreadySet,
				Siblings: 1,
			}),

		frame("destroy-pin-verified.answered", ipc.FrameDestroyPINVerified, "c-15",
			ipc.DestroyPINVerifiedResponse{
				Answered: true,
				Verified: true,
			}),

		frame("destroy-pin-verified.refused", ipc.FrameDestroyPINVerified, "c-16",
			ipc.DestroyPINVerifiedResponse{
				Answered: true,
				Verified: false,
			}),

		frame("destroy-pin-verified.no-salt", ipc.FrameDestroyPINVerified, "c-17",
			ipc.DestroyPINVerifiedResponse{
				Answered: false,
				Verified: false,
				Reason:   ipc.DestroyPINVerifyNoSalt,
			}),

		frame("device-unlinked", ipc.FrameDeviceUnlinked, "c-18",
			ipc.DeviceUnlinkedResponse{
				Unlinked:  true,
				Sent:      true,
				Receipted: true,
				Deadline:  1754924490,
			}),

		frame("destroy-device", ipc.FrameDestroyDevice, "",
			ipc.DestroyDevicePush{
				Reason:  "a linked device commanded this one to erase itself",
				EraseID: "3f9c2ab7d1e04856",
			}),

		frame("destroy-attempts-seen", ipc.FrameDestroyAttemptsSeen, "",
			ipc.DestroyAttemptsSeenPush{
				Count:  3,
				LastAt: 1754903010,
			}),

		frame("chat-presence-changed", ipc.FrameChatPresenceChanged, "",
			ipc.ChatPresenceChangedPayload{
				ChatID:    chatMara,
				Accepting: true,
				Sending:   true,
				Chatty:    "busy",
				Effective: "busy",
			}),

		frame("presence-changed", ipc.FramePresenceChanged, "",
			ipc.PresenceChangedPayload{
				PeerID:    peerMara2,
				Accepting: true,
				Sending:   true,
				Chatty:    "away",
				Effective: "away",
			}),
	}
}

var ladderStates = []string{
	"",
	events.DeliveryPending,
	events.DeliveryEnqueued,
	events.DeliverySent,
	events.DeliveryDelivered,
	events.DeliveryRead,
	events.DeliveryFailed,
	"downloading",
	"awaiting_key",
	"ready",
	"failed_transient",
	"failed_permanent",
	"expired",
}

type ladderTable struct {
	Note      string `json:"note"`
	Authority string `json:"authority"`

	States []string `json:"states"`

	Rank []*int `json:"rank"`

	Outranks [][]bool `json:"outranks"`

	Advances [][]bool `json:"advances"`

	SiblingFloor string `json:"sibling_floor"`

	SeedFromSibling []string `json:"seed_from_sibling"`
}

func buildLadderTable() ladderTable {
	n := len(ladderStates)
	tbl := ladderTable{
		Note: "Generated. Never hand-edited. These are the answers the haoma daemon " +
			"computes for the delivery axis. Row and column of both matrices are " +
			"positions in the states list.",
		Authority:       "frontend/internal/events/delivery_rank.go",
		States:          ladderStates,
		Rank:            make([]*int, n),
		Outranks:        make([][]bool, n),
		Advances:        make([][]bool, n),
		SiblingFloor:    events.DeliverySiblingFloor,
		SeedFromSibling: make([]string, n),
	}
	for i, cur := range ladderStates {
		if rank, ok := events.DeliveryRank(cur); ok {
			r := rank
			tbl.Rank[i] = &r
		}
		tbl.Outranks[i] = make([]bool, n)
		tbl.Advances[i] = make([]bool, n)
		for j, next := range ladderStates {
			tbl.Outranks[i][j] = events.DeliveryOutranks(cur, next)
			tbl.Advances[i][j] = events.DeliveryAdvances(cur, next)
		}
		tbl.SeedFromSibling[i] = events.DeliverySeedFromSibling(cur)
	}
	return tbl
}

type manifestFrame struct {
	Name string `json:"name"`
	File string `json:"file"`
	Type string `json:"type"`
}

type manifestLadder struct {
	File      string `json:"file"`
	Authority string `json:"authority"`
}

type wireManifest struct {
	Note      string          `json:"note"`
	Authority string          `json:"authority"`
	Frames    []manifestFrame `json:"frames"`
	Ladder    manifestLadder  `json:"ladder"`
}

const manifestNote = "Generated. Never hand-edited. Every file named here is a frame the " +
	"haoma daemon marshalled from its own payload structs. The wire-corpus " +
	"Makefile target rewrites this directory; an ordinary test run only checks it."

const corpusReadme = `Wire fixture corpus.

Generated. Never hand-edited.

Every JSON file here is a frame the haoma daemon marshalled from its own
payload structs, or a table of answers the daemon computed. The mobile
unit tests parse these files and assert their own parsing and reduction
against them, instead of writing frames of their own.

The generator is frontend/cmd/haoma/wirecorpus_test.go. It checks these
files on every ordinary test run and fails when they drift. The
wire-corpus target in the repository Makefile is the only thing that
rewrites them.

What the corpus covers and what it deliberately leaves out is written
down in tools/README.md.
`

func buildManifest(fixtures []wireFixture) wireManifest {
	m := wireManifest{
		Note:      manifestNote,
		Authority: "frontend/cmd/haoma/wirecorpus_test.go",
		Ladder: manifestLadder{
			File:      wireCorpusLadder,
			Authority: "frontend/internal/events/delivery_rank.go",
		},
	}
	for _, f := range fixtures {
		m.Frames = append(m.Frames, manifestFrame{
			Name: f.Name,
			File: f.Name + ".json",
			Type: string(f.Frame.Type),
		})
	}
	return m
}

func renderJSON(t *testing.T, what string, v any) []byte {
	t.Helper()
	compact, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", what, err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", "  "); err != nil {
		t.Fatalf("indent %s: %v", what, err)
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

func renderFrame(t *testing.T, f wireFixture) []byte {
	t.Helper()
	onTheWire, err := ipc.Encode(f.Frame)
	if err != nil {
		t.Fatalf("encode fixture %s: %v", f.Name, err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, onTheWire, "", "  "); err != nil {
		t.Fatalf("indent fixture %s: %v", f.Name, err)
	}
	buf.WriteByte('\n')
	out := buf.Bytes()

	var round bytes.Buffer
	if err := json.Compact(&round, out); err != nil {
		t.Fatalf("compact fixture %s: %v", f.Name, err)
	}
	if !bytes.Equal(round.Bytes(), onTheWire) {
		t.Fatalf("fixture %s: the committed form does not compact back to the frame the daemon encoded", f.Name)
	}
	return out
}

func TestWireCorpusIsCurrent(t *testing.T) {
	root := wireCorpusRoot(t)
	dir := filepath.Join(root, wireCorpusDir)
	fixtures := wireCorpusFixtures(t)

	want := map[string][]byte{
		wireCorpusManifest: renderJSON(t, wireCorpusManifest, buildManifest(fixtures)),
		wireCorpusLadder:   renderJSON(t, wireCorpusLadder, buildLadderTable()),
		wireCorpusReadme:   []byte(corpusReadme),
	}
	for _, f := range fixtures {
		want[f.Name+".json"] = renderFrame(t, f)
	}

	if *writeWireCorpus {
		writeCorpusFiles(t, dir, want)
		return
	}
	checkCorpusFiles(t, dir, want)
}

func writeCorpusFiles(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}

	held, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, ent := range held {
		if _, named := want[ent.Name()]; !named {
			if err := os.Remove(filepath.Join(dir, ent.Name())); err != nil {
				t.Fatalf("remove stale %s: %v", ent.Name(), err)
			}
			t.Logf("removed %s", ent.Name())
		}
	}
	for _, name := range sortedKeys(want) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, want[name], 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Logf("wrote %d files to %s", len(want), dir)
}

func checkCorpusFiles(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()

	held, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the corpus at %s: %v — the corpus is committed, so a missing directory means this gate read nothing", dir, err)
	}
	heldNames := map[string]bool{}
	for _, ent := range held {
		heldNames[ent.Name()] = true
	}
	for name := range heldNames {
		if _, named := want[name]; !named {
			t.Errorf("%s holds %s, which the generator does not produce. A file nobody generates is a file nobody checks", wireCorpusDir, name)
		}
	}

	for _, name := range sortedKeys(want) {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s is missing from the corpus: %v", name, err)
			continue
		}
		if bytes.Equal(got, want[name]) {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			t.Errorf("%s has drifted from what the generator produces", name)
			continue
		}
		reportJSONDrift(t, name, got, want[name])
	}
}

func reportJSONDrift(t *testing.T, name string, committed, fresh []byte) {
	t.Helper()

	var oldV, newV any
	if err := json.Unmarshal(committed, &oldV); err != nil {
		t.Errorf("%s: the committed file is not valid JSON: %v", name, err)
		return
	}
	if err := json.Unmarshal(fresh, &newV); err != nil {
		t.Errorf("%s: the generator produced invalid JSON: %v", name, err)
		return
	}

	var lines []string
	diffJSON("", oldV, newV, &lines)
	if len(lines) == 0 {
		t.Errorf("%s: the committed file carries the same values but different formatting. Regenerate it — one encoder writes this corpus, so a formatting difference means the file was hand-edited", name)
		return
	}
	sort.Strings(lines)
	t.Errorf("%s has drifted. The daemon is authoritative; the committed file is stale.\n  %s\nRegenerate the corpus deliberately, and only after the payload change was intended.",
		name, strings.Join(lines, "\n  "))
}

func diffJSON(path string, committed, fresh any, out *[]string) {
	at := path
	if at == "" {
		at = "(the whole payload)"
	}
	switch c := committed.(type) {
	case map[string]any:
		f, ok := fresh.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: the corpus holds an object, the daemon now marshals %T", at, fresh))
			return
		}
		seen := map[string]bool{}
		for k, cv := range c {
			seen[k] = true
			fv, present := f[k]
			if !present {
				*out = append(*out, fmt.Sprintf("%s: the daemon no longer marshals %q. The field was removed, renamed, or gained omitempty and now carries a zero value", at, k))
				continue
			}
			diffJSON(joinPath(path, k), cv, fv, out)
		}
		for k := range f {
			if !seen[k] {
				*out = append(*out, fmt.Sprintf("%s: the daemon now marshals %q, which the corpus does not carry. A new field is a new thing every client has to decide about", at, k))
			}
		}
	case []any:
		f, ok := fresh.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: the corpus holds a list, the daemon now marshals %T", at, fresh))
			return
		}
		if len(c) != len(f) {
			*out = append(*out, fmt.Sprintf("%s: the corpus holds %d entries, the daemon marshals %d", at, len(c), len(f)))
			return
		}
		for i := range c {
			diffJSON(fmt.Sprintf("%s[%d]", path, i), c[i], f[i], out)
		}
	default:
		if fmt.Sprintf("%#v", committed) != fmt.Sprintf("%#v", fresh) {
			*out = append(*out, fmt.Sprintf("%s: the corpus holds %#v, the daemon says %#v", at, committed, fresh))
		}
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestWireCorpusCoversEveryEventKind(t *testing.T) {

	declared := []events.Kind{
		events.KindText,
		events.KindTimerChange,
		events.KindReaction,
		events.KindFile,
		events.KindCallSummary,
		events.KindOnionRotation,
		events.KindInvitation,
	}

	if got := countDeclaredKinds(t); got != len(declared) {
		t.Fatalf("frontend/internal/events declares %d Kind constants; this test names %d. Add the new kind to both this list and the corpus", got, len(declared))
	}

	carried := map[events.Kind]bool{}
	for _, f := range wireCorpusFixtures(t) {
		if f.Frame.Type != ipc.FrameTimelineEvent {
			continue
		}
		var payload ipc.TimelineEventPayload
		if err := json.Unmarshal(f.Frame.Payload, &payload); err != nil {
			t.Fatalf("%s: decode timeline payload: %v", f.Name, err)
		}
		var ev events.Event
		if err := json.Unmarshal(payload.Event, &ev); err != nil {
			t.Fatalf("%s: decode event: %v", f.Name, err)
		}
		carried[ev.Kind] = true
	}

	for _, k := range declared {
		if !carried[k] {
			t.Errorf("no committed fixture carries kind %q. A kind with no fixture is a kind no mobile test ever parses, and one that silently stopped being carried is how this went wrong before", k)
		}
	}
}

func countDeclaredKinds(t *testing.T) int {
	t.Helper()
	path := filepath.Join(wireCorpusRoot(t), "frontend/internal/events/events.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	n := strings.Count(string(raw), " Kind = \"")
	if n == 0 {
		t.Fatalf("%s declares no Kind constants in the shape this gate looks for — the declaration changed and this gate stopped reading it", path)
	}
	return n
}

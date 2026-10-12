package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/pair"
)

const pendingPairingWindow = 10 * time.Minute

type pairingInputs struct {
	own []uint32

	pending int

	sibling []uint32

	reported bool

	invited bool

	mine, theirs string
}

type pairingPlan struct {
	devices, paired int

	missing []uint32

	offer bool

	invited bool

	legacy bool
}

func planPairings(in pairingInputs) pairingPlan {
	p := planMissing(in)
	if len(p.missing) > 0 && in.invited {
		p.offer, p.invited = false, true
	}
	return p
}

func planMissing(in pairingInputs) pairingPlan {
	own := sortedDistinct(in.own)
	sib := sortedDistinct(in.sibling)
	known := sortedDistinct(append(append([]uint32(nil), own...), sib...))

	if len(own) == 0 && in.pending == 0 {

		if !in.reported || len(sib) == 0 {

			return pairingPlan{devices: len(sib), missing: []uint32{0}, offer: true, legacy: true}
		}
		return pairingPlan{devices: len(sib), missing: sib, offer: true}
	}

	p := pairingPlan{devices: len(known), paired: len(own) + in.pending}
	if p.paired > p.devices {
		p.devices = p.paired
	}
	if in.pending > 0 {

		return p
	}
	if !in.reported {
		return p
	}
	for _, dev := range sib {
		if !slices.Contains(own, dev) {
			p.missing = append(p.missing, dev)
		}
	}
	if len(p.missing) == 0 {
		return p
	}

	p.offer = in.mine != "" && in.theirs != "" && in.mine < in.theirs
	return p
}

func sortedDistinct(in []uint32) []uint32 {
	out := make([]uint32, 0, len(in))
	for _, v := range in {
		if v != 0 {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return slices.Compact(out)
}

func (d *daemon) pairingDevice(ctx context.Context, peerID string) (uint32, bool, error) {
	if d == nil || d.stores == nil || peerID == "" {
		return 0, false, nil
	}
	return d.stores.RemoteRegistrationID(ctx, protocol.NewSignalAddress(peerID, pair.DeviceID))
}

func personRoutes(d *daemon, dc *chat.DirectChat, fingerprint string) ([]string, error) {
	var out []string
	for _, peerID := range dc.FanoutPeerIDs() {
		if peerID == "" {
			continue
		}
		fp, err := d.pairingFingerprint(peerID)
		if err != nil {
			return nil, err
		}
		if fp != fingerprint {
			continue
		}
		mine, err := d.readIsSibling(peerID)
		if err != nil {
			return nil, err
		}
		if mine {
			continue
		}
		out = append(out, peerID)
	}
	return out, nil
}

func ownContactDevices(ctx context.Context, d *daemon, fingerprint string) (devices []uint32, pending int, err error) {
	dc, found := chatForPerson(d, fingerprint)
	if !found {
		return nil, 0, nil
	}
	routes, err := personRoutes(d, dc, fingerprint)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	for _, peerID := range routes {
		dev, ok, err := d.pairingDevice(ctx, peerID)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			if pairingIsYoung(d, peerID, now) {
				pending++
			}
			continue
		}
		devices = append(devices, dev)
	}
	return sortedDistinct(devices), pending, nil
}

func pairingIsYoung(d *daemon, peerID string, now time.Time) bool {
	at, err := d.peerMeta.PairedAt(peerID)
	if err != nil || at <= 0 {
		return false
	}
	return now.Sub(time.Unix(at, 0)) < pendingPairingWindow
}

func liveInvitationFrom(d *daemon, chatID chat.ChatID) bool {
	if d.invitations == nil {
		return false
	}
	recs, err := d.invitations.ListByChat(chatID)
	if err != nil {
		slog.Warn("invitation: the invitations offered to this device could not be read; no offer is made beside them",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return true
	}
	now := time.Now().Unix()
	for _, r := range recs {
		if events.InvitationAnswerable(r.State, r.ExpiresAt, now) {
			return true
		}
	}
	return false
}

func pairedDevicesForSibling(ctx context.Context, d *daemon, fingerprint string) []uint32 {
	devices, _, err := ownContactDevices(ctx, d, fingerprint)
	if err != nil {
		slog.Warn("contacts: the devices of a person this device is paired with could not be read; the report is empty",
			slog.String("fingerprint", fingerprint),
			slog.Any("err", err),
		)
		return nil
	}
	return devices
}

func siblingReport(d *daemon, fingerprint string) (devices []uint32, reported bool, err error) {
	rep, found, err := d.peerMeta.SiblingDevicesFor(fingerprint)
	if err != nil || !found {
		return nil, false, err
	}
	mine, err := d.readIsSibling(rep.Sibling)
	if err != nil {
		return nil, false, err
	}
	if !mine {
		return nil, false, nil
	}
	return rep.Devices, true, nil
}

func (d *daemon) selfFingerprint() string {
	if d == nil || d.signalState == nil || d.signalState.IdentityKeyPair == nil {
		return ""
	}
	return d.signalState.IdentityKeyPair.PublicKey().Fingerprint()
}

func pairingOfferPlan(ctx context.Context, d *daemon, fingerprint string, force bool) (chatID chat.ChatID, plan pairingPlan, reason, detail string, ok bool) {
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		return "", plan, reasonNotReady, fmt.Sprintf("read peer records: %v", err), false
	}
	dc, found := chatForPerson(d, fingerprint)
	if !found {
		if len(peerIDs) > 0 {
			return "", plan, reasonAlreadyPaired, fmt.Sprintf("this device holds %d pairing(s) with that person and no conversation for them", len(peerIDs)), false
		}
		return "", plan, reasonUnknownContact, "no conversation on this device belongs to that person", false
	}
	own, pending, err := ownContactDevices(ctx, d, fingerprint)
	if err != nil {
		return "", plan, reasonNotReady, fmt.Sprintf("read pairings: %v", err), false
	}
	if len(own) == 0 && pending == 0 {
		if len(peerIDs) > 0 {
			return "", plan, reasonAlreadyPaired, fmt.Sprintf("this device holds %d pairing(s) with that person, none of them in the conversation", len(peerIDs)), false
		}
		if routes := dc.FanoutPeerIDs(); len(routes) > 0 {
			return "", plan, reasonAlreadyPaired, fmt.Sprintf("that conversation already has %d route(s) of its own", len(routes)), false
		}
		if dc.Fingerprint == "" {
			return "", plan, reasonNotInherited, "that conversation did not come from a linked device", false
		}
	}
	sib, reported, err := siblingReport(d, fingerprint)
	if err != nil {
		return "", plan, reasonNotReady, fmt.Sprintf("read the linked device's report: %v", err), false
	}
	plan = planPairings(pairingInputs{
		own:      own,
		pending:  pending,
		sibling:  sib,
		reported: reported,
		invited:  liveInvitationFrom(d, dc.ID),
		mine:     d.selfFingerprint(),
		theirs:   fingerprint,
	})
	if len(plan.missing) == 0 {
		return dc.ID, plan, reasonAlreadyPaired, fmt.Sprintf("this device is paired with %d of %d known device(s) of that person, %d pairing(s) without a session yet", plan.paired, plan.devices, pending), false
	}
	if plan.invited {
		return dc.ID, plan, reasonInvitationFromContact, fmt.Sprintf("%d device(s) of that person missing here; an invitation from them stands", len(plan.missing)), false
	}
	if !plan.offer && !force {
		return dc.ID, plan, reasonContactOffers, fmt.Sprintf("%d device(s) of that person missing here; their device offers", len(plan.missing)), false
	}
	return dc.ID, plan, "", "", true
}

func chooseOfferTarget(plan pairingPlan, notes []invitation.Note) (target uint32, open bool) {
	var oldest uint32
	var oldestAt int64
	first := true
	for _, m := range plan.missing {
		var covering *invitation.Note
		for i := range notes {
			if notes[i].Device == m {
				covering = &notes[i]
				break
			}
		}
		if covering == nil {
			return m, true
		}
		if first || covering.MintedAt < oldestAt {
			oldest, oldestAt, first = m, covering.MintedAt, false
		}
	}
	return oldest, false
}

func stillMissing(plan pairingPlan, device uint32) bool {
	if device == 0 {
		return len(plan.missing) > 0
	}
	return slices.Contains(plan.missing, device)
}

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"haoma-frontend/internal/ipc"
)

var destroyRigNames = []string{"keeper", "doomed"}

const destroyRigCount = 2

const (
	destroyPINKeeper = "1379"
	destroyPINDoomed = "2468"
	destroyPINWrong  = "9999"
)

const destroyGoneTimeout = 60 * time.Second

func runDestroyFleet(keeper, doomed *rig) *pairingResult {
	res := &pairingResult{a: keeper.name, b: doomed.name}

	link, err := linkDevices(keeper, doomed)
	if err != nil {
		res.add("link", err)
		return res
	}
	res.add("link", nil)

	if _, err := setDestroyPIN(keeper, destroyPINKeeper); err != nil {
		res.add("destroy pin set keeper", err)
		return res
	}
	res.add("destroy pin set keeper", nil)
	if _, err := setDestroyPIN(doomed, destroyPINDoomed); err != nil {
		res.add("destroy pin set doomed", err)
		return res
	}
	res.add("destroy pin set doomed", nil)

	if err := awaitSiblingSalt(keeper, link.offeringSeesJoining); err != nil {
		res.add("destroy pin salt reaches keeper", err)
		return res
	}
	res.add("destroy pin salt reaches keeper", nil)

	res.add("a wrong PIN erases nothing and can be retyped",
		assertWrongPINErasesNothing(keeper, doomed, link.offeringSeesJoining))

	res.add("nothing reaches the device being removed",
		assertWindowSealed(keeper, doomed, link.joiningSeesOffering))

	res.add("destroy is commanded",
		assertDestroyCommanded(keeper, link.offeringSeesJoining))

	res.add("the erased device keeps nothing",
		assertTreeEmptied(doomed))
	res.add("the device that stays keeps its own data",
		assertTreeIntact(keeper))
	res.add("the device that stays is linked to nobody",
		assertNoLinkedDevices(keeper))

	return res
}

func assertWrongPINErasesNothing(keeper, doomed *rig, doomedPeerID string) error {
	before, err := countTreeEntries(doomed)
	if err != nil {
		return err
	}
	if before == 0 {
		return fmt.Errorf("%s holds nothing before the wrong PIN, so this proves nothing", doomed.name)
	}

	out, err := unlinkDevice(keeper, doomedPeerID, destroyPINWrong)
	if err != nil {
		return fmt.Errorf("wrong-PIN destroy: %w", err)
	}
	if out.Unlinked {
		return fmt.Errorf("%s acted on a wrong PIN: %+v", keeper.name, out)
	}

	if out.Reason != ipc.DestroyPINRefusedWrong {
		return fmt.Errorf("%s refused with %q, want %q — the wrong PIN is not what stopped it",
			keeper.name, out.Reason, ipc.DestroyPINRefusedWrong)
	}

	rigSleep(5*time.Second, "watching that a wrong PIN erases nothing")

	after, err := countTreeEntries(doomed)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("%s went from %d entries to %d on a WRONG PIN",
			doomed.name, before, after)
	}
	linked, err := linkedDeviceCount(keeper)
	if err != nil {
		return err
	}
	if linked != 1 {
		return fmt.Errorf("%s holds %d linked devices after a wrong PIN, want 1 — it forgot the other device over a typo",
			keeper.name, linked)
	}

	if err := awaitSiblingSalt(keeper, doomedPeerID); err != nil {
		return fmt.Errorf("%s lost the other device's salt on a wrong PIN, so it cannot retype: %w",
			keeper.name, err)
	}
	return nil
}

func assertWindowSealed(keeper, doomed *rig, keeperPeerID string) error {
	mark := time.Now()

	before, err := countInboundFrom(doomed, keeperPeerID, mark)
	if err != nil {
		return err
	}
	if before != 0 {
		return fmt.Errorf("%s took %d envelopes from %s before the sweep was driven",
			doomed.name, before, keeper.name)
	}

	sent, err := driveSyncCount(keeper)
	if err != nil {
		return fmt.Errorf("drive the sweep on %s: %w", keeper.name, err)
	}
	if sent == 0 {
		return fmt.Errorf("%s dispatched to no device, so this measured nothing", keeper.name)
	}

	rigSleep(15*time.Second, "watching that nothing reaches the device being removed")

	after, err := countInboundFrom(doomed, keeperPeerID, mark)
	if err != nil {
		return err
	}
	if after != 0 {
		return fmt.Errorf("%s took %d envelopes from %s while it was being removed",
			doomed.name, after, keeper.name)
	}
	return nil
}

func countInboundFrom(r *rig, peerID string, since time.Time) (int, error) {
	f, err := os.Open(filepath.Join(r.root, "haomad.log"))
	if err != nil {
		return 0, fmt.Errorf("open %s daemon log: %w", r.name, err)
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, inboundEnvelopeLine) {
			continue
		}

		if !lineNamesPeer(line, peerID) {
			continue
		}
		if lineTime(line).Before(since) {
			continue
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("read %s daemon log: %w", r.name, err)
	}
	return n, nil
}

const inboundEnvelopeLine = "envelope received and queued to inbox"

func driveSyncCount(r *rig) (int, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameDeviceSync, ipc.DeviceSyncRequest{}); err != nil {
		return 0, fmt.Errorf("device_sync send: %w", err)
	}
	f, err := awaitFrame(sub, linkSetTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameDeviceSyncSent || f.Type == ipc.FrameError
	})
	if err != nil {
		return 0, fmt.Errorf("await device_sync_sent: %w", err)
	}
	if f.Type == ipc.FrameError {
		return 0, frameErr("device_sync", f)
	}
	var out ipc.DeviceSyncSentResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		return 0, fmt.Errorf("decode device_sync_sent: %w", err)
	}
	return out.Siblings, nil
}

func assertDestroyCommanded(keeper *rig, doomedPeerID string) error {
	out, err := unlinkDevice(keeper, doomedPeerID, destroyPINDoomed)
	if err != nil {
		return fmt.Errorf("destroy: %w", err)
	}
	if !out.Unlinked {
		return fmt.Errorf("the command was refused: %+v", out)
	}
	return nil
}

func assertTreeEmptied(r *rig) error {
	deadline := time.Now().Add(destroyGoneTimeout)
	for {
		n, err := countTreeEntries(r)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			names, _ := treeEntries(r)
			return fmt.Errorf("%s still holds %d entries after %s: %v",
				r.name, n, destroyGoneTimeout, names)
		}
		rigSleep(2*time.Second, "waiting for an erased device's tree to go")
	}
}

func assertTreeIntact(r *rig) error {
	n, err := countTreeEntries(r)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s was erased too — it commanded the destroy, it was not the target", r.name)
	}
	return nil
}

func assertNoLinkedDevices(r *rig) error {
	n, err := linkedDeviceCount(r)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%s still lists %d linked devices after destroying its only one", r.name, n)
	}
	return nil
}

func treeEntries(r *rig) ([]string, error) {
	entries, err := os.ReadDir(r.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", r.name, r.root, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, filepath.Base(e.Name()))
	}
	return out, nil
}

func countTreeEntries(r *rig) (int, error) {
	names, err := treeEntries(r)
	return len(names), err
}

func unlinkDevice(r *rig, peerID, pin string) (ipc.DeviceUnlinkedResponse, error) {
	sub, cancel := r.subscribe()
	defer cancel()

	id, err := r.sendReq(ipc.FrameUnlinkDevice, ipc.UnlinkDeviceRequest{PeerID: peerID, PIN: pin})
	if err != nil {
		return ipc.DeviceUnlinkedResponse{}, fmt.Errorf("unlink_device send: %w", err)
	}
	f, err := awaitFrame(sub, unlinkAwait, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameDeviceUnlinked || f.Type == ipc.FrameError)
	})
	if err != nil {
		return ipc.DeviceUnlinkedResponse{}, fmt.Errorf("await device_unlinked: %w", err)
	}
	if f.Type == ipc.FrameError {
		return ipc.DeviceUnlinkedResponse{}, frameErr("unlink_device", f)
	}
	var out ipc.DeviceUnlinkedResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		return ipc.DeviceUnlinkedResponse{}, fmt.Errorf("decode device_unlinked: %w", err)
	}
	return out, nil
}

func linkedDeviceCount(r *rig) (int, error) {
	sub, cancel := r.subscribe()
	defer cancel()

	id, err := r.sendReq(ipc.FrameDeviceLinksList, ipc.DeviceLinksListRequest{})
	if err != nil {
		return 0, fmt.Errorf("device_links_list send: %w", err)
	}
	f, err := awaitFrame(sub, linkSetTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameDeviceLinksListed || f.Type == ipc.FrameError)
	})
	if err != nil {
		return 0, fmt.Errorf("await device_links_listed: %w", err)
	}
	if f.Type == ipc.FrameError {
		return 0, frameErr("device_links_list", f)
	}
	var out ipc.DeviceLinksListedResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		return 0, fmt.Errorf("decode device_links_listed: %w", err)
	}
	return len(out.Devices), nil
}

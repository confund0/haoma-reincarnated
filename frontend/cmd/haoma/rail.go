package main

import (
	"errors"
	"log/slog"

	"haoma-frontend/internal/syncrail"
)

var errNoIdentity = errors.New("rail: no identity keypair loaded")

func (d *daemon) railKey() ([]byte, error) {
	if d == nil || d.stores == nil {
		return nil, errNoIdentity
	}
	kp := d.stores.GetIdentityKeyPair()
	if kp == nil {
		return nil, errNoIdentity
	}
	priv := kp.PrivateKey().Serialize()
	return syncrail.DeriveKey(priv[:])
}

func (d *daemon) siblings() []string {
	if d == nil || d.linkedDevices == nil {
		return nil
	}
	members, err := d.linkedDevices.Members()
	if err != nil {
		slog.Error("rail: read linked-device set failed", slog.Any("err", err))
		return nil
	}
	return members
}

func (d *daemon) isSibling(peerID string) bool {
	mine, err := d.readIsSibling(peerID)
	if err != nil {
		slog.Error("rail: sibling check failed; treating as peer",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return false
	}
	return mine
}

func (d *daemon) readIsSibling(peerID string) (bool, error) {
	if d == nil || d.linkedDevices == nil || peerID == "" {
		return false, nil
	}
	set, err := d.linkedDevices.Get()
	if err != nil {
		return false, err
	}
	return set.Contains(peerID), nil
}

func (d *daemon) readHasSibling() (bool, error) {
	if d == nil || d.linkedDevices == nil {
		return false, nil
	}
	members, err := d.linkedDevices.Members()
	if err != nil {
		return false, err
	}
	return len(members) > 0, nil
}

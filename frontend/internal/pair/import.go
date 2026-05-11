package pair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	"go.mau.fi/libsignal/session"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/signal"
)

func Import(ctx context.Context, stores *signal.Stores, backend *backendapi.Client, inv *Invite, myKeys *MyKeys, minted backendapi.MintedOnion) error {
	if err := inv.Validate(); err != nil {
		return err
	}
	if myKeys == nil {
		return errors.New("pair: nil MyKeys")
	}
	if myKeys.PeerID == "" {
		return errors.New("pair: empty MyKeys.PeerID")
	}
	if len(myKeys.OutboundSecret) != SecretLen {
		return fmt.Errorf("pair: MyKeys.OutboundSecret length %d, want %d", len(myKeys.OutboundSecret), SecretLen)
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		return errors.New("pair: empty MintedOnion (call backend.MintOnion before Import)")
	}

	be := inv.Backend(myKeys, minted)
	if err := postPeer(ctx, backend, be); err != nil {
		return fmt.Errorf("pair: backend register: %w", err)
	}

	addr := protocol.NewSignalAddress(inv.PeerID, DeviceID)

	bundle, err := inv.ToBundle()
	if err != nil {
		return fmt.Errorf("pair: build bundle: %w", err)
	}

	if err := stores.SaveIdentity(ctx, addr, bundle.IdentityKey()); err != nil {
		return fmt.Errorf("pair: save identity: %w", err)
	}

	ser := serialize.NewJSONSerializer()
	builder := session.NewBuilder(stores, stores, stores, stores, addr, ser)
	if err := builder.ProcessBundle(ctx, bundle); err != nil {
		return fmt.Errorf("pair: process bundle: %w", err)
	}
	return nil
}

func postPeer(ctx context.Context, backend *backendapi.Client, be BackendInvite) error {
	body, err := json.Marshal(be)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return backend.PostPeer(ctx, body)
}

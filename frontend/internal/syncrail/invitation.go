package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/logid"
)

type InvitationBody struct {
	ID string `json:"id"`

	Fingerprint string `json:"fingerprint"`

	OfferingDevice string `json:"offering_device"`

	Secret string `json:"secret"`

	MintedAt int64 `json:"minted_at"`

	ExpiresAt int64 `json:"expires_at"`

	Device uint32 `json:"device,omitempty"`
}

func (b InvitationBody) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("invitation_id", logid.Short(b.ID)),
		slog.String("fingerprint", logid.Short(b.Fingerprint)),
		slog.String("offering_device", logid.Short(b.OfferingDevice)),
		slog.Int64("minted_at", b.MintedAt),
		slog.Int64("expires_at", b.ExpiresAt),

		slog.String("device", devicetag.Tag(b.Device)),
	)
}

func ParseInvitationBody(env Envelope) (InvitationBody, error) {
	if env.Kind != KindInvitation {
		return InvitationBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body InvitationBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return InvitationBody{}, errors.New("syncrail: invitation body will not decode")
	}
	switch {
	case body.ID == "":

		return InvitationBody{}, errors.New("syncrail: invitation body has no id")
	case body.Fingerprint == "":
		return InvitationBody{}, errors.New("syncrail: invitation body has no fingerprint")
	case body.OfferingDevice == "":

		return InvitationBody{}, errors.New("syncrail: invitation body names no offering device")
	case body.Secret == "":

		return InvitationBody{}, errors.New("syncrail: invitation body has no secret")
	case body.MintedAt <= 0:

		return InvitationBody{}, errors.New("syncrail: invitation body has no mint time")
	}
	return body, nil
}

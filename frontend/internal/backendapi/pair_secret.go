package backendapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var (
	ErrRendezvousNotReady = errors.New("backendapi: the daemon is not ready to dial a rendezvous")

	ErrRendezvousRequestRefused = errors.New("backendapi: the daemon refused the dial request")

	ErrRendezvousRefused = errors.New("backendapi: the rendezvous refused the secret")

	ErrRendezvousUnreachable = errors.New("backendapi: the rendezvous did not answer")
)

func (c *Client) PairOnionAcceptSecret(ctx context.Context, secret string, payload []byte) (PairOnionAcceptResponse, error) {
	var out PairOnionAcceptResponse
	if secret == "" {
		return out, errors.New("backendapi: no secret to dial with")
	}
	if len(payload) == 0 {
		return out, errors.New("backendapi: no payload to present")
	}
	body, err := json.Marshal(PairOnionAcceptRequest{Secret: secret, Payload: payload})
	if err != nil {
		return out, errors.New("backendapi: the accept request will not encode")
	}
	defer zeroBytes(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/pair/onion/accept", bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("backendapi: build pair onion accept by secret: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)

	resp, err := doRequest(c.longHTTP, req)
	if err != nil {

		return out, fmt.Errorf("%w: %v", ErrRendezvousNotReady, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {

			return PairOnionAcceptResponse{}, errors.New("backendapi: the answer from the rendezvous will not decode")
		}
		if len(out.InviterPayload) == 0 {
			return PairOnionAcceptResponse{}, errors.New("backendapi: the rendezvous answered with nothing")
		}
		return out, nil
	case http.StatusServiceUnavailable:
		return out, ErrRendezvousNotReady
	case http.StatusBadRequest, http.StatusUnsupportedMediaType:
		return out, ErrRendezvousRequestRefused
	case http.StatusUnauthorized:
		return out, ErrRendezvousRefused
	case http.StatusBadGateway:
		return out, ErrRendezvousUnreachable
	default:
		return out, fmt.Errorf("backendapi: pair onion accept by secret: status %d", resp.StatusCode)
	}
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

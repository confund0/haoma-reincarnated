package backendapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	DestroyPINAlreadySet = "already_set"
)

type DestroyPINState struct {
	Set   bool   `json:"set"`
	Salt  string `json:"salt,omitempty"`
	SetAt int64  `json:"set_at,omitempty"`

	SiblingPeerID string `json:"sibling_peer_id,omitempty"`
}

type SetDestroyPINRequest struct {
	Salt     string `json:"salt"`
	Verifier string `json:"verifier"`

	SiblingPeerID string `json:"sibling_peer_id"`
}

type SetDestroyPINResponse struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

func (c *Client) DestroyPIN(ctx context.Context) (DestroyPINState, error) {
	var out DestroyPINState
	if err := c.getJSON(ctx, "/destroy-pin", &out); err != nil {
		return DestroyPINState{}, err
	}
	return out, nil
}

func (c *Client) SetDestroyPIN(ctx context.Context, req SetDestroyPINRequest) (SetDestroyPINResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SetDestroyPINResponse{}, fmt.Errorf("backendapi: encode destroy pin request: %w", err)
	}
	var out SetDestroyPINResponse
	if err := c.postJSON(ctx, "/destroy-pin", body, http.StatusOK, &out); err != nil {
		return SetDestroyPINResponse{}, err
	}
	return out, nil
}

type VerifyDestroyPINRequest struct {
	PeerID string `json:"peer_id"`
	Proof  string `json:"proof"`
}

type VerifyDestroyPINResponse struct {
	Answered bool   `json:"answered"`
	Verified bool   `json:"verified"`
	Reason   string `json:"reason,omitempty"`
}

const DestroyVerifyAnswerBad = "answer_did_not_check_out"

const DestroyVerifyDialTimeout = 120 * time.Second

const DestroyVerifyWait = DestroyVerifyDialTimeout + 30*time.Second

func (c *Client) VerifyDestroyPIN(ctx context.Context, req VerifyDestroyPINRequest) (VerifyDestroyPINResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return VerifyDestroyPINResponse{}, fmt.Errorf("backendapi: encode destroy pin verify request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, DestroyVerifyWait)
	defer cancel()
	var out VerifyDestroyPINResponse
	if err := c.postJSONWith(ctx, c.longHTTP, "/destroy-pin/verify", body, http.StatusOK, &out); err != nil {
		return VerifyDestroyPINResponse{}, err
	}
	return out, nil
}

type DrainDestroyAttemptsResponse struct {
	Count  int   `json:"count"`
	LastAt int64 `json:"last_at,omitempty"`
}

func (c *Client) DrainDestroyAttempts(ctx context.Context) (DrainDestroyAttemptsResponse, error) {
	var out DrainDestroyAttemptsResponse
	if err := c.postJSON(ctx, "/destroy-pin/attempts/drain", []byte("{}"), http.StatusOK, &out); err != nil {
		return DrainDestroyAttemptsResponse{}, err
	}
	return out, nil
}

type UnlinkRequest struct {
	PeerID string `json:"peer_id"`
	Proof  string `json:"proof"`

	NarrowOnly bool `json:"narrow_only,omitempty"`
}

type UnlinkResponse struct {
	Narrowed  bool  `json:"narrowed"`
	Sent      bool  `json:"sent"`
	Receipted bool  `json:"receipted"`
	Deadline  int64 `json:"deadline"`
}

func (c *Client) Unlink(ctx context.Context, req UnlinkRequest) (UnlinkResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return UnlinkResponse{}, fmt.Errorf("backendapi: encode unlink request: %w", err)
	}
	var out UnlinkResponse
	if err := c.postJSON(ctx, "/unlink", body, http.StatusOK, &out); err != nil {
		return UnlinkResponse{}, err
	}
	return out, nil
}

func (c *Client) ClearDestroyPIN(ctx context.Context) error {
	var out SetDestroyPINResponse
	return c.postJSON(ctx, "/destroy-pin/clear", []byte("{}"), http.StatusOK, &out)
}

type UnlinkPendingResponse struct {
	PeerID    string `json:"peer_id"`
	Deadline  int64  `json:"deadline"`
	StartedAt int64  `json:"started_at"`
}

func (c *Client) UnlinkPending(ctx context.Context) (UnlinkPendingResponse, error) {
	var out UnlinkPendingResponse
	if err := c.getJSON(ctx, "/unlink/pending", &out); err != nil {
		return UnlinkPendingResponse{}, err
	}
	return out, nil
}

func (c *Client) StopUnlinkWaiting(ctx context.Context) error {
	var out UnlinkPendingResponse
	return c.postJSON(ctx, "/unlink/stop-waiting", []byte("{}"), http.StatusOK, &out)
}

package backendapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client

	longHTTP *http.Client
}

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	long := &http.Client{Transport: httpClient.Transport}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		http:     httpClient,
		longHTTP: long,
	}
}

func (c *Client) authHeader(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	if err := c.getJSON(ctx, "/health", &out); err != nil {
		return HealthResponse{}, err
	}
	return out, nil
}

type TorSlot struct {
	Slot      int    `json:"slot"`
	ServiceID string `json:"service_id"`
	URL       string `json:"url"`
}

type TorHealth struct {
	Bootstrap   int  `json:"bootstrap"`
	Ready       bool `json:"ready"`
	Unreachable bool `json:"unreachable"`
}

type TorInfoResponse struct {
	Slots  []TorSlot `json:"slots"`
	Health TorHealth `json:"health"`
}

func (c *Client) TorInfo(ctx context.Context) (TorInfoResponse, error) {
	var out TorInfoResponse
	if err := c.getJSON(ctx, "/tor", &out); err != nil {
		return TorInfoResponse{}, err
	}
	return out, nil
}

type SystemInfo struct {
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
}

func (c *Client) SystemInfo(ctx context.Context) (SystemInfo, error) {
	var out SystemInfo
	if err := c.getJSON(ctx, "/system", &out); err != nil {
		return SystemInfo{}, err
	}
	return out, nil
}

type MintedOnion struct {
	Address    string `json:"address"`
	PrivateKey string `json:"private_key"`
}

func (c *Client) MintOnion(ctx context.Context) (MintedOnion, error) {
	var out MintedOnion
	if err := c.postJSON(ctx, "/onion/mint", nil, http.StatusOK, &out); err != nil {
		return MintedOnion{}, err
	}
	return out, nil
}

func (c *Client) DelOnion(ctx context.Context, address string) error {
	body, err := json.Marshal(struct {
		Address string `json:"address"`
	}{Address: address})
	if err != nil {
		return fmt.Errorf("backendapi: marshal del-onion: %w", err)
	}
	return c.postJSON(ctx, "/onion/del", body, http.StatusOK, nil)
}

func (c *Client) OverlayPeerAddress(ctx context.Context, peerID, address string) error {
	body, err := json.Marshal(struct {
		Address string `json:"address"`
	}{Address: address})
	if err != nil {
		return fmt.Errorf("backendapi: marshal overlay-address: %w", err)
	}
	return c.postJSON(ctx, "/peers/"+peerID+"/overlay-address", body, http.StatusOK, nil)
}

func (c *Client) CollapsePeerAddress(ctx context.Context, peerID, retain string) error {
	body, err := json.Marshal(struct {
		Retain string `json:"retain"`
	}{Retain: retain})
	if err != nil {
		return fmt.Errorf("backendapi: marshal collapse-address: %w", err)
	}
	return c.postJSON(ctx, "/peers/"+peerID+"/collapse-address", body, http.StatusOK, nil)
}

type RotateOwnOnionResponse struct {
	Status     string `json:"status"`
	OldAddress string `json:"old_address"`
}

func (c *Client) RotateOwnOnion(ctx context.Context, peerID, address, privateKey string) (RotateOwnOnionResponse, error) {
	body, err := json.Marshal(struct {
		Address    string `json:"address"`
		PrivateKey string `json:"private_key"`
	}{Address: address, PrivateKey: privateKey})
	if err != nil {
		return RotateOwnOnionResponse{}, fmt.Errorf("backendapi: marshal rotate-own-onion: %w", err)
	}
	var out RotateOwnOnionResponse
	if err := c.postJSON(ctx, "/peers/"+peerID+"/rotate-own-onion", body, http.StatusOK, &out); err != nil {
		return RotateOwnOnionResponse{}, err
	}
	return out, nil
}

func (c *Client) NewCircuitForPeer(ctx context.Context, peerID string) (int, error) {
	var out struct {
		Closed int `json:"closed"`
	}
	if err := c.postJSON(ctx, "/peers/"+peerID+"/new-circuit", nil, http.StatusOK, &out); err != nil {
		return 0, err
	}
	return out.Closed, nil
}

type PeerSelfReach struct {
	PeerID string `json:"peer_id"`
	Onion  string `json:"onion,omitempty"`
	Ok     bool   `json:"ok"`
	At     int64  `json:"at"`
}

func (c *Client) ProbePeerSelf(ctx context.Context, peerID string) (PeerSelfReach, error) {
	var out PeerSelfReach
	if err := c.postJSON(ctx, "/peers/"+peerID+"/self-probe", nil, http.StatusOK, &out); err != nil {
		return PeerSelfReach{}, err
	}
	return out, nil
}

func (c *Client) ExternalProbeBurst(ctx context.Context) error {
	return c.postJSON(ctx, "/external-probe-burst", nil, http.StatusAccepted, nil)
}

type Peer struct {
	ID                   string         `json:"id"`
	KnownAddresses       []string       `json:"known_addresses"`
	IDSCounters          map[string]int `json:"ids_counters,omitempty"`
	LastActiveAt         int64          `json:"last_active_at,omitempty"`
	LastPassiveAt        int64          `json:"last_passive_at,omitempty"`
	RetiredAt            int64          `json:"retired_at,omitempty"`
	PrevMyOnionExpiresAt int64          `json:"prev_my_onion_expires_at,omitempty"`
}

type PeersResponse struct {
	Peers []Peer `json:"peers"`
}

func (c *Client) Peers(ctx context.Context) (PeersResponse, error) {
	var out PeersResponse
	if err := c.getJSON(ctx, "/peers", &out); err != nil {
		return PeersResponse{}, err
	}
	return out, nil
}

func (c *Client) Peer(ctx context.Context, id string) (Peer, error) {
	var out Peer
	if err := c.getJSON(ctx, "/peers/"+id, &out); err != nil {
		return Peer{}, err
	}
	return out, nil
}

func (c *Client) PeerAction(ctx context.Context, id, action string) (Peer, error) {
	body, err := json.Marshal(struct {
		Action string `json:"action"`
	}{Action: action})
	if err != nil {
		return Peer{}, fmt.Errorf("backendapi: marshal peer action: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/peers/"+id+"/action", bytes.NewReader(body))
	if err != nil {
		return Peer{}, fmt.Errorf("backendapi: build peer action: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Peer{}, fmt.Errorf("backendapi: post peer action: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Peer{}, httpError(resp)
	}
	var out Peer
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Peer{}, fmt.Errorf("backendapi: decode peer action: %w", err)
	}
	return out, nil
}

func (c *Client) SetTorPassword(ctx context.Context, password string) error {
	body, err := json.Marshal(struct {
		Password string `json:"password"`
	}{Password: password})
	if err != nil {
		return fmt.Errorf("backendapi: marshal tor password: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/tor-password", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backendapi: build tor password: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: post tor password: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return httpError(resp)
	}
	return nil
}

func (c *Client) PostPeer(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/peers", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backendapi: build post peer: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: post peer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return httpError(resp)
	}
	return nil
}

type InboxEntry struct {
	ArrivalAt int64       `json:"arrival_at"`
	PeerID    string      `json:"peer_id"`
	Envelope  RawEnvelope `json:"envelope"`
}

type RawEnvelope struct {
	ID        string `json:"id"`
	Timestamp int64  `json:"ts"`
	From      string `json:"from"`
	Kind      string `json:"kind,omitempty"`
	Payload   []byte `json:"payload"`
	Padding   []byte `json:"padding,omitempty"`
	Mac       []byte `json:"mac"`
}

type InboxResponse struct {
	Entries []InboxEntry `json:"entries"`
}

func (c *Client) Inbox(ctx context.Context, since int64, limit int) (InboxResponse, error) {
	q := ""
	if since > 0 {
		q = "?since=" + strconv.FormatInt(since, 10)
	}
	if limit > 0 {
		sep := "?"
		if q != "" {
			sep = "&"
		}
		q += sep + "limit=" + strconv.Itoa(limit)
	}
	var out InboxResponse
	if err := c.getJSON(ctx, "/inbox"+q, &out); err != nil {
		return InboxResponse{}, err
	}
	return out, nil
}

func (c *Client) DeleteInboxEntry(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/inbox/"+id, nil)
	if err != nil {
		return fmt.Errorf("backendapi: build delete inbox: %w", err)
	}
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: delete inbox: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return httpError(resp)
}

type IDSStats struct {
	EventCounts  map[string]int64 `json:"event_counts"`
	ActionCounts map[string]int64 `json:"action_counts"`
	LastEventAt  string           `json:"last_event_at"`
}

func (c *Client) IDSStats(ctx context.Context) (IDSStats, error) {
	var out IDSStats
	if err := c.getJSON(ctx, "/ids/stats", &out); err != nil {
		return IDSStats{}, err
	}
	return out, nil
}

const (
	WireKindText     = "text"
	WireKindStatus   = "status"
	WireKindPresence = "presence"

	PresenceSourceHaoma  = "haoma"
	PresenceSourceHaomad = "haomad"
)

type SendRequest struct {
	PeerID  string `json:"peer_id"`
	Payload []byte `json:"payload"`

	Kind           string `json:"kind,omitempty"`
	PresenceSource string `json:"presence_source,omitempty"`
}

type SendResponse struct {
	EnvelopeID string `json:"envelope_id"`
}

func (c *Client) Send(ctx context.Context, req SendRequest) (SendResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SendResponse{}, fmt.Errorf("backendapi: marshal send: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/send", bytes.NewReader(body))
	if err != nil {
		return SendResponse{}, fmt.Errorf("backendapi: build send: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return SendResponse{}, fmt.Errorf("backendapi: send post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return SendResponse{}, httpError(resp)
	}
	var out SendResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SendResponse{}, fmt.Errorf("backendapi: decode send: %w", err)
	}
	return out, nil
}

type OutboxRow struct {
	EnvelopeID     string `json:"envelope_id"`
	Dest           string `json:"dest"`
	State          string `json:"state"`
	Attempts       int    `json:"attempts"`
	AckFailures    int    `json:"ack_failures"`
	FirstAt        int64  `json:"first_at"`
	NextAttemptAt  int64  `json:"next_attempt_at"`
	StateChangedAt int64  `json:"state_changed_at"`
	LastError      string `json:"last_error,omitempty"`
}

type OutboxResponse struct {
	Rows []OutboxRow `json:"rows"`
}

func (c *Client) Outbox(ctx context.Context, state string, since int64, limit int) (OutboxResponse, error) {
	q := ""
	if state != "" {
		q += "?state=" + state
	}
	if since > 0 {
		sep := "?"
		if q != "" {
			sep = "&"
		}
		q += sep + "since=" + strconv.FormatInt(since, 10)
	}
	if limit > 0 {
		sep := "?"
		if q != "" {
			sep = "&"
		}
		q += sep + "limit=" + strconv.Itoa(limit)
	}
	var out OutboxResponse
	if err := c.getJSON(ctx, "/outbox"+q, &out); err != nil {
		return OutboxResponse{}, err
	}
	return out, nil
}

func (c *Client) OutboxEntry(ctx context.Context, id string) (OutboxRow, error) {
	var out OutboxRow
	if err := c.getJSON(ctx, "/outbox/"+id, &out); err != nil {
		return OutboxRow{}, err
	}
	return out, nil
}

type DHTInviteRequest struct {
	InviteJSON []byte `json:"invite_json"`
	SecretHex  string `json:"secret_hex"`
}

type DHTInviteResponse struct {
	GUID            string   `json:"guid"`
	IDWords         []string `json:"id_words"`
	PassphraseWords []string `json:"passphrase_words"`
	ExpiresAt       int64    `json:"expires_at"`
}

type DHTBootstrap struct {
	OnionURL  string `json:"onion_url"`
	GUID      string `json:"guid"`
	ExpiresAt int64  `json:"expires_at"`
}

type DHTFetchBootstrapRequest struct {
	IDWords         []string `json:"id_words"`
	PassphraseWords []string `json:"passphrase_words"`
}

type DHTProxyFetchRequest struct {
	OnionURL string `json:"onion_url"`
	GUID     string `json:"guid"`
}

type DHTProxyReturnRequest struct {
	OnionURL   string `json:"onion_url"`
	GUID       string `json:"guid"`
	SecretHex  string `json:"secret_hex"`
	ReturnBody []byte `json:"return_body"`
}

type DHTPendingEntry struct {
	GUID         string `json:"guid"`
	ReturnInvite []byte `json:"return_invite"`
	ReturnAt     int64  `json:"return_at"`
}

func (c *Client) DHTInvite(ctx context.Context, inviteJSON []byte, secretHex string) (DHTInviteResponse, error) {
	body, err := json.Marshal(DHTInviteRequest{InviteJSON: inviteJSON, SecretHex: secretHex})
	if err != nil {
		return DHTInviteResponse{}, fmt.Errorf("backendapi: marshal dht invite: %w", err)
	}
	var out DHTInviteResponse
	if err := c.postJSON(ctx, "/pair/publish", body, http.StatusCreated, &out); err != nil {
		return DHTInviteResponse{}, err
	}
	return out, nil
}

func (c *Client) DHTCancel(ctx context.Context, guid string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/pair/publish/"+guid, nil)
	if err != nil {
		return fmt.Errorf("backendapi: build dht cancel: %w", err)
	}
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: dht cancel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return httpError(resp)
}

func (c *Client) DHTFetchBootstrap(ctx context.Context, idWords, passphraseWords []string) (DHTBootstrap, error) {
	body, err := json.Marshal(DHTFetchBootstrapRequest{IDWords: idWords, PassphraseWords: passphraseWords})
	if err != nil {
		return DHTBootstrap{}, fmt.Errorf("backendapi: marshal dht fetch-bootstrap: %w", err)
	}
	var out DHTBootstrap
	if err := c.postJSON(ctx, "/pair/fetch", body, http.StatusOK, &out); err != nil {
		return DHTBootstrap{}, err
	}
	return out, nil
}

func (c *Client) DHTProxyFetch(ctx context.Context, onionURL, guid string) ([]byte, error) {
	body, err := json.Marshal(DHTProxyFetchRequest{OnionURL: onionURL, GUID: guid})
	if err != nil {
		return nil, fmt.Errorf("backendapi: marshal dht proxy fetch: %w", err)
	}
	return c.postRaw(ctx, "/pair/proxy/fetch", body, http.StatusOK)
}

func (c *Client) DHTProxyReturn(ctx context.Context, onionURL, guid, secretHex string, returnBody []byte) error {
	body, err := json.Marshal(DHTProxyReturnRequest{
		OnionURL:   onionURL,
		GUID:       guid,
		SecretHex:  secretHex,
		ReturnBody: returnBody,
	})
	if err != nil {
		return fmt.Errorf("backendapi: marshal dht proxy return: %w", err)
	}
	if _, err := c.postRaw(ctx, "/pair/proxy/return", body, http.StatusAccepted); err != nil {
		return err
	}
	return nil
}

func (c *Client) DHTPending(ctx context.Context) ([]DHTPendingEntry, error) {
	var wrapper struct {
		Pending []DHTPendingEntry `json:"pending"`
	}
	if err := c.getJSON(ctx, "/pair/return/pending", &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Pending, nil
}

type PairOnionInviteRequest struct {
	Payload        []byte `json:"payload"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type PairOnionInviteResponse struct {
	HandleID  string   `json:"handle_id"`
	Words     []string `json:"words"`
	ExpiresAt int64    `json:"expires_at"`
}

type PairOnionWaitResponse struct {
	JoinerPayload []byte `json:"joiner_payload"`
}

type PairOnionAcceptRequest struct {
	Words   []string `json:"words"`
	Payload []byte   `json:"payload"`
}

type PairOnionAcceptResponse struct {
	InviterPayload []byte `json:"inviter_payload"`
}

func (c *Client) PairOnionInvite(ctx context.Context, payload []byte, timeoutSeconds int) (PairOnionInviteResponse, error) {
	body, err := json.Marshal(PairOnionInviteRequest{Payload: payload, TimeoutSeconds: timeoutSeconds})
	if err != nil {
		return PairOnionInviteResponse{}, fmt.Errorf("backendapi: marshal pair onion invite: %w", err)
	}
	var out PairOnionInviteResponse
	if err := c.postJSON(ctx, "/pair/onion/invite", body, http.StatusCreated, &out); err != nil {
		return PairOnionInviteResponse{}, err
	}
	return out, nil
}

func (c *Client) PairOnionWait(ctx context.Context, handleID string) (PairOnionWaitResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/pair/onion/wait/"+handleID, nil)
	if err != nil {
		return PairOnionWaitResponse{}, fmt.Errorf("backendapi: build pair onion wait: %w", err)
	}
	c.authHeader(req)
	resp, err := c.longHTTP.Do(req)
	if err != nil {
		return PairOnionWaitResponse{}, fmt.Errorf("backendapi: pair onion wait: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PairOnionWaitResponse{}, httpError(resp)
	}
	var out PairOnionWaitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PairOnionWaitResponse{}, fmt.Errorf("backendapi: decode pair onion wait: %w", err)
	}
	return out, nil
}

func (c *Client) PairOnionCancel(ctx context.Context, handleID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/pair/onion/invite/"+handleID, nil)
	if err != nil {
		return fmt.Errorf("backendapi: build pair onion cancel: %w", err)
	}
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: pair onion cancel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return httpError(resp)
	}
	return nil
}

type StageFileRequest struct {
	MsgID            string   `json:"msg_id"`
	Ciphertext       []byte   `json:"ciphertext"`
	RecipientPeerIDs []string `json:"recipient_peer_ids"`
	ExpiresAt        int64    `json:"expires_at,omitempty"`
}

type StageFileResponse struct {
	MsgID  string   `json:"msg_id"`
	Tokens []string `json:"tokens"`
}

var ErrFileMsgIDInUse = fmt.Errorf("backendapi: file msg_id already staged")

func (c *Client) StageFile(ctx context.Context, req StageFileRequest) (StageFileResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return StageFileResponse{}, fmt.Errorf("backendapi: marshal stage file: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files", bytes.NewReader(body))
	if err != nil {
		return StageFileResponse{}, fmt.Errorf("backendapi: build stage file: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return StageFileResponse{}, fmt.Errorf("backendapi: stage file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return StageFileResponse{}, ErrFileMsgIDInUse
	}
	if resp.StatusCode != http.StatusCreated {
		return StageFileResponse{}, httpError(resp)
	}
	var out StageFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return StageFileResponse{}, fmt.Errorf("backendapi: decode stage file: %w", err)
	}
	return out, nil
}

type FetchFileRequest struct {
	MsgID          string `json:"msg_id"`
	PeerID         string `json:"peer_id"`
	Token          string `json:"token"`
	UrlPath        string `json:"url_path"`
	ExpectedSize   int64  `json:"expected_size"`
	ExpectedSha256 string `json:"expected_sha256"`
}

type FetchFileResponse struct {
	MsgID         string `json:"msg_id"`
	Token         string `json:"token"`
	State         string `json:"state"`
	BytesReceived int64  `json:"bytes_received,omitempty"`
}

func (c *Client) FetchFile(ctx context.Context, req FetchFileRequest) (FetchFileResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return FetchFileResponse{}, fmt.Errorf("backendapi: marshal fetch file: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files/fetch", bytes.NewReader(body))
	if err != nil {
		return FetchFileResponse{}, fmt.Errorf("backendapi: build fetch file: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return FetchFileResponse{}, fmt.Errorf("backendapi: fetch file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return FetchFileResponse{}, httpError(resp)
	}
	var out FetchFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return FetchFileResponse{}, fmt.Errorf("backendapi: decode fetch file: %w", err)
	}
	return out, nil
}

var ErrStagingNotFound = fmt.Errorf("backendapi: staging blob not found")

func (c *Client) FetchStagingBlob(ctx context.Context, msgID string) (io.ReadCloser, int64, error) {
	if msgID == "" {
		return nil, 0, fmt.Errorf("backendapi: empty msg_id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/files/staging/"+msgID, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("backendapi: build staging get: %w", err)
	}
	c.authHeader(req)
	resp, err := c.longHTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("backendapi: staging get: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, 0, ErrStagingNotFound
	}
	if resp.StatusCode != http.StatusOK {
		err := httpError(resp)
		resp.Body.Close()
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

func (c *Client) DropStagingBlob(ctx context.Context, msgID, token string) error {
	if msgID == "" {
		return fmt.Errorf("backendapi: empty msg_id")
	}
	body := []byte("{}")
	if token != "" {
		var err error
		body, err = json.Marshal(struct {
			Token string `json:"token"`
		}{Token: token})
		if err != nil {
			return fmt.Errorf("backendapi: marshal drop staging: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/files/staging/"+msgID, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backendapi: build drop staging: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: drop staging: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return httpError(resp)
}

func (c *Client) DropFile(ctx context.Context, msgID string) error {
	if msgID == "" {
		return fmt.Errorf("backendapi: empty msg_id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/files/"+msgID, nil)
	if err != nil {
		return fmt.Errorf("backendapi: build drop file: %w", err)
	}
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: drop file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return httpError(resp)
}

func (c *Client) RetryFailedFiles(ctx context.Context) (int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files/retry-failed", nil)
	if err != nil {
		return 0, fmt.Errorf("backendapi: build retry failed: %w", err)
	}
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("backendapi: retry failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, httpError(resp)
	}
	var out struct {
		Enqueued int `json:"enqueued"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("backendapi: decode retry failed: %w", err)
	}
	return out.Enqueued, nil
}

type RedeemFileReceiptRequest struct {
	Token           string `json:"token"`
	RecipientPeerID string `json:"recipient_peer_id"`
}

type RedeemFileReceiptResponse struct {
	Token             string `json:"token"`
	ReceiptsRemaining int    `json:"receipts_remaining"`
}

var (
	ErrFileReceiptRecipientMismatch = fmt.Errorf("backendapi: file receipt recipient mismatch")
	ErrFileReceiptTokenUnknown      = fmt.Errorf("backendapi: file receipt token unknown")
)

func (c *Client) RedeemFileReceipt(ctx context.Context, req RedeemFileReceiptRequest) (RedeemFileReceiptResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return RedeemFileReceiptResponse{}, fmt.Errorf("backendapi: marshal redeem receipt: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files/receipts", bytes.NewReader(body))
	if err != nil {
		return RedeemFileReceiptResponse{}, fmt.Errorf("backendapi: build redeem receipt: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return RedeemFileReceiptResponse{}, fmt.Errorf("backendapi: redeem receipt: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusForbidden:
		return RedeemFileReceiptResponse{}, ErrFileReceiptRecipientMismatch
	case http.StatusNotFound:
		return RedeemFileReceiptResponse{}, ErrFileReceiptTokenUnknown
	case http.StatusOK:
		var out RedeemFileReceiptResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return RedeemFileReceiptResponse{}, fmt.Errorf("backendapi: decode redeem receipt: %w", err)
		}
		return out, nil
	default:
		return RedeemFileReceiptResponse{}, httpError(resp)
	}
}

type ProxyServeRequest struct {
	Token     string `json:"token"`
	Modality  string `json:"modality"`
	LocalPort int    `json:"local_port"`
}

type ProxyFetchRequest struct {
	Token     string `json:"token"`
	Modality  string `json:"modality"`
	PeerURL   string `json:"peer_url"`
	LocalPort int    `json:"local_port"`
}

var ErrProxyTokenInUse = fmt.Errorf("backendapi: proxy token already registered with different params")

func (c *Client) ProxyServe(ctx context.Context, req ProxyServeRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("backendapi: marshal proxy serve: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/proxy/serve", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backendapi: build proxy serve: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("backendapi: proxy serve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrProxyTokenInUse
	}
	if resp.StatusCode != http.StatusCreated {
		return httpError(resp)
	}
	return nil
}

func (c *Client) ProxyFetch(ctx context.Context, req ProxyFetchRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("backendapi: marshal proxy fetch: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/proxy/fetch", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backendapi: build proxy fetch: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authHeader(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("backendapi: proxy fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrProxyTokenInUse
	}
	if resp.StatusCode != http.StatusCreated {
		return httpError(resp)
	}
	return nil
}

func (c *Client) ProxyCancel(ctx context.Context, token string) error {
	if token == "" {
		return fmt.Errorf("backendapi: empty token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/proxy/"+token, nil)
	if err != nil {
		return fmt.Errorf("backendapi: build proxy cancel: %w", err)
	}
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: proxy cancel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return httpError(resp)
}

func (c *Client) PairOnionAccept(ctx context.Context, words []string, payload []byte) (PairOnionAcceptResponse, error) {
	body, err := json.Marshal(PairOnionAcceptRequest{Words: words, Payload: payload})
	if err != nil {
		return PairOnionAcceptResponse{}, fmt.Errorf("backendapi: marshal pair onion accept: %w", err)
	}
	var out PairOnionAcceptResponse
	if err := c.postJSON(ctx, "/pair/onion/accept", body, http.StatusOK, &out); err != nil {
		return PairOnionAcceptResponse{}, err
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("backendapi: build GET %s: %w", path, err)
	}
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("backendapi: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) postJSON(ctx context.Context, path string, body []byte, wantStatus int, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backendapi: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return httpError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("backendapi: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) postRaw(ctx context.Context, path string, body []byte, wantStatus int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("backendapi: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backendapi: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return nil, httpError(resp)
	}
	return io.ReadAll(resp.Body)
}

func httpError(resp *http.Response) error {
	const cap = 512
	b, _ := io.ReadAll(io.LimitReader(resp.Body, cap))
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("backendapi: %s: %s", resp.Request.URL.Path, msg)
}

func (c *Client) BaseURL() string { return c.baseURL }

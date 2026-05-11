package xport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	HTTP *http.Client
}

var ErrEmptyResponse = errors.New("xport: peer returned empty body")

type PeerHTTPError struct {
	StatusCode int
	Body       string
}

func (e *PeerHTTPError) Error() string {
	return fmt.Sprintf("xport: peer returned %d: %s", e.StatusCode, e.Body)
}

func (c *Client) Send(ctx context.Context, baseURL string, env Envelope) ([]byte, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("xport: encode envelope: %w", err)
	}
	url := strings.TrimRight(baseURL, "/") + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("xport: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xport: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxEnvelopeBytes))
		if err != nil {
			return nil, fmt.Errorf("xport: read response body: %w", err)
		}
		return respBody, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return nil, &PeerHTTPError{StatusCode: resp.StatusCode, Body: string(bytes.TrimSpace(msg))}
}

func (c *Client) Probe(ctx context.Context, baseURL string, env Envelope) (*Envelope, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("xport: encode probe envelope: %w", err)
	}
	url := strings.TrimRight(baseURL, "/") + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("xport: build probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xport: probe post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("xport: probe peer returned %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxEnvelopeBytes))
	if err != nil {
		return nil, fmt.Errorf("xport: read probe response: %w", err)
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil, ErrEmptyResponse
	}
	var out Envelope
	dec := json.NewDecoder(bytes.NewReader(respBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("xport: decode probe response: %w", err)
	}
	return &out, nil
}

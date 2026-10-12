package backendapi

import (
	"context"
	"net/http"
)

type WipeResponse struct {
	Started bool `json:"started"`
}

func (c *Client) WipeDevice(ctx context.Context) (WipeResponse, error) {
	var out WipeResponse
	if err := c.postJSON(ctx, "/wipe", []byte("{}"), http.StatusOK, &out); err != nil {
		return WipeResponse{}, err
	}
	return out, nil
}

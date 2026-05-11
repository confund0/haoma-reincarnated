package main

import (
	"context"
	"errors"
)

type daemonRemoteDropper struct {
	d *daemon
}

func (r *daemonRemoteDropper) DropFile(ctx context.Context, msgID string) error {
	if r == nil || r.d == nil {
		return nil
	}
	c := r.d.backendClient
	if c == nil {
		return nil
	}
	if msgID == "" {
		return errors.New("daemonRemoteDropper: empty msg id")
	}
	return c.DropFile(ctx, msgID)
}

package pair

import (
	"context"
	"errors"
	"time"
)

type Driver interface {
	Tag() string

	CreateInvite(ctx context.Context, req CreateRequest) (PendingInvite, error)

	AcceptInvite(ctx context.Context, req AcceptRequest) (AcceptResult, error)
}

type CreateRequest struct {
	Payload []byte

	Timeout time.Duration

	LongLived bool

	Secret string
}

type AcceptRequest struct {
	Blob InviteBlob

	Payload []byte
}

type AcceptResult struct {
	InviterPayload []byte
}

type InviteBlob struct {
	Words []string

	Secret string

	Bytes []byte
}

type PendingInvite interface {
	OOB() OOB

	ExpiresAt() int64

	Wait(ctx context.Context) (WaitResult, error)

	Cancel()
}

type OOB struct {
	Words []string

	Secret string

	Tag string
}

type WaitResult struct {
	JoinerPayload []byte
}

var ErrCancelled = errors.New("pair: invite cancelled")

var ErrTimedOut = errors.New("pair: rendezvous timed out")

var ErrMACMismatch = errors.New("pair: handshake MAC mismatch")

var ErrRendezvousSpent = errors.New("pair: rendezvous already answered")

var ErrOnionWordCount = errors.New("pair: onion phrase has the wrong number of words")

var ErrInvalidBlob = errors.New("pair: invalid invite blob")

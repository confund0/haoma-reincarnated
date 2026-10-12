package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

const (
	rendezvousRepublishGrace = 20 * time.Second
	rendezvousDialTimeout    = 4 * time.Minute

	rendezvousReachableTimeout = 3 * time.Minute

	joinCheckTimeout = 150 * time.Second
)

func runRendezvousScenario(rigs []*rig) map[string]error {
	out := map[string]error{}
	inviter, joiner := rigs[0], rigs[1]

	fail := func(err error) map[string]error {
		out["rendezvous survives restart"] = err
		return out
	}

	invClient, err := backendClientFor(inviter)
	if err != nil {
		return fail(fmt.Errorf("could not reach %s's haomad: %w", inviter.name, err))
	}
	joinClient, err := backendClientFor(joiner)
	if err != nil {
		return fail(fmt.Errorf("could not reach %s's haomad: %w", joiner.name, err))
	}

	inviterPayload := []byte(`{"rig":"inviter"}`)
	joinerPayload := []byte(`{"rig":"joiner"}`)

	logf("[%s] offering a long-lived rendezvous", inviter.name)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	invite, err := invClient.PairOnionInvite(ctx, inviterPayload, 0, true)
	cancel()
	if err != nil {
		return fail(fmt.Errorf("could not offer the rendezvous: %w", err))
	}
	if invite.Secret == "" {
		return fail(fmt.Errorf("a long-lived rendezvous came back without a secret — nothing addresses it"))
	}
	if len(invite.Words) != 0 {
		return fail(fmt.Errorf("a long-lived rendezvous came back with words as well as a secret — " +
			"the phrase is the thing that gets read aloud, and this one is never read"))
	}

	if left := time.Until(time.Unix(invite.ExpiresAt, 0)); left < 6*24*time.Hour {
		return fail(fmt.Errorf("the rendezvous expires in %s, want about a week — "+
			"an invitation this short never outlives a restart in the first place", left.Round(time.Hour)))
	}

	logf("[%s] waiting for the invitation to become reachable", inviter.name)
	if err := awaitSelfProbe(inviter, invite.HandleID, rendezvousReachableTimeout); err != nil {
		out["a long-lived rendezvous is reachable"] = err
		return out
	}
	out["a long-lived rendezvous is reachable"] = nil

	logf("[%s] restarting haomad — the invitation is now only what was written down", inviter.name)
	inviter.stopHaomad()
	if err := inviter.restartHaomad(); err != nil {
		return fail(fmt.Errorf("could not bring %s's haomad back: %w", inviter.name, err))
	}

	time.Sleep(rendezvousRepublishGrace)

	waitErr := make(chan error, 1)
	waitOut := make(chan backendapi.PairOnionWaitResponse, 1)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), rendezvousDialTimeout+time.Minute)
	defer waitCancel()
	go func() {
		resp, err := waitForJoiner(waitCtx, invClient, invite.HandleID)
		waitOut <- resp
		waitErr <- err
	}()

	logf("[%s] dialling the address the invitation was offered at", joiner.name)
	accepted, err := dialUntilAnswered(joinClient, invite.Secret, joinerPayload, rendezvousDialTimeout)
	if err != nil {
		return fail(fmt.Errorf("the rendezvous did not answer after the restart — "+
			"the invitation is stranded and its holder is told nothing: %w", err))
	}
	if string(accepted.InviterPayload) != string(inviterPayload) {
		return fail(fmt.Errorf("the rendezvous answered with material that is not the inviter's — got %q",
			string(accepted.InviterPayload)))
	}
	out["rendezvous survives restart"] = nil

	out["restarted haomad completes the handshake"] = func() error {
		if err := <-waitErr; err != nil {
			return fmt.Errorf("the restarted haomad never saw the joiner arrive: %w", err)
		}
		got := <-waitOut
		if string(got.JoinerPayload) != string(joinerPayload) {
			return fmt.Errorf("the inviter got %q from the joiner, want %q",
				string(got.JoinerPayload), string(joinerPayload))
		}
		return nil
	}()

	logf("[%s] restarting haomad again — a concluded invitation must not come back", inviter.name)
	inviter.stopHaomad()
	if err := inviter.restartHaomad(); err != nil {
		out["a concluded rendezvous stays down"] = fmt.Errorf("could not bring %s's haomad back: %w", inviter.name, err)
		return out
	}
	time.Sleep(rendezvousRepublishGrace)

	secondCtx, secondCancel := context.WithTimeout(context.Background(), joinCheckTimeout)
	_, err = joinClient.PairOnionAccept(secondCtx, nil, invite.Secret, joinerPayload)
	secondCancel()
	switch {
	case err == nil:
		out["a concluded rendezvous stays down"] = fmt.Errorf(
			"the invitation answered a second dial — one secret, handed over once, is now serving anybody who kept it")
	case errors.Is(err, context.DeadlineExceeded):
		out["a concluded rendezvous stays down"] = fmt.Errorf(
			"the second dial got no answer from haomad inside %s, so this measured nothing: %w", joinCheckTimeout, err)
	default:
		logf("[%s] second dial refused, as it must be", joiner.name)
		out["a concluded rendezvous stays down"] = nil
	}
	return out
}

func backendClientFor(r *rig) (*backendapi.Client, error) {
	tierDir := filepath.Join(r.root, "backend")
	token, err := ipc.ReadSensitive(filepath.Join(tierDir, "haomad-token"))
	if err != nil {
		return nil, fmt.Errorf("read haomad token: %w", err)
	}
	tls, err := backendapi.PinnedTLSConfig(filepath.Join(tierDir, "cert.pem"))
	if err != nil {
		return nil, fmt.Errorf("pin haomad cert: %w", err)
	}
	return backendapi.New("https://"+r.haomadAddr, token, &http.Client{
		Timeout:   2 * time.Minute,
		Transport: &http.Transport{TLSClientConfig: tls},
	}), nil
}

func runRendezvousFleet(inviter, joiner *rig) *pairingResult {
	res := &pairingResult{a: inviter.name, b: joiner.name}
	out := runRendezvousScenario([]*rig{inviter, joiner})
	for _, name := range []string{
		"a long-lived rendezvous is reachable",
		"rendezvous survives restart",
		"restarted haomad completes the handshake",
		"a concluded rendezvous stays down",
	} {
		if err, ok := out[name]; ok {
			res.add(name, err)
		}
	}
	for _, c := range res.checks {
		if !c.ok {
			logf("[%s<->%s] %s FAILED: %s", inviter.name, joiner.name, c.name, c.err)
		}
	}
	return res
}

const rendezvousRigCount = 2

func waitForJoiner(ctx context.Context, c *backendapi.Client, handle string) (backendapi.PairOnionWaitResponse, error) {
	var last error
	for ctx.Err() == nil {
		resp, err := c.PairOnionWait(ctx, handle)
		if err == nil {
			return resp, nil
		}
		last = err
		if !strings.Contains(err.Error(), "unknown handle") {
			return resp, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if last == nil {
		last = ctx.Err()
	}
	return backendapi.PairOnionWaitResponse{}, last
}

func dialUntilAnswered(c *backendapi.Client, secret string, payload []byte, timeout time.Duration) (backendapi.PairOnionAcceptResponse, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		resp, err := c.PairOnionAccept(ctx, nil, secret, payload)
		cancel()
		if err == nil {
			if attempt > 1 {
				logf("rendezvous answered on attempt %d", attempt)
			}
			return resp, nil
		}
		last = err

		if !strings.Contains(err.Error(), "dial onion") {
			return resp, err
		}
		logf("attempt %d found nothing at the address yet", attempt)
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Second)
	}
	if last == nil {
		last = fmt.Errorf("no attempt was made within %s", timeout)
	}
	return backendapi.PairOnionAcceptResponse{}, last
}

func awaitSelfProbe(r *rig, handle string, timeout time.Duration) error {
	short := handle
	if len(short) > 8 {
		short = short[:8]
	}
	deadline := time.Now().Add(timeout)
	var lastErr string
	for time.Now().Before(deadline) {
		ready, last, err := scanSelfProbe(filepath.Join(r.root, "haomad.log"), short)
		if err == nil && ready {
			return nil
		}
		if last != "" {
			lastErr = last
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr == "" {
		lastErr = "haomad never probed it at all"
	}
	return fmt.Errorf("%s never reached its own rendezvous within %s: %s", r.name, timeout, lastErr)
}

func scanSelfProbe(path, shortHandle string) (ready bool, lastErr string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "pair: self-probe attempt") || !strings.Contains(line, shortHandle) {
			continue
		}
		if strings.Contains(line, "ready=true") {
			return true, "", nil
		}
		if i := strings.Index(line, "err="); i >= 0 {
			lastErr = line[i:]
		}
	}
	return false, lastErr, sc.Err()
}

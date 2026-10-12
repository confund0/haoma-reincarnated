package main

import (
	"strings"
	"testing"
	"time"
)

const ncMsgID = "0123456789abcdef0123456789abcdef"

func ncLine(at time.Time, msg string, fields ...string) string {
	return "time=" + at.Format(time.RFC3339Nano) + ` level=INFO msg="` + msg + `" ` + strings.Join(fields, " ")
}

func ncGood() netChangeObs {
	t0 := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	id := logMatchID("msg_id", ncMsgID)
	return netChangeObs{
		lines: []string{
			ncLine(t0.Add(-90*time.Second), fetchBeginLine, id),
			ncLine(t0.Add(-89*time.Second), fetchFailedTransient, id, "retry_attempts=1"),
			ncLine(t0.Add(2*time.Second), netChangeTorReset, "took_ms=40", "socks_moved=false"),
			ncLine(t0.Add(2*time.Second), netChangeKickedLine, "source=os", "rows=2", "destinations=2", "fetches=1", "failed=false"),
			ncLine(t0.Add(3*time.Second), fetchBeginLine, id),
			ncLine(t0.Add(9*time.Second), fetchReadyHaomadLine, id),
		},
		fetchMsgID:  ncMsgID,
		reenabledAt: t0,
		textNextAt:  t0.Add(150 * time.Second),
		textHeldAt:  t0.Add(10 * time.Second),
		source:      netChangeSourceOS,
		blobPulled:  true,
		socksBefore: "127.0.0.1:40001",
		socksAfter:  "127.0.0.1:40001",
		inSentAt:    t0.Add(20 * time.Second),
		inAckedAt:   t0.Add(50 * time.Second),
	}
}

func ncTorRun() netChangeObs {
	o := ncGood()
	t0 := o.reenabledAt
	id := logMatchID("msg_id", ncMsgID)
	o.lines = []string{
		o.lines[0], o.lines[1],
		ncLine(t0.Add(2*time.Second), netChangeKickedLine, "source=tor", "rows=2", "destinations=2", "fetches=1", "failed=false"),
		ncLine(t0.Add(3*time.Second), fetchBeginLine, id),
		ncLine(t0.Add(9*time.Second), fetchReadyHaomadLine, id),
		ncLine(t0.Add(63*time.Second), netChangeTorReset, "took_ms=40", "socks_moved=false"),
		ncLine(t0.Add(63*time.Second), netChangeKickedLine, "source=os", "rows=0", "destinations=0", "fetches=0", "failed=false"),
	}
	o.source = netChangeSourceTor
	o.inSentAt = t0.Add(70 * time.Second)
	o.inAckedAt = t0.Add(90 * time.Second)
	return o
}

func TestNetChangeVerdict_PassesOnTheTorTrigger(t *testing.T) {
	if err := netChangeVerdict(ncTorRun()); err != nil {
		t.Fatal(err)
	}
}

func TestNetChangeVerdict_Passes(t *testing.T) {
	if err := netChangeVerdict(ncGood()); err != nil {
		t.Fatal(err)
	}
}

func TestNetChangeVerdict_TorPathResetClaims(t *testing.T) {
	t0 := ncTorRun().reenabledAt
	cases := map[string]struct {
		edit func(o *netChangeObs)
		want string
	}{
		"no os run": {func(o *netChangeObs) { o.lines = o.lines[:5] }, "not asserted: dev-a logged no source=os handler run"},
		"reset before the tor run": {func(o *netChangeObs) {
			o.lines = append(o.lines[:2], append([]string{ncLine(t0.Add(time.Second), netChangeTorReset, "took_ms=40", "socks_moved=false")}, append(o.lines[2:5], o.lines[6])...)...)
		}, "before the source=tor run"},
		"two tor runs": {func(o *netChangeObs) {
			o.lines = append(o.lines, ncLine(t0.Add(80*time.Second), netChangeKickedLine, "source=tor", "rows=0", "destinations=0", "fetches=0", "failed=false"))
		}, "source=tor 2 times"},
		"the peer's text before the os run": {func(o *netChangeObs) { o.inSentAt = t0.Add(30 * time.Second) }, "sent before the source=os run"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := ncTorRun()
			tc.edit(&o)
			err := netChangeVerdict(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verdict = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

func TestNetChangeVerdict_Fails(t *testing.T) {
	t0 := ncGood().reenabledAt
	id := logMatchID("msg_id", ncMsgID)
	cases := map[string]struct {
		edit func(o *netChangeObs)
		want string
	}{
		"no handler run": {func(o *netChangeObs) { o.lines = append(o.lines[:3], o.lines[4:]...) }, "no network change handler run"},
		"handler without fetches": {func(o *netChangeObs) {
			o.lines[3] = ncLine(t0.Add(2*time.Second), netChangeKickedLine, "source=os", "rows=2", "destinations=2", "fetches=0", "failed=false")
		}, "retried no fetch"},
		"handler without rows": {func(o *netChangeObs) {
			o.lines[3] = ncLine(t0.Add(2*time.Second), netChangeKickedLine, "source=os", "rows=0", "destinations=0", "fetches=1", "failed=false")
		}, "reset no outbox row"},
		"wrong source":        {func(o *netChangeObs) { o.source = netChangeSourceTor }, `want "tor"`},
		"text by its backoff": {func(o *netChangeObs) { o.textHeldAt = o.textNextAt.Add(time.Second) }, "the backoff sent it"},
		"text never held":     {func(o *netChangeObs) { o.textHeldAt = time.Time{} }, "never held"},
		"another kick first": {func(o *netChangeObs) {
			o.lines = append(o.lines, ncLine(t0.Add(time.Second), peerKickResetLine, `signal="inbound envelope"`, "rows_moved=1"))
		}, "other reset"},
		"a manual sweep after": {func(o *netChangeObs) {
			o.lines = append(o.lines, ncLine(t0.Add(4*time.Second), fileRetrySweepAPILine, "enqueued=1"))
		}, "retry sweep"},
		"fetch never failed": {func(o *netChangeObs) { o.lines = o.lines[2:] }, "never failed"},
		"blob before handler": {func(o *netChangeObs) {
			o.lines[5] = ncLine(t0.Add(time.Second), fetchReadyHaomadLine, id)
		}, "did not pull the blob after the handler"},
		"two os runs": {func(o *netChangeObs) {
			o.lines = append(o.lines, ncLine(t0.Add(70*time.Second), netChangeKickedLine, "source=os", "rows=1", "destinations=1", "fetches=0", "failed=false"))
		}, "source=os 2 times"},
		"two tor runs after the os run": {func(o *netChangeObs) {
			for _, at := range []time.Duration{30 * time.Second, 95 * time.Second} {
				o.lines = append(o.lines, ncLine(t0.Add(at), netChangeKickedLine, "source=tor", "rows=0", "destinations=0", "fetches=0", "failed=false"))
			}
		}, "source=tor 2 times"},
		"haoma holds no file": {func(o *netChangeObs) { o.blobPulled = false }, "does not hold the file"},
		"no reset on the os run": {func(o *netChangeObs) {
			o.lines = append(o.lines[:2], o.lines[3:]...)
		}, "no tor network reset on the source=os run"},
		"reset after the kick": {func(o *netChangeObs) {
			o.lines[2], o.lines[3] = o.lines[3], o.lines[2]
		}, "after the kick"},
		"two resets": {func(o *netChangeObs) {
			o.lines = append(o.lines, ncLine(t0.Add(30*time.Second), netChangeTorReset, "took_ms=40", "socks_moved=false"))
		}, "2 times"},
		"reset logged the port moved": {func(o *netChangeObs) {
			o.lines[2] = ncLine(t0.Add(2*time.Second), netChangeTorReset, "took_ms=40", "socks_moved=true")
		}, "SOCKS pin did not hold"},
		"listener moved over the reset": {func(o *netChangeObs) { o.socksAfter = "127.0.0.1:40002" }, "moved from 127.0.0.1:40001 to 127.0.0.1:40002"},
		"listener not read":             {func(o *netChangeObs) { o.socksAfter = "" }, "could not read tor's SOCKS listener"},
		"a failed reset": {func(o *netChangeObs) {
			o.lines = append(o.lines, ncLine(t0.Add(time.Second), netChangeTorResetFail, "took_ms=3", `err="boom"`))
		}, "failed tor network reset"},
		"no text from the peer":            {func(o *netChangeObs) { o.inSentAt = time.Time{} }, "sent dev-a no text after the reset"},
		"the peer's text not acked":        {func(o *netChangeObs) { o.inAckedAt = time.Time{} }, "no sent_ack"},
		"the peer's text before the reset": {func(o *netChangeObs) { o.inSentAt = t0.Add(time.Second) }, "sent before the source=os run or its reset"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := ncGood()
			o.lines = append([]string(nil), o.lines...)
			tc.edit(&o)
			err := netChangeVerdict(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verdict = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

func TestFetchSettledFailed(t *testing.T) {
	t0 := time.Now()
	id := logMatchID("msg_id", ncMsgID)
	failed := []string{ncLine(t0, fetchBeginLine, id), ncLine(t0, fetchFailedTransient, id)}
	if !fetchSettledFailed(failed, ncMsgID) {
		t.Error("a fetch whose last line is a failure is settled")
	}
	if fetchSettledFailed(append(failed, ncLine(t0, fetchBeginLine, id)), ncMsgID) {
		t.Error("a fetch with an attempt after its failure is not settled")
	}
}

func TestGiveUpWindow(t *testing.T) {
	at := time.Now()
	got, err := giveUpWindow([]string{ncLine(at, giveUpOverrideLine, "give_up_age=30m0s", "default=48h0m0s")})
	if err != nil || got != 30*time.Minute {
		t.Errorf("giveUpWindow = %s %v, want 30m", got, err)
	}
	if _, err := giveUpWindow([]string{ncLine(at, "tor ready")}); err == nil {
		t.Error("no override line must be refused, not read as the shipped window")
	}
}

func TestGiveUpLeftCheck(t *testing.T) {
	first := time.Date(2026, 10, 7, 2, 20, 0, 0, time.UTC)
	next := first.Add(4 * time.Minute)
	if err := giveUpLeftCheck(first, 90*time.Second, next, netChangeGiveUpMargin); err == nil {
		t.Error("a 90 s window passed")
	}
	if err := giveUpLeftCheck(first, 8*time.Minute, next, netChangeGiveUpMargin); err == nil {
		t.Error("a window ending 1 min after the next attempt passed with a 5 min margin")
	}
	if err := giveUpLeftCheck(first, 30*time.Minute, next, netChangeGiveUpMargin); err != nil {
		t.Errorf("a 30 min window was refused: %v", err)
	}
}

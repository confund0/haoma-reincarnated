package backendapi

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

const (
	haomadVerifySource = "../../../backend/cmd/haomad/destroy_verify_send.go"
	haomadPeerSource   = "../../../backend/cmd/haomad/circuit.go"
	phoneVerifySource  = "../../../mobile/Android/app/src/main/java/io/haoma/calculator/messenger/MessengerStore_DeviceLink.kt"
)

func TestDestroyVerifyWaitsOutlastHaomad(t *testing.T) {
	haomad := time.Duration(readSeconds(t, haomadVerifySource,
		regexp.MustCompile(`const destroyVerifyDialTimeout = (\d+) \* time\.Second`))) * time.Second
	if haomad != DestroyVerifyDialTimeout {
		t.Errorf("haomad waits %v for the other device and this package says %v; change both in one edit", haomad, DestroyVerifyDialTimeout)
	}
	if haomad < 120*time.Second {
		t.Errorf("haomad waits %v for the other device; the join waits 120 s and the check must wait as long", haomad)
	}
	if DestroyVerifyWait <= DestroyVerifyDialTimeout {
		t.Errorf("haoma waits %v for haomad, which waits %v; haoma must wait longer", DestroyVerifyWait, DestroyVerifyDialTimeout)
	}

	verify := time.Duration(readSeconds(t, phoneVerifySource,
		regexp.MustCompile(`internal val DEVICE_LINK_VERIFY_TIMEOUT: Duration = (\d+)\.seconds`))) * time.Second
	if verify <= DestroyVerifyWait {
		t.Errorf("the phone waits %v for a check and haoma waits %v; the phone must wait longer", verify, DestroyVerifyWait)
	}

	command := time.Duration(readSeconds(t, haomadPeerSource,
		regexp.MustCompile(`hc\.Timeout = (\d+) \* time\.Second`))) * time.Second
	destroy := time.Duration(readSeconds(t, phoneVerifySource,
		regexp.MustCompile(`internal val DEVICE_LINK_DESTROY_TIMEOUT: Duration = (\d+)\.seconds`))) * time.Second
	if destroy <= DestroyVerifyWait+command {
		t.Errorf("the phone waits %v for a destroy, which runs a check (%v) and a command (%v); the phone must wait longer",
			destroy, DestroyVerifyWait, command)
	}
}

func readSeconds(t *testing.T, path string, re *regexp.Regexp) int {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	all := re.FindAllSubmatch(src, -1)
	if len(all) != 1 {
		t.Fatalf("%s: want exactly one line matching %q, found %d", path, re, len(all))
	}
	n, err := strconv.Atoi(string(all[0][1]))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return n
}

func TestVerifyDestroyPIN_OutwaitsTheShortClient(t *testing.T) {
	const shortLimit = 50 * time.Millisecond
	const answerAfter = 300 * time.Millisecond

	fb := newFakeBackend(t)
	fb.on(http.MethodPost, "/destroy-pin/verify", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(answerAfter):
		case <-r.Context().Done():
			return
		}
		jsonReply(w, http.StatusOK, VerifyDestroyPINResponse{Answered: true, Verified: true})
	})
	c := New(fb.URL, "", &http.Client{Timeout: shortLimit})

	out, err := c.VerifyDestroyPIN(context.Background(), VerifyDestroyPINRequest{PeerID: "sibling", Proof: "00"})
	if err != nil {
		t.Fatalf("VerifyDestroyPIN gave up before the daemon answered: %v", err)
	}
	if !out.Answered || !out.Verified {
		t.Fatalf("answer = %+v", out)
	}
}

func TestVerifyDestroyPIN_IsBoundedByTheWait(t *testing.T) {
	fb := newFakeBackend(t)
	c := New(fb.URL, "", nil)
	fb.on(http.MethodPost, "/destroy-pin/verify", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusOK, VerifyDestroyPINResponse{})
	})

	rec := &deadlineTransport{next: c.longHTTP.Transport}
	c.longHTTP = &http.Client{Transport: rec}

	before := time.Now()
	if _, err := c.VerifyDestroyPIN(context.Background(), VerifyDestroyPINRequest{PeerID: "sibling", Proof: "00"}); err != nil {
		t.Fatalf("VerifyDestroyPIN: %v", err)
	}
	if !rec.held {
		t.Fatal("the call reached haomad with no deadline at all")
	}
	left := rec.deadline.Sub(before)
	if left < DestroyVerifyWait-5*time.Second || left > DestroyVerifyWait+5*time.Second {
		t.Fatalf("the call reached haomad with %v left, want about %v", left.Round(time.Second), DestroyVerifyWait)
	}
}

type deadlineTransport struct {
	next     http.RoundTripper
	deadline time.Time
	held     bool
}

func (d *deadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.deadline, d.held = req.Context().Deadline()
	next := d.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

func TestDestroyVerifyAnswerBadMatchesHaomad(t *testing.T) {
	src, err := os.ReadFile(haomadVerifySource)
	if err != nil {
		t.Fatalf("read %s: %v", haomadVerifySource, err)
	}
	all := regexp.MustCompile(`const destroyVerifyAnswerBad = "([a-z_]+)"`).FindAllSubmatch(src, -1)
	if len(all) != 1 {
		t.Fatalf("%s: want exactly one destroyVerifyAnswerBad line, found %d", haomadVerifySource, len(all))
	}
	if got := string(all[0][1]); got != DestroyVerifyAnswerBad {
		t.Errorf("haomad says %q and this package says %q; change both in one edit", got, DestroyVerifyAnswerBad)
	}
}

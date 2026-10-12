package syncrail_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/syncrail"
)

var everyVerb = []string{"%d", "%v", "%+v", "%#v", "%s", "%x"}

func assertNoVerbRenders(t *testing.T, args []any, secrets []string) {
	t.Helper()
	for _, verb := range everyVerb {
		for _, arg := range args {
			out := fmt.Sprintf(verb, arg)
			for _, secret := range secrets {
				if strings.Contains(out, secret) {
					t.Fatalf("%s on %T rendered a secret: %q in %s", verb, arg, secret, out)
				}
			}
		}
	}
}

func offerSecrets(o syncrail.SiblingOffer) []string {
	return []string{
		o.Token,
		hex.EncodeToString(o.Key),
		hex.EncodeToString(o.Nonce),
		fmt.Sprint(o.Key),
		fmt.Sprint(o.Nonce),
	}
}

func TestSiblingOffer_NoVerbRendersItsSecrets(t *testing.T) {
	o := sampleSiblingOffer()
	assertNoVerbRenders(t, []any{o, &o}, offerSecrets(o))
}

func sampleOfferMirrorBody() syncrail.MirrorBody {
	b := sampleMirrorBody()
	o := sampleSiblingOffer()
	b.WireKind = "file_offer"
	b.Wrapper = []byte(`{"v":1,"kind":"file_offer","body":{"token":"` + o.Token + `"}}`)
	b.SiblingOffer = &o
	return b
}

func TestMirrorBody_NoVerbRendersItsSecrets(t *testing.T) {
	b := sampleOfferMirrorBody()
	secrets := append(offerSecrets(*b.SiblingOffer),
		string(b.Wrapper),

		fmt.Sprint(b.Wrapper),
		hex.EncodeToString(b.Wrapper),
	)
	assertNoVerbRenders(t, []any{b, &b}, secrets)
}

func TestMirrorBody_LogWithholdsTheWrapper(t *testing.T) {
	plain := sampleMirrorBody()
	withOffer := sampleOfferMirrorBody()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Debug("plain", slog.Any("body", plain))
	logger.Debug("offer", slog.Any("body", withOffer), slog.Any("ptr", &withOffer))
	var jbuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&jbuf, &slog.HandlerOptions{Level: slog.LevelDebug})).
		Debug("offer", slog.Any("body", withOffer))
	out := buf.String() + jbuf.String()

	secrets := append(offerSecrets(*withOffer.SiblingOffer),
		string(withOffer.Wrapper), fmt.Sprint(withOffer.Wrapper), fmt.Sprint(plain.Wrapper))
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Fatalf("the log rendered %q: %s", s, out)
		}
	}
	for _, want := range []string{"sibling_offer_present=false", "sibling_offer_present=true",
		"body.sibling_offer.key_bytes=32", "wrapper_bytes="} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("the log lost %q: %s", want, buf.String())
		}
	}
}

package syncrail_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func TestFileRequest_WireIsPinned(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	const raw = `{"v":1,"kind":"file-request","ts":1,"body":{"msg_ids":["0123456789abcdef0123456789abcdef","fedcba9876543210fedcba9876543210"]}}`

	env, err := syncrail.OpenEnvelope(key, sealRawEnvelope(t, key, raw))
	if err != nil {
		t.Fatalf("OpenEnvelope on a hand-written file request: %v", err)
	}
	if env.Kind != syncrail.KindFileRequest {
		t.Fatalf("kind = %q, want KindFileRequest", env.Kind)
	}
	body, err := syncrail.ParseFileRequest(env)
	if err != nil {
		t.Fatalf("ParseFileRequest: %v", err)
	}
	if len(body.MsgIDs) != 2 || body.MsgIDs[0] != "0123456789abcdef0123456789abcdef" ||
		body.MsgIDs[1] != "fedcba9876543210fedcba9876543210" {
		t.Fatalf("msg ids = %v; the msg_ids tag changed", body.MsgIDs)
	}

	out, err := json.Marshal(syncrail.FileRequestBody{MsgIDs: []string{"a1"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"msg_ids":["a1"]}`; string(out) != want {
		t.Fatalf("encoded %s, want %s", out, want)
	}
	if string(syncrail.KindFileRequest) != "file-request" {
		t.Fatalf("KindFileRequest = %q, want \"file-request\"", syncrail.KindFileRequest)
	}
}

func TestGapSiblingOffer_WireIsPinned(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	offerKey := bytes.Repeat([]byte{0xa7}, syncrail.KeySize)
	offerNonce := bytes.Repeat([]byte{0x5c}, syncrail.NonceSize)
	b64 := base64.StdEncoding.EncodeToString
	body := `{"fingerprint":"aa11bb22cc33dd44ee55ff6600778899",` +
		`"msg_id":"0123456789abcdef0123456789abcdef",` +
		`"file_offer":{"token":"tok-1","url_path":"/files/tok-1","name":"a.pdf","size":4016,"mime":"application/pdf","sha256_ciphertext":"c0ffee"},` +
		`"sibling_offer":{"staging_id":"5a1b2c3d4e5f60718293a4b5c6d7e8f9","token":"tok-1",` +
		`"key":"` + b64(offerKey) + `","nonce":"` + b64(offerNonce) + `",` +
		`"blob_size":5000,"plaintext_size":4000,"sha256_ciphertext":"c0ffee"}}`
	raw := `{"v":1,"kind":"gap-sibling-offer","ts":1,"body":` + body + `}`

	env, err := syncrail.OpenEnvelope(key, sealRawEnvelope(t, key, raw))
	if err != nil {
		t.Fatalf("OpenEnvelope on a hand-written sibling offer on gap-fill: %v", err)
	}
	if env.Kind != syncrail.KindGapSiblingOffer {
		t.Fatalf("kind = %q, want KindGapSiblingOffer", env.Kind)
	}
	got, err := syncrail.ParseGapSiblingOffer(env)
	if err != nil {
		t.Fatalf("ParseGapSiblingOffer: %v", err)
	}
	o := got.SiblingOffer
	switch {
	case got.Fingerprint != "aa11bb22cc33dd44ee55ff6600778899":
		t.Fatal("the fingerprint tag changed")
	case got.MsgID != "0123456789abcdef0123456789abcdef":
		t.Fatal("the msg_id tag changed")
	case !bytes.Contains(got.FileOffer, []byte(`"url_path":"/files/tok-1"`)):
		t.Fatal("the file_offer tag changed")
	case o.StagingID != "5a1b2c3d4e5f60718293a4b5c6d7e8f9":
		t.Fatal("the staging_id tag changed")
	case o.Token != "tok-1":
		t.Fatal("the token tag changed")
	case !bytes.Equal(o.Key, offerKey):
		t.Fatal("the key tag changed")
	case !bytes.Equal(o.Nonce, offerNonce):
		t.Fatal("the nonce tag changed")
	case o.BlobSize != 5000 || o.PlaintextSize != 4000:
		t.Fatal("a size tag changed")
	case o.Sha256Ciphertext != "c0ffee":
		t.Fatal("the sha256_ciphertext tag changed")
	}

	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != body {
		t.Fatalf("encoded\n%s\nwant\n%s", out, body)
	}
	if string(syncrail.KindGapSiblingOffer) != "gap-sibling-offer" {
		t.Fatalf("KindGapSiblingOffer = %q, want \"gap-sibling-offer\"", syncrail.KindGapSiblingOffer)
	}
}

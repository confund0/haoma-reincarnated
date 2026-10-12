package syncrail_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func fileRequestEnvelope(t *testing.T, body any) syncrail.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindFileRequest, Ts: 1, Body: raw}
}

func fileRequestIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%032x", i+1)
	}
	return ids
}

func TestFileRequest_RoundTripThroughTheSeal(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity-material"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	ids := fileRequestIDs(3)
	sealed, err := syncrail.SealEnvelope(key, fileRequestEnvelope(t, syncrail.FileRequestBody{MsgIDs: ids}))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	if env.Kind != syncrail.KindFileRequest {
		t.Fatalf("kind = %q, want %q", env.Kind, syncrail.KindFileRequest)
	}
	body, err := syncrail.ParseFileRequest(env)
	if err != nil {
		t.Fatalf("ParseFileRequest: %v", err)
	}
	if strings.Join(body.MsgIDs, ",") != strings.Join(ids, ",") {
		t.Fatalf("msg ids = %v, want %v", body.MsgIDs, ids)
	}
}

func TestFileRequest_KindIsKnown(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity-material"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	sealed, err := syncrail.SealEnvelope(key, fileRequestEnvelope(t, syncrail.FileRequestBody{MsgIDs: fileRequestIDs(1)}))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	if _, err := syncrail.OpenEnvelope(key, sealed); errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatal("the file-request kind opens as unknown")
	}
}

func TestFileRequest_TakesARequestAtTheLimit(t *testing.T) {
	body, err := syncrail.ParseFileRequest(fileRequestEnvelope(t,
		syncrail.FileRequestBody{MsgIDs: fileRequestIDs(syncrail.MaxFileRequestIDs)}))
	if err != nil {
		t.Fatalf("a request of exactly %d ids was refused: %v", syncrail.MaxFileRequestIDs, err)
	}
	if len(body.MsgIDs) != syncrail.MaxFileRequestIDs {
		t.Fatalf("msg ids = %d, want %d", len(body.MsgIDs), syncrail.MaxFileRequestIDs)
	}
}

func TestFileRequest_RefusesAnUnusableBody(t *testing.T) {
	dup := fileRequestIDs(2)
	dup[1] = dup[0]
	cases := map[string]any{
		"no body field":  map[string]any{},
		"null ids":       map[string]any{"msg_ids": nil},
		"no ids":         syncrail.FileRequestBody{MsgIDs: []string{}},
		"an empty id":    syncrail.FileRequestBody{MsgIDs: []string{fileRequestIDs(1)[0], ""}},
		"an id twice":    syncrail.FileRequestBody{MsgIDs: dup},
		"over the limit": syncrail.FileRequestBody{MsgIDs: fileRequestIDs(syncrail.MaxFileRequestIDs + 1)},
		"ids not a list": map[string]any{"msg_ids": "0000"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := syncrail.ParseFileRequest(fileRequestEnvelope(t, body))
			if err == nil {
				t.Fatalf("parsed without error: %v", got.MsgIDs)
			}
			if len(got.MsgIDs) != 0 {
				t.Fatalf("returned msg ids alongside the error: %v", got.MsgIDs)
			}
		})
	}
}

func TestFileRequest_RefusesAnotherKind(t *testing.T) {
	env := fileRequestEnvelope(t, syncrail.FileRequestBody{MsgIDs: fileRequestIDs(1)})
	env.Kind = syncrail.KindGapRequest
	if _, err := syncrail.ParseFileRequest(env); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("err = %v, want ErrUnknownKind", err)
	}
}

func TestFileRequest_ErrorNeverCarriesAMsgID(t *testing.T) {
	const id = "feedfacefeedfacefeedfacefeedface"
	_, err := syncrail.ParseFileRequest(fileRequestEnvelope(t, syncrail.FileRequestBody{MsgIDs: []string{id, id}}))
	if err == nil {
		t.Fatal("a request naming one id twice parsed")
	}
	if strings.Contains(err.Error(), id) || strings.Contains(err.Error(), id[:8]) {
		t.Fatalf("the error carries the msg id: %v", err)
	}
}

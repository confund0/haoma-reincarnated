package identity

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"haoma/internal/store"
	"haoma/internal/tor/control"
)

func TestMain(m *testing.M) {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

type mockPublisher struct {
	newResp  *control.Onion
	newErr   error
	newQueue []*control.Onion
	newPorts [][]control.OnionPort

	addResp  *control.Onion
	addErr   error
	addCalls []addCall

	delCalls []string
	delErr   error
}

type addCall struct {
	privateKey string
	ports      []control.OnionPort
}

func (m *mockPublisher) AddOnionNew(ports []control.OnionPort, flags ...string) (*control.Onion, error) {
	m.newPorts = append(m.newPorts, ports)
	if m.newErr != nil {
		return nil, m.newErr
	}
	if len(m.newQueue) > 0 {
		o := m.newQueue[0]
		m.newQueue = m.newQueue[1:]
		return o, nil
	}
	return m.newResp, nil
}

func (m *mockPublisher) AddOnion(privateKey string, ports []control.OnionPort, flags ...string) (*control.Onion, error) {
	m.addCalls = append(m.addCalls, addCall{privateKey, ports})
	return m.addResp, m.addErr
}

func (m *mockPublisher) DelOnion(serviceID string) error {
	m.delCalls = append(m.delCalls, serviceID)
	return m.delErr
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatalf("store.Unlock: %v", err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	return s
}

func testPorts() [][]control.OnionPort {
	return [][]control.OnionPort{
		{{VirtPort: 80, Target: "127.0.0.1:8080"}},
		{{VirtPort: 80, Target: "127.0.0.1:8081"}},
	}
}

func TestLoadOrPublish_Fresh_GeneratesSlotCount(t *testing.T) {
	s := newTestStore(t)
	p := &mockPublisher{
		newQueue: []*control.Onion{
			{ServiceID: "svc1", PrivateKey: "pk1"},
			{ServiceID: "svc2", PrivateKey: "pk2"},
		},
	}

	id, err := LoadOrPublish(s, p, testPorts())
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Active) != SlotCount {
		t.Fatalf("Active length = %d, want %d", len(id.Active), SlotCount)
	}
	if id.Active[0].ServiceID != "svc1" || id.Active[1].ServiceID != "svc2" {
		t.Errorf("Active = %+v", id.Active)
	}
	if len(p.newPorts) != SlotCount {
		t.Errorf("AddOnionNew called %d times, want %d", len(p.newPorts), SlotCount)
	}
	if p.newPorts[0][0].Target != "127.0.0.1:8080" || p.newPorts[1][0].Target != "127.0.0.1:8081" {
		t.Errorf("per-slot ports not routed: %v", p.newPorts)
	}
	if id.RotatedAt == 0 {
		t.Errorf("RotatedAt = 0, want >0 on fresh generate")
	}

	raw, err := s.GetState(stateKey)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unparseable: %v", err)
	}
	if env.Version != stateVersion {
		t.Errorf("persisted version = %d, want %d", env.Version, stateVersion)
	}
	if len(env.Active) != SlotCount {
		t.Errorf("persisted Active = %+v", env.Active)
	}
}

func TestLoadOrPublish_Restart_V2(t *testing.T) {
	s := newTestStore(t)
	seed := envelope{
		Version:   stateVersion,
		RotatedAt: 1_700_000_000,
		Active: []Onion{
			{ServiceID: "svc1", PrivateKey: "pk1"},
			{ServiceID: "svc2", PrivateKey: "pk2"},
		},
	}
	raw, _ := json.Marshal(seed)
	if err := s.PutState(stateKey, raw); err != nil {
		t.Fatal(err)
	}

	p := &mockPublisher{}
	p.addResp = &control.Onion{ServiceID: ""}

	callIdx := 0
	expect := []string{"svc1", "svc2"}
	addResponder := func(pk string, _ []control.OnionPort, _ ...string) (*control.Onion, error) {
		defer func() { callIdx++ }()
		return &control.Onion{ServiceID: expect[callIdx]}, nil
	}
	wp := &wrappedPublisher{AddOnionFn: addResponder}
	id, err := LoadOrPublish(s, wp, testPorts())
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Active) != SlotCount {
		t.Fatalf("Active = %+v", id.Active)
	}
	if id.RotatedAt != 1_700_000_000 {
		t.Errorf("RotatedAt = %d, want preserved 1_700_000_000", id.RotatedAt)
	}
	if len(wp.addCalls) != 2 {
		t.Errorf("AddOnion calls = %d, want 2", len(wp.addCalls))
	}
	if len(p.newPorts) != 0 {
		t.Errorf("AddOnionNew called %d times on restart, want 0", len(p.newPorts))
	}
	_ = p
}

func TestLoadOrPublish_Migration_V1ToV2(t *testing.T) {
	s := newTestStore(t)
	seed := envelope{Version: 1, Active: []Onion{{ServiceID: "existing", PrivateKey: "pk"}}}
	raw, _ := json.Marshal(seed)
	if err := s.PutState(stateKey, raw); err != nil {
		t.Fatal(err)
	}

	wp := &wrappedPublisher{
		AddOnionFn: func(pk string, _ []control.OnionPort, _ ...string) (*control.Onion, error) {
			return &control.Onion{ServiceID: "existing"}, nil
		},
		AddOnionNewFn: func(_ []control.OnionPort, _ ...string) (*control.Onion, error) {
			return &control.Onion{ServiceID: "topup", PrivateKey: "topupPK"}, nil
		},
	}
	id, err := LoadOrPublish(s, wp, testPorts())
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Active) != SlotCount {
		t.Fatalf("Active = %+v", id.Active)
	}
	if id.Active[0].ServiceID != "existing" || id.Active[1].ServiceID != "topup" {
		t.Errorf("Active = %+v, want [existing, topup]", id.Active)
	}

	raw2, _ := s.GetState(stateKey)
	var env envelope
	_ = json.Unmarshal(raw2, &env)
	if env.Version != stateVersion {
		t.Errorf("post-migration version = %d, want %d", env.Version, stateVersion)
	}
	if len(env.Active) != SlotCount {
		t.Errorf("post-migration Active len = %d", len(env.Active))
	}
}

func TestLoadOrPublish_RepublishMismatchDetected(t *testing.T) {
	s := newTestStore(t)
	seed := envelope{
		Version: stateVersion,
		Active:  []Onion{{ServiceID: "expected-a", PrivateKey: "pka"}, {ServiceID: "expected-b", PrivateKey: "pkb"}},
	}
	raw, _ := json.Marshal(seed)
	_ = s.PutState(stateKey, raw)

	wp := &wrappedPublisher{
		AddOnionFn: func(pk string, _ []control.OnionPort, _ ...string) (*control.Onion, error) {
			return &control.Onion{ServiceID: "different"}, nil
		},
	}
	_, err := LoadOrPublish(s, wp, testPorts())
	if err == nil {
		t.Fatal("expected error on ServiceID mismatch")
	}
}

func TestLoadOrPublish_CorruptJSON(t *testing.T) {
	s := newTestStore(t)
	_ = s.PutState(stateKey, []byte("{not json"))
	p := &mockPublisher{}
	if _, err := LoadOrPublish(s, p, testPorts()); err == nil {
		t.Fatal("expected error on corrupt JSON")
	}
}

func TestLoadOrPublish_UnsupportedVersion(t *testing.T) {
	s := newTestStore(t)
	seed := envelope{Version: 999, Active: nil}
	raw, _ := json.Marshal(seed)
	_ = s.PutState(stateKey, raw)

	p := &mockPublisher{}
	_, err := LoadOrPublish(s, p, testPorts())
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestLoadOrPublish_GenerateFailure_StoreUnchanged(t *testing.T) {
	s := newTestStore(t)
	p := &mockPublisher{newErr: errors.New("tor broke")}
	_, err := LoadOrPublish(s, p, testPorts())
	if err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.GetState(stateKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestLoadOrPublish_PortsPerSlotWrongLength(t *testing.T) {
	s := newTestStore(t)
	p := &mockPublisher{}
	if _, err := LoadOrPublish(s, p, nil); err == nil {
		t.Error("nil portsPerSlot accepted")
	}
	if _, err := LoadOrPublish(s, p, [][]control.OnionPort{{{VirtPort: 80}}}); err == nil {
		t.Error("length-1 portsPerSlot accepted")
	}
}

func TestServiceIDs(t *testing.T) {
	id := &Identity{Active: []Onion{{ServiceID: "a"}, {ServiceID: "b"}}}
	want := []string{"a", "b"}
	got := id.ServiceIDs()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

type wrappedPublisher struct {
	AddOnionNewFn func(ports []control.OnionPort, flags ...string) (*control.Onion, error)
	AddOnionFn    func(pk string, ports []control.OnionPort, flags ...string) (*control.Onion, error)
	DelOnionFn    func(sid string) error

	newCalls []newCall
	addCalls []addCall
	delCalls []string
}

type newCall struct {
	ports []control.OnionPort
}

func (w *wrappedPublisher) AddOnionNew(ports []control.OnionPort, flags ...string) (*control.Onion, error) {
	w.newCalls = append(w.newCalls, newCall{ports})
	if w.AddOnionNewFn != nil {
		return w.AddOnionNewFn(ports, flags...)
	}
	return nil, errors.New("wrappedPublisher: AddOnionNew not configured")
}

func (w *wrappedPublisher) AddOnion(pk string, ports []control.OnionPort, flags ...string) (*control.Onion, error) {
	w.addCalls = append(w.addCalls, addCall{pk, ports})
	if w.AddOnionFn != nil {
		return w.AddOnionFn(pk, ports, flags...)
	}
	return nil, errors.New("wrappedPublisher: AddOnion not configured")
}

func (w *wrappedPublisher) DelOnion(sid string) error {
	w.delCalls = append(w.delCalls, sid)
	if w.DelOnionFn != nil {
		return w.DelOnionFn(sid)
	}
	return nil
}

func TestRepublish_CallsAddOnionForEachSlot(t *testing.T) {
	id := &Identity{
		Active: []Onion{
			{ServiceID: "svc1", PrivateKey: "pk1"},
			{ServiceID: "svc2", PrivateKey: "pk2"},
		},
	}
	ports := testPorts()
	wp := &wrappedPublisher{
		AddOnionFn: func(pk string, _ []control.OnionPort, _ ...string) (*control.Onion, error) {
			switch pk {
			case "pk1":
				return &control.Onion{ServiceID: "svc1", PrivateKey: "pk1"}, nil
			case "pk2":
				return &control.Onion{ServiceID: "svc2", PrivateKey: "pk2"}, nil
			}
			return nil, errors.New("unexpected key")
		},
	}

	if err := id.Republish(wp, ports); err != nil {
		t.Fatal(err)
	}
	if len(wp.addCalls) != 2 {
		t.Fatalf("AddOnion calls = %d, want 2", len(wp.addCalls))
	}
	if wp.addCalls[0].privateKey != "pk1" || wp.addCalls[1].privateKey != "pk2" {
		t.Errorf("wrong keys: %v", wp.addCalls)
	}
}

func TestRepublish_AddOnionError_Propagates(t *testing.T) {
	id := &Identity{
		Active: []Onion{
			{ServiceID: "svc1", PrivateKey: "pk1"},
			{ServiceID: "svc2", PrivateKey: "pk2"},
		},
	}
	wp := &wrappedPublisher{
		AddOnionFn: func(_ string, _ []control.OnionPort, _ ...string) (*control.Onion, error) {
			return nil, errors.New("tor gone")
		},
	}
	if err := id.Republish(wp, testPorts()); err == nil {
		t.Error("expected error from AddOnion failure")
	}
}

func TestRepublish_ServiceIDMismatch_Errors(t *testing.T) {
	id := &Identity{
		Active: []Onion{
			{ServiceID: "svc1", PrivateKey: "pk1"},
			{ServiceID: "svc2", PrivateKey: "pk2"},
		},
	}
	wp := &wrappedPublisher{
		AddOnionFn: func(_ string, _ []control.OnionPort, _ ...string) (*control.Onion, error) {
			return &control.Onion{ServiceID: "different", PrivateKey: "pk1"}, nil
		},
	}
	if err := id.Republish(wp, testPorts()); err == nil {
		t.Error("expected error when ServiceID doesn't match stored value")
	}
}

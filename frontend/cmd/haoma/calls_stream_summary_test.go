package main

import (
	"log/slog"
	"strings"
	"testing"
)

func TestDecodeRawStreamFields_KeepsOrder_SkipsType(t *testing.T) {
	raw := []byte(`{"type":"summary","period_ms":1000,"frames_in":50,"jitter_ms":12.40,"muted":false,"reason":"cap"}`)
	attrs, err := decodeRawStreamFields(raw)
	if err != nil {
		t.Fatalf("decodeRawStreamFields: %v", err)
	}

	var gotKeys []string
	for _, a := range attrs {
		gotKeys = append(gotKeys, a.(slog.Attr).Key)
	}
	wantKeys := []string{"period_ms", "frames_in", "jitter_ms", "muted", "reason"}
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("key order = %v, want %v", gotKeys, wantKeys)
	}
}

func TestDecodeRawStreamFields_IntVsFloatVsBool(t *testing.T) {
	raw := []byte(`{"type":"event","ev":"xrun","xrun_total":3,"jitter_ms":12.40,"muted":false,"name":"x"}`)
	attrs, err := decodeRawStreamFields(raw)
	if err != nil {
		t.Fatalf("decodeRawStreamFields: %v", err)
	}

	want := map[string]slog.Attr{
		"ev":         slog.String("ev", "xrun"),
		"xrun_total": slog.Int64("xrun_total", 3),
		"jitter_ms":  slog.Float64("jitter_ms", 12.40),
		"muted":      slog.Bool("muted", false),
		"name":       slog.String("name", "x"),
	}
	for _, a := range attrs {
		attr := a.(slog.Attr)
		wantAttr, ok := want[attr.Key]
		if !ok {
			t.Fatalf("unexpected key %q", attr.Key)
		}
		if attr.Value.Kind() != wantAttr.Value.Kind() {
			t.Errorf("key %q: kind = %v, want %v", attr.Key, attr.Value.Kind(), wantAttr.Value.Kind())
		}
		if attr.Value.Any() != wantAttr.Value.Any() {
			t.Errorf("key %q: value = %v, want %v", attr.Key, attr.Value.Any(), wantAttr.Value.Any())
		}
	}
}

func TestDecodeRawStreamFields_NestedValueErrors(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"type":"summary","extra":{"nested":1}}`),
		[]byte(`{"type":"summary","extra":[1,2,3]}`),
		[]byte(`{"type":"summary",`),
		[]byte(`[1,2,3]`),
	}
	for _, raw := range cases {
		if _, err := decodeRawStreamFields(raw); err == nil {
			t.Errorf("decodeRawStreamFields(%s): want an error, got nil", raw)
		}
	}
}

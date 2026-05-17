package main

import (
	"reflect"
	"strings"
	"testing"

	"haoma-frontend/internal/msg"
)

func TestResolveStartCallModalities(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{name: "empty defaults audio", in: nil, want: []string{msg.ModalityAudio}},
		{name: "empty slice defaults audio", in: []string{}, want: []string{msg.ModalityAudio}},
		{name: "audio only", in: []string{msg.ModalityAudio}, want: []string{msg.ModalityAudio}},
		{name: "video only", in: []string{msg.ModalityVideo}, want: []string{msg.ModalityVideo}},
		{name: "audio then video", in: []string{msg.ModalityAudio, msg.ModalityVideo}, want: []string{msg.ModalityAudio, msg.ModalityVideo}},
		{name: "video then audio normalises to canonical order", in: []string{msg.ModalityVideo, msg.ModalityAudio}, want: []string{msg.ModalityAudio, msg.ModalityVideo}},
		{name: "duplicates collapsed", in: []string{msg.ModalityAudio, msg.ModalityAudio}, want: []string{msg.ModalityAudio}},
		{name: "screen rejected", in: []string{msg.ModalityScreen}, wantErr: "unsupported modality"},
		{name: "unknown rejected", in: []string{"telepathy"}, wantErr: "unsupported modality"},
		{name: "partial unknown rejected", in: []string{msg.ModalityAudio, "screen"}, wantErr: "unsupported modality"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveStartCallModalities(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveStartCallModalities(%v): want error containing %q, got nil (result=%v)", tc.in, tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveStartCallModalities(%v): error %q missing substring %q", tc.in, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStartCallModalities(%v): unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolveStartCallModalities(%v): got %v want %v", tc.in, got, tc.want)
			}
		})
	}
}

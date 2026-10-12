package main

import (
	"fmt"
	"strings"
	"time"
)

type filters struct {
	msg      string
	envelope string
	kind     string
	message  string
	accounts []string
	from     time.Time
	to       time.Time
	hasFrom  bool
	hasTo    bool
}

func (f filters) any() bool {
	return f.msg != "" || f.envelope != "" || f.kind != "" || f.message != "" ||
		len(f.accounts) > 0 || f.hasFrom || f.hasTo
}

func (f filters) match(e entry) bool {
	if len(f.accounts) > 0 {
		ok := false
		for _, a := range f.accounts {
			if a == e.account {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.hasFrom && e.ts.Before(f.from) {
		return false
	}
	if f.hasTo && e.ts.After(f.to) {
		return false
	}
	if f.message != "" && !strings.Contains(e.msg, f.message) {
		return false
	}
	if f.msg != "" && !matchesAny(e, msgIDKeys, f.msg) {
		return false
	}
	if f.envelope != "" && !matchesAny(e, envelopeIDKeys, f.envelope) {
		return false
	}
	if f.kind != "" {
		ok := false
		for _, v := range values(e, kindKeys) {
			if v == f.kind {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
	"15:04:05",
	"15:04",
}

func parseWhen(s string, day time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		t, err := time.ParseInLocation(layout, s, day.Location())
		if err != nil {
			continue
		}
		if t.Year() == 0 {
			t = time.Date(day.Year(), day.Month(), day.Day(),
				t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), day.Location())
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time — write it as 2026-08-15T10:12:00 or as 10:12:00", s)
}

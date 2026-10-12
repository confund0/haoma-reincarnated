package main

import (
	"fmt"
	"strings"
	"time"
)

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

const dateOnly = "2006-01-02"

type window struct {
	from time.Time
	to   time.Time
}

func (w window) unset() bool { return w.from.IsZero() && w.to.IsZero() }

func (w window) holds(unixSeconds int64) bool {
	if w.unset() {
		return true
	}
	t := time.Unix(unixSeconds, 0)
	if !w.from.IsZero() && t.Before(w.from) {
		return false
	}
	if !w.to.IsZero() && t.After(w.to) {
		return false
	}
	return true
}

func (w window) describe() string {
	switch {
	case w.unset():
		return "any time"
	case w.from.IsZero():
		return "up to " + w.to.Format("2006-01-02 15:04:05")
	case w.to.IsZero():
		return "from " + w.from.Format("2006-01-02 15:04:05")
	default:
		return w.from.Format("2006-01-02 15:04:05") + " to " + w.to.Format("2006-01-02 15:04:05")
	}
}

func parseWhen(s string, day time.Time) (t time.Time, dateWide bool, err error) {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		parsed, perr := time.ParseInLocation(layout, s, day.Location())
		if perr != nil {
			continue
		}
		if parsed.Year() == 0 {
			parsed = time.Date(day.Year(), day.Month(), day.Day(),
				parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), day.Location())
		}
		return parsed, layout == dateOnly, nil
	}
	return time.Time{}, false, fmt.Errorf(
		"cannot read %q as a time — write it as 2026-08-15T10:12:00, or as 10:12, or as 2026-08-15", s)
}

func buildWindow(at, within, from, to string, now time.Time) (window, error) {
	if at != "" && (from != "" || to != "") {
		return window{}, fmt.Errorf("give either -at, or -from and -to, not both")
	}

	if at != "" {
		point, dateWide, err := parseWhen(at, now)
		if err != nil {
			return window{}, err
		}
		if within == "" {
			if dateWide {
				return window{from: point, to: point.AddDate(0, 0, 1).Add(-time.Second)}, nil
			}
			within = defaultWithin
		}
		span, err := time.ParseDuration(within)
		if err != nil {
			return window{}, fmt.Errorf("cannot read %q as a length of time — write it as 30m or 2h: %w", within, err)
		}
		if span <= 0 {
			return window{}, fmt.Errorf("-within must be longer than nothing, got %q", within)
		}
		return window{from: point.Add(-span), to: point.Add(span)}, nil
	}

	if within != "" {
		return window{}, fmt.Errorf("-within only means something with -at")
	}

	var w window
	if from != "" {
		t, _, err := parseWhen(from, now)
		if err != nil {
			return window{}, err
		}
		w.from = t
	}
	if to != "" {
		t, dateWide, err := parseWhen(to, now)
		if err != nil {
			return window{}, err
		}
		if dateWide {
			t = t.AddDate(0, 0, 1).Add(-time.Second)
		}
		w.to = t
	}
	if !w.from.IsZero() && !w.to.IsZero() && w.to.Before(w.from) {
		return window{}, fmt.Errorf("-to is before -from")
	}
	return w, nil
}

const defaultWithin = "30m"

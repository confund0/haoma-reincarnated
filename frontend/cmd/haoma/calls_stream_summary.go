package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
)

func decodeRawStreamFields(raw []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("calls: raw stream line: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("calls: raw stream line is not an object")
	}

	var attrs []any
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("calls: raw stream line: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("calls: raw stream line: non-string key")
		}

		valTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("calls: raw stream line: %w", err)
		}
		if key == "type" {
			continue
		}
		switch v := valTok.(type) {
		case json.Delim:
			return nil, fmt.Errorf("calls: raw stream line: nested value for %q", key)
		case json.Number:
			if i, err := v.Int64(); err == nil {
				attrs = append(attrs, slog.Int64(key, i))
				continue
			}
			f, err := v.Float64()
			if err != nil {
				return nil, fmt.Errorf("calls: raw stream line: number %q: %w", key, err)
			}
			attrs = append(attrs, slog.Float64(key, f))
		case bool:
			attrs = append(attrs, slog.Bool(key, v))
		case string:
			attrs = append(attrs, slog.String(key, v))
		case nil:
			attrs = append(attrs, slog.Any(key, nil))
		default:
			return nil, fmt.Errorf("calls: raw stream line: unsupported value for %q", key)
		}
	}

	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("calls: raw stream line: %w", err)
	}
	return attrs, nil
}

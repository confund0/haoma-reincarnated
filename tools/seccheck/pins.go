package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Pin is one pinned upstream library: the name it is keyed by in the
// versions JSON, the git URL to ask, and the tag it is pinned to.
type Pin struct {
	File string // relative path of the versions JSON it came from, for error text
	Name string
	URL  string
	Tag  string
}

// loadPins reads one tor-versions.json or streams-versions.json file
// and returns its library pins. It skips the "_comment" key, the
// "timestamp" key inside each version block, and any entry without
// both "url" and "commit" (the "ndk" entry has "url" but "revision",
// not "commit").
func loadPins(path string) ([]Pin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var pins []Pin
	for key, raw := range top {
		if key == "_comment" {
			continue
		}
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, fmt.Errorf("%s: version block %q: %w", path, key, err)
		}
		for name, entryRaw := range block {
			if name == "timestamp" {
				continue
			}
			var fields map[string]string
			if err := json.Unmarshal(entryRaw, &fields); err != nil {
				return nil, fmt.Errorf("%s: entry %q: %w", path, name, err)
			}
			url, hasURL := fields["url"]
			commit, hasCommit := fields["commit"]
			if !hasURL || !hasCommit {
				continue
			}
			pins = append(pins, Pin{File: path, Name: name, URL: url, Tag: commit})
		}
	}
	return pins, nil
}

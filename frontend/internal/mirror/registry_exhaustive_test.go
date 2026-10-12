package mirror

import (
	"testing"

	"haoma-frontend/internal/msg"
)

func TestRegistryExhaustive(t *testing.T) {
	for _, k := range msg.AllKinds {
		c, ok := Classify(k)
		if !ok {
			t.Errorf("wire kind %q has no mirror classification — add it to the registry in registry.go", k)
			continue
		}
		if c == ClassInvalid {
			t.Errorf("wire kind %q is classified ClassInvalid (zero value) — set ClassMirror or ClassNoMirror", k)
		}
	}

	known := make(map[msg.Kind]bool, len(msg.AllKinds))
	for _, k := range msg.AllKinds {
		known[k] = true
	}
	for k := range registry {
		if !known[k] {
			t.Errorf("registry classifies unknown wire kind %q — remove it or add it to msg.AllKinds", k)
		}
	}
}

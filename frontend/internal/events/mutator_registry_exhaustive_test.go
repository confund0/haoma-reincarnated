package events

import "testing"

func TestMutatorRegistryExhaustive(t *testing.T) {
	for _, m := range AllMutators {
		c, ok := Classify(m)
		if !ok {
			t.Errorf("mutator %q has no journal classification — add it to mutatorRegistry in mutator_registry.go", m)
			continue
		}
		if c == ClassMutatorInvalid {
			t.Errorf("mutator %q is classified ClassMutatorInvalid (zero value) — set ClassJournal or ClassExempt", m)
		}
	}

	known := make(map[Mutator]bool, len(AllMutators))
	for _, m := range AllMutators {
		known[m] = true
	}
	for m := range mutatorRegistry {
		if !known[m] {
			t.Errorf("registry classifies unknown mutator %q — remove it or add it to AllMutators", m)
		}
	}
}

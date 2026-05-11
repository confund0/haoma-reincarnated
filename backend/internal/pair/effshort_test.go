package pair

import (
	"strings"
	"testing"
)

func TestEFFShort_BasicProperties(t *testing.T) {
	if got := len(EFFShort); got != EFFShortCount {
		t.Fatalf("len(EFFShort) = %d, want %d", got, EFFShortCount)
	}
	for i, w := range EFFShort {
		if w == "" {
			t.Errorf("entry %d empty", i)
		}
		if w != strings.ToLower(w) {
			t.Errorf("entry %d %q not lowercased", i, w)
		}
		if len(w) > 6 {
			t.Errorf("entry %d %q exceeds 6 chars", i, w)
		}
	}
}

func TestIsEFFShort(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"acid", true},
		{"ACID", true},
		{"  acid  ", true},
		{"zoom", true},
		{"abandon", false},
		{"definitelynotonthelist", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsEFFShort(c.in); got != c.want {
			t.Errorf("IsEFFShort(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidateEFFShortPhrase(t *testing.T) {
	t.Run("clean accepts", func(t *testing.T) {
		got, err := ValidateEFFShortPhrase([]string{"acid", "acorn", "acre"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(got) != 3 || got[0] != "acid" || got[1] != "acorn" || got[2] != "acre" {
			t.Fatalf("unexpected output: %v", got)
		}
	})
	t.Run("normalises case + whitespace", func(t *testing.T) {
		got, err := ValidateEFFShortPhrase([]string{"  ACID ", "Acorn"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got[0] != "acid" || got[1] != "acorn" {
			t.Fatalf("normalisation failed: %v", got)
		}
	})
	t.Run("rejects unknown word", func(t *testing.T) {
		_, err := ValidateEFFShortPhrase([]string{"acid", "definitelynotaword"})
		if err == nil {
			t.Fatal("expected err for non-member word")
		}
		if _, ok := err.(ErrInvalidEFFShortWord); !ok {
			t.Errorf("expected ErrInvalidEFFShortWord, got %T", err)
		}
	})
	t.Run("rejects empty input", func(t *testing.T) {
		if _, err := ValidateEFFShortPhrase(nil); err == nil {
			t.Error("expected err on nil")
		}
		if _, err := ValidateEFFShortPhrase([]string{"  ", " "}); err == nil {
			t.Error("expected err on all-blank")
		}
	})
}

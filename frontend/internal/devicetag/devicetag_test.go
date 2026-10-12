package devicetag

import (
	"strconv"
	"strings"
	"testing"
)

func TestTag(t *testing.T) {
	const id = 3735928559
	a, b := Tag(id), Tag(id)
	if a != b || len(a) != 8 {
		t.Fatalf("Tag(%d) = %q then %q, want one 8-character value", id, a, b)
	}
	if strings.Contains(a, strconv.FormatUint(id, 10)) || strings.Contains(a, strconv.FormatUint(id, 16)) {
		t.Fatalf("Tag(%d) = %q carries the id", id, a)
	}
	if Tag(id+1) == a {
		t.Fatal("two devices got one tag")
	}
	if Tag(0) != "" {
		t.Fatal("no device got a tag")
	}
}

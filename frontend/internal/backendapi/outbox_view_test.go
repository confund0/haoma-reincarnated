package backendapi

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestOutboxRow_IsHaomadsStateOnlyView(t *testing.T) {
	var mine []string
	rt := reflect.TypeOf(OutboxRow{})
	for i := 0; i < rt.NumField(); i++ {
		tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		mine = append(mine, tag)
	}
	for _, banned := range []string{"dest", "envelope", "from"} {
		for _, k := range mine {
			if k == banned {
				t.Errorf("OutboxRow carries %q; an onion address must not reach haoma", banned)
			}
		}
	}

	src, err := os.ReadFile("../../../backend/cmd/haomad/api.go")
	if err != nil {
		t.Fatalf("cannot read haomad's API: %v", err)
	}
	block := regexp.MustCompile(`(?s)type outboxStateView struct \{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("backend/cmd/haomad/api.go no longer declares outboxStateView")
	}
	var theirs []string
	for _, m := range regexp.MustCompile("json:\"([a-z_]+)").FindAllSubmatch(block[1], -1) {
		theirs = append(theirs, string(m[1]))
	}
	sort.Strings(mine)
	sort.Strings(theirs)
	if !reflect.DeepEqual(mine, theirs) {
		t.Errorf("OutboxRow fields %v, haomad's view answers %v", mine, theirs)
	}
}

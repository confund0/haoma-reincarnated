package ipc

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

const (
	haomadDialSource = "../../../backend/internal/pair/onion_driver.go"
	phoneDialSource  = "../../../mobile/Android/app/src/main/java/io/haoma/calculator/messenger/JoinWait.kt"
)

func TestJoinerDialTimeoutMatchesEveryCopy(t *testing.T) {
	haomad := readSeconds(t, haomadDialSource,
		regexp.MustCompile(`const joinerDialTimeout = (\d+) \* time\.Second`))
	if got := time.Duration(haomad) * time.Second; got != JoinerDialTimeout {
		t.Errorf("haomad dials for %v and this package says %v; change both in one edit", got, JoinerDialTimeout)
	}

	phone := readSeconds(t, phoneDialSource,
		regexp.MustCompile(`const val JOINER_DIAL_SECONDS: Long = (\d+)L`))
	if got := time.Duration(phone) * time.Second; got != JoinerDialTimeout {
		t.Errorf("the phone counts down from %v and this package says %v; change both in one edit", got, JoinerDialTimeout)
	}
}

func readSeconds(t *testing.T, path string, re *regexp.Regexp) int {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	all := re.FindAllSubmatch(src, -1)
	if len(all) != 1 {
		t.Fatalf("%s: want exactly one line matching %q, found %d", path, re, len(all))
	}
	n, err := strconv.Atoi(string(all[0][1]))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return n
}

func TestJoinDialFailed(t *testing.T) {
	for _, code := range []string{ReasonOnionAcceptFailed, ReasonBackendDown} {
		if !JoinDialFailed(code) {
			t.Errorf("%q is a dial that did not get through, and was not read as one", code)
		}
	}

	for _, code := range []string{"", ReasonBadRequest, ReasonNotReady, "device_not_empty", "already_linked", "bad_invite", "import_failed", ReasonNoAnswer} {
		if JoinDialFailed(code) {
			t.Errorf("%q was read as a slow dial", code)
		}
	}
}

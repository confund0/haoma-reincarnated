package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func TestDeviceLinkWaitsMatchThePhone(t *testing.T) {
	const phone = "../../../mobile/Android/app/src/main/java/io/haoma/calculator/messenger/JoinWait.kt"
	src, err := os.ReadFile(phone)
	if err != nil {
		t.Fatalf("read %s: %v", phone, err)
	}
	for _, c := range []struct {
		name string
		want time.Duration
	}{
		{"DEVICE_LINK_REGISTER_SECONDS", deviceLinkRegisterTimeout},
		{"DEVICE_LINK_BUNDLE_SECONDS", deviceLinkBundleTimeout},
	} {
		all := regexp.MustCompile(`const val `+c.name+`: Long = (\d+)L`).FindAllSubmatch(src, -1)
		if len(all) != 1 {
			t.Fatalf("%s: want exactly one %s line, found %d", phone, c.name, len(all))
		}
		n, err := strconv.Atoi(string(all[0][1]))
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Duration(n) * time.Second; got != c.want {
			t.Errorf("the phone holds %v as %s and this daemon waits %v; change both in one edit", got, c.name, c.want)
		}
	}
}

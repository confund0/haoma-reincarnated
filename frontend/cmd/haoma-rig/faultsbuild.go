package main

import (
	"debug/buildinfo"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
)

const faultsBuildTag = "faults"

func checkFaultsBuilt(bin string) error {
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		return fmt.Errorf("read the build info of %s: %w", bin, err)
	}
	if !hasFaultsTag(info.Settings) {
		return fmt.Errorf("%s was built without -tags %s, so it reads no seeded fault; build it again (run with -build)", bin, faultsBuildTag)
	}
	return nil
}

func hasFaultsTag(settings []debug.BuildSetting) bool {
	for _, s := range settings {
		if s.Key == "-tags" && slices.Contains(strings.Split(s.Value, ","), faultsBuildTag) {
			return true
		}
	}
	return false
}

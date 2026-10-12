package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const chutneyTorrcMarker = "# written by haoma-rig -chutney"

const testingNetworkLogLine = "TestingTorNetwork is set"

func chutneyTorrc(netDir string) (string, error) {
	entries, err := os.ReadDir(netDir)
	if err != nil {
		return "", fmt.Errorf("-chutney: %w", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var want []string
	wantFrom := ""
	for _, name := range names {
		path := filepath.Join(netDir, name, "torrc")
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		lines, client, err := chutneyClientLines(path)
		if err != nil {
			return "", fmt.Errorf("-chutney: %w", err)
		}
		if !client {
			continue
		}
		if wantFrom == "" {
			want, wantFrom = lines, path
			continue
		}
		if !slices.Equal(lines, want) {
			return "", fmt.Errorf("-chutney: %s and %s give different network lines; configure the network again", wantFrom, path)
		}
	}
	if wantFrom == "" {
		return "", fmt.Errorf("-chutney: no client or onion service torrc under %s (want the Chutney nodes dir, e.g. tmp/chutney-net/nodes)", netDir)
	}
	auths, testing := 0, false
	for _, l := range want {
		key := strings.ToLower(torrcKey(l))
		switch {
		case key == "dirauthority":
			auths++
		case key == "testingtornetwork" && strings.TrimSpace(l[len(key):]) == "1":
			testing = true
		}
	}
	if auths == 0 {
		return "", fmt.Errorf("-chutney: %s has no DirAuthority line", wantFrom)
	}
	if !testing {
		return "", fmt.Errorf("-chutney: %s does not set TestingTorNetwork 1", wantFrom)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s from %s; do not edit\n", chutneyTorrcMarker, wantFrom)
	for _, l := range want {
		b.WriteString(l + "\n")
	}

	b.WriteString("UseDefaultFallbackDirs 0\n")
	return b.String(), nil
}

func chutneyClientLines(path string) (lines []string, client bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	client = true
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		key := strings.ToLower(torrcKey(l))
		switch {
		case key == "orport" || key == "authoritativedirectory":
			client = false
		case key == "dirauthority" || strings.HasPrefix(key, "testing"):
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return lines, client, nil
}

func torrcKey(line string) string {
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		return line[:i]
	}
	return line
}

func syncChutneyTorrc(torDir, body string) error {
	path := filepath.Join(torDir, "torrc.local")
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		old = nil
	case err != nil:
		return err
	}
	ours := strings.HasPrefix(string(old), chutneyTorrcMarker)
	if body == "" {
		if ours {
			return fmt.Errorf("%s was written by -chutney; pass -chutney, or use a -tor-dirs without it", path)
		}
		return nil
	}
	if old != nil && !ours {
		return fmt.Errorf("%s is not one -chutney wrote; move it away, or use another -tor-dirs", path)
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

func checkTorNetwork(torLog string, from int64, chutney bool) error {
	line := lastMatchingLine(torLog, from, func(l string) bool {
		return strings.Contains(l, testingNetworkLogLine)
	})
	switch {
	case chutney && line == "":
		return fmt.Errorf("-chutney, but %s has no %q line since this launch: this tor is not on the private network", torLog, testingNetworkLogLine)
	case !chutney && line != "":
		return fmt.Errorf("%s says %q since this launch, but the run has no -chutney: this tor is not on the public network", torLog, testingNetworkLogLine)
	}
	return nil
}

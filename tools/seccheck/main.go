// seccheck answers one question: is anything this project pins to —
// a cross-compiled C library, or the Go toolchain itself — older than
// the latest stable upstream release. It prints one line per check and
// exits non-zero when something is out of date or could not be read,
// so a daily CI run can mail a finding without opening a PR.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	checks := run(*root, gitLsRemoteTags, fetchGoReleases)

	var out bytes.Buffer
	for _, c := range checks {
		fmt.Fprintln(&out, c.Line())
	}
	os.Stdout.Write(out.Bytes())

	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		appendSummary(summaryPath, out.String())
	}

	os.Exit(exitCode(checks))
}

// appendSummary writes the report into GitHub's step summary file, as
// a markdown code block. A failure to write it is not fatal: the
// report already reached stdout.
func appendSummary(path, report string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seccheck: cannot write %s: %v\n", path, err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n## seccheck\n\n```\n%s```\n", report)
}

// gitLsRemoteTags lists the tags of a remote by running
// "git ls-remote --tags", stripping "refs/tags/" and the "^{}" suffix
// an annotated tag's dereferenced line carries. Both forms of one tag
// collapse to the same name.
func gitLsRemoteTags(url string) ([]string, error) {
	cmd := exec.Command("git", "ls-remote", "--tags", url)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote --tags %s: %w", url, err)
	}
	seen := map[string]bool{}
	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		ref = strings.TrimPrefix(ref, "refs/tags/")
		ref = strings.TrimSuffix(ref, "^{}")
		if !seen[ref] {
			seen[ref] = true
			tags = append(tags, ref)
		}
	}
	return tags, nil
}

const goReleaseListURL = "https://go.dev/dl/?mode=json"

// fetchGoReleases fetches the supported Go release list.
func fetchGoReleases() ([]goRelease, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(goReleaseListURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", goReleaseListURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %s", goReleaseListURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", goReleaseListURL, err)
	}
	var releases []goRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("GET %s: %w", goReleaseListURL, err)
	}
	return releases, nil
}

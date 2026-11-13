// Version parsing and comparison for upstream release tags.
package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is a dotted release number, most significant component
// first. Different libraries use two to four components (openssl is
// major.minor.patch, tor is major.minor.micro.patch), so comparisons
// pad the shorter one with zeros rather than assume a fixed length.
type Version []int

func (v Version) String() string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// compareVersions returns -1, 0 or 1 the way strings.Compare does,
// treating a missing trailing component as 0.
func compareVersions(a, b Version) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// sameSeries reports whether two versions share their first two
// components (the major.minor series openssl and zlib are pinned to).
func sameSeries(a, b Version) bool {
	at := func(v Version, i int) int {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	return at(a, 0) == at(b, 0) && at(a, 1) == at(b, 1)
}

// tagRe matches the stable upstream tag shapes this check knows:
//
//	tor-0.4.9.16          tor-0.5.0.1-alpha
//	openssl-3.5.9         openssl-3.6.0-alpha1
//	release-2.1.12-stable release-2.2.1-alpha
//	v1.3.2                v1.14.1
//	1.0.20-RELEASE
//
// A tag of a shape it does not know (the old "OpenSSL_1_1_1w" form,
// for example) does not match at all and is ignored, not an error.
var tagRe = regexp.MustCompile(`^(?:tor-|openssl-|release-|v)?(\d+(?:\.\d+){1,3})(?:-(.+))?$`)

// stableSuffixes are the suffix words that still mark a tag as a
// stable release. Any other suffix (alpha, beta, rc, pre, dev, or an
// alpha/beta build number such as "alpha1") marks it as not stable.
var stableSuffixes = map[string]bool{
	"stable":  true,
	"release": true,
}

// parseTag parses one upstream tag into a version and whether it is a
// stable release. ok is false when the tag does not match a known
// shape at all; the caller ignores such tags rather than treating
// them as an error, except for the pinned tag itself, which is a
// source error if it does not parse.
func parseTag(tag string) (v Version, stable bool, ok bool) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return nil, false, false
	}
	parts := strings.Split(m[1], ".")
	v = make(Version, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false, false
		}
		v[i] = n
	}
	suffix := m[2]
	if suffix == "" {
		return v, true, true
	}
	return v, stableSuffixes[strings.ToLower(suffix)], true
}

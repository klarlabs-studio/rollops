package imageupdate

import (
	"regexp"
	"strconv"
	"strings"
)

// semver is a parsed major.minor.patch (a leading "v" and any pre-release/build
// suffix are tolerated; pre-release tags are skipped by the selector).
type semver struct {
	major, minor, patch int
	pre                 bool
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(.*)$`)

func parseSemver(tag string) (semver, bool) {
	m := semverRe.FindStringSubmatch(tag)
	if m == nil {
		return semver{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	suffix := m[4]
	pre := strings.HasPrefix(suffix, "-")
	return semver{major: maj, minor: min, patch: pat, pre: pre}, true
}

func (a semver) greater(b semver) bool {
	switch {
	case a.major != b.major:
		return a.major > b.major
	case a.minor != b.minor:
		return a.minor > b.minor
	default:
		return a.patch > b.patch
	}
}

// SelectTag picks the best update tag from available, given the current tag and
// an update mode, or returns ok=false when nothing newer qualifies. Modes
// mirror keel's policies:
//
//	major / any — highest semver overall
//	minor       — highest semver with the same major
//	patch       — highest semver with the same major.minor
//
// An optional pattern (regexp) further filters candidate tags. Pre-release tags
// and the current tag are never selected; only a strictly greater version wins.
func SelectTag(current string, available []string, mode, pattern string) (string, bool) {
	cur, ok := parseSemver(current)
	if !ok {
		return "", false // current tag isn't semver — nothing to compare against
	}
	var re *regexp.Regexp
	if pattern != "" {
		var err error
		if re, err = regexp.Compile(pattern); err != nil {
			return "", false
		}
	}
	best := cur
	bestTag := ""
	for _, t := range available {
		if re != nil && !re.MatchString(t) {
			continue
		}
		v, ok := parseSemver(t)
		if !ok || v.pre {
			continue
		}
		switch mode {
		case "minor":
			if v.major != cur.major {
				continue
			}
		case "patch":
			if v.major != cur.major || v.minor != cur.minor {
				continue
			}
		}
		if v.greater(best) {
			best, bestTag = v, t
		}
	}
	return bestTag, bestTag != ""
}

package imageupdate

import (
	"regexp"
	"sort"
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
	// Atoi overflow clamps to MaxInt64 (with an error), so a bogus huge tag like
	// "99999999999999999999.0.0" would otherwise silently beat every real version
	// in major/any mode. Reject the tag as non-semver instead of comparing a
	// clamped number.
	maj, err1 := strconv.Atoi(m[1])
	min, err2 := strconv.Atoi(m[2])
	pat, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return semver{}, false
	}
	suffix := m[4]
	pre := strings.HasPrefix(suffix, "-")
	return semver{major: maj, minor: min, patch: pat, pre: pre}, true
}

// compileTagPattern compiles an operator-supplied tag pattern, anchoring it so
// an otherwise-unanchored pattern matches the whole tag rather than a substring.
// regexp.MatchString is a substring match, so an operator writing `2\.0`
// (intending the 2.0 line) would also match "12.0.5" or "2.0-evil" — far more
// than intended. A pattern that already carries an anchor (^ at the start or $
// at the end) is respected verbatim, so a deliberate prefix (`^v1\.`) or suffix
// (`-stable$`) match still works; only a pattern with neither anchor is wrapped
// as ^(?:…)$.
func compileTagPattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	expr := pattern
	if !strings.HasPrefix(pattern, "^") && !strings.HasSuffix(pattern, "$") {
		expr = "^(?:" + pattern + ")$"
	}
	return regexp.Compile(expr)
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
	re, err := compileTagPattern(pattern)
	if err != nil {
		return "", false
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

// IsSemver reports whether tag parses as a non-pre-release semver version.
func IsSemver(tag string) bool {
	v, ok := parseSemver(tag)
	return ok && !v.pre
}

// SemverTagsDesc returns the non-pre-release semver tags from available
// (optionally filtered by pattern), sorted highest version first. Used by
// digest→semver migration to find the version a pinned digest corresponds to,
// checking the most likely (newest) tags first so the highest matching tag wins.
func SemverTagsDesc(available []string, pattern string) []string {
	re, err := compileTagPattern(pattern)
	if err != nil {
		return nil
	}
	type tagged struct {
		tag string
		v   semver
	}
	var out []tagged
	for _, t := range available {
		if re != nil && !re.MatchString(t) {
			continue
		}
		v, ok := parseSemver(t)
		if !ok || v.pre {
			continue
		}
		out = append(out, tagged{t, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].v.greater(out[j].v) })
	tags := make([]string, len(out))
	for i, x := range out {
		tags[i] = x.tag
	}
	return tags
}

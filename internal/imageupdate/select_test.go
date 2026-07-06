package imageupdate

import "testing"

func TestParseSemver_OverflowRejected(t *testing.T) {
	// SECURITY (finding #5): a bogus huge tag overflows Atoi (clamped to MaxInt64)
	// and would otherwise beat every real version in major/any mode. It must be
	// rejected as non-semver instead.
	if _, ok := parseSemver("99999999999999999999.0.0"); ok {
		t.Fatal("overflowing major must not parse as semver")
	}
	if _, ok := parseSemver("1.99999999999999999999.0"); ok {
		t.Fatal("overflowing minor must not parse as semver")
	}
	if _, ok := parseSemver("1.0.99999999999999999999"); ok {
		t.Fatal("overflowing patch must not parse as semver")
	}
	// A sane version still parses.
	if v, ok := parseSemver("v1.2.3"); !ok || v.major != 1 || v.minor != 2 || v.patch != 3 {
		t.Fatalf("parseSemver(v1.2.3) = %+v ok=%v", v, ok)
	}
}

func TestSelectTag_OverflowTagCannotWin(t *testing.T) {
	// The overflowing tag must never be selected over a real version.
	got, ok := SelectTag("v1.0.0", []string{"v1.1.0", "99999999999999999999.0.0"}, "major", "")
	if !ok || got != "v1.1.0" {
		t.Fatalf("got %q ok=%v, want v1.1.0 (overflow tag ignored)", got, ok)
	}
}

func TestSelectTag_PatternAnchoring(t *testing.T) {
	// SECURITY (finding #4): an unanchored operator pattern must match the WHOLE
	// tag, not a substring. `2\.0` must not admit "12.0.5" or "2.0-evil".
	tags := []string{"v1.0.0", "12.0.5", "2.0.0", "2.0-evil"}

	// Unanchored `2\.0` is wrapped to ^(?:2\.0)$ → matches nothing here (none of
	// the tags equal exactly "2.0"), so no bump — certainly not "12.0.5".
	if got, ok := SelectTag("1.0.0", tags, "major", `2\.0`); ok {
		t.Errorf("unanchored 2\\.0 must not substring-match, got %q", got)
	}

	// A properly written exact pattern still selects.
	if got, ok := SelectTag("1.0.0", []string{"2.0.0", "12.0.5"}, "major", `2\.0\.0`); !ok || got != "2.0.0" {
		t.Errorf("got %q ok=%v, want 2.0.0", got, ok)
	}

	// A deliberately start-anchored prefix pattern keeps prefix-match semantics.
	if got, ok := SelectTag("v1.0.0", []string{"v1.5.0", "v12.0.0"}, "major", `^v1\.`); !ok || got != "v1.5.0" {
		t.Errorf("prefix ^v1\\. got %q ok=%v, want v1.5.0", got, ok)
	}
}

func TestCompileTagPattern(t *testing.T) {
	cases := []struct {
		pattern, tag string
		match        bool
	}{
		{"", "anything", true}, // nil regexp → callers skip filtering
		{`2\.0`, "2.0", true},
		{`2\.0`, "12.0.5", false}, // wrapped → whole-string
		{`2\.0`, "2.0-evil", false},
		{`^v1\.`, "v1.2.3", true},   // start-anchored prefix respected
		{`^v1\.`, "xv1.2.3", false}, // start anchor still binds
		{`-stable$`, "v1.2.3-stable", true},
		{`-stable$`, "v1.2.3-stable-x", false},
		{`^v\d+\.\d+\.\d+$`, "v1.2.3", true}, // fully anchored verbatim
	}
	for _, c := range cases {
		re, err := compileTagPattern(c.pattern)
		if err != nil {
			t.Fatalf("compileTagPattern(%q): %v", c.pattern, err)
		}
		if c.pattern == "" {
			if re != nil {
				t.Errorf("empty pattern should yield nil regexp")
			}
			continue
		}
		if got := re.MatchString(c.tag); got != c.match {
			t.Errorf("compileTagPattern(%q).MatchString(%q) = %v, want %v", c.pattern, c.tag, got, c.match)
		}
	}
}

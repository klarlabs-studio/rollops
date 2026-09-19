package digest

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAcceptsCanonicalForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		alg  Algorithm
	}{
		{"sha256", "sha256:" + strings.Repeat("a", 64), SHA256},
		{"sha512", "sha512:" + strings.Repeat("b", 128), SHA512},
		{"mixed hex", "sha256:" + strings.Repeat("0f", 32), SHA256},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.in, err)
			}
			if got.Algorithm() != c.alg {
				t.Errorf("algorithm = %q, want %q", got.Algorithm(), c.alg)
			}
			if got.String() != c.in {
				t.Errorf("String() = %q, want %q", got.String(), c.in)
			}
		})
	}
}

// A digest is content identity (INV-002). Anything that is not exactly one
// value must fail here rather than become a second name for the same artifact
// — or, worse, a name for a different one.
func TestParseRejectsAnythingNonCanonical(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no algorithm", strings.Repeat("a", 64)},
		{"unknown algorithm", "md5:" + strings.Repeat("a", 32)},
		{"algorithm only", "sha256:"},
		{"hex only", "sha256"},
		{"uppercase hex", "sha256:" + strings.Repeat("A", 64)},
		{"short", "sha256:" + strings.Repeat("a", 63)},
		{"long", "sha256:" + strings.Repeat("a", 65)},
		{"non-hex", "sha256:" + strings.Repeat("g", 64)},
		{"sha512 length under sha256 algorithm", "sha256:" + strings.Repeat("a", 128)},
		{"two separators", "sha256:sha256:" + strings.Repeat("a", 64)},
		{"leading space", " sha256:" + strings.Repeat("a", 64)},
		{"mutable tag", "latest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(c.in); err == nil {
				t.Errorf("Parse(%q) accepted a non-canonical digest", c.in)
			}
		})
	}
}

func TestParseReportsUnknownAlgorithmDistinctly(t *testing.T) {
	_, err := Parse("md5:" + strings.Repeat("a", 32))
	if !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Errorf("got %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestZeroDigestIsNotUsable(t *testing.T) {
	var d Digest
	if !d.IsZero() {
		t.Error("the zero Digest did not report itself as zero")
	}
	if d.String() != "" {
		t.Errorf("zero String() = %q, want empty", d.String())
	}
	if err := d.Validate(); err == nil {
		t.Error("the zero Digest validated; it names no content")
	}
}

func TestEqualityIsByValue(t *testing.T) {
	a, err := Parse("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse("sha256:" + strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("two parses of the same text produced unequal digests")
	}
	if a == c {
		t.Error("different content compared equal")
	}
}

func TestOfHashesContent(t *testing.T) {
	d := Of([]byte("hello"))
	const want = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if d.String() != want {
		t.Errorf("Of(\"hello\") = %q, want %q", d.String(), want)
	}
	if !d.Matches([]byte("hello")) {
		t.Error("digest did not match the content it was computed from")
	}
	if d.Matches([]byte("hello ")) {
		t.Error("digest matched different content")
	}
}

func TestMatchesHonoursTheStatedAlgorithm(t *testing.T) {
	// sha512("hello")
	const sum = "9b71d224bd62f3785d96d46ad3ea3d73319bfbc2890caadae2dff72519673ca7" +
		"2323c3d99ba5c11d7c7acc6e14b8c5da0c4663475c2e5c3adef46f73bcdec043"
	d, err := Parse("sha512:" + sum)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Matches([]byte("hello")) {
		t.Error("a sha512 digest did not match its content")
	}
	if d.Matches([]byte("goodbye")) {
		t.Error("a sha512 digest matched different content")
	}
}

// Verification is the point of a digest, so a zero one must never verify —
// otherwise an artifact that was never digested would pass every check.
func TestZeroDigestMatchesNothing(t *testing.T) {
	var d Digest
	if d.Matches(nil) {
		t.Error("the zero Digest matched empty content")
	}
	if d.Matches([]byte("anything")) {
		t.Error("the zero Digest matched content")
	}
}

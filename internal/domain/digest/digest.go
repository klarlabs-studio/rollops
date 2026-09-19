// Package digest models content identity: the cryptographic digest that says
// which bytes an artifact is, as opposed to the locator that says where a copy
// of them can be fetched.
//
// A Release resolves mutable tags to digests once and never consults the tag
// again (INV-002). Everything downstream compares digests.
package digest

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Algorithm names a supported hash function.
type Algorithm string

const (
	SHA256 Algorithm = "sha256"
	SHA512 Algorithm = "sha512"
)

// ErrUnsupportedAlgorithm reports a digest whose algorithm this build does not
// implement. It is distinct from a malformed digest because the remedy differs:
// one is a bad value, the other is a version skew.
var ErrUnsupportedAlgorithm = errors.New("digest: unsupported algorithm")

// hexLen is the number of hex characters a correct digest of each algorithm
// has. An algorithm missing from this map is unsupported.
var hexLen = map[Algorithm]int{
	SHA256: sha256.Size * 2,
	SHA512: sha512.Size * 2,
}

// Digest is an immutable, comparable content identifier. The zero value names
// no content and verifies nothing.
type Digest struct {
	algorithm Algorithm
	encoded   string
}

// Parse reads the canonical "algorithm:hex" form. It is strict on purpose: a
// digest that has two spellings is two names for one artifact, and code that
// compares them as text would decide they differ.
func Parse(s string) (Digest, error) {
	alg, encoded, ok := strings.Cut(s, ":")
	if !ok {
		return Digest{}, fmt.Errorf("digest: %q is not in algorithm:hex form", s)
	}
	d := Digest{algorithm: Algorithm(alg), encoded: encoded}
	if err := d.Validate(); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// Of computes the SHA-256 digest of content.
func Of(content []byte) Digest {
	sum := sha256.Sum256(content)
	return Digest{algorithm: SHA256, encoded: hex.EncodeToString(sum[:])}
}

// Validate reports whether the digest names content.
func (d Digest) Validate() error {
	want, ok := hexLen[d.algorithm]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, d.algorithm)
	}
	if len(d.encoded) != want {
		return fmt.Errorf("digest: %s wants %d hex characters, got %d",
			d.algorithm, want, len(d.encoded))
	}
	if strings.ToLower(d.encoded) != d.encoded {
		return fmt.Errorf("digest: %q is not lower-case hex", d.encoded)
	}
	if _, err := hex.DecodeString(d.encoded); err != nil {
		return fmt.Errorf("digest: %q is not hex: %w", d.encoded, err)
	}
	return nil
}

// Algorithm returns the hash function that produced the digest.
func (d Digest) Algorithm() Algorithm { return d.algorithm }

// IsZero reports whether the digest names no content.
func (d Digest) IsZero() bool { return d == Digest{} }

// String returns the canonical form, or "" for the zero digest.
func (d Digest) String() string {
	if d.IsZero() {
		return ""
	}
	return string(d.algorithm) + ":" + d.encoded
}

// Matches reports whether content hashes to this digest. The zero digest
// matches nothing, so an artifact that was never digested cannot pass
// verification by omission.
func (d Digest) Matches(content []byte) bool {
	if d.IsZero() {
		return false
	}
	var encoded string
	switch d.algorithm {
	case SHA256:
		sum := sha256.Sum256(content)
		encoded = hex.EncodeToString(sum[:])
	case SHA512:
		sum := sha512.Sum512(content)
		encoded = hex.EncodeToString(sum[:])
	default:
		return false
	}
	return encoded == d.encoded
}

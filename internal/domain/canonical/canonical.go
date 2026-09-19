// Package canonical encodes values into one unambiguous byte sequence so that
// content identity can be derived from them.
//
// Two things in the model are identified by their contents rather than by an
// assigned name: a release fingerprint answers "have we already built this",
// and a deployment plan hash answers "is this the plan that was approved". Both
// are security-relevant in the same way — if two different values can be made
// to encode alike, one can be substituted for the other after the fact — so the
// encoding lives here once, audited, rather than being written twice.
//
// The single rule is that every field carries its own length. Without one there
// is a boundary between fields, and a boundary is something a value can be
// crafted to cross.
package canonical

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/digest"
)

// Writer accumulates a canonical encoding. The zero value is ready to use.
type Writer struct {
	b strings.Builder
}

// Bytes returns the encoding written so far.
func (w *Writer) Bytes() []byte { return []byte(w.b.String()) }

// Sum returns the digest of the encoding written so far.
func (w *Writer) Sum() digest.Digest { return digest.Of(w.Bytes()) }

// String appends a string field.
func (w *Writer) String(s string) {
	w.b.WriteString(strconv.Itoa(len(s)))
	w.b.WriteByte(':')
	w.b.WriteString(s)
}

// tagged appends a field under a type tag, so that a value cannot impersonate
// a value of another type whose rendering happens to match.
func (w *Writer) tagged(tag byte, s string) {
	w.b.WriteByte(tag)
	w.String(s)
}

// Int appends an integer field.
func (w *Writer) Int(i int64) { w.tagged('i', strconv.FormatInt(i, 10)) }

// Uint appends an unsigned integer field.
func (w *Writer) Uint(u uint64) { w.tagged('u', strconv.FormatUint(u, 10)) }

// Bool appends a boolean field.
func (w *Writer) Bool(v bool) {
	if v {
		w.tagged('b', "1")
		return
	}
	w.tagged('b', "0")
}

// Float appends a floating-point field by its bit pattern rather than by any
// decimal rendering. A float has many spellings per value and a formatter
// chooses one; the bits do not, so a score that survived a round trip through
// storage hashes the same as the score that was computed.
func (w *Writer) Float(f float64) {
	w.tagged('f', strconv.FormatUint(math.Float64bits(f), 16))
}

// Time appends an instant. It is normalised to UTC nanoseconds so that the same
// moment encodes alike whatever location the value carried: a plan hash must
// not depend on the time zone of the process that computed it.
func (w *Writer) Time(t time.Time) { w.tagged('t', strconv.FormatInt(t.UTC().UnixNano(), 10)) }

// Strings appends a sequence, preserving order. A nil slice and an empty one
// encode alike: both say that nothing is present.
func (w *Writer) Strings(ss []string) {
	w.Int(int64(len(ss)))
	for _, s := range ss {
		w.String(s)
	}
}

// StringMap appends a map in key order. A map has no order of its own, so one
// is imposed here — without it the encoding would depend on Go's randomised
// iteration and the same value would hash differently on each run.
func (w *Writer) StringMap(m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	w.Int(int64(len(keys)))
	for _, k := range keys {
		w.String(k)
		w.String(m[k])
	}
}

// Nested appends a self-contained sub-encoding as a single field, so that an
// inner sequence cannot merge with the fields that follow it.
func (w *Writer) Nested(fn func(*Writer)) {
	var inner Writer
	fn(&inner)
	w.tagged('n', inner.b.String())
}

// Each appends a sequence of structured items, preserving order.
func Each[T any](w *Writer, items []T, fn func(*Writer, T)) {
	w.Int(int64(len(items)))
	for _, item := range items {
		w.Nested(func(inner *Writer) { fn(inner, item) })
	}
}

// Sorted appends a set of structured items in an order derived from the items
// themselves, for collections whose listing order is presentation rather than
// meaning.
func Sorted[T any, K cmp.Ordered](w *Writer, items []T, key func(T) K, fn func(*Writer, T)) {
	ordered := slices.Clone(items)
	slices.SortStableFunc(ordered, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	Each(w, ordered, fn)
}

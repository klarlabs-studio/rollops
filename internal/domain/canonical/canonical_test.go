package canonical_test

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/canonical"
)

// The encoding is a stored fact: a release fingerprint written by an older
// binary must still match one computed by a newer one. Asserting the exact
// bytes is what makes a change to the format a deliberate act rather than an
// accident with a green suite.
func TestTheEncodingIsLengthPrefixed(t *testing.T) {
	var w canonical.Writer
	w.String("abc")
	if got, want := string(w.Bytes()), "3:abc"; got != want {
		t.Errorf("encoding = %q, want %q", got, want)
	}
}

// Without a length prefix there is a boundary a value can be crafted to cross.
// This is the property the whole package exists for, so it is asserted rather
// than assumed.
func TestFieldsCannotBeMadeToSpanTheirBoundary(t *testing.T) {
	var ab canonical.Writer
	ab.String("a")
	ab.String("bc")

	var abc canonical.Writer
	abc.String("ab")
	abc.String("c")

	if string(ab.Bytes()) == string(abc.Bytes()) {
		t.Errorf("(%q,%q) and (%q,%q) encode alike as %q", "a", "bc", "ab", "c", ab.Bytes())
	}
}

func TestAnEmptyValueIsNotTheSameAsNoValue(t *testing.T) {
	var one canonical.Writer
	one.String("")

	var two canonical.Writer
	two.String("")
	two.String("")

	if string(one.Bytes()) == string(two.Bytes()) {
		t.Error("one empty field and two empty fields encode alike")
	}
}

// A value that looks like the encoding must not be able to impersonate it.
func TestAValueContainingTheSeparatorIsUnambiguous(t *testing.T) {
	var sneaky canonical.Writer
	sneaky.String("1:x")

	var honest canonical.Writer
	honest.String("1")
	honest.String("x")

	if string(sneaky.Bytes()) == string(honest.Bytes()) {
		t.Errorf("%q impersonated two fields: %q", "1:x", sneaky.Bytes())
	}
}

func TestEveryScalarIsDistinguishableFromItsRendering(t *testing.T) {
	// A number and the string of that number must not collide, or a field
	// whose type changed would keep its old hash.
	var num canonical.Writer
	num.Int(7)

	var str canonical.Writer
	str.String("7")

	if string(num.Bytes()) == string(str.Bytes()) {
		t.Errorf("the int 7 and the string %q encode alike as %q", "7", num.Bytes())
	}
}

func TestBoolsAreDistinguishable(t *testing.T) {
	var yes, no canonical.Writer
	yes.Bool(true)
	no.Bool(false)
	if string(yes.Bytes()) == string(no.Bytes()) {
		t.Error("true and false encode alike")
	}
}

// A time is encoded as an instant, not as a rendering: the same moment in two
// locations is the same moment, and a plan hash must not depend on the time
// zone of the process that computed it.
func TestTimesEncodeAsInstantsNotRenderings(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	var utc, local canonical.Writer
	utc.Time(at)
	local.Time(at.In(berlin))

	if string(utc.Bytes()) != string(local.Bytes()) {
		t.Errorf("the same instant encoded differently: %q vs %q", utc.Bytes(), local.Bytes())
	}
}

func TestDifferentInstantsEncodeDifferently(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	var a, b canonical.Writer
	a.Time(at)
	b.Time(at.Add(time.Nanosecond))

	if string(a.Bytes()) == string(b.Bytes()) {
		t.Error("two instants a nanosecond apart encode alike")
	}
}

// Floats are the one scalar with more than one spelling per value. Encoding
// them by their bits rather than by formatting keeps 0.1+0.2 from depending on
// how many digits the formatter chose to print.
func TestFloatsEncodeByValueNotByFormatting(t *testing.T) {
	var a, b canonical.Writer
	a.Float(0.5)
	b.Float(0.5)
	if string(a.Bytes()) != string(b.Bytes()) {
		t.Error("the same float encoded differently")
	}

	// The operands are variables so that Go evaluates the sum at runtime in
	// float64. As constants it would fold to exactly 0.3 and prove nothing.
	tenth, fifth := 0.1, 0.2
	sum := tenth + fifth

	if strconv.FormatFloat(sum, 'f', 1, 64) != strconv.FormatFloat(0.3, 'f', 1, 64) {
		t.Fatal("the premise is wrong: these two already format differently")
	}

	var c, d canonical.Writer
	c.Float(sum)
	d.Float(0.3)
	if string(c.Bytes()) == string(d.Bytes()) {
		t.Error("two distinct floats that share a rendering encoded alike")
	}
}

// Order is part of the encoding: a writer is a sequence, and reordering fields
// must produce a different result or a plan could be rearranged undetectably.
func TestOrderIsPartOfTheEncoding(t *testing.T) {
	var ab, ba canonical.Writer
	ab.String("a")
	ab.String("b")
	ba.String("b")
	ba.String("a")

	if string(ab.Bytes()) == string(ba.Bytes()) {
		t.Error("reordering two fields did not change the encoding")
	}
}

// Maps have no order of their own, so the writer must impose one. Without this
// a hash would depend on Go's randomised map iteration and the same plan would
// verify only sometimes.
func TestMapsEncodeIndependentlyOfIteration(t *testing.T) {
	m := map[string]string{"b": "2", "a": "1", "c": "3"}

	var first canonical.Writer
	first.StringMap(m)
	want := string(first.Bytes())

	for i := 0; i < 50; i++ {
		var w canonical.Writer
		w.StringMap(map[string]string{"c": "3", "a": "1", "b": "2"})
		if got := string(w.Bytes()); got != want {
			t.Fatalf("iteration %d encoded %q, want %q", i, got, want)
		}
	}
}

func TestAMapKeyCannotBeMovedIntoItsValue(t *testing.T) {
	var a, b canonical.Writer
	a.StringMap(map[string]string{"ab": "c"})
	b.StringMap(map[string]string{"a": "bc"})
	if string(a.Bytes()) == string(b.Bytes()) {
		t.Error("a map key crossed into its value")
	}
}

// An absent map and an empty one are the same statement: nothing is set. They
// must encode alike, or whether a store returned nil or an empty map would
// change a plan's hash.
func TestAnAbsentMapEncodesLikeAnEmptyOne(t *testing.T) {
	var nilMap, emptyMap canonical.Writer
	nilMap.StringMap(nil)
	emptyMap.StringMap(map[string]string{})
	if string(nilMap.Bytes()) != string(emptyMap.Bytes()) {
		t.Error("a nil map and an empty map encode differently")
	}
}

func TestAnAbsentSliceEncodesLikeAnEmptyOne(t *testing.T) {
	var nilSlice, emptySlice canonical.Writer
	nilSlice.Strings(nil)
	emptySlice.Strings([]string{})
	if string(nilSlice.Bytes()) != string(emptySlice.Bytes()) {
		t.Error("a nil slice and an empty slice encode differently")
	}
}

// A slice keeps its order, unlike a map: for operations the order is meaning.
func TestSlicesKeepTheirOrder(t *testing.T) {
	var ab, ba canonical.Writer
	ab.Strings([]string{"a", "b"})
	ba.Strings([]string{"b", "a"})
	if string(ab.Bytes()) == string(ba.Bytes()) {
		t.Error("reordering a slice did not change the encoding")
	}
}

// A slice of two items must not encode like one item containing both, which is
// what a length-prefixed count prevents.
func TestASliceIsBoundedByItsCount(t *testing.T) {
	var two, one canonical.Writer
	two.Strings([]string{"a", "b"})
	one.Strings([]string{"1:a1:b"})
	if string(two.Bytes()) == string(one.Bytes()) {
		t.Error("a single crafted element impersonated two elements")
	}
}

// Nesting must not let an inner structure merge with its neighbours.
func TestNestedWritersAreBounded(t *testing.T) {
	var outer canonical.Writer
	outer.Nested(func(w *canonical.Writer) {
		w.String("a")
		w.String("b")
	})
	outer.String("c")

	var flat canonical.Writer
	flat.String("a")
	flat.String("b")
	flat.String("c")

	if string(outer.Bytes()) == string(flat.Bytes()) {
		t.Error("a nested writer merged into the surrounding sequence")
	}
}

func TestUintsAreDistinguishableFromInts(t *testing.T) {
	var u, i canonical.Writer
	u.Uint(7)
	i.Int(7)
	if string(u.Bytes()) == string(i.Bytes()) {
		t.Error("a uint and an int of the same value encode alike")
	}

	// A revision is a uint64, and the top of that range must survive: an int64
	// round trip would wrap it to -1.
	var max, minusOne canonical.Writer
	max.Uint(math.MaxUint64)
	minusOne.Int(-1)
	if string(max.Bytes()) == string(minusOne.Bytes()) {
		t.Error("MaxUint64 encoded as -1, so the value wrapped")
	}
}

type op struct {
	name string
	size int64
}

func writeOp(w *canonical.Writer, o op) {
	w.String(o.name)
	w.Int(o.size)
}

func TestEachKeepsOrderAndBoundsItsItems(t *testing.T) {
	var ab, ba canonical.Writer
	canonical.Each(&ab, []op{{"a", 1}, {"b", 2}}, writeOp)
	canonical.Each(&ba, []op{{"b", 2}, {"a", 1}}, writeOp)
	if string(ab.Bytes()) == string(ba.Bytes()) {
		t.Error("Each ignored order")
	}

	var none, empty canonical.Writer
	canonical.Each(&none, nil, writeOp)
	canonical.Each(&empty, []op{}, writeOp)
	if string(none.Bytes()) != string(empty.Bytes()) {
		t.Error("a nil sequence and an empty one encode differently")
	}

	// Two items must not encode like one item whose fields span both.
	var two, one canonical.Writer
	canonical.Each(&two, []op{{"a", 1}, {"b", 2}}, writeOp)
	canonical.Each(&one, []op{{"a", 1}}, writeOp)
	if string(two.Bytes()) == string(one.Bytes()) {
		t.Error("dropping an item did not change the encoding")
	}
}

// Sorted is for collections whose listing order is presentation. Two orderings
// of the same set must agree, which is what makes a fingerprint independent of
// how a store happened to return its rows.
func TestSortedIgnoresTheOrderItIsGiven(t *testing.T) {
	key := func(o op) string { return o.name }

	var ab, ba canonical.Writer
	canonical.Sorted(&ab, []op{{"a", 1}, {"b", 2}}, key, writeOp)
	canonical.Sorted(&ba, []op{{"b", 2}, {"a", 1}}, key, writeOp)
	if string(ab.Bytes()) != string(ba.Bytes()) {
		t.Errorf("Sorted depended on input order: %q vs %q", ab.Bytes(), ba.Bytes())
	}
}

func TestSortedStillNoticesADifferentSet(t *testing.T) {
	key := func(o op) string { return o.name }

	var ab, ac canonical.Writer
	canonical.Sorted(&ab, []op{{"a", 1}, {"b", 2}}, key, writeOp)
	canonical.Sorted(&ac, []op{{"a", 1}, {"c", 2}}, key, writeOp)
	if string(ab.Bytes()) == string(ac.Bytes()) {
		t.Error("Sorted did not distinguish two different sets")
	}
}

// Sorting must not reach past the key into the rest of the item, or two items
// sharing a key would be reordered by a detail and hash inconsistently.
func TestSortedIsStableAcrossEqualKeys(t *testing.T) {
	key := func(o op) string { return o.name }

	var first, second canonical.Writer
	canonical.Sorted(&first, []op{{"a", 1}, {"a", 2}}, key, writeOp)
	canonical.Sorted(&second, []op{{"a", 1}, {"a", 2}}, key, writeOp)
	if string(first.Bytes()) != string(second.Bytes()) {
		t.Error("Sorted is not stable for equal keys")
	}
}

func TestSortedDoesNotDisturbTheCallersSlice(t *testing.T) {
	items := []op{{"b", 2}, {"a", 1}}
	var w canonical.Writer
	canonical.Sorted(&w, items, func(o op) string { return o.name }, writeOp)
	if items[0].name != "b" {
		t.Errorf("Sorted reordered the caller's slice: %v", items)
	}
}

func TestSumProducesAStableDigest(t *testing.T) {
	build := func() canonical.Writer {
		var w canonical.Writer
		w.String("pln_1")
		w.Int(42)
		w.Bool(true)
		return w
	}
	a, b := build(), build()
	if a.Sum() != b.Sum() {
		t.Errorf("Sum is not stable: %s vs %s", a.Sum(), b.Sum())
	}
	if a.Sum().IsZero() {
		t.Error("Sum returned a zero digest")
	}
}

func TestSumChangesWithAnyField(t *testing.T) {
	var a canonical.Writer
	a.String("pln_1")
	a.Int(42)

	var b canonical.Writer
	b.String("pln_1")
	b.Int(43)

	if a.Sum() == b.Sum() {
		t.Error("changing a field did not change the digest")
	}
}

// The writer must not become a hidden global: two writers are independent.
func TestWritersAreIndependent(t *testing.T) {
	var a, b canonical.Writer
	a.String("x")
	if len(b.Bytes()) != 0 {
		t.Errorf("an untouched writer holds %q", b.Bytes())
	}
	if !strings.Contains(string(a.Bytes()), "x") {
		t.Errorf("the written value is missing from %q", a.Bytes())
	}
}

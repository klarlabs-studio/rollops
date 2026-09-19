package identity

// Revision is an aggregate's optimistic-concurrency counter. It starts at zero
// for an aggregate that has never been persisted and advances by one per
// committed change.
type Revision uint64

// Next returns the revision a change commits at.
func (r Revision) Next() Revision { return r + 1 }

// Matches reports whether expected agrees with the live revision r. A zero
// expectation means "I did not read the aggregate first" and never matches:
// treating it as agreement would let a blind write clobber a concurrent one.
func (r Revision) Matches(expected Revision) bool {
	if expected == 0 {
		return false
	}
	return expected == r
}

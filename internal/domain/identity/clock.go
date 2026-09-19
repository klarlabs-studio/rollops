package identity

import "time"

// Clock is the port every timestamp in the domain comes from. Nothing calls
// time.Now directly, so a test can assert on exact instants (spec §29).
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

// NewSystemClock returns the production clock. It reports UTC: a timestamp that
// carries a local zone into storage or an API response is a bug that only
// surfaces when the host is moved.
func NewSystemClock() Clock {
	return ClockFunc(func() time.Time { return time.Now().UTC() })
}

// NewFixedClock returns a clock stopped at at.
func NewFixedClock(at time.Time) Clock {
	return ClockFunc(func() time.Time { return at })
}

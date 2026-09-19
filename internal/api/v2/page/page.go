// Package page cuts a list into the pages a v2 API answers with.
//
// Spec 24 asks for pagination from the start on list APIs, and the reason is
// the response shape rather than the estate size: a list that returns
// everything cannot grow a limit later without breaking every client that
// already depends on getting everything.
//
// Paging is by key, not by offset. A cursor names the last row the caller saw,
// so a row inserted ahead of it shifts nothing — with offsets, one insert makes
// the next page skip a row, and nobody finds out.
//
// The slicing here is linear over a list the repository already returned whole.
// That is deliberate and temporary: the wire contract is what has to be right
// now, and pushing the limit down into the repositories is a change behind it
// rather than in front of it.
package page

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// DefaultSize is what a caller who did not ask gets.
	DefaultSize = 50

	// MaxSize caps what a caller who asked for too much gets.
	MaxSize = 500
)

// ErrBadCursor reports a cursor this API did not issue, or one naming a row
// that is no longer in the list. Both are refused rather than quietly restarting
// from the beginning, which would hand a client a page it has already processed
// and give it no way to notice.
var ErrBadCursor = errors.New("page: cursor is not one this API issued")

// cursorPrefix versions the encoding and makes a decoded cursor recognisable as
// ours. Without it any base64 string would decode to a plausible key.
const cursorPrefix = "p1:"

// Request is what a caller asked for.
type Request struct {
	// Cursor is opaque. Empty means the beginning.
	Cursor string

	// Size is the most rows wanted. Zero or less means DefaultSize; more than
	// MaxSize is clamped rather than refused, because a caller asking for more
	// than we serve wants as much as it can have and refusing makes them guess
	// the limit.
	Size int
}

// Limit is the most rows this page will hold, with the caller's ask clamped.
func (r Request) Limit() int {
	switch {
	case r.Size <= 0:
		return DefaultSize
	case r.Size > MaxSize:
		return MaxSize
	default:
		return r.Size
	}
}

// Fetch is how many rows to ask a repository for: one more than the page
// holds. There is no other way to tell a list that ended from one that ended on
// the boundary, and guessing wrong drops the last page.
func (r Request) Fetch() int { return r.Limit() + 1 }

// Start is the key to resume after, or empty at the beginning. A caller hands
// it to a repository that can page for itself, so a forged cursor is refused
// before it becomes a query.
func (r Request) Start() (string, error) {
	if r.Cursor == "" {
		return "", nil
	}
	return decode(r.Cursor)
}

// Response is one page and the way to ask for the next.
type Response[T any] struct {
	Items []T

	// Next is empty when the list ended. A caller stops on that rather than on
	// a short page, because a short page can also mean the list ended exactly
	// on a boundary.
	Next string
}

// Of returns the page of items the request asks for. key must be unique across
// items and must agree with the order they are in; it is what the cursor names.
func Of[T any](items []T, req Request, key func(T) string) (Response[T], error) {
	start, err := offset(items, req.Cursor, key)
	if err != nil {
		return Response[T]{}, err
	}
	return Trim(items[start:], req, key), nil
}

// Trim turns what a repository returned for Fetch rows into a page. The extra
// row is dropped: it was only there to prove there was more.
func Trim[T any](fetched []T, req Request, key func(T) string) Response[T] {
	limit := req.Limit()
	if len(fetched) <= limit {
		return Response[T]{Items: fetched}
	}
	got := fetched[:limit]
	return Response[T]{Items: got, Next: encode(key(got[len(got)-1]))}
}

// offset finds where the cursor left off.
func offset[T any](items []T, cursor string, key func(T) string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	last, err := decode(cursor)
	if err != nil {
		return 0, err
	}
	for i, it := range items {
		if key(it) == last {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("%w: it names a row that is no longer here", ErrBadCursor)
}

func encode(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + key))
}

func decode(cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadCursor, err)
	}
	key, ok := strings.CutPrefix(string(raw), cursorPrefix)
	if !ok {
		return "", fmt.Errorf("%w: it carries no %q", ErrBadCursor, cursorPrefix)
	}
	return key, nil
}

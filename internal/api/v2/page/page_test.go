package page_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/page"
)

type row struct{ id string }

func key(r row) string { return r.id }

func rows(ids ...string) []row {
	out := make([]row, 0, len(ids))
	for _, id := range ids {
		out = append(out, row{id})
	}
	return out
}

func ids(rs []row) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.id)
	}
	return out
}

func many(n int) []row {
	out := make([]row, 0, n)
	for i := range n {
		out = append(out, row{strconv.Itoa(i)})
	}
	return out
}

func of(t *testing.T, items []row, req page.Request) page.Response[row] {
	t.Helper()
	got, err := page.Of(items, req, key)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	return got
}

func TestAPageStartsAtTheBeginningWhenNobodySaidOtherwise(t *testing.T) {
	got := of(t, rows("a", "b", "c"), page.Request{Size: 2})

	if want := []string{"a", "b"}; !slices.Equal(ids(got.Items), want) {
		t.Fatalf("items %v, want %v", ids(got.Items), want)
	}
}

func TestTheNextCursorContinuesWhereThePageStopped(t *testing.T) {
	all := rows("a", "b", "c", "d", "e")

	first := of(t, all, page.Request{Size: 2})
	second := of(t, all, page.Request{Cursor: first.Next, Size: 2})
	third := of(t, all, page.Request{Cursor: second.Next, Size: 2})

	if want := []string{"c", "d"}; !slices.Equal(ids(second.Items), want) {
		t.Errorf("second page %v, want %v", ids(second.Items), want)
	}
	if want := []string{"e"}; !slices.Equal(ids(third.Items), want) {
		t.Errorf("third page %v, want %v", ids(third.Items), want)
	}
}

func TestTheLastPageOffersNoCursor(t *testing.T) {
	// An empty Next is how a caller knows to stop. A cursor that returned an
	// empty page instead would make every client paginate one request further
	// than it needs to, forever.
	got := of(t, rows("a", "b"), page.Request{Size: 2})

	if got.Next != "" {
		t.Fatalf("next %q, want none", got.Next)
	}
}

func TestAnEmptyListIsAPageWithNothingInIt(t *testing.T) {
	got := of(t, nil, page.Request{})

	if len(got.Items) != 0 {
		t.Errorf("items %v, want none", ids(got.Items))
	}
	if got.Next != "" {
		t.Errorf("next %q, want none", got.Next)
	}
}

func TestAnUnaskedSizeIsTheDefault(t *testing.T) {
	got := of(t, many(page.DefaultSize+10), page.Request{})

	if len(got.Items) != page.DefaultSize {
		t.Fatalf("%d items, want %d", len(got.Items), page.DefaultSize)
	}
}

func TestAnOversizedRequestIsClampedRatherThanRefused(t *testing.T) {
	// A caller asking for more than we serve wants as much as it can have.
	// Refusing makes them guess the limit; clamping tells them by answering.
	got := of(t, many(page.MaxSize+10), page.Request{Size: page.MaxSize * 2})

	if len(got.Items) != page.MaxSize {
		t.Fatalf("%d items, want %d", len(got.Items), page.MaxSize)
	}
}

func TestANegativeSizeIsTheDefault(t *testing.T) {
	got := of(t, many(page.DefaultSize+10), page.Request{Size: -1})

	if len(got.Items) != page.DefaultSize {
		t.Fatalf("%d items, want %d", len(got.Items), page.DefaultSize)
	}
}

func TestACursorDoesNotShowACallerWhatItIsMadeOf(t *testing.T) {
	// An opaque cursor is a cursor we can change. One a client can read is one
	// a client will construct, and then its encoding is contract.
	got := of(t, rows("rel_the-one-they-would-hand-back", "b"), page.Request{Size: 1})

	if strings.Contains(got.Next, "rel_the-one-they-would-hand-back") {
		t.Fatalf("cursor %q shows its key", got.Next)
	}
}

func TestACursorThatWasNotIssuedHereIsRefused(t *testing.T) {
	_, err := page.Of(rows("a", "b"), page.Request{Cursor: "rel_a"}, key)

	if !errors.Is(err, page.ErrBadCursor) {
		t.Fatalf("err %v, want ErrBadCursor", err)
	}
}

func TestACursorPointingAtNothingIsRefused(t *testing.T) {
	// Silently restarting from the beginning would make a client re-process a
	// page it has already seen and never find out why.
	issued := of(t, rows("a", "b", "c"), page.Request{Size: 1}).Next

	_, err := page.Of(rows("x", "y"), page.Request{Cursor: issued}, key)

	if !errors.Is(err, page.ErrBadCursor) {
		t.Fatalf("err %v, want ErrBadCursor", err)
	}
}

func TestAPageIsNotDisturbedByItemsAddedAfterIt(t *testing.T) {
	// Paging by key rather than by offset is what makes this hold: an insert
	// ahead of the cursor would shift every offset and skip a row.
	all := rows("a", "b", "c", "d")
	first := of(t, all, page.Request{Size: 2})

	grown := rows("a", "a2", "b", "c", "d")
	second := of(t, grown, page.Request{Cursor: first.Next, Size: 2})

	if want := []string{"c", "d"}; !slices.Equal(ids(second.Items), want) {
		t.Fatalf("second page %v, want %v", ids(second.Items), want)
	}
}

func TestExactlyOneFullPageStillOffersACursor(t *testing.T) {
	// There is no way to tell a list that ended from one that ended here
	// without looking, and guessing wrong loses the last page.
	got := of(t, rows("a", "b", "c"), page.Request{Size: 2})

	if got.Next == "" {
		t.Fatal("a page with more behind it offered no cursor")
	}
}

func TestTheLimitIsWhatAPageWillHold(t *testing.T) {
	for _, tc := range []struct {
		req  page.Request
		want int
	}{
		{page.Request{}, page.DefaultSize},
		{page.Request{Size: -1}, page.DefaultSize},
		{page.Request{Size: 10}, 10},
		{page.Request{Size: page.MaxSize * 2}, page.MaxSize},
	} {
		if got := tc.req.Limit(); got != tc.want {
			t.Errorf("Limit of %+v is %d, want %d", tc.req, got, tc.want)
		}
	}
}

func TestARepositoryIsAskedForOneMoreThanThePageHolds(t *testing.T) {
	// There is no other way to tell a list that ended from one that ended on
	// the boundary, and guessing wrong drops the last page.
	req := page.Request{Size: 10}

	if got := req.Fetch(); got != req.Limit()+1 {
		t.Fatalf("Fetch %d, want %d", got, req.Limit()+1)
	}
}

func TestTheStartIsTheRowTheCursorNamed(t *testing.T) {
	issued := of(t, rows("a", "b", "c"), page.Request{Size: 1}).Next

	got, err := page.Request{Cursor: issued}.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got != "a" {
		t.Fatalf("start %q, want a", got)
	}
}

func TestNoCursorStartsAtNothing(t *testing.T) {
	got, err := page.Request{}.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got != "" {
		t.Fatalf("start %q, want empty", got)
	}
}

func TestAForgedCursorIsRefusedBeforeAnythingIsRead(t *testing.T) {
	// Start is what a caller hands a repository. Refusing here means a bad
	// cursor never becomes a query.
	if _, err := (page.Request{Cursor: "not-ours"}).Start(); !errors.Is(err, page.ErrBadCursor) {
		t.Fatalf("err %v, want ErrBadCursor", err)
	}
}

func TestTrimDropsTheRowThatOnlyProvedThereWasMore(t *testing.T) {
	req := page.Request{Size: 2}
	fetched := rows("a", "b", "c") // what Fetch asked for

	got := page.Trim(fetched, req, key)

	if want := []string{"a", "b"}; !slices.Equal(ids(got.Items), want) {
		t.Errorf("items %v, want %v", ids(got.Items), want)
	}
	if got.Next == "" {
		t.Error("the page offered no cursor although a row was held back")
	}
}

func TestTrimOffersNoCursorWhenTheRepositoryHadNoMore(t *testing.T) {
	got := page.Trim(rows("a", "b"), page.Request{Size: 2}, key)

	if want := []string{"a", "b"}; !slices.Equal(ids(got.Items), want) {
		t.Errorf("items %v, want %v", ids(got.Items), want)
	}
	if got.Next != "" {
		t.Errorf("next %q, want none", got.Next)
	}
}

func TestACursorFromTrimResumesWhereItStopped(t *testing.T) {
	first := page.Trim(rows("a", "b", "c"), page.Request{Size: 2}, key)

	start, err := (page.Request{Cursor: first.Next}).Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if start != "b" {
		t.Fatalf("start %q, want b", start)
	}
}

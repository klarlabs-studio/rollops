package reconcile

import "testing"

// Short is what makes `current` checkable in a reconcile summary: the verdict
// is printed with the observation behind it, so a stale resolution is visible
// rather than implied.

func TestImageStatusShortRendering(t *testing.T) {
	cases := []struct {
		name string
		s    ImageStatus
		want string
	}{
		{"agreeing", ImageStatus{Resolved: "sha256:da334a515126aaaa", Pinned: "sha256:da334a515126aaaa"}, "(sha256:da334a515126)"},
		{"differing", ImageStatus{Resolved: "sha256:da334a515126aaaa", Pinned: "sha256:09292beaf63abbbb"}, "(sha256:09292beaf63a->sha256:da334a515126)"},
		{"nothing resolved", ImageStatus{}, ""},
	}
	for _, c := range cases {
		if got := c.s.Short(); got != c.want {
			t.Errorf("%s: Short() = %q, want %q", c.name, got, c.want)
		}
	}
}

package reconcile

import "testing"

// A target present every tick is never a candidate.
func TestReaperIgnoresPresentTargets(t *testing.T) {
	r := newReaper(3)
	for i := 0; i < 10; i++ {
		if got := r.observe("repo", []string{"a", "b"}, nil); len(got) != 0 {
			t.Fatalf("tick %d: verdicts %+v, want none", i, got)
		}
	}
}

// Absence must persist for the full threshold. A target that vanishes and
// returns — a branch switch, a file moved in two commits — must not be reaped.
func TestReaperRequiresConsecutiveAbsence(t *testing.T) {
	r := newReaper(3)
	r.observe("repo", []string{"a", "b"}, nil)
	if got := r.observe("repo", []string{"a"}, nil); len(got) != 0 {
		t.Fatalf("verdict after 1 absent tick: %+v, want none", got)
	}
	if got := r.observe("repo", []string{"a", "b"}, nil); len(got) != 0 {
		t.Fatalf("verdict after b returned: %+v, want none", got)
	}
	// The counter must have reset, so three more absences are needed.
	r.observe("repo", []string{"a"}, nil)
	r.observe("repo", []string{"a"}, nil)
	if got := r.observe("repo", []string{"a"}, nil); len(got) != 1 || got[0].Key != "repo/b" {
		t.Fatalf("verdict after 3 consecutive absences: %+v, want repo/b", got)
	}
}

// The reaper fires once, not every tick after. A target that keeps being
// absent must not produce a verdict per tick forever.
func TestReaperFiresOnce(t *testing.T) {
	r := newReaper(2)
	r.observe("repo", []string{"a"}, nil)
	r.observe("repo", []string{}, nil)
	if got := r.observe("repo", []string{}, nil); len(got) != 1 {
		t.Fatalf("first verdict: %+v, want one", got)
	}
	for i := 0; i < 5; i++ {
		if got := r.observe("repo", []string{}, nil); len(got) != 0 {
			t.Fatalf("tick %d after firing: %+v, want none", i, got)
		}
	}
}

// A load error means we did not learn anything about what exists. Absence
// counters must not advance, or a repo that briefly failed to clone would
// retire every service it manages.
func TestReaperLoadErrorSuppressesEntirely(t *testing.T) {
	r := newReaper(2)
	r.observe("repo", []string{"a", "b"}, nil)
	err := errLoad{}
	for i := 0; i < 10; i++ {
		if got := r.observe("repo", nil, err); len(got) != 0 {
			t.Fatalf("tick %d during load error: %+v, want none", i, got)
		}
	}
	// After recovery the target is still there — nothing was ever absent.
	if got := r.observe("repo", []string{"a", "b"}, nil); len(got) != 0 {
		t.Fatalf("verdict after recovery: %+v, want none", got)
	}
}

// A load error must also RESET progress, not merely pause it. Otherwise an
// intermittently-failing repo accumulates absences across outages and reaps a
// target that was present every time we actually looked.
func TestReaperLoadErrorResetsProgress(t *testing.T) {
	r := newReaper(3)
	r.observe("repo", []string{"a", "b"}, nil)
	r.observe("repo", []string{"a"}, nil) // 1 absent
	r.observe("repo", nil, errLoad{})     // outage
	r.observe("repo", []string{"a"}, nil) // 1 absent again, not 2
	if got := r.observe("repo", []string{"a"}, nil); len(got) != 0 {
		t.Fatalf("verdict at 2 post-outage absences: %+v, want none", got)
	}
	if got := r.observe("repo", []string{"a"}, nil); len(got) != 1 {
		t.Fatalf("verdict at 3 post-outage absences: %+v, want one", got)
	}
}

// Repos are independent: one repo's outage must not disturb another's counters.
func TestReaperScopesPerRepo(t *testing.T) {
	r := newReaper(2)
	r.observe("one", []string{"a"}, nil)
	r.observe("two", []string{"b"}, nil)
	r.observe("one", []string{}, nil)
	r.observe("two", nil, errLoad{})
	got := r.observe("one", []string{}, nil)
	if len(got) != 1 || got[0].Key != "one/a" {
		t.Fatalf("verdicts: %+v, want one/a only", got)
	}
}

// A target first seen only after it vanished cannot be judged: the reaper
// learns what exists from ticks it observed, so nothing is reaped on the
// strength of never having been seen.
func TestReaperNeverReapsUnknownTargets(t *testing.T) {
	r := newReaper(1)
	if got := r.observe("repo", []string{}, nil); len(got) != 0 {
		t.Fatalf("verdict on first empty tick: %+v, want none", got)
	}
}

type errLoad struct{}

func (errLoad) Error() string { return "load failed" }

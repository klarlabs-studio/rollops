package reconcile

import (
	"fmt"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

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

// Uncorrected drift must become louder the longer it lasts. Under
// `verification: detect` rollops alerts and deliberately does not fix, so the
// alert is the whole mechanism — and tick 1 reads identically to tick 1,100.
// #154 was 19 hours of a target nobody looked at.
func TestDriftStreakEscalatesOnceAfterThreshold(t *testing.T) {
	d := newDriftStreak(3)
	if got := d.observe("k", true); got != 0 {
		t.Fatalf("tick 1: %d, want 0", got)
	}
	if got := d.observe("k", true); got != 0 {
		t.Fatalf("tick 2: %d, want 0", got)
	}
	if got := d.observe("k", true); got != 3 {
		t.Fatalf("tick 3 should escalate with the streak, got %d", got)
	}
	// Louder once, not once per tick: a target drifting for a day must not
	// emit a day's worth of identical escalations.
	for i := 0; i < 5; i++ {
		if got := d.observe("k", true); got != 0 {
			t.Fatalf("tick %d after escalating: %d, want 0", 4+i, got)
		}
	}
}

// Recovery resets, and a target that drifts again later is judged afresh
// rather than escalating on its first tick because of an old streak.
func TestDriftStreakResetsOnRecovery(t *testing.T) {
	d := newDriftStreak(2)
	d.observe("k", true)
	d.observe("k", true) // escalated
	d.observe("k", false)
	if got := d.observe("k", true); got != 0 {
		t.Fatalf("first drift tick after recovery: %d, want 0", got)
	}
	if got := d.observe("k", true); got != 2 {
		t.Fatalf("second drift tick after recovery should escalate, got %d", got)
	}
}

// Targets are independent: one drifting target must not escalate another.
func TestDriftStreakScopesPerTarget(t *testing.T) {
	d := newDriftStreak(2)
	d.observe("a", true)
	d.observe("b", false)
	d.observe("a", true)
	if got := d.observe("b", true); got != 0 {
		t.Fatalf("b escalated on its first drift tick: %d", got)
	}
}

// The opt-in is the whole safety boundary. `prune: true` means "delete what I
// stopped declaring inside a live apply"; reaping means "delete everything when
// the declaration is gone". The second must not be inherited from the first.
func TestReapOnDelete_RequiresItsOwnOptIn(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want bool
	}{
		{"nothing set", map[string]any{}, false},
		{"prune alone does NOT imply reap", map[string]any{"prune": true}, false},
		{"explicit opt-in", map[string]any{"reapOnDelete": true}, true},
		{"explicit opt-out", map[string]any{"prune": true, "reapOnDelete": false}, false},
		{"non-bool is not an opt-in", map[string]any{"reapOnDelete": "yes"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Spec.Target.Spec = tc.spec
			if got := reapOnDelete(cfg); got != tc.want {
				t.Errorf("reapOnDelete(%v) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
	if reapOnDelete(nil) {
		t.Error("a nil config must never authorise a deletion")
	}
}

// A reap that removed nothing must not read as a cleanup.
//
// defaultReapTypes is kubectl's `all`, which excludes PVCs, ingresses,
// configmaps and secrets. A target whose resources are all of an excluded kind
// deletes nothing, succeeds, and — before this — logged "removed 0 resource(s)",
// which is indistinguishable from "there was nothing left to remove". That is
// the silent-success failure this whole issue is an instance of.
func TestReapReport_ZeroRemovedIsNotReportedAsCleanup(t *testing.T) {
	var logs []string
	w := &Watcher{logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}

	w.logOrphanReap(verdict{Repo: "r", Key: "r/pvc.yaml"}, "brotwerk/pvc", 0, nil)
	if len(logs) != 1 {
		t.Fatalf("logs = %v", logs)
	}
	got := logs[0]
	if !strings.Contains(got, "reapTypes") {
		t.Errorf("a zero-removal reap must point at the likely cause: %q", got)
	}
	if strings.Contains(got, "removed 0 resource(s)") {
		t.Errorf("still phrased as a successful cleanup: %q", got)
	}

	// A reap that actually removed something stays plainly a success.
	logs = nil
	w.logOrphanReap(verdict{Repo: "r", Key: "r/api.yaml"}, "brotwerk/api", 3, nil)
	if !strings.Contains(logs[0], "removed 3") {
		t.Errorf("a real removal should say so: %q", logs[0])
	}
	if strings.Contains(logs[0], "reapTypes") {
		t.Errorf("a successful reap should not warn about coverage: %q", logs[0])
	}
}

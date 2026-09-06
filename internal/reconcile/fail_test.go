package reconcile

import "testing"

func TestPermanentFailureClassification(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{`Forbidden: middlewares.traefik.io "x" is forbidden`, true},
		{`kubectl apply -f -: exit status 1: Error from server (Forbidden): ...`, true},
		{`Unauthorized`, true},
		{`connection refused`, false},
		{`i/o timeout`, false},
		{`Conflict: apply conflict`, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := permanentFailure(tc.err); got != tc.want {
			t.Errorf("permanentFailure(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// Forbidden escalates on the FIRST tick and stays quiet afterwards — that is
// the whole point of #182 suggestion 3. Retrying it every 60s cannot heal RBAC.
func TestFailStreak_ForbiddenEscalatesImmediatelyThenQuiets(t *testing.T) {
	f := newFailStreak(10)
	const msg = `Forbidden: middlewares.traefik.io "security-headers" is forbidden`

	if v := f.note("repo", msg); v != failEscalate {
		t.Fatalf("first Forbidden: %v, want escalate", v)
	}
	held, ok := f.held("repo")
	if !ok || held != msg {
		t.Fatalf("held after escalate: %q ok=%v", held, ok)
	}
	for i := 0; i < 5; i++ {
		if v := f.note("repo", msg); v != failQuiet {
			t.Fatalf("tick %d after escalate: %v, want quiet", i+2, v)
		}
	}
}

// Transient failures keep retrying and only escalate once they have been the
// SAME error for `after` consecutive ticks — mirroring driftStreak.
func TestFailStreak_TransientEscalatesAtThreshold(t *testing.T) {
	f := newFailStreak(3)
	const msg = "connection refused"

	if v := f.note("a", msg); v != failFresh {
		t.Fatalf("tick 1: %v, want fresh", v)
	}
	if v := f.note("a", msg); v != failFresh {
		t.Fatalf("tick 2: %v, want fresh", v)
	}
	if v := f.note("a", msg); v != failEscalate {
		t.Fatalf("tick 3: %v, want escalate", v)
	}
	if _, ok := f.held("a"); ok {
		t.Fatal("transient failures must not enter the permanent hold")
	}
	if v := f.note("a", msg); v != failQuiet {
		t.Fatalf("tick 4: %v, want quiet", v)
	}
}

// A changed error text resets the streak: a new failure is a new problem.
func TestFailStreak_ResetsWhenErrorChanges(t *testing.T) {
	f := newFailStreak(2)
	f.note("a", "connection refused")
	f.note("a", "connection refused") // escalated
	if v := f.note("a", "i/o timeout"); v != failFresh {
		t.Fatalf("changed error: %v, want fresh", v)
	}
	if v := f.note("a", "i/o timeout"); v != failEscalate {
		t.Fatalf("second tick of new error: %v, want escalate", v)
	}
}

// Success clears; a later failure is judged afresh.
func TestFailStreak_ClearOnSuccess(t *testing.T) {
	f := newFailStreak(2)
	f.note("a", "connection refused")
	f.note("a", "") // success
	if v := f.note("a", "connection refused"); v != failFresh {
		t.Fatalf("after success: %v, want fresh", v)
	}
}

// A Git change for the repo must reopen a permanent hold — desired state moved,
// so the previously-known Forbidden deserves another look.
func TestFailStreak_ClearRepoReleasesHold(t *testing.T) {
	f := newFailStreak(10)
	f.note("acme", "Forbidden: no")
	f.note("acme/app.yaml", "Forbidden: no")
	f.note("other", "Forbidden: no")
	f.clearRepo("acme")
	if _, ok := f.held("acme"); ok {
		t.Fatal("repo key still held after clearRepo")
	}
	if _, ok := f.held("acme/app.yaml"); ok {
		t.Fatal("target key still held after clearRepo")
	}
	if _, ok := f.held("other"); !ok {
		t.Fatal("clearRepo must not touch a different repo")
	}
}

func TestFailStreak_ScopesPerKey(t *testing.T) {
	f := newFailStreak(2)
	f.note("a", "connection refused")
	if v := f.note("b", "connection refused"); v != failFresh {
		t.Fatalf("b escalated on its first tick: %v", v)
	}
}

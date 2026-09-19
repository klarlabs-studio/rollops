package ssh

import (
	"context"
	"sync"
	"testing"

	conformancev2 "go.klarlabs.de/rollops/pkg/conformance/v2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// fakeTransport is an in-memory host: a file map plus a command runner.
type fakeTransport struct {
	mu     sync.Mutex
	files  map[string][]byte
	writes []string // paths in the order they were written
	fail   bool     // make Run/commands fail
}

func newFakeTransport() *fakeTransport { return &fakeTransport{files: map[string][]byte{}} }

func (f *fakeTransport) Run(_ context.Context, _ string) (int, string, error) {
	if f.fail {
		return 1, "boom", nil
	}
	return 0, "", nil
}
func (f *fakeTransport) WriteFile(_ context.Context, path string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(content))
	copy(cp, content)
	f.files[path] = cp
	f.writes = append(f.writes, path)
	return nil
}
func (f *fakeTransport) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[path]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

var sample = targetv2.DesiredState{
	Kind:     "ssh",
	Spec:     []byte(`{"app":"api","ver":3}`),
	Checksum: "sum-ssh-v3",
}

func host() *Target { return newWith(newFakeTransport(), spec{"deployPath": "/srv/app"}) }

func key(s string) string { return targetv2.IdempotencyKeyFor("ssh-test", s) }

// TestConformance measures this target against §9.5's ten axes directly. It is
// the second subject of the suite and the one that matters for R8: a contract
// only one implementation can satisfy is a description of that implementation.
//
// Invalid is left zero. An SSH payload is opaque bytes written verbatim, so
// there is no desired state this target can be shown to reject — and the axis
// says so rather than reporting a pass it did not earn.
func TestConformance(t *testing.T) {
	s := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return host(), nil },
		Desired: sample,
	}
	s.Run(t)
}

func TestApply_StampsAndIsIdempotent(t *testing.T) {
	tr := newFakeTransport()
	tgt := newWith(tr, spec{"deployPath": "/srv/app"})
	ctx := context.Background()

	res, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("one")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Error("first apply should report Changed")
	}
	if string(tr.files["/srv/app"]) != `{"app":"api","ver":3}` {
		t.Errorf("payload not written: %q", tr.files["/srv/app"])
	}
	obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{})
	if obs.Fingerprint != sample.Checksum {
		t.Errorf("observed %q, want %q", obs.Fingerprint, sample.Checksum)
	}
	// A different key over the same desired state is a second intent, not a
	// replay, so this measures convergence rather than the key record.
	res2, _ := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("two")})
	if res2.Changed {
		t.Error("re-apply of same checksum must be a no-op")
	}
}

// TestTheKeyIsRecordedAfterTheWorkItStandsFor pins the write order, which is
// the whole of this target's crash safety. The host has no atomic multi-file
// write, so one of the three files is necessarily last — and it has to be the
// key, because the key is the claim that everything before it landed. A crash
// before it means a retry redoes idempotent writes; a crash after a key
// written first would mean a retry believing work that never happened.
func TestTheKeyIsRecordedAfterTheWorkItStandsFor(t *testing.T) {
	tr := newFakeTransport()
	tgt := newWith(tr, spec{"deployPath": "/srv/app"})

	if _, err := tgt.Apply(context.Background(), targetv2.ApplyRequest{
		Desired: sample, IdempotencyKey: key("order"),
	}); err != nil {
		t.Fatal(err)
	}
	if len(tr.writes) != 3 {
		t.Fatalf("apply wrote %d files (%v), want payload, stamp and key", len(tr.writes), tr.writes)
	}
	if last := tr.writes[len(tr.writes)-1]; last != tgt.keyPath {
		t.Errorf("the last file written was %q, want the key at %q", last, tgt.keyPath)
	}
}

// TestASpentKeyReplaysRatherThanRedeploying is §9.4 from the side that
// matters: the retry comes from a process that crashed between the call and
// its response, so the answer has to be on the host rather than in memory.
func TestASpentKeyReplaysRatherThanRedeploying(t *testing.T) {
	tr := newFakeTransport()
	s := spec{"deployPath": "/srv/app"}
	ctx := context.Background()
	k := key("crash")

	first, err := newWith(tr, s).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	writes := len(tr.writes)

	// A fresh Target is the retry: the process that made the first call is gone.
	replay, err := newWith(tr, s).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Changed != first.Changed {
		t.Errorf("the replay reported changed=%v, want the first answer %v", replay.Changed, first.Changed)
	}
	if len(tr.writes) != writes {
		t.Errorf("the replay wrote to the host again: %v", tr.writes[writes:])
	}
}

// TestTheSameKeyOverADifferentStateIsAConflict is the other branch: guessing
// which of two requests the caller meant is worse than refusing.
func TestTheSameKeyOverADifferentStateIsAConflict(t *testing.T) {
	tr := newFakeTransport()
	s := spec{"deployPath": "/srv/app"}
	ctx := context.Background()
	k := key("reused")

	if _, err := newWith(tr, s).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	writes := len(tr.writes)

	other := sample
	other.Checksum = "sum-ssh-v4"
	_, err := newWith(tr, s).Apply(ctx, targetv2.ApplyRequest{Desired: other, IdempotencyKey: k})
	if got := targetv2.KindOf(err); got != targetv2.KindConflict {
		t.Errorf("a reused key over a different desired state was reported as kind %q, want %q", got, targetv2.KindConflict)
	}
	if len(tr.writes) != writes {
		t.Errorf("the conflicting apply reached the host: %v", tr.writes[writes:])
	}
}

// TestPlanDoesNotTouchTheHost is the side-effect-free axis stated in this
// target's own terms: a dumb target plans by reading its stamp, and a plan
// that wrote anything would have deployed.
func TestPlanDoesNotTouchTheHost(t *testing.T) {
	tr := newFakeTransport()
	tgt := newWith(tr, spec{"deployPath": "/srv/app"})

	res, err := tgt.Plan(context.Background(), targetv2.PlanRequest{Desired: sample})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changes {
		t.Error("planning an undeployed checksum onto a fresh host reported no change")
	}
	if len(tr.writes) != 0 {
		t.Errorf("Plan wrote to the host: %v", tr.writes)
	}
}

func TestObserve_NeverDeployed(t *testing.T) {
	obs, err := host().Observe(context.Background(), targetv2.ObserveRequest{})
	if err != nil {
		t.Fatalf("observe on fresh host should not error: %v", err)
	}
	if obs.Fingerprint != "" {
		t.Errorf("fresh host fingerprint = %q, want empty", obs.Fingerprint)
	}
}

func TestHealth_CommandExit(t *testing.T) {
	tr := newFakeTransport()
	tgt := newWith(tr, spec{"deployPath": "/srv/app", "healthCmd": "systemctl is-active api"})
	ctx := context.Background()
	if obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{}); obs.Health.State != targetv2.HealthHealthy {
		t.Errorf("exit 0 health = %v, want healthy", obs.Health.State)
	}
	tr.fail = true
	if obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{}); obs.Health.State != targetv2.HealthUnhealthy {
		t.Errorf("nonzero exit health = %v, want unhealthy", obs.Health.State)
	}
}

// TestRollbackIsRefused keeps capability truthfulness honest at the source: a
// host with one payload file has nothing to return to, and the engine's
// fallback — applying the previous desired state — is the correct answer.
func TestRollbackIsRefused(t *testing.T) {
	_, err := host().Rollback(context.Background(), targetv2.RollbackRequest{})
	if !targetv2.IsUnsupported(err) {
		t.Errorf("Rollback answered %v, want unsupported", err)
	}
}

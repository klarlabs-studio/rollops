package ftp

import (
	"context"
	"testing"

	conformancev2 "go.klarlabs.de/rollops/pkg/conformance/v2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

type fakeConn struct {
	files  map[string][]byte
	stores []string // paths in the order they were stored
	down   bool
}

func newFakeConn() *fakeConn { return &fakeConn{files: map[string][]byte{}} }

func (f *fakeConn) Store(_ context.Context, path string, content []byte) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	f.files[path] = cp
	f.stores = append(f.stores, path)
	return nil
}
func (f *fakeConn) Retrieve(_ context.Context, path string) ([]byte, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}
func (f *fakeConn) Ping(context.Context) error {
	if f.down {
		return ErrNotFound
	}
	return nil
}

var sample = targetv2.DesiredState{
	Kind:     "ftp",
	Spec:     []byte("<html>v2</html>"),
	Checksum: "sum-ftp-v2",
}

func site() spec { return spec{"deployPath": "site/index.html"} }

func key(s string) string { return targetv2.IdempotencyKeyFor("ftp-test", s) }

// TestConformance measures this target against §9.5's ten axes directly. FTP is
// the thinnest target there is — store a file, read a marker back — which makes
// it the useful third subject: an axis it cannot pass is an axis asking for
// more than the contract states.
func TestConformance(t *testing.T) {
	s := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return newWith(newFakeConn(), site()), nil },
		Desired: sample,
	}
	s.Run(t)
}

func TestApply_Idempotent(t *testing.T) {
	c := newFakeConn()
	tgt := newWith(c, site())
	ctx := context.Background()

	r1, _ := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("one")})
	if !r1.Changed {
		t.Error("first apply should change")
	}
	if string(c.files["site/index.html"]) != "<html>v2</html>" {
		t.Errorf("payload = %q", c.files["site/index.html"])
	}
	r2, _ := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("two")})
	if r2.Changed {
		t.Error("re-apply must be no-op")
	}
}

// TestTheKeyIsStoredAfterTheWorkItStandsFor pins the store order. FTP has no
// atomic multi-file write either, so the key goes last: it is the claim that
// the payload and the stamp landed, and a claim stored before them would have
// a retry believing an upload that never happened.
func TestTheKeyIsStoredAfterTheWorkItStandsFor(t *testing.T) {
	c := newFakeConn()
	tgt := newWith(c, site())

	if _, err := tgt.Apply(context.Background(), targetv2.ApplyRequest{
		Desired: sample, IdempotencyKey: key("order"),
	}); err != nil {
		t.Fatal(err)
	}
	if len(c.stores) != 3 {
		t.Fatalf("apply stored %d files (%v), want payload, stamp and key", len(c.stores), c.stores)
	}
	if last := c.stores[len(c.stores)-1]; last != tgt.keyPath {
		t.Errorf("the last file stored was %q, want the key at %q", last, tgt.keyPath)
	}
}

// TestASpentKeyReplaysRatherThanReuploading is §9.4 from the side that
// matters: the retry comes from a process that crashed between the call and
// its response, so the answer has to be on the server rather than in memory.
func TestASpentKeyReplaysRatherThanReuploading(t *testing.T) {
	c := newFakeConn()
	ctx := context.Background()
	k := key("crash")

	first, err := newWith(c, site()).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	stores := len(c.stores)

	// A fresh Target is the retry: the process that made the first call is gone.
	replay, err := newWith(c, site()).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Changed != first.Changed {
		t.Errorf("the replay reported changed=%v, want the first answer %v", replay.Changed, first.Changed)
	}
	if len(c.stores) != stores {
		t.Errorf("the replay uploaded again: %v", c.stores[stores:])
	}
}

// TestTheSameKeyOverADifferentStateIsAConflict is the other branch: guessing
// which of two requests the caller meant is worse than refusing.
func TestTheSameKeyOverADifferentStateIsAConflict(t *testing.T) {
	c := newFakeConn()
	ctx := context.Background()
	k := key("reused")

	if _, err := newWith(c, site()).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	stores := len(c.stores)

	other := sample
	other.Checksum = "sum-ftp-v3"
	_, err := newWith(c, site()).Apply(ctx, targetv2.ApplyRequest{Desired: other, IdempotencyKey: k})
	if got := targetv2.KindOf(err); got != targetv2.KindConflict {
		t.Errorf("a reused key over a different desired state was reported as kind %q, want %q", got, targetv2.KindConflict)
	}
	if len(c.stores) != stores {
		t.Errorf("the conflicting apply reached the server: %v", c.stores[stores:])
	}
}

// TestPlanUploadsNothing is the side-effect-free axis in this target's own
// terms: a plan that stored anything would have deployed.
func TestPlanUploadsNothing(t *testing.T) {
	c := newFakeConn()
	tgt := newWith(c, site())

	res, err := tgt.Plan(context.Background(), targetv2.PlanRequest{Desired: sample})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changes {
		t.Error("planning an undeployed checksum onto an empty server reported no change")
	}
	if len(c.stores) != 0 {
		t.Errorf("Plan stored: %v", c.stores)
	}
}

func TestHealth_Reachability(t *testing.T) {
	c := newFakeConn()
	tgt := newWith(c, spec{})
	ctx := context.Background()
	if obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{}); obs.Health.State != targetv2.HealthHealthy {
		t.Error("reachable server should be healthy")
	}
	c.down = true
	if obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{}); obs.Health.State != targetv2.HealthUnhealthy {
		t.Error("unreachable server should be unhealthy")
	}
}

// TestRollbackIsRefused keeps capability truthfulness honest at the source: one
// path holds one file, so there is nothing to return to and the engine's
// fallback of applying the previous desired state is the right answer.
func TestRollbackIsRefused(t *testing.T) {
	_, err := newWith(newFakeConn(), site()).Rollback(context.Background(), targetv2.RollbackRequest{})
	if !targetv2.IsUnsupported(err) {
		t.Errorf("Rollback answered %v, want unsupported", err)
	}
}

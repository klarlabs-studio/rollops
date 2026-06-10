package ssh

import (
	"context"
	"sync"
	"testing"

	"go.klarlabs.de/rollops/pkg/conformance"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// fakeTransport is an in-memory host: a file map plus a command runner.
type fakeTransport struct {
	mu    sync.Mutex
	files map[string][]byte
	fail  bool // make Run/commands fail
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

var sample = pt.Manifest{Kind: "ssh", Spec: []byte(`{"app":"api","ver":3}`), Checksum: "sum-ssh-v3"}

func TestConformance(t *testing.T) {
	conformance.Run(t, func() (pt.Target, error) {
		return newWith(newFakeTransport(), spec{"deployPath": "/srv/app"}), nil
	}, sample)
}

func TestApply_StampsAndIsIdempotent(t *testing.T) {
	tr := newFakeTransport()
	tgt := newWith(tr, spec{"deployPath": "/srv/app"})
	ctx := context.Background()

	res, err := tgt.Apply(ctx, sample)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Error("first apply should report Changed")
	}
	if string(tr.files["/srv/app"]) != `{"app":"api","ver":3}` {
		t.Errorf("payload not written: %q", tr.files["/srv/app"])
	}
	fp, _ := tgt.Observe(ctx)
	if fp.Value != sample.Checksum {
		t.Errorf("observed %q, want %q", fp.Value, sample.Checksum)
	}
	res2, _ := tgt.Apply(ctx, sample)
	if res2.Changed {
		t.Error("re-apply of same checksum must be a no-op")
	}
}

func TestObserve_NeverDeployed(t *testing.T) {
	tgt := newWith(newFakeTransport(), spec{"deployPath": "/srv/app"})
	fp, err := tgt.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe on fresh host should not error: %v", err)
	}
	if fp.Value != "" {
		t.Errorf("fresh host fingerprint = %q, want empty", fp.Value)
	}
}

func TestHealth_CommandExit(t *testing.T) {
	tr := newFakeTransport()
	tgt := newWith(tr, spec{"deployPath": "/srv/app", "healthCmd": "systemctl is-active api"})
	if hs, _ := tgt.Health(context.Background()); hs.State != pt.HealthHealthy {
		t.Errorf("exit 0 health = %v, want healthy", hs.State)
	}
	tr.fail = true
	if hs, _ := tgt.Health(context.Background()); hs.State != pt.HealthUnhealthy {
		t.Errorf("nonzero exit health = %v, want unhealthy", hs.State)
	}
}

package ftp

import (
	"context"
	"testing"

	"go.klarlabs.de/rolloffs/pkg/conformance"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

type fakeConn struct {
	files map[string][]byte
	down  bool
}

func newFakeConn() *fakeConn { return &fakeConn{files: map[string][]byte{}} }

func (f *fakeConn) Store(_ context.Context, path string, content []byte) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	f.files[path] = cp
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

var sample = pt.Manifest{Kind: "ftp", Spec: []byte("<html>v2</html>"), Checksum: "sum-ftp-v2"}

func TestConformance(t *testing.T) {
	conformance.Run(t, func() (pt.Target, error) {
		return newWith(newFakeConn(), spec{"deployPath": "site/index.html"}), nil
	}, sample)
}

func TestApply_Idempotent(t *testing.T) {
	c := newFakeConn()
	tgt := newWith(c, spec{"deployPath": "site/index.html"})
	ctx := context.Background()

	r1, _ := tgt.Apply(ctx, sample)
	if !r1.Changed {
		t.Error("first apply should change")
	}
	if string(c.files["site/index.html"]) != "<html>v2</html>" {
		t.Errorf("payload = %q", c.files["site/index.html"])
	}
	r2, _ := tgt.Apply(ctx, sample)
	if r2.Changed {
		t.Error("re-apply must be no-op")
	}
}

func TestHealth_Reachability(t *testing.T) {
	c := newFakeConn()
	tgt := newWith(c, spec{})
	if hs, _ := tgt.Health(context.Background()); hs.State != pt.HealthHealthy {
		t.Error("reachable server should be healthy")
	}
	c.down = true
	if hs, _ := tgt.Health(context.Background()); hs.State != pt.HealthUnhealthy {
		t.Error("unreachable server should be unhealthy")
	}
}

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// listFailingStore fails ListRollouts and delegates everything else.
type listFailingStore struct{ store.Store }

func (listFailingStore) ListRollouts(context.Context, int) ([]rollout.Rollout, error) {
	return nil, errors.New("database disk image is malformed")
}

// A store error used to read as "no prior state", which disables the
// baseline check: auto-rollback was then allowed to restore a manifest no
// one had compared with what is live.
func TestRollbackBaseline_StoreErrorBlocksRollback(t *testing.T) {
	e, db := newEngine(t, &fakeTarget{})
	e.store = listFailingStore{db}
	why := e.rollbackBaseline(context.Background(), "demo/prod/app", pt.Manifest{Checksum: "sha:new"})
	if !strings.Contains(why, "malformed") {
		t.Fatalf("rollbackBaseline = %q; want a reason naming the store error", why)
	}
}

func TestPriorManifestErr_ReportsTheStoreError(t *testing.T) {
	e, db := newEngine(t, &fakeTarget{})
	e.store = listFailingStore{db}
	if _, _, err := e.PriorManifestErr(context.Background(), "demo/prod/app", "sha:new"); err == nil {
		t.Fatal("PriorManifestErr returned nil error for a failing store")
	}
}

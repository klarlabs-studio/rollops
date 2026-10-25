package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

func TestReady_HealthyStore(t *testing.T) {
	db := openTemp(t)
	if err := db.Ready(context.Background()); err != nil {
		t.Fatalf("Ready on a fresh store: %v", err)
	}
}

// The 2026-09-19 incident: the database went corrupt and /readyz kept
// answering 200 while every reconcile failed. Ready must see the damage.
func TestReady_CorruptStoreIsNotReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollops.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	// Enough rows to spread the rollouts table over many pages.
	for i := range 400 {
		r := sampleRollout(fmt.Sprintf("r-%03d", i), rollout.PhasePromoted)
		r.Desired.Spec = []byte(strings.Repeat("x", 512))
		if err := db.SaveRollout(ctx, r); err != nil {
			t.Fatalf("SaveRollout: %v", err)
		}
	}
	if _, err := db.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	_ = db.Close()

	// Overwrite pages in the middle of the file, as a torn write would.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", 16*1024)), info.Size()/2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	db, err = Open(path)
	if err != nil {
		// Refusing to open is also "not ready"; nothing more to check.
		t.Logf("Open refused the corrupt file: %v", err)
		return
	}
	defer func() { _ = db.Close() }()
	err = db.Ready(ctx)
	if err == nil {
		t.Fatal("Ready returned nil for a corrupt database")
	}
	t.Logf("Ready: %v", err)
}

func TestReady_ClosedStoreIsNotReady(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "rollops.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := db.Ready(context.Background()); err == nil {
		t.Fatal("Ready returned nil for a closed store")
	}
}

// ListRollouts kept a row it could not decode, with an empty manifest and
// zero timestamps, while LoadRollout rejected the same row. The row still
// read as in flight, every Tick/Abort through LoadRollout failed, and its
// manifest vanished from rollback selection: a silent wedge. Both readers
// now refuse it, naming the row.
func TestListRollouts_UndecodableRowIsAnError(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.SaveRollout(ctx, sampleRollout("r-good", rollout.PhasePromoted)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRollout(ctx, sampleRollout("r-bad", rollout.PhaseDeploying)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE rollouts SET manifest = '{not json' WHERE id = 'r-bad'`); err != nil {
		t.Fatal(err)
	}
	_, err := db.ListRollouts(ctx, 0)
	if err == nil || !strings.Contains(err.Error(), "r-bad") {
		t.Fatalf("ListRollouts = %v; want an error naming r-bad", err)
	}
	if _, err := db.LoadRollout(ctx, "r-bad"); err == nil {
		t.Fatal("LoadRollout accepted the undecodable row")
	}
}

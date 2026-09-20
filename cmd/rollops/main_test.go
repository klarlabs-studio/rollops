package main

import (
	"context"
	"path/filepath"
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/cliapi"
	"go.klarlabs.de/rollops/internal/store/sqlite"
)

// These drive run directly rather than a built binary, so what is under test is
// the composition root itself — the one place where the release model could be
// assembled and then never reached.

func TestAProjectCreatedByTheBinaryIsInTheDatabaseAfterwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollops.db")
	t.Setenv("ROLLOPS_DB", path)
	t.Setenv("ROLLOPS_DAEMON", "")

	if err := run([]string{"project", "create", "--name", "checkout"}); err != nil {
		t.Fatalf("project create: %v", err)
	}
	if err := run([]string{"project", "list"}); err != nil {
		t.Fatalf("project list: %v", err)
	}

	// Asserted against storage rather than against stdout: what matters is that
	// the command reached a database and left something durable in it, and a
	// printed line could be produced without ever having done so.
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	got, err := db.Projects().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "checkout" {
		t.Fatalf("stored %+v; want one project named checkout", got)
	}
}

func TestAFailureReachesTheShellAsACodeTheShellCanBranchOn(t *testing.T) {
	t.Setenv("ROLLOPS_DB", filepath.Join(t.TempDir(), "rollops.db"))
	t.Setenv("ROLLOPS_DAEMON", "")

	// Exit 1 is what a missing binary, a killed process and a confused wrapper
	// all produce, so a refused command must not join them.
	err := run([]string{"project", "get", "does-not-exist"})
	if err == nil {
		t.Fatal("a project that does not exist was got")
	}
	if code := cliapi.Exit(err); code != cliapi.ExitValidation {
		t.Errorf("exit %d, want %d", code, cliapi.ExitValidation)
	}
}

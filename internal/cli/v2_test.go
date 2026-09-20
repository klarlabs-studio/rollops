package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/cliapi"
	"go.klarlabs.de/rollops/internal/boot"
	"go.klarlabs.de/rollops/internal/cli"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/store/sqlite"
)

// v2App builds the whole production v2 stack over a real sqlite file. Nothing
// here is a fake: the point of these tests is that the assembly reaches
// storage, which is precisely what a fake would hide.
func v2App(t *testing.T) (*cli.App, *bytes.Buffer) {
	t.Helper()

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "rollops.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc, err := boot.V2(context.Background(), boot.V2Config{Store: db})
	if err != nil {
		t.Fatalf("boot.V2: %v", err)
	}

	var out bytes.Buffer
	surface, err := cliapi.New(cliapi.Config{
		Service: svc,
		Identity: cliapi.IdentifierFunc(func(context.Context) (identity.Principal, error) {
			return identity.Principal{ID: "tester", Type: "human"}, nil
		}),
		Out: &out,
	})
	if err != nil {
		t.Fatalf("cliapi.New: %v", err)
	}
	return &cli.App{Out: &out, V2: surface}, &out
}

func TestAProjectCreatedThroughTheCLIIsListedBackFromTheDatabase(t *testing.T) {
	t.Parallel()

	// This is the assertion that would have caught the v2 stack being
	// unreachable: it goes in through the top-level command surface and comes
	// back out of a real sqlite file, so every seam between them is exercised.
	app, out := v2App(t)
	ctx := context.Background()

	if err := app.Run(ctx, []string{"project", "create", "--name", "checkout"}); err != nil {
		t.Fatalf("project create: %v", err)
	}
	out.Reset()
	if err := app.Run(ctx, []string{"project", "list"}); err != nil {
		t.Fatalf("project list: %v", err)
	}
	if !strings.Contains(out.String(), "checkout") {
		t.Errorf("project list printed %q; the project did not survive the round trip", out.String())
	}
}

func TestEveryReleaseModelCommandIsReachableAtTheTopLevel(t *testing.T) {
	t.Parallel()

	// Reachable, not successful: most of these want arguments. What is being
	// pinned is that none of them is answered by the migration notice, which is
	// how the whole surface was unreachable before.
	for _, cmd := range cliapi.Commands {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			app, _ := v2App(t)
			err := app.Run(context.Background(), []string{cmd})
			if err != nil && strings.Contains(err.Error(), "rollops rollout ") {
				t.Fatalf("%q was answered by the rollout migration notice: %v", cmd, err)
			}
			if err != nil && strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("%q reached no dispatch at all: %v", cmd, err)
			}
		})
	}
}

func TestACommandOnlyTheRolloutEngineHasStillNamesItsNewSpelling(t *testing.T) {
	t.Parallel()

	app, _ := v2App(t)
	for _, cmd := range []string{
		"apply", "fleet", "pause", "resume", "abort",
		"approve", "reject", "freeze", "unfreeze",
	} {
		err := app.Run(context.Background(), []string{cmd})
		if err == nil {
			t.Fatalf("%q was accepted at the top level", cmd)
		}
		if !strings.Contains(err.Error(), "rollops rollout "+cmd) {
			t.Errorf("%q: error %q does not name where the command went", cmd, err)
		}
	}
}

func TestWithoutAV2SurfaceAReleaseCommandSaysWhereItWentRatherThanPanicking(t *testing.T) {
	t.Parallel()

	// Daemon mode builds no local service, so the overlapping names must still
	// land somewhere that explains itself instead of dereferencing a nil.
	app := &cli.App{Out: &bytes.Buffer{}}
	err := app.Run(context.Background(), []string{"plan", "./rollops.yaml"})
	if err == nil {
		t.Fatal("plan was accepted with no service behind it")
	}
	if !strings.Contains(err.Error(), "rollops rollout plan") {
		t.Errorf("error %q does not name the rollout spelling", err)
	}
}

func TestARolloutVerbIsNotInterceptedByTheReleaseModel(t *testing.T) {
	t.Parallel()

	app, out := v2App(t)
	if err := app.Run(context.Background(), []string{"rollout", "help"}); err != nil {
		t.Fatalf("rollout help: %v", err)
	}
	if !strings.Contains(out.String(), "rollops rollout <operation>") {
		t.Errorf("rollout help printed %q", out.String())
	}
}

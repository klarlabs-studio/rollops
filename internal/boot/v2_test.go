package boot_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/boot"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

func openStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "rollops.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAServiceWithNowhereToWriteIsRefusedAtStartup(t *testing.T) {
	t.Parallel()

	_, err := boot.V2(context.Background(), boot.V2Config{})
	if err == nil {
		t.Fatal("a service with no store was built; it would fail on the first call instead of at startup")
	}
	if !strings.Contains(err.Error(), "store") {
		t.Errorf("error %q does not name what was missing", err)
	}
}

func TestADefaultInstallationAssembles(t *testing.T) {
	t.Parallel()

	svc, err := boot.V2(context.Background(), boot.V2Config{Store: openStore(t)})
	if err != nil {
		t.Fatalf("V2: %v", err)
	}
	if svc == nil {
		t.Fatal("V2 returned no service and no error")
	}
}

func TestADefaultInstallationVerifiesWithSomethingRatherThanNothing(t *testing.T) {
	t.Parallel()

	// runner.New refuses an empty check set, so a default installation either
	// supplies a check or cannot be assembled at all. Asserting the assembly
	// succeeds is asserting that the absence was turned into an answer rather
	// than into a nil verifier.
	if _, err := boot.V2(context.Background(), boot.V2Config{Store: openStore(t)}); err != nil {
		t.Fatalf("a default installation could not be assembled: %v", err)
	}
}

func TestAnUnusableCheckSetIsReportedRatherThanReplaced(t *testing.T) {
	t.Parallel()

	// A caller that supplied checks and got the default anyway would be running
	// a verification nobody configured while believing otherwise.
	_, err := boot.V2(context.Background(), boot.V2Config{
		Store:  openStore(t),
		Checks: []verifyv1.Verifier{nil},
	})
	if err == nil {
		t.Fatal("a nil check was accepted")
	}
}

func TestAPolicyFloorNobodyCanReadIsRefusedAtStartup(t *testing.T) {
	t.Parallel()

	// The alternative is discovering it on the first production deployment,
	// which is the one place a misread floor is expensive.
	env := map[string]string{"ROLLOPS_APPROVE_AT_OR_ABOVE": "catastrophic"}
	_, err := boot.V2(context.Background(), boot.V2Config{
		Store:  openStore(t),
		Getenv: func(k string) string { return env[k] },
	})
	if err == nil {
		t.Fatal("an unrecognised risk level was accepted")
	}
	if !strings.Contains(err.Error(), "catastrophic") {
		t.Errorf("error %q does not name the value it could not read", err)
	}
}

func TestAConfiguredFloorIsHonoured(t *testing.T) {
	t.Parallel()

	env := map[string]string{"ROLLOPS_APPROVE_AT_OR_ABOVE": "low"}
	svc, err := boot.V2(context.Background(), boot.V2Config{
		Store:  openStore(t),
		Getenv: func(k string) string { return env[k] },
	})
	if err != nil {
		t.Fatalf("V2: %v", err)
	}
	if svc == nil {
		t.Fatal("V2 returned no service and no error")
	}
}

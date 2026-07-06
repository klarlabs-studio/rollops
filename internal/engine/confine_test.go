package engine

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/security"
)

func confineFromEnv(m map[string]string) security.Confinement {
	return security.ConfinementFromEnv(func(k string) string { return m[k] })
}

// TestExecSmoke_CommandAllowlist covers the smoke-test exec chokepoint: an
// allow-listed command runs, a disallowed one is rejected before exec, and an
// unset allowlist preserves today's behavior.
func TestExecSmoke_CommandAllowlist(t *testing.T) {
	ctx := context.Background()

	t.Run("disallowed rejected before exec", func(t *testing.T) {
		r := execSmoke{confinement: confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_COMMANDS": "true"})}
		code, err := r.Run(ctx, []string{"/bin/sh", "-c", "exit 0"})
		if err == nil {
			t.Fatal("disallowed command must be rejected")
		}
		if code != -1 {
			t.Errorf("rejected command must not report a real exit code, got %d", code)
		}
		if !strings.Contains(err.Error(), "not allow-listed") {
			t.Errorf("error should name the allowlist, got %v", err)
		}
	})

	t.Run("allowed runs", func(t *testing.T) {
		r := execSmoke{confinement: confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_COMMANDS": "true,false"})}
		if code, err := r.Run(ctx, []string{"true"}); err != nil || code != 0 {
			t.Errorf("allow-listed 'true' => (0,nil), got (%d,%v)", code, err)
		}
		if code, err := r.Run(ctx, []string{"false"}); err != nil || code != 1 {
			t.Errorf("allow-listed 'false' => (1,nil), got (%d,%v)", code, err)
		}
	})

	t.Run("unset allowlist runs as today", func(t *testing.T) {
		r := execSmoke{} // zero-value confinement = off
		if code, err := r.Run(ctx, []string{"true"}); err != nil || code != 0 {
			t.Errorf("unset allowlist must run 'true', got (%d,%v)", code, err)
		}
	})
}

// TestExecDBRollback_CommandAllowlist covers the database migrate/rollback exec
// chokepoint under the same allowlist.
func TestExecDBRollback_CommandAllowlist(t *testing.T) {
	ctx := context.Background()

	t.Run("disallowed rejected", func(t *testing.T) {
		r := execDBRollback{confinement: confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_COMMANDS": "true"})}
		if err := r.Run(ctx, []string{"/bin/sh", "-c", "exit 0"}); err == nil {
			t.Fatal("disallowed DB command must be rejected")
		}
	})

	t.Run("allowed runs", func(t *testing.T) {
		r := execDBRollback{confinement: confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_COMMANDS": "true"})}
		if err := r.Run(ctx, []string{"true"}); err != nil {
			t.Errorf("allow-listed DB command must run, got %v", err)
		}
	})

	t.Run("unset runs as today", func(t *testing.T) {
		r := execDBRollback{}
		if err := r.Run(ctx, []string{"true"}); err != nil {
			t.Errorf("unset allowlist must run 'true', got %v", err)
		}
	})
}

// TestWithConfinement_WiresRunners verifies the option installs the policy on
// both default exec runners so config-sourced commands are gated.
func TestWithConfinement_WiresRunners(t *testing.T) {
	conf := confineFromEnv(map[string]string{"ROLLOPS_ALLOWED_COMMANDS": "true"})
	e := &Engine{}
	WithConfinement(conf)(e)

	s, ok := e.smoke.(execSmoke)
	if !ok {
		t.Fatalf("smoke runner = %T, want execSmoke", e.smoke)
	}
	if _, err := s.Run(context.Background(), []string{"/bin/sh", "-c", ":"}); err == nil {
		t.Error("WithConfinement should gate the smoke runner")
	}

	db, ok := e.dbRollback.(execDBRollback)
	if !ok {
		t.Fatalf("db runner = %T, want execDBRollback", e.dbRollback)
	}
	if err := db.Run(context.Background(), []string{"/bin/sh", "-c", ":"}); err == nil {
		t.Error("WithConfinement should gate the db runner")
	}
}

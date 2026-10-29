package security

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

// The freeze was read from memory only; the persisted row was loaded once at
// boot. A freeze set through the CLI against the shared store, or through
// the API on another replica, never reached the leader, which kept applying.
func TestCheckApply_FreezeSetElsewhereBlocks(t *testing.T) {
	g := &Guardrails{
		Freeze: NewFreeze(), // in memory: not frozen
		FreezeSource: func(context.Context) (bool, string, error) {
			return true, "incident 4711", nil // persisted by another process
		},
	}
	if _, err := g.CheckApply(context.Background(), rollout.Identity{Kind: "ci", Name: "reconciler"}, FloorInput{}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("CheckApply = %v; want ErrFrozen", err)
	}
}

func TestCheckApply_LiftedElsewhereAllows(t *testing.T) {
	fz := NewFreeze()
	fz.Engage(rollout.Identity{Name: "felix"}, "old")
	g := &Guardrails{Freeze: fz, FreezeSource: func(context.Context) (bool, string, error) { return false, "", nil }}
	if _, err := g.CheckApply(context.Background(), rollout.Identity{Kind: "ci"}, FloorInput{}); err != nil {
		t.Fatalf("CheckApply = %v; a freeze lifted in the store must not keep blocking", err)
	}
}

// A kill-switch that cannot be read must not read as "off".
func TestCheckApply_UnreadableFreezeBlocks(t *testing.T) {
	g := &Guardrails{FreezeSource: func(context.Context) (bool, string, error) {
		return false, "", errors.New("database is locked")
	}}
	if _, err := g.CheckApply(context.Background(), rollout.Identity{Kind: "ci"}, FloorInput{}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("CheckApply = %v; want ErrFrozen when the freeze cannot be read", err)
	}
}

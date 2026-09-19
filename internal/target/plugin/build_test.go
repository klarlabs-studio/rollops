package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

func compileOnce(pkg string) func() (string, error) {
	return sync.OnceValues(func() (string, error) {
		dir, err := os.MkdirTemp("", "rollops-testplugin-")
		if err != nil {
			return "", err
		}
		bin := filepath.Join(dir, filepath.Base(pkg))
		cmd := exec.Command("go", "build", "-o", bin, pkg)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", &exec.ExitError{ProcessState: cmd.ProcessState, Stderr: out}
		}
		return bin, nil
	})
}

var (
	buildTestPlugin   = compileOnce("./testdata/testplugin")
	buildTestPluginV2 = compileOnce("./testdata/testpluginv2")
)

func testPluginBinary(t *testing.T) string {
	t.Helper()
	bin, err := buildTestPlugin()
	if err != nil {
		t.Fatalf("build testplugin: %v", err)
	}
	return bin
}

func testPluginV2Binary(t *testing.T) string {
	t.Helper()
	bin, err := buildTestPluginV2()
	if err != nil {
		t.Fatalf("build testpluginv2: %v", err)
	}
	return bin
}

func spec(t *testing.T, bin string) map[string]any {
	t.Helper()
	return map[string]any{"binary": bin, "sha256": sha256Of(t, bin)}
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestBuild_TargetCapabilityEndToEnd(t *testing.T) {
	bin := testPluginBinary(t)
	ctx := context.Background()

	tgt, err := Build(config.Target{Kind: "plugin", Ref: "x/prod/exotic", Spec: map[string]any{
		"binary": bin,
		"sha256": sha256Of(t, bin),
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = tgt.(interface{ Close() error }).Close() }()

	res, err := tgt.Apply(ctx, pt.Manifest{Kind: "plugin", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"})
	if err != nil || !res.Changed || !strings.Contains(res.Detail, "sha:abc") {
		t.Fatalf("apply = %+v err=%v", res, err)
	}
	res2, err := tgt.Apply(ctx, pt.Manifest{Kind: "plugin", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"})
	if err != nil || res2.Changed {
		t.Errorf("reapply must be idempotent: %+v err=%v", res2, err)
	}
	fp, err := tgt.Observe(ctx)
	if err != nil || fp.Value != "sha:abc" || fp.Meta["backend"] != "mem" {
		t.Errorf("observe = %+v err=%v", fp, err)
	}
	hs, err := tgt.Health(ctx)
	if err != nil || hs.State != pt.HealthHealthy {
		t.Errorf("health = %+v err=%v", hs, err)
	}
}

func TestBuild_RejectsBadPinAndMissingBinary(t *testing.T) {
	bin := testPluginBinary(t)
	if _, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": bin, "sha256": strings.Repeat("0", 64)}}); err == nil {
		t.Error("wrong pin must be rejected")
	}
	if _, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": bin}}); err == nil {
		t.Error("missing pin must be rejected")
	}
	if _, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{}}); err == nil {
		t.Error("missing binary must be rejected")
	}
}

// TestBuildV2_TypedContractEndToEnd drives the whole typed path: handshake,
// manifest, declared contract, typed RPC. The plugin is a real subprocess, so
// what is exercised is the wire rather than an in-process stand-in.
func TestBuildV2_TypedContractEndToEnd(t *testing.T) {
	ctx := context.Background()
	tgt, err := BuildV2(config.Target{Kind: "plugin", Ref: "x/prod/exotic", Spec: spec(t, testPluginV2Binary(t))})
	if err != nil {
		t.Fatalf("BuildV2: %v", err)
	}
	defer func() { _ = tgt.Close() }()

	desired := targetv2.DesiredState{Kind: "mem", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"}

	plan, err := tgt.Plan(ctx, targetv2.PlanRequest{Desired: desired})
	if err != nil || !plan.Changes {
		t.Fatalf("plan = %+v err=%v, want changes", plan, err)
	}

	res, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired, IdempotencyKey: "d1/op1"})
	if err != nil || !res.Changed {
		t.Fatalf("apply = %+v err=%v", res, err)
	}
	// The same key must not apply twice. The plugin replays its stored result,
	// so a second Changed:true here would mean the key never reached it.
	replay, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired, IdempotencyKey: "d1/op1"})
	if err != nil || replay != res {
		t.Errorf("replay = %+v err=%v, want %+v", replay, err, res)
	}
	if _, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired}); err == nil {
		t.Error("an apply with no idempotency key was accepted")
	}

	obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{Handle: res.Handle})
	if err != nil || obs.Health.State != targetv2.HealthHealthy || obs.Fingerprint != desired.Checksum {
		t.Errorf("observe = %+v err=%v", obs, err)
	}

	state, err := tgt.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil || len(state.Resources) != 1 || state.Meta["backend"] != "mem" {
		t.Errorf("inspect = %+v err=%v", state, err)
	}

	drift, err := tgt.DetectDrift(ctx, targetv2.DriftRequest{Desired: desired})
	if err != nil || drift.Drifted {
		t.Errorf("drift = %+v err=%v, want no drift after apply", drift, err)
	}
}

// TestBuildV2_TheCeilingRefusesAnOverclaimedCapability is the intersection
// doing its job. testpluginv2 claims Prune at runtime while its manifest
// declares only health observation and drift — the operator never authorized
// pruning, so claiming it louder must not reach the plugin.
func TestBuildV2_TheCeilingRefusesAnOverclaimedCapability(t *testing.T) {
	tgt, err := BuildV2(config.Target{Kind: "plugin", Ref: "x", Spec: spec(t, testPluginV2Binary(t))})
	if err != nil {
		t.Fatalf("BuildV2: %v", err)
	}
	defer func() { _ = tgt.Close() }()

	if tgt.Can(targetv2.CapabilityPrune) {
		t.Error("a capability the plugin was not installed with survived the narrowing")
	}
	if _, err := tgt.Prune(context.Background(), targetv2.PruneRequest{}); err == nil {
		t.Error("prune was reachable")
	} else if got := targetv2.KindOf(err); got != targetv2.KindUnsupported {
		t.Errorf("prune failed with kind %q, want %q", got, targetv2.KindUnsupported)
	}
	if len(tgt.Overclaimed) != 1 || tgt.Overclaimed[0] != targetv2.CapabilityPrune {
		t.Errorf("overclaimed = %v, want [%s]", tgt.Overclaimed, targetv2.CapabilityPrune)
	}
}

// TestBuildV2_APluginFromBeforeV2StillWorks is the compatibility promise
// (ADR-0006, §9.6). testplugin declares no contract and serves the generic
// tool wire, and its author did nothing to make this work.
func TestBuildV2_APluginFromBeforeV2StillWorks(t *testing.T) {
	ctx := context.Background()
	tgt, err := BuildV2(config.Target{Kind: "plugin", Ref: "x/legacy", Spec: spec(t, testPluginBinary(t))})
	if err != nil {
		t.Fatalf("BuildV2: %v", err)
	}
	defer func() { _ = tgt.Close() }()

	desired := targetv2.DesiredState{Kind: "plugin", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"}
	res, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired, IdempotencyKey: "d1/op1"})
	if err != nil || !res.Changed {
		t.Fatalf("apply = %+v err=%v", res, err)
	}
	obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{Handle: res.Handle})
	if err != nil || obs.Health.State != targetv2.HealthHealthy {
		t.Errorf("observe = %+v err=%v", obs, err)
	}

	// There was nowhere for a v1 author to declare a ceiling, so the adapter's
	// claim stands unnarrowed — nothing was authorized separately, so nothing
	// is refused separately.
	if len(tgt.Overclaimed) != 0 {
		t.Errorf("a v1 plugin overclaimed %v", tgt.Overclaimed)
	}
	if !tgt.Can(targetv2.CapabilityHealthObservation) {
		t.Error("the adapter's own claim was dropped")
	}
}

func TestBuild_ResolvesSymlink(t *testing.T) {
	bin := testPluginBinary(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	tgt, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": link, "sha256": sha256Of(t, bin)}})
	if err != nil {
		t.Fatalf("Build via symlink: %v", err)
	}
	_ = tgt.(interface{ Close() error }).Close()
}

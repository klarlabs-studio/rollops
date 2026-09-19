// Command testpluginv2 is the in-memory target used by the launcher's typed
// end-to-end tests. It serves the rollops.target.v2 contract rather than the
// generic tool wire, so the whole subprocess path — handshake, manifest,
// declared contract, typed RPC — is exercised the way a real v2 plugin is.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"go.klarlabs.de/rollops/pkg/plugin"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// ceiling is what this plugin could ever be asked to do. Prune is deliberately
// left out while the target claims it at runtime, so a host test can see the
// intersection refuse a capability the plugin was not installed with.
var ceiling = targetv2.Capabilities{HealthObservation: true, DriftDetection: true}

type memTarget struct {
	mu       sync.Mutex
	checksum string
	applied  map[string]targetv2.ApplyResult
}

func (m *memTarget) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "mem", Name: "rollops/testpluginv2", Version: "1.0.0"}
}

func (m *memTarget) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return targetv2.Capabilities{HealthObservation: true, DriftDetection: true, Prune: true}, nil
}

func (m *memTarget) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return targetv2.ObservedState{
		Fingerprint: m.checksum,
		Resources:   []targetv2.Resource{{Kind: "Thing", Name: "one", Status: "ready"}},
		Meta:        map[string]string{"backend": "mem"},
	}, nil
}

func (m *memTarget) Plan(_ context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checksum == req.Desired.Checksum {
		return targetv2.PlanResult{}, nil
	}
	return targetv2.PlanResult{Changes: true, Diff: "- " + m.checksum + "\n+ " + req.Desired.Checksum}, nil
}

func (m *memTarget) Apply(_ context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	if req.IdempotencyKey == "" {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindInvalid, "Apply", nil,
			"an idempotency key is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior, seen := m.applied[req.IdempotencyKey]; seen {
		return prior, nil
	}
	res := targetv2.ApplyResult{
		Changed: m.checksum != req.Desired.Checksum,
		Detail:  "applied " + req.Desired.Checksum,
		Handle:  req.IdempotencyKey,
	}
	m.checksum = req.Desired.Checksum
	m.applied[req.IdempotencyKey] = res
	return res, nil
}

func (m *memTarget) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return targetv2.Observation{
		Fingerprint: m.checksum,
		Health:      targetv2.HealthStatus{State: targetv2.HealthHealthy, Reason: "in-memory"},
	}, nil
}

func (m *memTarget) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

func (m *memTarget) DetectDrift(_ context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return targetv2.DriftResult{Drifted: m.checksum != req.Desired.Checksum}, nil
}

func (m *memTarget) Prune(context.Context, targetv2.PruneRequest) (targetv2.PruneResult, error) {
	return targetv2.PruneResult{Removed: 1}, nil
}

func main() {
	t := &memTarget{applied: map[string]targetv2.ApplyResult{}}
	err := plugin.ServeTargetV2("rollops/testpluginv2", "1.0.0", t, ceiling,
		plugin.Safety{RiskClass: plugin.RiskActive})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testpluginv2:", err)
		os.Exit(1)
	}
}

// Package boot assembles the production engine options shared by rollopsd and
// the one-shot CLI. The two paths must not diverge: a freeze, secret provider,
// audit trail, or governance gate that exists only on the daemon is a bypass.
package boot

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/governance"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/secrets"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store"
)

// Getenv is os.Getenv, injectable in tests.
type Getenv func(string) string

// Config is the process-wide wiring for a production Engine.
type Config struct {
	Getenv Getenv
	Store  store.Store
	Log    io.Writer // startup notes; nil discards
}

func (c Config) getenv(key string) string {
	if c.Getenv == nil {
		return ""
	}
	return c.Getenv(key)
}

func (c Config) logf(format string, args ...any) {
	if c.Log == nil {
		return
	}
	_, _ = fmt.Fprintf(c.Log, format, args...)
}

// Options builds the engine option set the daemon and one-shot CLI share.
// It restores a persisted freeze so a restart cannot silently lift the
// kill-switch. Returns an error if the freeze row cannot be read.
func (c Config) Options(ctx context.Context) ([]engine.Option, error) {
	if c.Store == nil {
		return nil, fmt.Errorf("boot: store is required")
	}

	fz := security.NewFreeze()
	rec, err := c.Store.LoadFreeze(ctx)
	if err != nil {
		return nil, fmt.Errorf("boot: load freeze: %w", err)
	}
	if rec.Active {
		fz.Engage(rec.By, rec.Reason)
		c.logf("rollops: freeze restored (%s)\n", rec.Reason)
	}

	guard := &security.Guardrails{
		Floor:      security.DefaultPolicyFloor(),
		Freeze:     fz,
		AgentLimit: security.NewAgentLimiter(20, time.Minute),
	}

	owner := c.getenv("ROLLOPS_INSTANCE_ID")
	if owner == "" {
		owner = "rollopsd"
	}

	prov, err := secrets.FromEnv(c.getenv)
	if err != nil {
		return nil, fmt.Errorf("boot: secrets: %w", err)
	}

	opts := []engine.Option{
		engine.WithAudit(audit.New(logOrDiscard(c.Log))),
		engine.WithGuardrails(guard),
		engine.WithSecrets(prov),
		engine.WithLeaseOwner(owner),
	}
	if c.getenv("VAULT_ADDR") != "" {
		c.logf("rollops: secret chain Vault+Env (%s)\n", c.getenv("VAULT_ADDR"))
	}
	if analysisEnabled(c.getenv("ROLLOPS_ANALYSIS")) {
		opts = append(opts, engine.WithMetricAnalysis())
		c.logf("rollops: metric analysis enabled (observability-free default is off)\n")
	}
	if key := c.getenv("ROLLOPS_COSIGN_KEY"); key != "" {
		opts = append(opts, engine.WithArtifactGate(security.ArtifactGate{
			Mode:     security.VerifyEnforce,
			Verifier: security.CosignVerifier{KeyPath: key},
		}))
	}
	if n, _ := notify.FromEnv(c.getenv); n != nil {
		opts = append(opts, engine.WithNotifier(n))
	}
	if g := governance.FromEnv(c.getenv); g != nil {
		opts = append(opts, engine.WithGovernance(g))
		c.logf("rollops: external governance: %s (fail-closed)\n", c.getenv("ROLLOPS_GOVERNANCE_URL"))
	}
	confinement := security.ConfinementFromEnv(c.getenv)
	opts = append(opts, engine.WithConfinement(confinement))
	c.logf("rollops: multi-tenant confinement: %s\n", confinement.LogSummary())
	if !confinement.Active() {
		c.logf("rollops: multi-tenant confinement is OFF (trusted-repo mode); for untrusted/multi-tenant repos set ROLLOPS_ALLOWED_COMMANDS, ROLLOPS_ALLOWED_NAMESPACES, and/or ROLLOPS_CONFINE_TARGET_CLUSTER=1\n")
	}
	return opts, nil
}

func logOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func analysisEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

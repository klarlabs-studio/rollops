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
	if gate, describe := c.artifactGate(); gate != nil {
		opts = append(opts, engine.WithArtifactGate(*gate))
		c.logf("rollops: artifact gate: %s\n", describe)
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

// artifactGate assembles the deploy-time artifact policy.
//
// Three claims by three authorities, each opt-in and each checkable on its
// own. A signature proves somebody holding the key vouched for these bytes and
// says nothing about where they came from, so an attacker who obtains that key
// can sign an arbitrary image and a signature-only gate deploys it. Build
// provenance pins the artifact to a commit and to the platform that built it.
// The source gate's verification summary says that commit passed its policy —
// carried from the gate itself rather than summarised by the builder, so the
// verdict names its verifier and the policy file it was measured against.
//
// Setting only ROLLOPS_COSIGN_KEY keeps the previous behaviour exactly.
func (c Config) artifactGate() (*security.ArtifactGate, string) {
	key := c.getenv("ROLLOPS_COSIGN_KEY")
	identity := c.getenv("ROLLOPS_COSIGN_IDENTITY")
	issuer := c.getenv("ROLLOPS_COSIGN_ISSUER")
	builders := splitList(c.getenv("ROLLOPS_PROVENANCE_BUILDERS"))
	gates := splitList(c.getenv("ROLLOPS_SOURCE_GATES"))

	authenticated := key != "" || (identity != "" && issuer != "")

	var verifiers []security.ArtifactVerifier
	var parts []string

	if authenticated {
		verifiers = append(verifiers, security.CosignVerifier{
			KeyPath: key, CertIdentity: identity, CertOIDCIssuer: issuer,
		})
		parts = append(parts, "signature")
	}

	var provenance *security.ProvenanceVerifier
	if len(builders) > 0 {
		if !authenticated {
			// A verifier with nothing to authenticate against fails every
			// deploy, which reads as a broken pipeline rather than the
			// misconfiguration it is.
			c.logf("rollops: ROLLOPS_PROVENANCE_BUILDERS is set but no cosign key or identity is; " +
				"provenance cannot be verified without one\n")
		} else {
			provenance = &security.ProvenanceVerifier{
				KeyPath: key, CertIdentity: identity, CertOIDCIssuer: issuer,
				AllowedBuilders: builders,
				RequireReproved: truthy(c.getenv("ROLLOPS_PROVENANCE_REQUIRE_REPROVED")),
			}
			verifiers = append(verifiers, *provenance)
			parts = append(parts, "provenance from "+strings.Join(builders, ", "))
		}
	}

	if len(gates) > 0 {
		if !authenticated {
			c.logf("rollops: ROLLOPS_SOURCE_GATES is set but no cosign key or identity is; " +
				"the source summary cannot be verified without one\n")
		} else {
			// Handing the source gate the provenance verifier is what lets it
			// check that the commit the gate vouched for is the commit the
			// artifact was built from. Without that join both attestations
			// verify alone while saying nothing about each other.
			verifiers = append(verifiers, security.SourceGateVerifier{
				KeyPath: key, CertIdentity: identity, CertOIDCIssuer: issuer,
				AllowedVerifiers: gates,
				RequireLevels:    splitList(c.getenv("ROLLOPS_SOURCE_REQUIRE_LEVELS")),
				Provenance:       provenance,
			})
			parts = append(parts, "source gated by "+strings.Join(gates, ", "))
		}
	}

	if len(verifiers) == 0 {
		return nil, ""
	}
	return &security.ArtifactGate{
		Mode:     security.VerifyEnforce,
		Verifier: security.ChainVerifier{Verifiers: verifiers},
	}, "enforcing " + strings.Join(parts, " + ")
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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

// Package verifyv1 is the verification contract (§11). A Verifier answers one
// question about a deployment that has already been applied — is it actually
// serving? — and answers it in a vocabulary with five verdicts rather than a
// bool, because "the check could not tell" and "the check said yes" lead to
// different decisions and a bool cannot hold both (P8, INV-010).
//
// Health observation, smoke tests and metric analysis are three implementations
// of this one interface, not three bespoke gates.
package verifyv1

import (
	"context"
	"time"
)

// Verifier answers one verification question (§11.1).
type Verifier interface {
	Metadata() VerifierMetadata

	Verify(context.Context, VerificationRequest) (VerificationResult, error)
}

// VerifierMetadata identifies a verifier the way target metadata identifies a
// target: Kind is what it does ("http", "prometheus", "command"), Name is which
// configured instance this is.
type VerifierMetadata struct {
	Kind    string
	Name    string
	Version string
}

// VerificationRequest names what is under verification. A verifier's own
// configuration — a URL, a command, a query — is bound when it is constructed;
// what changes between calls is the deployment being asked about.
type VerificationRequest struct {
	// Target is the stable ref of the target the deployment landed on.
	Target string
	// Revision identifies the desired state being verified, so a result can be
	// attributed to the deploy it measured rather than to whatever is live now.
	Revision string
}

// Measurement is one number a verifier observed. Verifiers report these so a
// verdict can be read back to whoever has to act on it — a fail with no
// measurement is an assertion, a fail with one is evidence.
type Measurement struct {
	Name  string
	Value float64
	At    time.Time
}

// EvidenceRef points at something outside this result that supports it: a
// dashboard, a log query, a run artifact. It is a reference rather than content
// on purpose — evidence can be large, and INV-012 means it can also be secret.
type EvidenceRef struct {
	Kind string // "url", "log", "artifact"
	URI  string
}

// VerificationResult is what one check concluded (§11.3).
//
// The zero value is deliberately not a pass: Verdict is "", which Combine
// reports as an error. A verifier that returns before setting a verdict has not
// verified anything, and the contract says so rather than defaulting.
type VerificationResult struct {
	Verdict      Verdict
	Measurements []Measurement
	StartedAt    time.Time
	FinishedAt   time.Time
	Reason       string
	Evidence     []EvidenceRef
}

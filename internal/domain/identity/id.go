// Package identity holds the primitives every aggregate in the domain is built
// from: typed opaque identifiers, the principal a mutation is attributed to,
// optimistic-concurrency revisions, and the clock and ID-generator ports that
// keep both deterministic under test.
//
// Nothing here may reach for a transport, a store, or a provider (INV-006,
// INV-007). The encoding of an ID is ADR-0001.
package identity

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrWrongKind reports an identifier that parses but belongs to another
// aggregate — an environment id handed to ParseProjectID, say. It is a distinct
// error because a caller can usefully tell "misrouted" from "malformed".
var ErrWrongKind = errors.New("identity: identifier belongs to another kind")

// Type prefixes. These are public surface the moment an ID is persisted or
// returned to a client; they cannot be renamed (ADR-0001).
const (
	prefixProject         = "prj_"
	prefixEnvironment     = "env_"
	prefixArtifact        = "art_"
	prefixRelease         = "rel_"
	prefixDeployment      = "dep_"
	prefixPlan            = "pln_"
	prefixVerificationRun = "vrf_"
	prefixPipelineRun     = "run_"
	prefixExecution       = "exe_"
	prefixEvent           = "evt_"
	prefixApproval        = "apr_"
)

// The typed IDs. Each is a distinct type so the compiler refuses a swap that a
// bare string would accept.
type (
	ProjectID         string
	EnvironmentID     string
	ArtifactID        string
	ReleaseID         string
	DeploymentID      string
	PlanID            string
	VerificationRunID string
	PipelineRunID     string
	ExecutionID       string
	EventID           string
	ApprovalID        string
)

func newID(g Generator, prefix string) (string, error) {
	if g == nil {
		return "", errors.New("identity: nil generator")
	}
	raw, err := g.NewID()
	if err != nil {
		return "", err
	}
	if _, err := uuid.Parse(raw); err != nil {
		return "", fmt.Errorf("identity: generator produced %q: %w", raw, err)
	}
	return prefix + raw, nil
}

func parseID(s, prefix string) (string, error) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		// Anything carrying a known prefix that is not this one is misrouted
		// rather than malformed, and the caller may want to say so.
		if isKnownPrefix(s) {
			return "", fmt.Errorf("%w: want %q, got %q", ErrWrongKind, prefix, s[:4])
		}
		return "", fmt.Errorf("identity: %q does not carry prefix %q", s, prefix)
	}
	if _, err := uuid.Parse(rest); err != nil {
		return "", fmt.Errorf("identity: %q: %w", s, err)
	}
	// uuid.Parse accepts brace and urn:uuid: forms; only the canonical one is
	// ours, and accepting a second spelling would make an ID non-unique as text.
	if len(rest) != 36 {
		return "", fmt.Errorf("identity: %q is not a canonical uuid", s)
	}
	return prefix + rest, nil
}

func isKnownPrefix(s string) bool {
	if len(s) < 4 {
		return false
	}
	switch s[:4] {
	case prefixProject, prefixEnvironment, prefixArtifact, prefixRelease,
		prefixDeployment, prefixPlan, prefixVerificationRun, prefixPipelineRun,
		prefixExecution, prefixEvent, prefixApproval:
		return true
	}
	return false
}

func NewProjectID(g Generator) (ProjectID, error) {
	s, err := newID(g, prefixProject)
	return ProjectID(s), err
}

func NewEnvironmentID(g Generator) (EnvironmentID, error) {
	s, err := newID(g, prefixEnvironment)
	return EnvironmentID(s), err
}

func NewArtifactID(g Generator) (ArtifactID, error) {
	s, err := newID(g, prefixArtifact)
	return ArtifactID(s), err
}

func NewReleaseID(g Generator) (ReleaseID, error) {
	s, err := newID(g, prefixRelease)
	return ReleaseID(s), err
}

func NewDeploymentID(g Generator) (DeploymentID, error) {
	s, err := newID(g, prefixDeployment)
	return DeploymentID(s), err
}

func NewPlanID(g Generator) (PlanID, error) {
	s, err := newID(g, prefixPlan)
	return PlanID(s), err
}

func NewVerificationRunID(g Generator) (VerificationRunID, error) {
	s, err := newID(g, prefixVerificationRun)
	return VerificationRunID(s), err
}

func NewPipelineRunID(g Generator) (PipelineRunID, error) {
	s, err := newID(g, prefixPipelineRun)
	return PipelineRunID(s), err
}

func NewExecutionID(g Generator) (ExecutionID, error) {
	s, err := newID(g, prefixExecution)
	return ExecutionID(s), err
}

func NewEventID(g Generator) (EventID, error) {
	s, err := newID(g, prefixEvent)
	return EventID(s), err
}

func NewApprovalID(g Generator) (ApprovalID, error) {
	s, err := newID(g, prefixApproval)
	return ApprovalID(s), err
}

func ParseProjectID(s string) (ProjectID, error) {
	got, err := parseID(s, prefixProject)
	return ProjectID(got), err
}

func ParseEnvironmentID(s string) (EnvironmentID, error) {
	got, err := parseID(s, prefixEnvironment)
	return EnvironmentID(got), err
}

func ParseArtifactID(s string) (ArtifactID, error) {
	got, err := parseID(s, prefixArtifact)
	return ArtifactID(got), err
}

func ParseReleaseID(s string) (ReleaseID, error) {
	got, err := parseID(s, prefixRelease)
	return ReleaseID(got), err
}

func ParseDeploymentID(s string) (DeploymentID, error) {
	got, err := parseID(s, prefixDeployment)
	return DeploymentID(got), err
}

func ParsePlanID(s string) (PlanID, error) {
	got, err := parseID(s, prefixPlan)
	return PlanID(got), err
}

func ParseVerificationRunID(s string) (VerificationRunID, error) {
	got, err := parseID(s, prefixVerificationRun)
	return VerificationRunID(got), err
}

func ParsePipelineRunID(s string) (PipelineRunID, error) {
	got, err := parseID(s, prefixPipelineRun)
	return PipelineRunID(got), err
}

func ParseExecutionID(s string) (ExecutionID, error) {
	got, err := parseID(s, prefixExecution)
	return ExecutionID(got), err
}

func ParseEventID(s string) (EventID, error) {
	got, err := parseID(s, prefixEvent)
	return EventID(got), err
}

func ParseApprovalID(s string) (ApprovalID, error) {
	got, err := parseID(s, prefixApproval)
	return ApprovalID(got), err
}

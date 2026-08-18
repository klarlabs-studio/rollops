package security

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// VSAPredicateType is the SLSA Verification Summary Attestation: a statement
// that some verifier checked a resource against a policy and reached a
// verdict. A source gate publishes one about a commit.
const VSAPredicateType = "https://slsa.dev/verification_summary/v1"

// VSAStatement is a verification summary as a consumer reads it.
type VSAStatement struct {
	PredicateType string    `json:"predicateType"`
	Subject       []Subject `json:"subject"`
	Predicate     VSA       `json:"predicate"`
}

// VSA is the summary body.
type VSA struct {
	Verifier struct {
		ID      string            `json:"id"`
		Version map[string]string `json:"version"`
	} `json:"verifier"`
	TimeVerified string `json:"timeVerified"`
	// ResourceURI names what was verified — for a source gate, a git URI
	// ending in the commit.
	ResourceURI string `json:"resourceUri"`
	Policy      struct {
		URI string `json:"uri"`
	} `json:"policy"`
	VerificationResult string   `json:"verificationResult"`
	VerifiedLevels     []string `json:"verifiedLevels"`
}

// Passed reports a clean verdict.
func (s VSAStatement) Passed() bool {
	return strings.EqualFold(s.Predicate.VerificationResult, "PASSED")
}

// SourceCommit is the commit the summary is about.
//
// The subject is preferred, but a summary attached to an artifact has had its
// subject rewritten to that artifact by cosign, so resourceUri is where the
// commit actually survives.
func (s VSAStatement) SourceCommit() string {
	for _, sub := range s.Subject {
		if c := sub.Digest["gitCommit"]; c != "" {
			return c
		}
	}
	if i := strings.LastIndex(s.Predicate.ResourceURI, "@"); i >= 0 {
		return s.Predicate.ResourceURI[i+1:]
	}
	return ""
}

// SourceGateVerifier requires an artifact to carry a passing verification
// summary from a source gate RollOps trusts.
//
// This is a different claim from build provenance and comes from a different
// authority. Build provenance says "kiln built this artifact from commit C".
// The summary says "warden checked commit C against its policy and it passed".
// A build platform asserting the second on the gate's behalf would only be
// worth the build platform's word; carrying the gate's own statement means the
// verdict names its verifier, its policy file and the levels it reached.
//
// The two are joined by the commit, and the join is checked. Without it, a
// summary for some other well-gated commit could be attached to an artifact
// built from an ungated one, and each attestation would verify perfectly on
// its own.
type SourceGateVerifier struct {
	KeyPath        string
	CertIdentity   string
	CertOIDCIssuer string
	AllowHTTP      bool

	// AllowedVerifiers are the gate identities that may vouch for a source.
	// Empty accepts any, and says so rather than implying a check happened.
	AllowedVerifiers []string

	// RequireLevels are verification levels the summary must claim, e.g.
	// WARDEN_SOURCE_SIGNED to insist the note was signed rather than merely
	// present.
	RequireLevels []string

	// Provenance supplies the commit to join against. Without it the summary
	// is checked on its own terms and the join is skipped — which is weaker,
	// and reported as such.
	Provenance *ProvenanceVerifier

	Run func(ctx context.Context, name string, args ...string) (output string, err error)
}

// Configured reports whether this verifier can check anything.
func (v SourceGateVerifier) Configured() bool {
	return v.KeyPath != "" || (v.CertIdentity != "" && v.CertOIDCIssuer != "")
}

// Verify fetches the summary and applies the policy.
func (v SourceGateVerifier) Verify(ctx context.Context, ref string) (bool, string, error) {
	if !v.Configured() {
		return false, "no cosign key or certificate identity configured for the source gate", nil
	}

	run := v.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}
	}

	args := []string{"verify-attestation", "--type", VSAPredicateType}
	if v.AllowHTTP {
		args = append(args, "--allow-http-registry")
	}
	if v.KeyPath != "" {
		args = append(args, "--key", v.KeyPath)
	}
	if v.CertIdentity != "" {
		args = append(args, "--certificate-identity", v.CertIdentity)
	}
	if v.CertOIDCIssuer != "" {
		args = append(args, "--certificate-oidc-issuer", v.CertOIDCIssuer)
	}
	args = append(args, ref)

	out, err := run(ctx, "cosign", args...)
	if err != nil {
		return false, "no verifiable source gate summary: " + lastLine(out), nil
	}

	summaries, err := ParseVSAs(out)
	if err != nil {
		return false, err.Error(), nil
	}

	builtFrom := v.buildCommit(ctx, ref)

	var lastDetail string
	for _, s := range summaries {
		ok, detail := v.check(s, builtFrom)
		if ok {
			return true, detail, nil
		}
		lastDetail = detail
	}
	return false, lastDetail, nil
}

// buildCommit asks the provenance verifier which commit the artifact came
// from, so the summary can be joined to it. Empty when unavailable.
func (v SourceGateVerifier) buildCommit(ctx context.Context, ref string) string {
	if v.Provenance == nil {
		return ""
	}
	statements, err := v.Provenance.statements(ctx, ref)
	if err != nil {
		return ""
	}
	for _, s := range statements {
		if c := s.SourceCommit(); c != "" {
			return c
		}
	}
	return ""
}

func (v SourceGateVerifier) check(s VSAStatement, builtFrom string) (bool, string) {
	verifier := s.Predicate.Verifier.ID

	if len(v.AllowedVerifiers) > 0 && !builderAllowed(verifier, v.AllowedVerifiers) {
		return false, fmt.Sprintf("source verified by %s, which is not an allowed gate", orUnknown(verifier))
	}
	if !s.Passed() {
		return false, fmt.Sprintf("source gate reported %q", orUnknown(s.Predicate.VerificationResult))
	}
	for _, want := range v.RequireLevels {
		if !hasLevel(s.Predicate.VerifiedLevels, want) {
			return false, fmt.Sprintf("source gate did not reach %s (got %v)", want, s.Predicate.VerifiedLevels)
		}
	}

	commit := s.SourceCommit()
	if commit == "" {
		return false, "the source summary names no commit"
	}
	if builtFrom != "" && commit != builtFrom {
		// The attack this exists to stop: a summary for a well-gated commit
		// attached to an artifact built from an ungated one. Both attestations
		// verify perfectly on their own; only the join catches it.
		return false, fmt.Sprintf(
			"the source summary is for %s but the artifact was built from %s",
			short(commit), short(builtFrom))
	}

	detail := fmt.Sprintf("source gated by %s at %s", orUnknown(verifier), short(commit))

	// Every way the check was weakened, not just the first. An operator
	// reading "accepted" needs to know which parts were actually established,
	// and a caveat hidden behind another is one they will never learn about.
	var caveats []string
	if builtFrom == "" {
		caveats = append(caveats, "not joined to the build: no provenance to compare against")
	}
	if len(v.AllowedVerifiers) == 0 {
		caveats = append(caveats, "no allowed-gate policy set")
	}
	if len(caveats) > 0 {
		detail += " (" + strings.Join(caveats, "; ") + ")"
	}
	return true, detail
}

func hasLevel(levels []string, want string) bool {
	for _, l := range levels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// ParseVSAs reads every verification summary out of cosign's output.
func ParseVSAs(out string) ([]VSAStatement, error) {
	var found []VSAStatement
	for _, payload := range dssePayloads(out) {
		var s VSAStatement
		if err := jsonUnmarshal(payload, &s); err != nil {
			continue
		}
		if s.PredicateType == VSAPredicateType {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no source gate summary in the attestation")
	}
	return found, nil
}

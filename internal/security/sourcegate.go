package security

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
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
		// Only if what follows is actually an object id. `git+ssh://git@host/o/r.git`
		// has an @ in it and no commit at all, and taking the tail regardless
		// yields "host/o/r.git" as the commit — which then gets reported in
		// the accepted verdict and, with nothing to join against, accepted.
		if commit := s.Predicate.ResourceURI[i+1:]; isObjectID(commit) {
			return commit
		}
	}
	return ""
}

// isObjectID reports whether s is a full git object id, sha-1 or sha-256.
//
// Full length only: an abbreviated id would make the join between the summary
// and the build provenance a prefix comparison, and a prefix match is not an
// identity.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
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
	// PublicKeys are the source gate's own ed25519 public keys, base64 as
	// `warden key show` prints them.
	//
	// The summary is verified against these directly — not through cosign,
	// and not against the build platform's key. That is the whole point: the
	// build platform only carried the envelope, so a signature it made would
	// attest to the carrier. Checking the gate's own signature means the claim
	// stands on the gate's key and nothing else, which is what lets an auditor
	// follow the chain without trusting the pipeline that produced it.
	PublicKeys []string

	AllowHTTP bool

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
func (v SourceGateVerifier) Configured() bool { return len(v.PublicKeys) > 0 }

// Verify downloads the summary and checks the gate's own signature over it.
func (v SourceGateVerifier) Verify(ctx context.Context, ref string) (bool, string, error) {
	if !v.Configured() {
		return false, "no source gate public keys configured", nil
	}

	run := v.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}
	}

	// `download`, not `verify-attestation`. cosign would check the envelope
	// against a cosign key — the build platform's — and that is not the
	// signature that matters here. RollOps wants the raw envelope so it can
	// check the gate's signature itself.
	args := []string{"download", "attestation"}
	if v.AllowHTTP {
		args = append(args, "--allow-http-registry")
	}
	args = append(args, ref)

	out, err := run(ctx, "cosign", args...)
	if err != nil {
		return false, "no source gate summary on this artifact: " + lastLine(out), nil
	}

	envelopes := ParseEnvelopes(out)
	if len(envelopes) == 0 {
		return false, "no source gate summary on this artifact", nil
	}

	builtFrom := v.buildCommit(ctx, ref)

	var lastDetail string
	for _, e := range envelopes {
		summary, keyID, err := v.authenticate(e)
		if errors.Is(err, errNotASummary) {
			// Some other attestation on the same artifact. Not a failure, and
			// reporting it as one would put an internal skip marker in front
			// of an operator.
			continue
		}
		if err != nil {
			lastDetail = err.Error()
			continue
		}
		ok, detail := v.check(summary, keyID, builtFrom)
		if ok {
			return true, detail, nil
		}
		lastDetail = detail
	}
	if lastDetail == "" {
		lastDetail = "no source gate summary on this artifact"
	}
	return false, lastDetail, nil
}

// authenticate checks the gate's signature over the envelope and returns the
// summary inside it.
//
// A statement is only worth reading once its signature holds, so this refuses
// to hand back a summary it could not authenticate. Trying each configured key
// rather than only the one named in keyid: the keyid is attacker-controlled
// metadata, useful as a hint and not as an authorisation.
func (v SourceGateVerifier) authenticate(e Envelope) (VSAStatement, string, error) {
	payload, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return VSAStatement{}, "", fmt.Errorf("source summary payload is not base64")
	}

	var summary VSAStatement
	if err := jsonUnmarshal(payload, &summary); err != nil {
		return VSAStatement{}, "", fmt.Errorf("source summary is not readable")
	}
	if summary.PredicateType != VSAPredicateType {
		// Some other attestation on the same artifact — an SBOM, the build
		// provenance. Not an error, just not this.
		return VSAStatement{}, "", errNotASummary
	}
	if len(e.Signatures) == 0 {
		return VSAStatement{}, "", fmt.Errorf("the source summary carries no signature")
	}

	message := pae(e.PayloadType, payload)
	for _, encoded := range v.PublicKeys {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(key) != ed25519.PublicKeySize {
			continue
		}
		for _, sig := range e.Signatures {
			raw, err := base64.StdEncoding.DecodeString(sig.Sig)
			if err != nil {
				continue
			}
			if ed25519.Verify(ed25519.PublicKey(key), message, raw) {
				return summary, sig.KeyID, nil
			}
		}
	}
	return VSAStatement{}, "", fmt.Errorf(
		"the source summary is not signed by any configured gate key")
}

// errNotASummary marks an attestation of some other kind, so the caller can
// skip it without reporting it as a failure.
var errNotASummary = errors.New("not a source gate summary")

// pae reconstructs DSSE's pre-authentication encoding.
//
// It must match the signer's byte for byte — the framing is part of what was
// signed, which is what stops the same payload being replayed under a
// different media type.
func pae(payloadType string, payload []byte) []byte {
	return fmt.Appendf(nil, "DSSEv1 %d %s %d %s",
		len(payloadType), payloadType, len(payload), payload)
}

// Envelope is a DSSE envelope as the source gate signed it.
type Envelope struct {
	PayloadType string `json:"payloadType"`
	Payload     string `json:"payload"`
	Signatures  []struct {
		KeyID string `json:"keyid"`
		Sig   string `json:"sig"`
	} `json:"signatures"`
}

// ParseEnvelopes reads every DSSE envelope out of cosign's download output.
func ParseEnvelopes(out string) []Envelope {
	var found []Envelope
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e Envelope
		if err := jsonUnmarshal([]byte(line), &e); err != nil || e.Payload == "" {
			continue
		}
		found = append(found, e)
	}
	return found
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

func (v SourceGateVerifier) check(s VSAStatement, keyID, builtFrom string) (bool, string) {
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

	detail := fmt.Sprintf("source gated by %s at %s, signed by %s",
		orUnknown(verifier), short(commit), orUnknown(keyID))

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

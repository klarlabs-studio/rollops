package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// SLSAPredicateType is the attestation this verifier reads. A build platform
// that attaches something else — an SBOM, a vulnerability scan — is not
// claiming provenance and is not accepted as such.
const SLSAPredicateType = "https://slsa.dev/provenance/v1"

// Statement is the in-toto envelope a provenance attestation carries.
//
// These types are deliberately RollOps' own rather than imported from the
// build tool that produced them. SLSA is a standard, and a CD system that
// could only verify one builder's output would be the wrong shape: this reads
// provenance from kiln, from GitHub's generator, or from anything else that
// emits the spec.
type Statement struct {
	Type          string        `json:"_type"`
	PredicateType string        `json:"predicateType"`
	Subject       []Subject     `json:"subject"`
	Predicate     SLSAPredicate `json:"predicate"`
}

// Subject is what the statement is about.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// SLSAPredicate is the subset of the v1 predicate RollOps makes decisions on.
type SLSAPredicate struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

// BuildDefinition says what was built and from what.
type BuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies"`
	// InternalParameters is read best-effort. Everything in it is the build
	// platform's own extension, so a producer that omits it is not
	// malformed — it just offers less to police.
	InternalParameters InternalParameters `json:"internalParameters"`
}

// InternalParameters carries a builder's extensions. The fields here are
// kiln's; another producer simply leaves them zero.
type InternalParameters struct {
	Isolated   bool       `json:"isolated"`
	SourceGate SourceGate `json:"sourceGate"`
}

// SourceGate is a build platform's record of the source check.
//
// Reproved is the interesting one. A build may legitimately inherit a verdict
// from a signed note instead of re-running the checks; both are gated, but an
// operator may want production to take only artifacts whose checks actually
// ran for that build.
type SourceGate struct {
	Tool     string `json:"tool"`
	Verified bool   `json:"verified"`
	Reproved bool   `json:"reproved"`
	Reason   string `json:"reason"`
}

// ResourceDescriptor points at a build input.
type ResourceDescriptor struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

// RunDetails says who built it.
type RunDetails struct {
	Builder Builder `json:"builder"`
}

// Builder identifies the build platform.
type Builder struct {
	ID string `json:"id"`
}

// SourceCommit returns the commit the artifact was built from.
func (s Statement) SourceCommit() string {
	for _, dep := range s.Predicate.BuildDefinition.ResolvedDependencies {
		if c := dep.Digest["gitCommit"]; c != "" {
			return c
		}
	}
	return ""
}

// ProvenanceVerifier requires an artifact to carry SLSA provenance from a
// builder RollOps trusts.
//
// This is a strictly stronger claim than a signature. `cosign verify` proves
// somebody with the key vouched for these bytes; it says nothing about where
// they came from. An attacker who obtains the signing key can sign anything,
// and a signature-only gate deploys it. Provenance pins the artifact to a
// commit and to the platform that built it, so the same stolen key no longer
// suffices to pass a build off as one from the pipeline.
//
// It is a separate verifier rather than an extension of CosignVerifier so an
// operator can adopt it independently — and so both can run, which is what
// ChainVerifier is for.
type ProvenanceVerifier struct {
	// KeyPath, CertIdentity and CertOIDCIssuer authenticate the attestation,
	// exactly as they do for a signature.
	KeyPath        string
	CertIdentity   string
	CertOIDCIssuer string
	AllowHTTP      bool

	// AllowedBuilders are the builder ids that may vouch for a deployable
	// artifact. Empty means any builder, which is a weak policy and is
	// reported as such rather than silently accepted.
	//
	// A trailing "@" match is on the prefix, so
	// "https://github.com/klarlabs-studio/kiln@" accepts any kiln version.
	AllowedBuilders []string

	// RequireReproved rejects an artifact whose source checks were inherited
	// from a note rather than run during that build. Off by default: the
	// inherited case is legitimate and refusing it would reject most of a
	// healthy pipeline's output.
	RequireReproved bool

	// RequireSourceCommit rejects provenance that names no commit. On by
	// default in effect, because provenance without a source is provenance
	// about nothing.
	AllowMissingSourceCommit bool

	Run func(ctx context.Context, name string, args ...string) (output string, err error)
}

// Configured reports whether this verifier has enough to check anything.
func (p ProvenanceVerifier) Configured() bool {
	return p.KeyPath != "" || (p.CertIdentity != "" && p.CertOIDCIssuer != "")
}

// Verify fetches and inspects the artifact's provenance.
//
// Following the CosignVerifier convention in this package, a failed check is
// (false, detail, nil) rather than an error: it is a verdict about the
// artifact, not a fault in the system, and the gate decides what to do with it.
func (p ProvenanceVerifier) Verify(ctx context.Context, ref string) (bool, string, error) {
	if !p.Configured() {
		return false, "no cosign key or certificate identity configured for provenance", nil
	}

	run := p.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}
	}

	args := []string{"verify-attestation", "--type", SLSAPredicateType}
	if p.AllowHTTP {
		args = append(args, "--allow-http-registry")
	}
	if p.KeyPath != "" {
		args = append(args, "--key", p.KeyPath)
	}
	if p.CertIdentity != "" {
		args = append(args, "--certificate-identity", p.CertIdentity)
	}
	if p.CertOIDCIssuer != "" {
		args = append(args, "--certificate-oidc-issuer", p.CertOIDCIssuer)
	}
	args = append(args, ref)

	out, err := run(ctx, "cosign", args...)
	if err != nil {
		return false, "no verifiable provenance: " + lastLine(out), nil
	}

	statements, err := ParseAttestations(out)
	if err != nil {
		return false, err.Error(), nil
	}

	// Any one satisfying statement is enough. cosign has already established
	// that every envelope it returned was signed by the trusted key, so the
	// question left is whether any of them says something acceptable.
	//
	// Requiring all of them would be wrong in a way that shows up months
	// later: an image accumulates attestations — a rebuild re-attests, a
	// second builder adds its own, an older tool version left one behind — and
	// a single stale entry would block every deploy of an otherwise good
	// artifact.
	var lastDetail string
	for _, stmt := range statements {
		if ok, detail := p.check(stmt); ok {
			return true, detail, nil
		} else {
			lastDetail = detail
		}
	}
	return false, lastDetail, nil
}

// check applies the policy to a statement cosign has already authenticated.
//
// No error return: every outcome here is a verdict about the artifact, not a
// fault in the system, which is the convention the rest of this package
// follows.
func (p ProvenanceVerifier) check(stmt Statement) (bool, string) {
	builder := stmt.Predicate.RunDetails.Builder.ID

	if len(p.AllowedBuilders) == 0 {
		// Authenticated, but by nobody in particular. Deploying on that is a
		// choice an operator should make knowingly, so it is surfaced in the
		// detail rather than passed off as a clean verification.
		return true, fmt.Sprintf("provenance from %s (no allowed-builder policy set)", orUnknown(builder))
	}
	if !builderAllowed(builder, p.AllowedBuilders) {
		return false, fmt.Sprintf("built by %s, which is not an allowed builder", orUnknown(builder))
	}

	commit := stmt.SourceCommit()
	if commit == "" && !p.AllowMissingSourceCommit {
		return false, "provenance names no source commit"
	}

	gate := stmt.Predicate.BuildDefinition.InternalParameters.SourceGate
	if p.RequireReproved && !gate.Reproved {
		return false, fmt.Sprintf(
			"source checks were inherited rather than run for this build (%s)", orUnknown(gate.Reason))
	}

	detail := fmt.Sprintf("built by %s from %s", builder, short(commit))
	if gate.Verified && !gate.Reproved {
		// True and worth saying even when policy permits it: an operator
		// reading a deploy log should see that this artifact inherited its
		// verdict.
		detail += " (source checks inherited)"
	}
	return true, detail
}

// builderAllowed matches an id against the policy, treating a trailing "@" as
// a version-agnostic prefix.
func builderAllowed(id string, allowed []string) bool {
	for _, want := range allowed {
		if strings.HasSuffix(want, "@") {
			if strings.HasPrefix(id, want) {
				return true
			}
			continue
		}
		if id == want {
			return true
		}
	}
	return false
}

// ErrNoProvenance reports output carrying no readable SLSA statement.
var ErrNoProvenance = errors.New("no slsa provenance in the attestation")

// ParseAttestations reads every SLSA build provenance statement out of
// cosign's verify-attestation output.
func ParseAttestations(out string) ([]Statement, error) {
	var found []Statement
	for _, payload := range dssePayloads(out) {
		var stmt Statement
		if err := json.Unmarshal(payload, &stmt); err != nil {
			continue
		}
		if stmt.PredicateType == SLSAPredicateType {
			found = append(found, stmt)
		}
	}
	if len(found) == 0 {
		return nil, ErrNoProvenance
	}
	return found, nil
}

// dssePayloads decodes every envelope cosign printed.
//
// cosign writes one JSON object per line, each a DSSE envelope with a base64
// payload, interleaved with human-readable notes on the same stream. An
// artifact commonly carries several attestations of different types, so this
// returns all the payloads and lets each reader pick out its own.
func dssePayloads(out string) [][]byte {
	var payloads [][]byte
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var env struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		payload, err := decodeDSSEPayload(env.Payload)
		if err != nil {
			continue
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

// jsonUnmarshal keeps the sibling readers from each importing encoding/json
// for one call.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// statements fetches and parses the artifact's build provenance, for callers
// that need the source commit rather than a verdict.
func (p ProvenanceVerifier) statements(ctx context.Context, ref string) ([]Statement, error) {
	run := p.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		}
	}
	args := []string{"verify-attestation", "--type", SLSAPredicateType}
	if p.AllowHTTP {
		args = append(args, "--allow-http-registry")
	}
	if p.KeyPath != "" {
		args = append(args, "--key", p.KeyPath)
	}
	if p.CertIdentity != "" {
		args = append(args, "--certificate-identity", p.CertIdentity)
	}
	if p.CertOIDCIssuer != "" {
		args = append(args, "--certificate-oidc-issuer", p.CertOIDCIssuer)
	}
	args = append(args, ref)

	out, err := run(ctx, "cosign", args...)
	if err != nil {
		return nil, fmt.Errorf("provenance unavailable: %s", lastLine(out))
	}
	return ParseAttestations(out)
}

// decodeDSSEPayload accepts both base64 alphabets. The spec says standard, and
// tools in this space have shipped URL-safe; refusing a valid attestation over
// an encoding detail helps nobody.
func decodeDSSEPayload(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if data, err := enc.DecodeString(s); err == nil {
			return data, nil
		}
	}
	return nil, errors.New("payload is not base64")
}

// ChainVerifier requires every verifier to pass.
//
// Signature and provenance answer different questions — "did somebody vouch
// for these bytes" and "where did they come from" — and a deploy gate wants
// both. Failing on the first miss keeps the detail pointed at the first thing
// an operator has to fix.
type ChainVerifier struct {
	Verifiers []ArtifactVerifier
}

// Verify runs each verifier in order.
func (c ChainVerifier) Verify(ctx context.Context, ref string) (bool, string, error) {
	details := make([]string, 0, len(c.Verifiers))
	for _, v := range c.Verifiers {
		ok, detail, err := v.Verify(ctx, ref)
		if err != nil {
			return false, detail, err
		}
		if !ok {
			return false, detail, nil
		}
		details = append(details, detail)
	}
	if len(details) == 0 {
		// An empty chain that returned "verified" would be a gate that checks
		// nothing while reporting success.
		return false, "no verifiers configured", nil
	}
	return true, strings.Join(details, "; "), nil
}

func short(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "an unnamed builder"
	}
	return s
}

// lastLine picks cosign's actionable tail out of a verbose failure.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

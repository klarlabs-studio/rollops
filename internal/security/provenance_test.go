package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kilnFixture is a real predicate emitted by kiln, copied from its published
// examples/provenance.example.json.
//
// This is the contract test between the two products. Kiln regenerates that
// file from its emitting code on every run, so if the shape it publishes ever
// changes, refreshing this fixture is what surfaces it here — rather than a
// deploy gate quietly failing open in production.
func kilnFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "kiln-provenance.json"))
	if err != nil {
		t.Fatalf("read the kiln contract fixture: %v", err)
	}
	return data
}

// envelope wraps a statement the way cosign prints it.
func envelope(t *testing.T, statement []byte) string {
	t.Helper()
	line, err := json.Marshal(map[string]string{
		"payload": base64.StdEncoding.EncodeToString(statement),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line) + "\n"
}

// cosignSaying builds a runner that returns the given output.
func cosignSaying(out string, err error) func(context.Context, string, ...string) (string, error) {
	return func(context.Context, string, ...string) (string, error) { return out, err }
}

const kilnBuilder = "https://github.com/klarlabs-studio/kiln@"

func verifier(t *testing.T, out string) ProvenanceVerifier {
	t.Helper()
	return ProvenanceVerifier{
		KeyPath:         "cosign.pub",
		AllowedBuilders: []string{kilnBuilder},
		Run:             cosignSaying(out, nil),
	}
}

func TestAKilnArtifactVerifies(t *testing.T) {
	p := verifier(t, envelope(t, kilnFixture(t)))

	ok, detail, err := p.Verify(context.Background(), "ghcr.io/x/y@sha256:aaa")

	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatalf("a genuine kiln artifact was rejected: %s", detail)
	}
	// The detail is what lands in a deploy log, so it should name both facts
	// an operator cares about.
	if !strings.Contains(detail, "kiln") || !strings.Contains(detail, "c3f7aca") {
		t.Errorf("detail = %q, want the builder and the source commit", detail)
	}
}

func TestTheContractFieldsAreStillWhereWeReadThem(t *testing.T) {
	var stmt Statement
	if err := json.Unmarshal(kilnFixture(t), &stmt); err != nil {
		t.Fatalf("the fixture no longer parses: %v", err)
	}

	// Each of these is a field RollOps makes a deploy decision on. If kiln
	// renames one, this is the test that says so.
	if stmt.PredicateType != SLSAPredicateType {
		t.Errorf("predicateType = %q", stmt.PredicateType)
	}
	if stmt.SourceCommit() == "" {
		t.Error("resolvedDependencies[].digest.gitCommit is gone")
	}
	if stmt.Predicate.RunDetails.Builder.ID == "" {
		t.Error("runDetails.builder.id is gone")
	}
	gate := stmt.Predicate.BuildDefinition.InternalParameters.SourceGate
	if !gate.Verified {
		t.Error("internalParameters.sourceGate.verified is gone or false")
	}
	if len(stmt.Subject) == 0 || stmt.Subject[0].Digest["sha256"] == "" {
		t.Error("subject[].digest.sha256 is gone")
	}
}

func TestAnUntrustedBuilderIsRejected(t *testing.T) {
	stmt := kilnFixture(t)
	forged := strings.Replace(string(stmt),
		"https://github.com/klarlabs-studio/kiln@v0.1.0",
		"https://github.com/attacker/builder@v1", 1)

	p := verifier(t, envelope(t, []byte(forged)))
	ok, detail, err := p.Verify(context.Background(), "ghcr.io/x/y@sha256:aaa")

	// cosign authenticated the signature; anyone holding that key can attest
	// anything. The builder check is what makes the claim mean something.
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("accepted provenance from a builder outside the policy")
	}
	if !strings.Contains(detail, "not an allowed builder") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAVersionAgnosticBuilderPolicy(t *testing.T) {
	// "…kiln@" must match any kiln version, or every release would need a
	// policy change on every deploying cluster.
	p := verifier(t, envelope(t, kilnFixture(t)))
	if ok, detail, _ := p.Verify(context.Background(), "ref"); !ok {
		t.Errorf("prefix policy rejected a kiln build: %s", detail)
	}

	exact := verifier(t, envelope(t, kilnFixture(t)))
	exact.AllowedBuilders = []string{"https://github.com/klarlabs-studio/kiln@v0.1.0"}
	if ok, detail, _ := exact.Verify(context.Background(), "ref"); !ok {
		t.Errorf("exact policy rejected the matching version: %s", detail)
	}

	wrong := verifier(t, envelope(t, kilnFixture(t)))
	wrong.AllowedBuilders = []string{"https://github.com/klarlabs-studio/kiln@v9.9.9"}
	if ok, _, _ := wrong.Verify(context.Background(), "ref"); ok {
		t.Error("exact policy accepted a different version")
	}
}

func TestNoBuilderPolicyIsAcceptedButSaidOutLoud(t *testing.T) {
	p := verifier(t, envelope(t, kilnFixture(t)))
	p.AllowedBuilders = nil

	ok, detail, _ := p.Verify(context.Background(), "ref")

	// Authenticated, but by nobody in particular. That is a weak policy and
	// an operator should see it in the log rather than read a clean pass.
	if !ok {
		t.Errorf("want acceptance, got %s", detail)
	}
	if !strings.Contains(detail, "no allowed-builder policy") {
		t.Errorf("detail = %q, want the weakness surfaced", detail)
	}
}

func TestMissingProvenanceIsRejected(t *testing.T) {
	p := verifier(t, "")
	p.Run = cosignSaying("Error: no matching attestations", errors.New("exit 1"))

	ok, detail, err := p.Verify(context.Background(), "ghcr.io/x/y@sha256:aaa")

	if err != nil {
		t.Fatalf("a missing attestation is a verdict, not a system fault: %v", err)
	}
	if ok || !strings.Contains(detail, "no verifiable provenance") {
		t.Errorf("(%v, %q)", ok, detail)
	}
}

func TestAnSBOMIsNotProvenance(t *testing.T) {
	sbom := []byte(`{"_type":"https://in-toto.io/Statement/v1",
	  "predicateType":"https://spdx.dev/Document","predicate":{}}`)

	p := verifier(t, envelope(t, sbom))
	ok, detail, _ := p.Verify(context.Background(), "ref")

	// A build platform attaching an SBOM is not claiming provenance.
	if ok {
		t.Error("accepted an SBOM as build provenance")
	}
	if !strings.Contains(detail, "no slsa provenance") {
		t.Errorf("detail = %q", detail)
	}
}

func TestProvenanceIsFoundAmongOtherAttestations(t *testing.T) {
	sbom := []byte(`{"_type":"https://in-toto.io/Statement/v1",
	  "predicateType":"https://spdx.dev/Document","predicate":{}}`)

	// An image commonly carries several. The reader must select, not assume
	// the first line.
	p := verifier(t, envelope(t, sbom)+envelope(t, kilnFixture(t)))
	if ok, detail, _ := p.Verify(context.Background(), "ref"); !ok {
		t.Errorf("did not find the provenance among several attestations: %s", detail)
	}
}

func TestInheritedChecksAreReportedAndOptionallyRefused(t *testing.T) {
	inherited := strings.Replace(string(kilnFixture(t)), `"reproved": true`, `"reproved": false`, 1)
	if inherited == string(kilnFixture(t)) {
		t.Fatal("the fixture no longer contains sourceGate.reproved")
	}

	// Permitted by default: inheriting a signed verdict is legitimate, and
	// refusing it would reject most of a healthy pipeline's output.
	p := verifier(t, envelope(t, []byte(inherited)))
	ok, detail, _ := p.Verify(context.Background(), "ref")
	if !ok {
		t.Errorf("an inherited verdict was rejected by default: %s", detail)
	}
	if !strings.Contains(detail, "inherited") {
		t.Errorf("detail = %q, want the inheritance visible in the deploy log", detail)
	}

	// An operator can require checks to have run for this artifact.
	strict := verifier(t, envelope(t, []byte(inherited)))
	strict.RequireReproved = true
	if ok, _, _ := strict.Verify(context.Background(), "ref"); ok {
		t.Error("RequireReproved accepted an inherited verdict")
	}
}

func TestProvenanceWithoutASourceCommitIsRejected(t *testing.T) {
	noSource := strings.Replace(string(kilnFixture(t)),
		`"gitCommit": "c3f7aca23fa4bfa8d65b3741f46c509713cd618e"`, `"gitCommit": ""`, 1)

	p := verifier(t, envelope(t, []byte(noSource)))
	ok, detail, _ := p.Verify(context.Background(), "ref")

	// Provenance that names no source is provenance about nothing.
	if ok {
		t.Errorf("accepted sourceless provenance: %s", detail)
	}
	if !strings.Contains(detail, "no source commit") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAnUnconfiguredVerifierRefuses(t *testing.T) {
	p := ProvenanceVerifier{AllowedBuilders: []string{kilnBuilder}}

	ok, detail, _ := p.Verify(context.Background(), "ref")

	// Nothing to authenticate the attestation with. Passing would be a gate
	// that checks nothing while reporting success.
	if ok || !strings.Contains(detail, "no cosign key") {
		t.Errorf("(%v, %q)", ok, detail)
	}
	if p.Configured() {
		t.Error("Configured = true with neither key nor identity")
	}
}

func TestURLSafePayloadIsAccepted(t *testing.T) {
	line, _ := json.Marshal(map[string]string{
		"payload": base64.RawURLEncoding.EncodeToString(kilnFixture(t)),
	})
	p := verifier(t, string(line))

	if ok, detail, _ := p.Verify(context.Background(), "ref"); !ok {
		t.Errorf("refused a valid attestation over an encoding detail: %s", detail)
	}
}

func TestChainRequiresEveryVerifier(t *testing.T) {
	pass := stubVerifier{ok: true, detail: "signature ok"}
	fail := stubVerifier{ok: false, detail: "no provenance"}

	ok, detail, err := ChainVerifier{Verifiers: []ArtifactVerifier{pass, fail}}.
		Verify(context.Background(), "ref")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("the chain passed with a failing member")
	}
	// The detail should point at the first thing to fix.
	if detail != "no provenance" {
		t.Errorf("detail = %q", detail)
	}

	both, detail, _ := ChainVerifier{Verifiers: []ArtifactVerifier{pass, pass}}.
		Verify(context.Background(), "ref")
	if !both || !strings.Contains(detail, "signature ok") {
		t.Errorf("(%v, %q)", both, detail)
	}
}

func TestAnEmptyChainDoesNotPass(t *testing.T) {
	ok, detail, _ := ChainVerifier{}.Verify(context.Background(), "ref")

	// A gate that checks nothing must not report success.
	if ok || !strings.Contains(detail, "no verifiers") {
		t.Errorf("(%v, %q)", ok, detail)
	}
}

func TestTheChainEnforcesThroughTheGate(t *testing.T) {
	gate := ArtifactGate{
		Mode: VerifyEnforce,
		Verifier: ChainVerifier{Verifiers: []ArtifactVerifier{
			stubVerifier{ok: true, detail: "signature ok"},
			stubVerifier{ok: false, detail: "built by someone else"},
		}},
	}

	err := gate.Check(context.Background(), "ghcr.io/x/y@sha256:aaa")

	var unverified ErrUnverifiedArtifact
	if !errors.As(err, &unverified) {
		t.Fatalf("err = %v, want the deploy blocked", err)
	}
	if !strings.Contains(unverified.Detail, "built by someone else") {
		t.Errorf("Detail = %q", unverified.Detail)
	}
}

type stubVerifier struct {
	ok     bool
	detail string
}

func (s stubVerifier) Verify(context.Context, string) (bool, string, error) {
	return s.ok, s.detail, nil
}

func TestAStaleAttestationDoesNotMaskAGoodOne(t *testing.T) {
	// The exact shape kiln emitted before it learned that cosign's --predicate
	// wants the predicate body, not a whole statement: correctly signed,
	// correctly typed, and empty where a consumer reads.
	malformed := []byte(`{"_type":"https://in-toto.io/Statement/v0.1",
	  "predicateType":"https://slsa.dev/provenance/v1",
	  "predicate":{"_type":"https://in-toto.io/Statement/v1","predicate":{}}}`)

	p := verifier(t, envelope(t, malformed)+envelope(t, kilnFixture(t)))
	ok, detail, err := p.Verify(context.Background(), "ref")

	if err != nil {
		t.Fatal(err)
	}
	// An image accumulates attestations — a rebuild re-attests, a second
	// builder adds its own. One bad entry must not block every deploy.
	if !ok {
		t.Fatalf("a stale attestation masked a good one: %s", detail)
	}
	if !strings.Contains(detail, "kiln") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAllAttestationsFailingReportsTheLastReason(t *testing.T) {
	forged := strings.Replace(string(kilnFixture(t)),
		"https://github.com/klarlabs-studio/kiln@v0.1.0",
		"https://github.com/attacker/builder@v1", 1)

	p := verifier(t, envelope(t, []byte(forged)))
	ok, detail, _ := p.Verify(context.Background(), "ref")

	if ok {
		t.Fatal("accepted an artifact no attestation vouched for")
	}
	// The operator needs to know why, not just that.
	if !strings.Contains(detail, "not an allowed builder") {
		t.Errorf("detail = %q", detail)
	}
}

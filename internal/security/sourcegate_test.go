package security

import (
	"context"
	"strings"
	"testing"
)

const commitSHA = "c3f7aca23fa4bfa8d65b3741f46c509713cd618e"

// wardenSummary is what `warden attest --predicate vsa` emits, as kiln
// republishes it onto an artifact — subject rewritten to the image by cosign,
// so the commit survives only in resourceUri.
const wardenSummary = `{"predicateType":"https://slsa.dev/verification_summary/v1",
 "subject":[{"name":"ghcr.io/x/y","digest":{"sha256":"aaa"}}],
 "predicate":{"verifier":{"id":"https://warden.klarlabs.de","version":{"warden":"0.28.0"}},
  "timeVerified":"2026-08-17T21:26:51Z",
  "resourceUri":"git+ssh://git@github.com/o/r.git@` + commitSHA + `",
  "policy":{"uri":"git+ssh://git@github.com/o/r.git@` + commitSHA + `#.warden.yaml"},
  "verificationResult":"PASSED","verifiedLevels":["WARDEN_SOURCE_GATED","WARDEN_SOURCE_SIGNED"]}}`

func sourceGate(t *testing.T, out string) SourceGateVerifier {
	t.Helper()
	return SourceGateVerifier{
		KeyPath:          "cosign.pub",
		AllowedVerifiers: []string{"https://warden.klarlabs.de"},
		Run:              cosignSaying(out, nil),
	}
}

func TestWardensSummaryIsAccepted(t *testing.T) {
	v := sourceGate(t, envelope(t, []byte(wardenSummary)))

	ok, detail, err := v.Verify(context.Background(), "ghcr.io/x/y@sha256:aaa")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("rejected a passing warden summary: %s", detail)
	}
	// The verdict names its verifier, which is the whole point of carrying
	// warden's statement rather than a build tool's summary of it.
	if !strings.Contains(detail, "warden.klarlabs.de") {
		t.Errorf("detail = %q, want the gate named", detail)
	}
}

func TestAnUntrustedGateIsRejected(t *testing.T) {
	forged := strings.Replace(wardenSummary, "https://warden.klarlabs.de", "https://attacker.example", 1)

	v := sourceGate(t, envelope(t, []byte(forged)))
	ok, detail, _ := v.Verify(context.Background(), "ref")

	if ok {
		t.Error("accepted a summary from an unlisted gate")
	}
	if !strings.Contains(detail, "not an allowed gate") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAFailedSourceVerdictIsRejected(t *testing.T) {
	failed := strings.Replace(wardenSummary, `"PASSED"`, `"FAILED"`, 1)

	v := sourceGate(t, envelope(t, []byte(failed)))
	ok, detail, _ := v.Verify(context.Background(), "ref")

	if ok {
		t.Error("deployed something whose source gate failed")
	}
	if !strings.Contains(detail, "FAILED") {
		t.Errorf("detail = %q", detail)
	}
}

func TestRequiredLevelsAreEnforced(t *testing.T) {
	v := sourceGate(t, envelope(t, []byte(wardenSummary)))
	v.RequireLevels = []string{"WARDEN_SOURCE_SIGNED"}
	if ok, detail, _ := v.Verify(context.Background(), "ref"); !ok {
		t.Errorf("a summary claiming the level was rejected: %s", detail)
	}

	unsigned := strings.Replace(wardenSummary,
		`["WARDEN_SOURCE_GATED","WARDEN_SOURCE_SIGNED"]`, `["WARDEN_SOURCE_GATED"]`, 1)
	strict := sourceGate(t, envelope(t, []byte(unsigned)))
	strict.RequireLevels = []string{"WARDEN_SOURCE_SIGNED"}

	// "The note existed" and "the note was signed by someone we trust" are
	// different assurances, and an operator can insist on the second.
	ok, detail, _ := strict.Verify(context.Background(), "ref")
	if ok {
		t.Error("accepted a gate that never reached the required level")
	}
	if !strings.Contains(detail, "WARDEN_SOURCE_SIGNED") {
		t.Errorf("detail = %q", detail)
	}
}

// TestTheSummaryMustBeAboutTheCommitThatWasBuilt is the attack the join
// exists to stop: a summary for a well-gated commit attached to an artifact
// built from an ungated one. Both attestations verify perfectly alone.
func TestTheSummaryMustBeAboutTheCommitThatWasBuilt(t *testing.T) {
	provenance := ProvenanceVerifier{
		KeyPath:         "cosign.pub",
		AllowedBuilders: []string{kilnBuilder},
		// The artifact was built from a different commit than the one warden
		// vouched for.
		Run: cosignSaying(envelope(t, kilnFixture(t)), nil),
	}
	otherCommit := strings.ReplaceAll(wardenSummary, commitSHA, "0000000000000000000000000000000000000000")

	v := sourceGate(t, envelope(t, []byte(otherCommit)))
	v.Provenance = &provenance

	ok, detail, _ := v.Verify(context.Background(), "ref")

	if ok {
		t.Fatal("accepted a source summary for a commit the artifact was not built from")
	}
	if !strings.Contains(detail, "but the artifact was built from") {
		t.Errorf("detail = %q, want the mismatch spelled out", detail)
	}
}

func TestTheJoinPassesWhenTheCommitsAgree(t *testing.T) {
	provenance := ProvenanceVerifier{
		KeyPath:         "cosign.pub",
		AllowedBuilders: []string{kilnBuilder},
		Run:             cosignSaying(envelope(t, kilnFixture(t)), nil),
	}

	v := sourceGate(t, envelope(t, []byte(wardenSummary)))
	v.Provenance = &provenance

	ok, detail, err := v.Verify(context.Background(), "ref")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("rejected a correctly joined chain: %s", detail)
	}
	if strings.Contains(detail, "not joined") {
		t.Errorf("detail = %q, want the join reported as done", detail)
	}
}

func TestWithoutProvenanceTheJoinIsSkippedAndSaidSoOutLoud(t *testing.T) {
	v := sourceGate(t, envelope(t, []byte(wardenSummary)))

	ok, detail, _ := v.Verify(context.Background(), "ref")

	// Weaker, and the operator should be able to see that from the log rather
	// than assume the chain was closed.
	if !ok {
		t.Fatalf("want acceptance, got %s", detail)
	}
	if !strings.Contains(detail, "not joined to the build") {
		t.Errorf("detail = %q, want the weakness surfaced", detail)
	}
}

func TestAMissingSummaryIsRejected(t *testing.T) {
	v := sourceGate(t, envelope(t, kilnFixture(t)))

	// Build provenance is not a source verdict; an artifact carrying only the
	// first has not been shown to come from gated source.
	ok, detail, _ := v.Verify(context.Background(), "ref")
	if ok {
		t.Error("build provenance was accepted as a source gate summary")
	}
	if !strings.Contains(detail, "no source gate summary") {
		t.Errorf("detail = %q", detail)
	}
}

func TestNoVerifierPolicyIsAcceptedButFlagged(t *testing.T) {
	v := sourceGate(t, envelope(t, []byte(wardenSummary)))
	v.AllowedVerifiers = nil

	ok, detail, _ := v.Verify(context.Background(), "ref")

	if !ok {
		t.Fatalf("want acceptance, got %s", detail)
	}
	if !strings.Contains(detail, "no allowed-gate policy") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAnUnconfiguredSourceGateRefuses(t *testing.T) {
	v := SourceGateVerifier{AllowedVerifiers: []string{"https://warden.klarlabs.de"}}

	if ok, detail, _ := v.Verify(context.Background(), "ref"); ok || !strings.Contains(detail, "no cosign key") {
		t.Errorf("(%v, %q)", ok, detail)
	}
}

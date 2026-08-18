package security

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const commitSHA = "c3f7aca23fa4bfa8d65b3741f46c509713cd618e"

// gateKey is a stand-in for the source gate's signing key. Tests sign
// envelopes with it and configure the verifier with its public half, which is
// exactly the arrangement in production: the gate signs, RollOps holds the
// public key, and nothing in between is trusted.
var gateKey = func() (ed25519.PrivateKey, string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

// signedSummary builds the envelope a source gate emits, signed with priv.
func signedSummary(t *testing.T, priv ed25519.PrivateKey, commit, verdict string, levels []string) string {
	t.Helper()
	stmt := map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []any{map[string]any{
			"name": "git+commit", "digest": map[string]string{"gitCommit": commit},
		}},
		"predicateType": VSAPredicateType,
		"predicate": map[string]any{
			"verifier":           map[string]any{"id": "https://warden.klarlabs.de"},
			"timeVerified":       "2026-08-17T21:26:51Z",
			"resourceUri":        "git+ssh://git@github.com/o/r.git@" + commit,
			"policy":             map[string]any{"uri": "git+ssh://git@github.com/o/r.git@" + commit + "#.warden.yaml"},
			"verificationResult": verdict,
			"verifiedLevels":     levels,
		},
	}
	return signedStatement(t, priv, stmt)
}

// signedStatement signs an arbitrary in-toto statement, for the cases that
// need a shape signedSummary does not produce.
func signedStatement(t *testing.T, priv ed25519.PrivateKey, stmt any) string {
	t.Helper()
	payload, err := json.Marshal(stmt)
	if err != nil {
		t.Fatal(err)
	}
	const payloadType = "application/vnd.in-toto+json"
	sig := ed25519.Sign(priv, pae(payloadType, payload))

	env, err := json.Marshal(map[string]any{
		"payloadType": payloadType,
		"payload":     base64.StdEncoding.EncodeToString(payload),
		"signatures": []any{map[string]string{
			"keyid": "139e6eb9e2611c76",
			"sig":   base64.StdEncoding.EncodeToString(sig),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(env) + "\n"
}

var defaultLevels = []string{"WARDEN_SOURCE_GATED", "WARDEN_SOURCE_SIGNED"}

// gate wires a verifier holding the gate's public key over the given output.
func gate(t *testing.T, pub, out string) SourceGateVerifier {
	t.Helper()
	return SourceGateVerifier{
		PublicKeys:       []string{pub},
		AllowedVerifiers: []string{"https://warden.klarlabs.de"},
		Run:              cosignSaying(out, nil),
	}
}

func TestWardensSummaryIsAccepted(t *testing.T) {
	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))

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
	priv, pub := gateKey()
	out := signedSummary(t, priv, commitSHA, "PASSED", defaultLevels)

	v := gate(t, pub, out)
	v.AllowedVerifiers = []string{"https://some-other-gate.example"}
	ok, detail, _ := v.Verify(context.Background(), "ref")

	if ok {
		t.Error("accepted a summary from an unlisted gate")
	}
	if !strings.Contains(detail, "not an allowed gate") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAFailedSourceVerdictIsRejected(t *testing.T) {
	priv, pub := gateKey()

	v := gate(t, pub, signedSummary(t, priv, commitSHA, "FAILED", defaultLevels))
	ok, detail, _ := v.Verify(context.Background(), "ref")

	if ok {
		t.Error("deployed something whose source gate failed")
	}
	if !strings.Contains(detail, "FAILED") {
		t.Errorf("detail = %q", detail)
	}
}

func TestRequiredLevelsAreEnforced(t *testing.T) {
	priv, pub := gateKey()

	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))
	v.RequireLevels = []string{"WARDEN_SOURCE_SIGNED"}
	if ok, detail, _ := v.Verify(context.Background(), "ref"); !ok {
		t.Errorf("a summary claiming the level was rejected: %s", detail)
	}

	strict := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", []string{"WARDEN_SOURCE_GATED"}))
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
	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, "0000000000000000000000000000000000000000", "PASSED", defaultLevels))
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

	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))
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
	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))

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
	_, pub := gateKey()
	v := gate(t, pub, envelope(t, kilnFixture(t)))

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
	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))
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

	// No public key means nothing to check the signature against, and
	// accepting anyway would mean trusting whoever carried the envelope.
	if ok, detail, _ := v.Verify(context.Background(), "ref"); ok || !strings.Contains(detail, "public keys") {
		t.Errorf("(%v, %q)", ok, detail)
	}
}

// TestTheGatesOwnSignatureIsWhatIsChecked is the property the whole
// arrangement exists for: the summary is authenticated against the gate's key,
// so a carrier that re-signed it — or forged one outright — does not pass.
func TestTheGatesOwnSignatureIsWhatIsChecked(t *testing.T) {
	priv, pub := gateKey()
	imposterPriv, _ := gateKey()

	genuine := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))
	if ok, detail, _ := genuine.Verify(context.Background(), "ref"); !ok {
		t.Fatalf("the gate's own signature was rejected: %s", detail)
	}

	// Same statement, signed by somebody else — a build platform that decided
	// to vouch for the gate's verdict on its behalf.
	forged := gate(t, pub, signedSummary(t, imposterPriv, commitSHA, "PASSED", defaultLevels))
	ok, detail, _ := forged.Verify(context.Background(), "ref")
	if ok {
		t.Error("accepted a summary the gate did not sign")
	}
	if !strings.Contains(detail, "not signed by any configured gate key") {
		t.Errorf("detail = %q", detail)
	}
}

func TestATamperedVerdictFailsTheSignature(t *testing.T) {
	priv, pub := gateKey()
	out := signedSummary(t, priv, commitSHA, "FAILED", defaultLevels)

	// Rewrite the verdict inside the signed payload and re-encode it, leaving
	// the signature untouched — the shape a forgery actually takes.
	var env map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(env["payload"].(string))
	env["payload"] = base64.StdEncoding.EncodeToString(
		[]byte(strings.Replace(string(payload), `"FAILED"`, `"PASSED"`, 1)))
	tampered, _ := json.Marshal(env)

	v := gate(t, pub, string(tampered))
	ok, detail, _ := v.Verify(context.Background(), "ref")

	if ok {
		t.Error("a rewritten verdict was accepted")
	}
	if !strings.Contains(detail, "not signed by any configured gate key") {
		t.Errorf("detail = %q", detail)
	}
}

func TestTheKeyIDIsAHintNotAnAuthorisation(t *testing.T) {
	priv, pub := gateKey()
	out := signedSummary(t, priv, commitSHA, "PASSED", defaultLevels)
	// An attacker controls the keyid; it must not decide anything. Renaming it
	// leaves a genuine signature genuine.
	renamed := strings.Replace(out, `"keyid":"139e6eb9e2611c76"`, `"keyid":"not-a-real-key"`, 1)

	v := gate(t, pub, renamed)
	if ok, detail, _ := v.Verify(context.Background(), "ref"); !ok {
		t.Errorf("a valid signature was rejected over its label: %s", detail)
	}
}

func TestCosignIsNotAskedToVerifyTheSummary(t *testing.T) {
	priv, pub := gateKey()
	var invoked []string
	v := SourceGateVerifier{
		PublicKeys:       []string{pub},
		AllowedVerifiers: []string{"https://warden.klarlabs.de"},
		Run: func(_ context.Context, _ string, args ...string) (string, error) {
			invoked = args
			return signedSummary(t, priv, commitSHA, "PASSED", defaultLevels), nil
		},
	}

	if _, detail, _ := v.Verify(context.Background(), "ref"); detail == "" {
		t.Fatal("no verdict")
	}

	// cosign is used to fetch the envelope, not to judge it: verifying with a
	// cosign key would check the carrier's signature, which is the one that
	// does not matter.
	if len(invoked) == 0 || invoked[0] != "download" {
		t.Errorf("cosign args = %v, want a download", invoked)
	}
	for _, a := range invoked {
		if a == "verify-attestation" || a == "--key" {
			t.Errorf("the summary was handed to cosign to verify: %v", invoked)
		}
	}
}

func TestCosignsReasonForNotFindingASummaryIsPassedOn(t *testing.T) {
	_, pub := gateKey()
	v := gate(t, pub, "")
	v.Run = cosignSaying("Error: no matching attestations\nmain.go:74\nno attestations found", errors.New("exit 1"))

	ok, detail, err := v.Verify(context.Background(), "ref")
	if err != nil {
		// A registry that cannot be reached is not a policy verdict, but it is
		// also not RollOps' error to raise: the gate simply has not been shown
		// what it needs, and the deploy stops on the verdict.
		t.Fatal(err)
	}
	if ok {
		t.Fatal("accepted an artifact whose attestations could not be fetched")
	}
	if !strings.Contains(detail, "no attestations found") {
		t.Errorf("detail = %q, want cosign's own reason", detail)
	}
}

func TestNoiseAroundTheEnvelopeIsIgnored(t *testing.T) {
	priv, pub := gateKey()

	// cosign interleaves human-readable notes with the JSON, an artifact
	// carries attestations of other kinds, and a truncated line is always
	// possible. None of that may stop the real envelope being found.
	out := strings.Join([]string{
		"Verification for ghcr.io/x/y@sha256:aaa --",
		`{"payloadType":"application/vnd.in-toto+json","payload":`,
		`{"payloadType":"application/vnd.in-toto+json"}`,
		strings.TrimSpace(signedSummary(t, priv, commitSHA, "PASSED", defaultLevels)),
	}, "\n")

	if ok, detail, _ := gate(t, pub, out).Verify(context.Background(), "ref"); !ok {
		t.Errorf("the genuine envelope was lost in the noise: %s", detail)
	}
}

func TestAnUnsignedSummaryIsRefused(t *testing.T) {
	priv, pub := gateKey()
	out := strings.Replace(
		signedSummary(t, priv, commitSHA, "PASSED", defaultLevels),
		`"signatures":[`, `"signatures":[],"ignored":[`, 1)

	// An envelope with the signatures stripped off is a bare assertion. It
	// must not be read as a verdict just because its payload parses.
	ok, detail, _ := gate(t, pub, out).Verify(context.Background(), "ref")
	if ok {
		t.Error("accepted a summary nobody signed")
	}
	if !strings.Contains(detail, "carries no signature") {
		t.Errorf("detail = %q", detail)
	}
}

func TestAnUnreadablePayloadIsRefused(t *testing.T) {
	_, pub := gateKey()

	for name, payload := range map[string]string{
		"not base64": "!!!!not-base64!!!!",
		"not json":   base64.StdEncoding.EncodeToString([]byte("a source gate said so")),
	} {
		out := `{"payloadType":"application/vnd.in-toto+json","payload":"` + payload +
			`","signatures":[{"keyid":"x","sig":"AA=="}]}`

		ok, detail, _ := gate(t, pub, out).Verify(context.Background(), "ref")
		if ok {
			t.Errorf("%s: accepted an unreadable payload", name)
		}
		if detail == "" {
			t.Errorf("%s: refused without saying why", name)
		}
	}
}

func TestAnUnusableConfiguredKeyDoesNotBlockAGoodOne(t *testing.T) {
	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))

	// Rosters accumulate: a retired key, a copy-paste with a newline in it, a
	// line that was never a key at all. One bad entry must not deny a deploy
	// that a good entry authorises — nor may it be treated as authorising one.
	v.PublicKeys = []string{"not-base64-at-all", base64.StdEncoding.EncodeToString([]byte("short")), "\n" + pub + "\n"}

	if ok, detail, _ := v.Verify(context.Background(), "ref"); !ok {
		t.Errorf("a valid signature was rejected because of unrelated roster entries: %s", detail)
	}
}

func TestAJunkSignatureAlongsideTheRealOneIsIgnored(t *testing.T) {
	priv, pub := gateKey()
	out := strings.Replace(
		signedSummary(t, priv, commitSHA, "PASSED", defaultLevels),
		`"signatures":[`, `"signatures":[{"keyid":"junk","sig":"%%%not-base64%%%"},`, 1)

	// A DSSE envelope may carry several signatures and an attacker can add
	// one. Finding one that verifies is what matters; failing to decode a
	// neighbour is not a reason to refuse.
	if ok, detail, _ := gate(t, pub, out).Verify(context.Background(), "ref"); !ok {
		t.Errorf("a genuine signature was rejected because of a junk sibling: %s", detail)
	}
}

func TestASummaryNamingNoCommitIsRefused(t *testing.T) {
	priv, pub := gateKey()
	out := signedStatement(t, priv, map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		// cosign rewrites the subject to the artifact when the envelope is
		// attached, so a summary whose resourceUri carries no commit either
		// leaves nothing to join against.
		"subject": []any{map[string]any{
			"name": "ghcr.io/x/y", "digest": map[string]string{"sha256": "aaa"},
		}},
		"predicateType": VSAPredicateType,
		"predicate": map[string]any{
			"verifier":           map[string]any{"id": "https://warden.klarlabs.de"},
			"resourceUri":        "git+ssh://git@github.com/o/r.git",
			"verificationResult": "PASSED",
			"verifiedLevels":     defaultLevels,
		},
	})

	ok, detail, _ := gate(t, pub, out).Verify(context.Background(), "ref")
	if ok {
		t.Error("accepted a verdict about nothing in particular")
	}
	if !strings.Contains(detail, "names no commit") {
		t.Errorf("detail = %q", detail)
	}
}

func TestTheSubjectIsPreferredToTheResourceURI(t *testing.T) {
	priv, pub := gateKey()
	out := signedStatement(t, priv, map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []any{map[string]any{
			"name": "git+commit", "digest": map[string]string{"gitCommit": commitSHA},
		}},
		"predicateType": VSAPredicateType,
		"predicate": map[string]any{
			"verifier": map[string]any{"id": "https://warden.klarlabs.de"},
			// Disagreeing with the subject. The subject is the statement's own
			// idea of what it is about; resourceUri is the fallback for when
			// cosign has overwritten it.
			"resourceUri":        "git+ssh://git@github.com/o/r.git@0000000000000000000000000000000000000000",
			"verificationResult": "PASSED",
			"verifiedLevels":     defaultLevels,
		},
	})

	ok, detail, _ := gate(t, pub, out).Verify(context.Background(), "ref")
	if !ok {
		t.Fatalf("rejected: %s", detail)
	}
	if !strings.Contains(detail, short(commitSHA)) {
		t.Errorf("detail = %q, want the subject's commit", detail)
	}
}

func TestTheJoinIsSkippedWhenTheProvenanceCannotBeRead(t *testing.T) {
	priv, pub := gateKey()
	v := gate(t, pub, signedSummary(t, priv, commitSHA, "PASSED", defaultLevels))
	v.Provenance = &ProvenanceVerifier{
		KeyPath: "cosign.pub",
		Run:     cosignSaying("Error: no matching attestations", errors.New("exit 1")),
	}

	// Configured but unreadable is not the same as absent, and it must not
	// silently pass as a closed chain: the summary stands on its own and the
	// operator is told the join did not happen.
	ok, detail, _ := v.Verify(context.Background(), "ref")
	if !ok {
		t.Fatalf("want the summary accepted on its own terms, got %s", detail)
	}
	if !strings.Contains(detail, "not joined to the build") {
		t.Errorf("detail = %q, want the missing join surfaced", detail)
	}
}

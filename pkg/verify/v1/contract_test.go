package verifyv1_test

import (
	"context"
	"testing"

	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// silent is a verifier that answers without ever setting a verdict — the bug
// this contract's zero value is chosen to catch.
type silent struct{}

func (silent) Metadata() verifyv1.VerifierMetadata {
	return verifyv1.VerifierMetadata{Kind: "silent", Name: "test", Version: "v1"}
}

func (silent) Verify(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
	return verifyv1.VerificationResult{}, nil
}

var _ verifyv1.Verifier = silent{}

// TestAVerifierThatSaidNothingDidNotPass keeps the permissive answer off the
// default path. A check that returns its zero value has measured nothing, and
// the contract has to report that rather than let an empty struct promote.
func TestAVerifierThatSaidNothingDidNotPass(t *testing.T) {
	res, err := silent{}.Verify(context.Background(), verifyv1.VerificationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := verifyv1.Combine(res.Verdict); got == verifyv1.VerdictPass {
		t.Fatal("a result with no verdict set was reported as a pass")
	}
}

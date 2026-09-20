package grpcapi

// This one test is inside the package rather than beside it, because the thing
// it checks cannot be seen from outside. Status falls back to INTERNAL for a
// code it has no entry for — which is the right answer to give a caller and the
// wrong one to give a test, since a code nobody decided about would pass an
// external check by looking exactly like a code somebody decided was internal.
// So it reads the table.

import (
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
)

func TestEveryCodeHasADecidedGRPCStatus(t *testing.T) {
	t.Parallel()

	for _, c := range apierr.Codes() {
		if _, decided := grpcStatus[c]; !decided {
			t.Errorf("%s reaches a caller as INTERNAL because nobody chose a gRPC status for it", c)
		}
	}
}

func TestTheTableNamesNoCodeThatDoesNotExist(t *testing.T) {
	t.Parallel()

	known := make(map[apierr.Code]bool, len(apierr.Codes()))
	for _, c := range apierr.Codes() {
		known[c] = true
	}
	for c := range grpcStatus {
		if !known[c] {
			t.Errorf("the table maps %q, which apierr.Codes() does not list", c)
		}
	}
}

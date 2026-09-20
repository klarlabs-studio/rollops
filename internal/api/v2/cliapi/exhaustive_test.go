package cliapi

// Inside the package for the reason grpcapi's equivalent gives: Exit falls back
// to the system exit for a code it has no entry for, which is the right answer
// to give an operator and the wrong one to give a test — a code nobody decided
// about would pass an external check by looking exactly like a code somebody
// decided was a system failure.

import (
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
)

func TestEveryCodeHasADecidedExit(t *testing.T) {
	t.Parallel()

	for _, c := range apierr.Codes() {
		if _, decided := exitCode[c]; !decided {
			t.Errorf("%s exits as a system failure because nobody chose an exit code for it", c)
		}
	}
}

func TestTheTableNamesNoCodeThatDoesNotExist(t *testing.T) {
	t.Parallel()

	known := make(map[apierr.Code]bool, len(apierr.Codes()))
	for _, c := range apierr.Codes() {
		known[c] = true
	}
	for c := range exitCode {
		if !known[c] {
			t.Errorf("the table maps %q, which apierr.Codes() does not list", c)
		}
	}
}

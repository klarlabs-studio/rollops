package ssh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellQuote wrapped values in single quotes without escaping the ones already inside,
// so a value containing an apostrophe closed the quoting and the remainder was
// interpreted by the remote shell.
//
// It was justified as "paths are operator-controlled config, not end-user input". A
// target spec comes from the repository's own config, and Rollops documents a
// multi-tenant model in which exactly that config is untrusted (see
// internal/security/confine.go). It is also a plain correctness bug for a trusted
// operator: /home/o'brien/app is a legitimate path.

// runThroughShell executes a command the way the remote host would, so these tests
// assert what the shell does with the quoting rather than what the string looks like.
// A test comparing quoted strings would have accepted the broken version too.
func runThroughShell(t *testing.T, command string) (string, error) {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", command).CombinedOutput()
	return string(out), err
}

// The injection the missing escape allowed.
//
// Detected by whether a side effect happened — a file created — rather than by looking
// for a marker in the output. The first version of this test searched stdout for
// "INJECTED", which the hostile path itself contains verbatim, so it could not tell an
// intact string from an executed command and failed against the working fix.
func TestQuotingContainsAnInjectedCommand(t *testing.T) {
	// A path the injected command would create. Inside t.TempDir so nothing outside the
	// test can be touched even if the quoting is broken.
	marker := filepath.Join(t.TempDir(), "injected")
	hostile := `/tmp/rollops-test'; touch ` + marker + `; echo '`

	out, err := runThroughShell(t, "printf %s "+shellQuote(hostile))
	if err != nil {
		t.Fatalf("running the quoted command: %v (%s)", err, out)
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("the injected command executed and created %s\n\nA value reaching the "+
			"remote shell must survive as one argument. This is the defect the escape fixes.",
			marker)
	}
	// And the value must arrive intact, or the fix would have traded injection for a
	// corrupted path — breaking deployments instead of enabling them.
	if out != hostile {
		t.Errorf("the value did not arrive intact:\n got  %q\n want %q", out, hostile)
	}
}

// A legitimate apostrophe in a path. The bug was not only a security question.
func TestAnApostropheInAPathSurvives(t *testing.T) {
	path := "/home/o'brien/app/current"

	out, err := runThroughShell(t, "echo "+shellQuote(path))
	if err != nil {
		t.Fatalf("a path with an apostrophe produced a broken command: %v (%s)", err, out)
	}
	if got := strings.TrimSpace(out); got != path {
		t.Errorf("echo returned %q, want %q", got, path)
	}
}

// The shapes a deployment path or command actually takes, each round-tripped through a
// real shell so the assertion is about behaviour rather than about escaping syntax.
func TestQuotingRoundTripsRealisticValues(t *testing.T) {
	for _, value := range []string{
		"/srv/app/releases/2026-08-12",
		"/srv/app with spaces/current",
		`/srv/app/$HOME/current`,
		"/srv/app/`whoami`/current",
		`/srv/app/$(id -u)/current`,
		`/srv/app/"quoted"/current`,
		`/srv/app/back\slash`,
		"/srv/app/multi'quote'path",
		"/srv/app/semi;colon",
		"/srv/app/pipe|char",
		"/srv/app/amp&ersand",
		"/srv/app/new\nline",
	} {
		t.Run(value, func(t *testing.T) {
			out, err := runThroughShell(t, "printf %s "+shellQuote(value))
			if err != nil {
				t.Fatalf("quoting %q produced a command the shell rejected: %v (%s)", value, err, out)
			}
			if out != value {
				t.Errorf("round trip changed the value:\n got  %q\n want %q\n\nA path that "+
					"arrives altered writes a release to the wrong place.", out, value)
			}
		})
	}
}

// An empty value must still produce a single empty argument rather than disappearing,
// which would silently shift every later argument left.
func TestQuotingAnEmptyValueYieldsAnEmptyArgument(t *testing.T) {
	out, err := runThroughShell(t, "printf '[%s]' "+shellQuote(""))
	if err != nil {
		t.Fatalf("quoting an empty value: %v (%s)", err, out)
	}
	if out != "[]" {
		t.Errorf("got %q, want \"[]\": an empty value must remain one argument", out)
	}
}

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

type controlOps struct {
	statusNoteOps
	pause, resume, abort rollout.Rollout
}

func (c controlOps) Pause(context.Context, string) (rollout.Rollout, error) { return c.pause, nil }
func (c controlOps) Resume(context.Context, string) (rollout.Rollout, error) {
	return c.resume, nil
}
func (c controlOps) Abort(context.Context, string) (rollout.Rollout, error) { return c.abort, nil }

func TestCLI_PauseResumeAbort(t *testing.T) {
	ops := controlOps{
		pause:  rollout.Rollout{ID: "ro-cli", Phase: rollout.PhasePaused},
		resume: rollout.Rollout{ID: "ro-cli", Phase: rollout.PhaseDeploying},
		abort:  rollout.Rollout{ID: "ro-cli", Phase: rollout.PhaseRolledBack},
	}
	for _, tc := range []struct {
		cmd  string
		want string
	}{
		{"pause", "paused"},
		{"resume", "deploying"},
		{"abort", "rolled-back"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			var buf bytes.Buffer
			app := &App{Out: &buf, Ops: ops}
			if err := app.Run(context.Background(), []string{tc.cmd, "ro-cli"}); err != nil {
				t.Fatalf("%s: %v", tc.cmd, err)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("%s output = %q, want %q", tc.cmd, buf.String(), tc.want)
			}
		})
	}
}

func TestCLI_PauseRequiresID(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf, Ops: controlOps{}}
	if err := app.Run(context.Background(), []string{"pause"}); err == nil {
		t.Fatal("pause without id should error")
	}
}

func TestCLI_UsageDocumentsPauseResumeAbort(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf, Ops: controlOps{}}
	_ = app.Run(context.Background(), nil)
	out := buf.String()
	for _, want := range []string{"pause <rollout-id>", "resume <rollout-id>", "abort <rollout-id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
}

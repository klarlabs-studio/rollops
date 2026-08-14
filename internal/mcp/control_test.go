package mcp

import (
	"testing"
)

const canaryPauseYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/staging/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: canary
    steps:
      - weight: 10
        pause: 50ms
      - weight: 100
        pause: 50ms
`

func TestTools_PauseResumeAbort(t *testing.T) {
	tl := newTools(t)
	if _, err := tl.Apply(asAgent("nomi"), ApplyInput{Config: canaryPauseYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, tc := range []struct {
		name  string
		call  func() (ActionOutput, error)
		deny  func() (ActionOutput, error)
		phase string
	}{
		{
			name:  "pause",
			call:  func() (ActionOutput, error) { return tl.Pause(asAgent("nomi"), ActionInput{RolloutID: "ro-mcp-1"}) },
			deny:  func() (ActionOutput, error) { return tl.Pause(asAgent("weak"), ActionInput{RolloutID: "ro-mcp-1"}) },
			phase: "paused",
		},
		{
			name:  "resume",
			call:  func() (ActionOutput, error) { return tl.Resume(asAgent("nomi"), ActionInput{RolloutID: "ro-mcp-1"}) },
			deny:  func() (ActionOutput, error) { return tl.Resume(asAgent("weak"), ActionInput{RolloutID: "ro-mcp-1"}) },
			phase: "deploying",
		},
		{
			name:  "abort",
			call:  func() (ActionOutput, error) { return tl.Abort(asAgent("nomi"), ActionInput{RolloutID: "ro-mcp-1"}) },
			deny:  func() (ActionOutput, error) { return tl.Abort(asAgent("weak"), ActionInput{RolloutID: "ro-mcp-1"}) },
			phase: "rolled-back",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.deny(); err == nil {
				t.Fatalf("readonly agent must not %s", tc.name)
			}
			out, err := tc.call()
			if err != nil {
				t.Fatalf("operator %s: %v", tc.name, err)
			}
			if out.Phase != tc.phase {
				t.Errorf("operator %s phase = %q, want %q", tc.name, out.Phase, tc.phase)
			}
		})
	}
}

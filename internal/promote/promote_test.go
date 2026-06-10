package promote

import (
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

const stagedYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: ssh
    ref: demo/app
    criticality: medium
    spec: {}
  strategy:
    type: rolling
  environments:
    - name: staging
      promote: true
    - name: prod
      promote: true
      strategy:
        type: canary
        steps:
          - weight: 10
    - name: sandbox
`

func load(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]byte(stagedYAML))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func TestChain_OrderedPromoteEnvs(t *testing.T) {
	got := Chain(load(t))
	if len(got) != 2 || got[0] != "staging" || got[1] != "prod" {
		t.Errorf("chain = %v, want [staging prod]", got)
	}
}

func TestNext_StagedProgression(t *testing.T) {
	c := load(t)
	if n, ok := Next(c, "staging"); !ok || n != "prod" {
		t.Errorf("after staging = %q,%v, want prod,true", n, ok)
	}
	if _, ok := Next(c, "prod"); ok {
		t.Error("prod is the last stage; Next should be false")
	}
	if n, ok := Next(c, "unknown"); !ok || n != "staging" {
		t.Errorf("unknown current should start chain at staging, got %q,%v", n, ok)
	}
}

func TestIndependent_NonPromoteEnvs(t *testing.T) {
	got := Independent(load(t))
	if len(got) != 1 || got[0] != "sandbox" {
		t.Errorf("independent = %v, want [sandbox]", got)
	}
}

func TestEffectiveStrategy_PerEnvOverride(t *testing.T) {
	c := load(t)
	if s := EffectiveStrategy(c, "prod"); s.Type != "canary" {
		t.Errorf("prod strategy = %q, want canary (override)", s.Type)
	}
	if s := EffectiveStrategy(c, "staging"); s.Type != "rolling" {
		t.Errorf("staging strategy = %q, want rolling (default)", s.Type)
	}
}

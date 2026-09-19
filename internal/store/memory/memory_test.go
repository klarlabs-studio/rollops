package memory_test

import (
	"testing"

	"go.klarlabs.de/rollops/internal/app/port/porttest"
	"go.klarlabs.de/rollops/internal/store/memory"
)

func TestMemoryStoreSatisfiesThePorts(t *testing.T) {
	porttest.Run(t, func(t *testing.T) porttest.Repositories {
		s := memory.New()
		return porttest.Repositories{
			Projects:     s.Projects(),
			Environments: s.Environments(),
			Artifacts:    s.Artifacts(),
			Releases:     s.Releases(),
			Plans:        s.Plans(),
			Deployments:  s.Deployments(),
			Approvals:    s.Approvals(),
			Idempotency:  s.Idempotency(),
			Events:       s.Events(),
			Tx:           s,
		}
	})
}

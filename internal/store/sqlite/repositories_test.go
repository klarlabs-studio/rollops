package sqlite

import (
	"path/filepath"
	"testing"

	"go.klarlabs.de/rollops/internal/app/port/porttest"
)

func TestSQLiteStoreSatisfiesThePorts(t *testing.T) {
	porttest.Run(t, func(t *testing.T) porttest.Repositories {
		s, err := Open(filepath.Join(t.TempDir(), "rollops.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return porttest.Repositories{
			Projects:     s.Projects(),
			Environments: s.Environments(),
			Artifacts:    s.Artifacts(),
			Releases:     s.Releases(),
			Plans:        s.Plans(),
			Deployments:  s.Deployments(),
			Events:       s.Events(),
			Tx:           s,
		}
	})
}

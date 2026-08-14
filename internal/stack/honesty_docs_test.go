package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Product-facing docs and package comments must not advertise surfaces the
// daemon does not ship. Historical memory/, CHANGELOG, and the make-it-real
// RFC may still name the gaps.
func TestHonestyDocsDoNotClaimUnwiredSurfaces(t *testing.T) {
	root := moduleRoot(t)
	cases := []struct {
		rel       string
		forbidden []string
	}{
		{
			rel: "README.md",
			forbidden: []string{
				"postgres / mnemos",
				"webhook+poll",
				"Concept / pre-MVP",
				"enabled in engine wiring",
			},
		},
		{
			rel: "rollops-vision.md",
			forbidden: []string{
				"Concept / pre-MVP",
				"uses decision-kit directly",
				"decision-kit computes",
				"decision-kit risk scoring",
			},
		},
		{
			rel: "rollops-tdd.md",
			forbidden: []string{
				"SQLite/PG/mnemos",
				"runnable standalone",
				"Postgres-backed",
				"Optional agent",
				"webhook + poll",
				"decision-kit scores",
			},
		},
		{
			rel: "wiki/architecture.md",
			forbidden: []string{
				"webhook+poll",
				"risk gate (decision-kit)",
				"same binary, Postgres",
			},
		},
		{
			rel: "internal/store/store.go",
			forbidden: []string{
				"SQLite / Postgres / mnemos",
				"Postgres (studio",
				"optional bitemporal",
			},
		},
		{
			rel: "internal/mcp/mcp.go",
			forbidden: []string{
				"runnable standalone",
				"or standalone",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.rel, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, c.rel))
			if err != nil {
				t.Fatal(err)
			}
			body := string(b)
			for _, needle := range c.forbidden {
				if strings.Contains(body, needle) {
					t.Errorf("%s still claims %q", c.rel, needle)
				}
			}
		})
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

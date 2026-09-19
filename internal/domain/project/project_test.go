package project

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func valid() Project {
	return Project{
		ID:          "prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002",
		Name:        "api",
		Description: "The public API service",
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

func TestValidProject(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestProjectRejectsAnIncompleteRecord(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Project)
	}{
		{"no id", func(p *Project) { p.ID = "" }},
		{"foreign id", func(p *Project) { p.ID = "env_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002" }},
		{"no name", func(p *Project) { p.Name = " " }},
		{"no creation time", func(p *Project) { p.CreatedAt = time.Time{} }},
		{"no update time", func(p *Project) { p.UpdatedAt = time.Time{} }},
		{"updated before created", func(p *Project) { p.UpdatedAt = at.Add(-time.Second) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid()
			c.mutate(&p)
			if err := p.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// A name is an alias, not identity (spec 4.2), but it is what appears in a CLI
// argument, a URL path and a config file — so it must be unambiguous there.
func TestNameMustBeAUsableAlias(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"simple", "api", true},
		{"hyphenated", "payments-api", true},
		{"digits", "api2", true},
		{"leading digit", "2api", true},
		{"upper case", "API", false},
		{"space", "payments api", false},
		{"slash", "acme/api", false},
		{"leading hyphen", "-api", false},
		{"trailing hyphen", "api-", false},
		{"dot", "api.v2", false},
		{"looks like an id", "prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002", false},
		{"too long", strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid()
			p.Name = c.in
			err := p.Validate()
			if c.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidName) {
				t.Errorf("Validate() = %v, want ErrInvalidName", err)
			}
		})
	}
}

func TestNewStampsIdentityAndTimes(t *testing.T) {
	g := identity.NewFixedGenerator("11111111-1111-7111-8111-111111111111")
	c := identity.NewFixedClock(at)
	p, err := New(g, c, Project{Name: "api", Description: "The public API service"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := identity.ProjectID("prj_11111111-1111-7111-8111-111111111111"); p.ID != want {
		t.Errorf("ID = %q, want %q", p.ID, want)
	}
	if !p.CreatedAt.Equal(at) || !p.UpdatedAt.Equal(at) {
		t.Errorf("times = %v / %v, want both %v", p.CreatedAt, p.UpdatedAt, at)
	}
}

func TestNewRefusesAnInvalidProject(t *testing.T) {
	g := identity.NewGenerator()
	c := identity.NewFixedClock(at)
	if _, err := New(g, c, Project{Name: "Not A Name"}); err == nil {
		t.Error("New accepted an invalid project name")
	}
}

func TestNewPropagatesAGeneratorFailure(t *testing.T) {
	boom := errors.New("no entropy")
	g := identity.GeneratorFunc(func() (string, error) { return "", boom })
	if _, err := New(g, identity.NewFixedClock(at), Project{Name: "api"}); !errors.Is(err, boom) {
		t.Errorf("got %v, want the generator's error", err)
	}
}

// A project is mutable, unlike a release — but CreatedAt is not, and an edit
// must move UpdatedAt or a reader cannot tell the record changed.
func TestRenameTouchesUpdatedAtAndNotCreatedAt(t *testing.T) {
	later := at.Add(time.Hour)
	p := valid()
	got, err := p.Rename(identity.NewFixedClock(later), "payments-api")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got.Name != "payments-api" {
		t.Errorf("Name = %q", got.Name)
	}
	if !got.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, later)
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt moved to %v", got.CreatedAt)
	}
	if p.Name != "api" {
		t.Error("Rename mutated the receiver")
	}
}

func TestRenameRefusesAnInvalidName(t *testing.T) {
	if _, err := valid().Rename(identity.NewFixedClock(at), "Not A Name"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("got %v, want ErrInvalidName", err)
	}
}

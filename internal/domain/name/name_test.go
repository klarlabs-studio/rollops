package name

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"simple", "api", true},
		{"hyphenated", "payments-api", true},
		{"digits", "api2", true},
		{"leading digit", "2api", true},
		{"all digits", "2026", true},
		{"at the limit", strings.Repeat("a", MaxLen), true},
		{"empty", "", false},
		{"blank", "   ", false},
		{"upper case", "API", false},
		{"space", "payments api", false},
		{"slash", "acme/api", false},
		{"leading hyphen", "-api", false},
		{"trailing hyphen", "api-", false},
		{"double hyphen", "api--v2", false},
		{"dot", "api.v2", false},
		{"underscore", "payments_api", false},
		{"over the limit", strings.Repeat("a", MaxLen+1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate("project", c.in)
			if c.ok && err != nil {
				t.Errorf("Validate(%q) = %v, want nil", c.in, err)
			}
			if !c.ok && !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate(%q) = %v, want ErrInvalid", c.in, err)
			}
		})
	}
}

// A typed ID carries an underscore, which is the character the rule excludes —
// so an ID can never be mistaken for the name of the thing it identifies.
func TestATypedIDIsNotAName(t *testing.T) {
	for _, id := range []string{
		"prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002",
		"env_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d003",
	} {
		if err := Validate("project", id); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%q) = %v, want ErrInvalid", id, err)
		}
	}
}

// The error names what was being named, so it reads as a sentence wherever it
// surfaces rather than leaving the reader to guess which field was wrong.
func TestTheErrorNamesWhatWasBeingNamed(t *testing.T) {
	err := Validate("target binding", "Primary")
	if !strings.Contains(err.Error(), "target binding") {
		t.Errorf("error = %q, want it to mention the target binding", err)
	}
	if !strings.Contains(err.Error(), "Primary") {
		t.Errorf("error = %q, want it to quote the offending name", err)
	}
}

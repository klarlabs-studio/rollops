package identity

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewIDsCarryTheirTypePrefix(t *testing.T) {
	g := NewGenerator()
	cases := []struct {
		name   string
		make   func() (string, error)
		prefix string
	}{
		{"project", func() (string, error) { id, err := NewProjectID(g); return string(id), err }, "prj_"},
		{"environment", func() (string, error) { id, err := NewEnvironmentID(g); return string(id), err }, "env_"},
		{"artifact", func() (string, error) { id, err := NewArtifactID(g); return string(id), err }, "art_"},
		{"release", func() (string, error) { id, err := NewReleaseID(g); return string(id), err }, "rel_"},
		{"deployment", func() (string, error) { id, err := NewDeploymentID(g); return string(id), err }, "dep_"},
		{"plan", func() (string, error) { id, err := NewPlanID(g); return string(id), err }, "pln_"},
		{"verification run", func() (string, error) { id, err := NewVerificationRunID(g); return string(id), err }, "vrf_"},
		{"pipeline run", func() (string, error) { id, err := NewPipelineRunID(g); return string(id), err }, "run_"},
		{"execution", func() (string, error) { id, err := NewExecutionID(g); return string(id), err }, "exe_"},
		{"event", func() (string, error) { id, err := NewEventID(g); return string(id), err }, "evt_"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.make()
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !strings.HasPrefix(got, c.prefix) {
				t.Errorf("id %q does not carry prefix %q", got, c.prefix)
			}
			if len(got) != len(c.prefix)+36 {
				t.Errorf("id %q is %d chars, want prefix + 36", got, len(got))
			}
		})
	}
}

// A misrouted identifier must fail at the parse boundary rather than reaching
// storage and finding a foreign row.
func TestParseRejectsAForeignPrefix(t *testing.T) {
	g := NewGenerator()
	env, err := NewEnvironmentID(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProjectID(string(env)); !errors.Is(err, ErrWrongKind) {
		t.Fatalf("parsing an environment id as a project id: got %v, want ErrWrongKind", err)
	}
}

func TestParseRoundTrips(t *testing.T) {
	g := NewGenerator()
	cases := []struct {
		name string
		trip func() (string, string, error)
	}{
		{"project", roundTrip(g, NewProjectID, ParseProjectID)},
		{"environment", roundTrip(g, NewEnvironmentID, ParseEnvironmentID)},
		{"artifact", roundTrip(g, NewArtifactID, ParseArtifactID)},
		{"release", roundTrip(g, NewReleaseID, ParseReleaseID)},
		{"deployment", roundTrip(g, NewDeploymentID, ParseDeploymentID)},
		{"plan", roundTrip(g, NewPlanID, ParsePlanID)},
		{"verification run", roundTrip(g, NewVerificationRunID, ParseVerificationRunID)},
		{"pipeline run", roundTrip(g, NewPipelineRunID, ParsePipelineRunID)},
		{"execution", roundTrip(g, NewExecutionID, ParseExecutionID)},
		{"event", roundTrip(g, NewEventID, ParseEventID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, got, err := c.trip()
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if got != want {
				t.Errorf("round trip: got %q, want %q", got, want)
			}
		})
	}
}

func roundTrip[T ~string](
	g Generator,
	make func(Generator) (T, error),
	parse func(string) (T, error),
) func() (string, string, error) {
	return func() (string, string, error) {
		want, err := make(g)
		if err != nil {
			return "", "", err
		}
		got, err := parse(string(want))
		return string(want), string(got), err
	}
}

// Every parser must reject every other kind, not just the one pair a single
// example would cover.
func TestEveryParserRejectsEveryOtherKind(t *testing.T) {
	g := NewGenerator()
	kinds := []struct {
		name  string
		make  func(Generator) (string, error)
		parse func(string) error
	}{
		{"project", erase(NewProjectID), discard(ParseProjectID)},
		{"environment", erase(NewEnvironmentID), discard(ParseEnvironmentID)},
		{"artifact", erase(NewArtifactID), discard(ParseArtifactID)},
		{"release", erase(NewReleaseID), discard(ParseReleaseID)},
		{"deployment", erase(NewDeploymentID), discard(ParseDeploymentID)},
		{"plan", erase(NewPlanID), discard(ParsePlanID)},
		{"verification run", erase(NewVerificationRunID), discard(ParseVerificationRunID)},
		{"pipeline run", erase(NewPipelineRunID), discard(ParsePipelineRunID)},
		{"execution", erase(NewExecutionID), discard(ParseExecutionID)},
		{"event", erase(NewEventID), discard(ParseEventID)},
	}
	for _, parser := range kinds {
		for _, source := range kinds {
			if parser.name == source.name {
				continue
			}
			t.Run(source.name+" as "+parser.name, func(t *testing.T) {
				id, err := source.make(g)
				if err != nil {
					t.Fatal(err)
				}
				if err := parser.parse(id); !errors.Is(err, ErrWrongKind) {
					t.Errorf("got %v, want ErrWrongKind", err)
				}
			})
		}
	}
}

func erase[T ~string](make func(Generator) (T, error)) func(Generator) (string, error) {
	return func(g Generator) (string, error) {
		id, err := make(g)
		return string(id), err
	}
}

func discard[T ~string](parse func(string) (T, error)) func(string) error {
	return func(s string) error {
		_, err := parse(s)
		return err
	}
}

// A generator is injected, so it can fail or misbehave; neither may produce an
// identifier that later parses as valid.
func TestGenerationRefusesABadGenerator(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if _, err := NewReleaseID(nil); err == nil {
			t.Error("a nil generator produced an id")
		}
	})
	t.Run("error", func(t *testing.T) {
		boom := errors.New("no entropy")
		g := GeneratorFunc(func() (string, error) { return "", boom })
		if _, err := NewReleaseID(g); !errors.Is(err, boom) {
			t.Errorf("got %v, want the generator's error", err)
		}
	})
	t.Run("not a uuid", func(t *testing.T) {
		g := NewFixedGenerator("definitely-not-a-uuid")
		if _, err := NewReleaseID(g); err == nil {
			t.Error("a non-uuid generator value was accepted")
		}
	})
}

// uuid.Parse accepts braced and urn: spellings. Admitting them would give one
// identifier several text forms and break equality on the string.
func TestParseRejectsNonCanonicalUUIDSpellings(t *testing.T) {
	for _, in := range []string{
		"rel_{0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001}",
		"rel_urn:uuid:0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001",
		"rel_0199f3c18a2e7b3d9f102c5ab4e7d001",
	} {
		if _, err := ParseReleaseID(in); err == nil {
			t.Errorf("ParseReleaseID(%q) accepted a non-canonical id", in)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"prefix only", "rel_"},
		{"no prefix", "0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001"},
		{"not a uuid", "rel_not-a-uuid-at-all"},
		{"legacy rollout id", "ro-20260919T120000.000000000"},
		{"truncated uuid", "rel_0199f3c1-8a2e-7b3d-9f10"},
		{"trailing junk", "rel_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseReleaseID(c.in); err == nil {
				t.Errorf("ParseReleaseID(%q) accepted a malformed id", c.in)
			}
		})
	}
}

func TestGeneratedIDsAreUniqueAndTimeOrdered(t *testing.T) {
	g := NewGenerator()
	const n = 1000
	seen := make(map[ReleaseID]struct{}, n)
	var prev ReleaseID
	for i := range n {
		id, err := NewReleaseID(g)
		if err != nil {
			t.Fatalf("generate %d: %v", i, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at %d", id, i)
		}
		seen[id] = struct{}{}
		if prev != "" && id < prev {
			t.Fatalf("id %q sorts before its predecessor %q", id, prev)
		}
		prev = id
	}
}

// Determinism is what makes plan and event assertions possible (spec §29).
func TestFixedGeneratorIsDeterministic(t *testing.T) {
	a := NewFixedGenerator("11111111-1111-7111-8111-111111111111")
	b := NewFixedGenerator("11111111-1111-7111-8111-111111111111")
	first, err := NewPlanID(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPlanID(b)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("fixed generator diverged: %q vs %q", first, second)
	}
	if want := PlanID("pln_11111111-1111-7111-8111-111111111111"); first != want {
		t.Errorf("got %q, want %q", first, want)
	}
}

func TestSequenceGeneratorCountsUp(t *testing.T) {
	g := NewSequenceGenerator()
	first, err := NewDeploymentID(g)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDeploymentID(g)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("sequence generator repeated itself")
	}
	if first >= second {
		t.Errorf("sequence is not increasing: %q then %q", first, second)
	}
	if _, err := ParseDeploymentID(string(first)); err != nil {
		t.Errorf("sequence generator produced an unparseable id %q: %v", first, err)
	}
}

func TestFixedClockDoesNotMove(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := NewFixedClock(at)
	if got := c.Now(); !got.Equal(at) {
		t.Errorf("got %v, want %v", got, at)
	}
	if got := c.Now(); !got.Equal(at) {
		t.Errorf("second read moved: got %v, want %v", got, at)
	}
}

func TestSystemClockIsUTC(t *testing.T) {
	if got := NewSystemClock().Now().Location(); got != time.UTC {
		t.Errorf("system clock returns %v, want UTC", got)
	}
}

func TestPrincipalValidation(t *testing.T) {
	cases := []struct {
		name string
		p    Principal
		ok   bool
	}{
		{"human", Principal{ID: "alice", Type: PrincipalHuman}, true},
		{"service", Principal{ID: "ci", Type: PrincipalService}, true},
		{"agent", Principal{ID: "claude", Type: PrincipalAgent}, true},
		{"git", Principal{ID: "webhook", Type: PrincipalGit}, true},
		{"scheduler", Principal{ID: "cron", Type: PrincipalScheduler}, true},
		{"system", Principal{ID: "reconciler", Type: PrincipalSystem}, true},
		{"missing id", Principal{Type: PrincipalHuman}, false},
		{"blank id", Principal{ID: "   ", Type: PrincipalHuman}, false},
		{"missing type", Principal{ID: "alice"}, false},
		{"unknown type", Principal{ID: "alice", Type: PrincipalType("wizard")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.Validate()
			if c.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// INV-005: a mutation without attribution must be impossible to express, not
// merely discouraged.
func TestZeroPrincipalIsInvalid(t *testing.T) {
	var p Principal
	if err := p.Validate(); err == nil {
		t.Error("the zero Principal validated; every mutation must be attributable")
	}
}

// INV-012: claims arrive from identity providers and are rendered in audit and
// API responses, so a value that looks like a credential must not survive.
func TestPrincipalRedactsCredentialBearingClaims(t *testing.T) {
	p := Principal{
		ID:   "ci",
		Type: PrincipalService,
		Claims: map[string]string{
			"email":         "ci@example.com",
			"token":         "ghp_realsecretvalue",
			"password":      "hunter2",
			"authorization": "Bearer abc.def",
			"api_key":       "sk-live-123",
		},
	}
	got := p.Redacted()
	if got.Claims["email"] != "ci@example.com" {
		t.Errorf("benign claim was altered: %q", got.Claims["email"])
	}
	for _, k := range []string{"token", "password", "authorization", "api_key"} {
		if got.Claims[k] != redacted {
			t.Errorf("claim %q = %q, want %q", k, got.Claims[k], redacted)
		}
	}
	if p.Claims["token"] != "ghp_realsecretvalue" {
		t.Error("Redacted mutated the receiver")
	}
}

func TestPrincipalWithoutClaimsRedactsCleanly(t *testing.T) {
	p := Principal{ID: "reconciler", Type: PrincipalSystem}
	got := p.Redacted()
	if got.ID != p.ID || got.Type != p.Type || got.Claims != nil {
		t.Errorf("got %+v, want the principal unchanged", got)
	}
}

func TestRevisionNext(t *testing.T) {
	var r Revision
	if r.Next() != Revision(1) {
		t.Errorf("zero revision advances to %d, want 1", r.Next())
	}
	if Revision(41).Next() != Revision(42) {
		t.Errorf("got %d, want 42", Revision(41).Next())
	}
}

func TestRevisionMatches(t *testing.T) {
	// A zero expectation means "I did not look" and must not silently pass as
	// agreement with an existing aggregate.
	if Revision(7).Matches(0) {
		t.Error("an unset expectation matched a live revision")
	}
	if !Revision(7).Matches(7) {
		t.Error("equal revisions did not match")
	}
	if Revision(7).Matches(6) {
		t.Error("a stale expectation matched")
	}
}

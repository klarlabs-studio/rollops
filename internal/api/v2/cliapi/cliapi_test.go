package cliapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/cliapi"
	"go.klarlabs.de/rollops/internal/app/deploy"
	appenv "go.klarlabs.de/rollops/internal/app/environment"
	appproject "go.klarlabs.de/rollops/internal/app/project"
	apprelease "go.klarlabs.de/rollops/internal/app/release"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/verification"
	"go.klarlabs.de/rollops/internal/store/memory"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return at }

func anyone() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

func everyone() cliapi.IdentifierFunc {
	return func(context.Context) (identity.Principal, error) { return anyone(), nil }
}

// nobody stands in for a shell that never proved who was at it.
func nobody() cliapi.IdentifierFunc {
	return func(context.Context) (identity.Principal, error) {
		return identity.Principal{}, errors.New("no credential")
	}
}

// refusingDeployer carries nothing out and records what it was asked to. The
// write side is exercised in full by the api/v2 tests; what is in question here
// is whether a line typed at a shell reached the right method carrying the
// right arguments, and that is answered whether or not the engine behind it
// would have succeeded.
//
// Verifying is the exception: it hands back a run the fixture stored, because
// what a verdict looks like on the way out is exactly what this package is for.
type refusingDeployer struct {
	verdict *deploy.Verification

	plan     deploy.PlanCommand
	apply    deploy.ApplyCommand
	promote  deploy.PromoteCommand
	rollback deploy.RollbackCommand
	verify   deploy.VerifyCommand
}

var errNotWired = errors.New("the deployer was not meant to carry this out")

func (d *refusingDeployer) Plan(_ context.Context, cmd deploy.PlanCommand) (plan.DeploymentPlan, error) {
	d.plan = cmd
	return plan.DeploymentPlan{}, errNotWired
}

func (d *refusingDeployer) Apply(_ context.Context, cmd deploy.ApplyCommand) (deployment.Deployment, error) {
	d.apply = cmd
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Approve(context.Context, deploy.ApproveCommand) (deployment.Deployment, error) {
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Cancel(context.Context, deploy.CancelCommand) (deployment.Deployment, error) {
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Verify(
	_ context.Context, cmd deploy.VerifyCommand,
) (deployment.Deployment, deploy.Verification, error) {
	d.verify = cmd
	if d.verdict == nil {
		return deployment.Deployment{}, deploy.Verification{}, errNotWired
	}
	return deployment.Deployment{}, *d.verdict, nil
}

func (d *refusingDeployer) Promote(_ context.Context, cmd deploy.PromoteCommand) (deployment.Deployment, error) {
	d.promote = cmd
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Rollback(_ context.Context, cmd deploy.RollbackCommand) (deployment.Deployment, error) {
	d.rollback = cmd
	return deployment.Deployment{}, errNotWired
}

type shell struct {
	svc      *apiv2.Service
	store    *memory.Store
	deployer *refusingDeployer
	who      cliapi.Identifier
}

// opened builds the surface over the real service and an in-memory store.
// Everything behind a command is the real thing: a fake service would let the
// command surface agree with a projection nobody serves.
func opened(t *testing.T, who cliapi.Identifier) *shell {
	t.Helper()
	store := memory.New()
	clock := fixedClock{}

	registrar, err := apprelease.New(apprelease.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Artifacts:  store.Artifacts(),
		Releases:   store.Releases(),
		Events:     store.Events(),
		Clock:      clock,
		IDs:        identity.NewGenerator(),
	})
	if err != nil {
		t.Fatalf("apprelease.New: %v", err)
	}
	founder, err := appproject.New(appproject.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Events:     store.Events(),
		Clock:      clock,
		IDs:        identity.NewGenerator(),
	})
	if err != nil {
		t.Fatalf("appproject.New: %v", err)
	}
	binder, err := appenv.New(appenv.Config{
		Transactor:   store,
		Projects:     store.Projects(),
		Environments: store.Environments(),
		Events:       store.Events(),
		Clock:        clock,
		IDs:          identity.NewGenerator(),
	})
	if err != nil {
		t.Fatalf("appenv.New: %v", err)
	}

	deployer := &refusingDeployer{}
	svc, err := apiv2.New(apiv2.Config{
		Projects:         store.Projects(),
		Environments:     store.Environments(),
		Releases:         store.Releases(),
		Artifacts:        store.Artifacts(),
		Deployments:      store.Deployments(),
		Plans:            store.Plans(),
		VerificationRuns: store.VerificationRuns(),
		Events:           store.Events(),
		Deployer:         deployer,
		Registrar:        registrar,
		Founder:          founder,
		Binder:           binder,
		Keys:             store.Idempotency(),
		Clock:            clock,
	})
	if err != nil {
		t.Fatalf("apiv2.New: %v", err)
	}
	return &shell{svc: svc, store: store, deployer: deployer, who: who}
}

// run types one command line and returns what it printed. Each call gets its
// own App and its own buffer, so what a command wrote is only ever its own.
func (s *shell) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app, err := cliapi.New(cliapi.Config{Service: s.svc, Identity: s.who, Out: &out})
	if err != nil {
		t.Fatalf("cliapi.New: %v", err)
	}
	ran := app.Run(context.Background(), args)
	return out.String(), ran
}

// must runs a command that is expected to work and returns its output.
func (s *shell) must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := s.run(t, args...)
	if err != nil {
		t.Fatalf("rollops %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// answered runs a command asking for JSON and decodes the document.
func answered[T any](t *testing.T, s *shell, args ...string) T {
	t.Helper()
	out := s.must(t, append(slices.Clone(args), "--output", "json")...)
	var got T
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("rollops %s: decode: %v\n%s", strings.Join(args, " "), err, out)
	}
	return got
}

func TestAServiceAndAnIdentityAreBothRequired(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  cliapi.Config
	}{
		{"no service", cliapi.Config{Identity: everyone()}},
		// Defaulting would attribute every deployment to nobody, which INV-005
		// exists to prevent.
		{"no identity", cliapi.Config{Service: &apiv2.Service{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := cliapi.New(tc.cfg); err == nil {
				t.Fatal("New accepted a config it cannot serve")
			}
		})
	}
}

// canonCommands is the command list §26.1 names, less the ones this package
// does not own: init and doctor run before any service exists, and diff, run
// and logs are not built. It is written out rather than read off the App so
// that a command quietly added, renamed or dropped is a failing test and a
// conversation about the canon rather than a silent change to a contract
// scripts are pointed at.
var canonCommands = []string{
	"project", "environment", "release",
	"plan", "deploy", "status", "verify", "promote", "rollback", "history",
}

func TestTheCommandSetIsTheOneTheCanonNames(t *testing.T) {
	t.Parallel()

	if !slices.Equal(cliapi.Commands, canonCommands) {
		t.Errorf("Commands = %v, want %v", cliapi.Commands, canonCommands)
	}
}

// Every command dispatches. A name in the list with no case behind it would
// reach the default and be reported as unknown, which is the one failure the
// list itself cannot show.
func TestEveryNamedCommandIsDispatched(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	for _, name := range cliapi.Commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := s.run(t, name)
			if err == nil {
				return // took no arguments and succeeded; dispatched either way.
			}
			if strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("%q is listed but not dispatched", name)
			}
		})
	}
}

func TestAMistypedCommandIsTheCallersMistake(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	for _, args := range [][]string{
		{},
		{"deploly"},
		{"project", "destroy"},
		{"release", "artifact", "delete"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			_, err := s.run(t, args...)
			if err == nil {
				t.Fatal("the surface accepted a command it does not have")
			}
			if got := cliapi.Exit(err); got != cliapi.ExitValidation {
				t.Errorf("exit = %d, want %d", got, cliapi.ExitValidation)
			}
		})
	}
}

// leaves is every command that reaches the service, with arguments that would
// satisfy its parser. None of the ids exist: what these assert happens before
// anything is looked up.
var leaves = [][]string{
	{"project", "list"},
	{"project", "get", "prj_1"},
	{"project", "create", "--name", "checkout"},
	{"environment", "list", "--project", "prj_1"},
	{"environment", "get", "env_1"},
	{"environment", "create", "--project", "prj_1", "--name", "production"},
	{"release", "list", "--project", "prj_1"},
	{"release", "get", "rel_1"},
	{"release", "create", "--project", "prj_1", "--version", "1.0.0"},
	{"release", "artifact", "list", "--project", "prj_1"},
	{"release", "artifact", "get", "art_1"},
	{"release", "artifact", "register", "--project", "prj_1", "--kind", "oci-image"},
	{"plan", "env_1", "--release", "rel_1", "--strategy", "rolling"},
	{"plan", "--explain", "pln_1"},
	{"deploy", "--plan", "pln_1"},
	{"status", "dpl_1"},
	{"verify", "dpl_1"},
	{"promote", "dpl_1"},
	{"rollback", "dpl_1"},
	{"history", "env_1"},
	{"history", "--deployment", "dpl_1"},
}

// Every command refuses a caller that has not said who it is, reads included:
// answering a read with NOT_FOUND would tell an unidentified caller which ids
// exist, and that is a fact about somebody else's project. The table is the
// whole surface, because the one read that forgets to ask is the one worth
// finding.
func TestEveryCommandRefusesAnUnidentifiedCaller(t *testing.T) {
	t.Parallel()
	s := opened(t, nobody())

	for _, args := range leaves {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			_, err := s.run(t, args...)
			if err == nil {
				t.Fatal("the command answered a caller it could not identify")
			}
			var classified *apierr.Error
			if !errors.As(err, &classified) || classified.Code != apierr.Unauthorized {
				t.Fatalf("err = %v, want UNAUTHORIZED", err)
			}
			if got := cliapi.Exit(err); got != cliapi.ExitPolicy {
				t.Errorf("exit = %d, want %d", got, cliapi.ExitPolicy)
			}
		})
	}
}

// An Identifier that has already worked out what kind of refusal this is keeps
// its answer. Telling a caller whose credential is fine but whose permissions
// are not to go and authenticate again will not help.
func TestAnIdentifierThatAlreadyChoseACodeKeepsIt(t *testing.T) {
	t.Parallel()
	s := opened(t, cliapi.IdentifierFunc(func(context.Context) (identity.Principal, error) {
		return identity.Principal{}, &apierr.Error{Code: apierr.Forbidden, Message: "not yours"}
	}))

	_, err := s.run(t, "project", "list")
	var classified *apierr.Error
	if !errors.As(err, &classified) || classified.Code != apierr.Forbidden {
		t.Fatalf("err = %v, want FORBIDDEN", err)
	}
}

// §26.3 makes JSON the contract. Every read and plan command takes --output,
// and what comes back parses — a command that printed prose to stdout under
// --output json would break a script that never reads English.
func TestEveryCommandTakesOutputJSON(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	for _, args := range leaves {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			out, err := s.run(t, append(slices.Clone(args), "--output", "json")...)
			if err != nil {
				// The ids do not exist, so most of these fail — but they must
				// fail on the lookup rather than on the flag.
				if strings.Contains(err.Error(), "--output") {
					t.Fatalf("the command does not take --output: %v", err)
				}
				return
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("--output json printed something that is not JSON:\n%s", out)
			}
		})
	}
}

func TestAnUnknownOutputFormatIsRefusedRatherThanGuessedAt(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	_, err := s.run(t, "project", "list", "--output", "yaml")
	if err == nil {
		t.Fatal("the command accepted a format it cannot write")
	}
	if got := cliapi.Exit(err); got != cliapi.ExitValidation {
		t.Errorf("exit = %d, want %d", got, cliapi.ExitValidation)
	}
}

// Go's flag package stops at the first non-flag word, so a command written the
// way an operator writes it — the subject first, the options after — would
// otherwise read `--output json` as two more arguments.
func TestAFlagAfterThePositionalIsStillAFlag(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seeded := s.seed(t)

	out := s.must(t, "release", "get", string(seeded.release), "--output", "json")
	if !json.Valid([]byte(out)) {
		t.Fatalf("the flag after the positional was not read as a flag:\n%s", out)
	}
}

func TestAnExtraOrMissingArgumentIsTheCallersMistake(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	for _, args := range [][]string{
		{"status"},                        // nothing to look at
		{"status", "dpl_1", "dpl_2"},      // two deployments, one command
		{"project", "list", "everything"}, // takes no subject
		{"project", "create", "--colour", "blue"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			_, err := s.run(t, args...)
			if err == nil {
				t.Fatal("the command accepted arguments it cannot serve")
			}
			if got := cliapi.Exit(err); got != cliapi.ExitValidation {
				t.Errorf("exit = %d, want %d", got, cliapi.ExitValidation)
			}
		})
	}
}

// INV-005: who acted is established by the surface, never typed. A flag that
// let anybody attribute a deployment to somebody else would make the timeline
// a record of what was claimed rather than of what happened.
func TestTheActorIsNeverTakenFromTheCommandLine(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	if _, err := s.run(t, "project", "create", "--name", "checkout", "--actor", "somebody-else"); err == nil {
		t.Fatal("the surface has an --actor flag")
	}

	seeded := s.seed(t)
	got := answered[cliapi.ReleaseView](t, s, "release", "get", string(seeded.release))
	if got.CreatedBy.ID != anyone().ID {
		t.Errorf("created_by.id = %q, want %q", got.CreatedBy.ID, anyone().ID)
	}
}

// §26.3: machine output MUST NOT contain terminal formatting. Nothing in this
// package writes an escape sequence, so what this guards is the next thing
// added that does.
func TestMachineOutputCarriesNoTerminalFormatting(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seeded := s.seed(t)

	for _, args := range [][]string{
		{"project", "list"},
		{"project", "get", seeded.projectID},
		{"environment", "get", seeded.envID},
		{"release", "list", "--project", seeded.projectID},
		{"release", "get", string(seeded.release)},
		{"release", "artifact", "list", "--project", seeded.projectID},
		{"release", "artifact", "get", seeded.artifactID},
		{"plan", "--explain", string(seeded.plan.ID)},
		{"status", string(seeded.deploy.ID)},
		{"history", seeded.envID},
		{"history", "--deployment", string(seeded.deploy.ID)},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			for _, format := range []string{"json", "text"} {
				out := s.must(t, append(slices.Clone(args), "--output", format)...)
				if strings.ContainsRune(out, 0x1b) {
					t.Errorf("--output %s wrote an escape sequence:\n%q", format, out)
				}
			}
		})
	}
}

// The value of a setting is given on the command line and never comes back.
// INV-012 keeps credentials out of persisted records and out of what is read
// back, and an environment's view names its keys rather than describing them.
func TestNoSettingGivenToAnEnvironmentIsReadBack(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	prj := answered[cliapi.ProjectView](t, s, "project", "create", "--name", "checkout")

	// Deliberately not shaped like the credential it stands for: a real-looking
	// connection string in a test file is a secret scanner's finding for as long
	// as the file exists.
	const literal = "THE-VALUE-NO-READER-GETS-BACK"

	env := answered[cliapi.EnvironmentView](t, s,
		"environment", "create",
		"--project", prj.ProjectID,
		"--name", "production",
		"--kind", "production",
		"--target", "name=api,driver=kubernetes",
		"--target-config", "api.namespace="+literal,
		"--target-secret", "api.kubeconfig=prod/kubeconfig",
		"--var", "REGION="+literal,
		"--var-secret", "DATABASE_URL=prod/database-url",
	)

	for _, args := range [][]string{
		{"environment", "get", env.EnvironmentID},
		{"environment", "list", "--project", prj.ProjectID},
	} {
		for _, format := range []string{"json", "text"} {
			out := s.must(t, append(slices.Clone(args), "--output", format)...)
			if strings.Contains(out, literal) {
				t.Errorf("rollops %s --output %s read a value back:\n%s", strings.Join(args, " "), format, out)
			}
		}
	}

	// What it does carry is the names, so an operator can tell what is set.
	if !slices.Contains(env.VariableNames, "DATABASE_URL") || !slices.Contains(env.VariableNames, "REGION") {
		t.Errorf("variable_names = %v, want both names", env.VariableNames)
	}
	if len(env.Targets) != 1 {
		t.Fatalf("targets = %v, want one", env.Targets)
	}
	if !slices.Contains(env.Targets[0].ConfigKeys, "namespace") {
		t.Errorf("config_keys = %v, want namespace", env.Targets[0].ConfigKeys)
	}
}

// A setting addressed to a target nobody declared is refused rather than
// dropped. Silently ignoring it would make an environment missing exactly the
// setting the operator believed they had given it.
func TestAMisdirectedOrDoubledSettingIsRefused(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	base := []string{"environment", "create", "--project", "prj_1", "--name", "production"}
	for name, extra := range map[string][]string{
		"a config naming no target": {
			"--target", "name=api,driver=kubernetes", "--target-config", "worker.namespace=payments",
		},
		"a config with no target at all": {"--target-config", "api.namespace=payments"},
		"a target declared twice": {
			"--target", "name=api,driver=kubernetes", "--target", "name=api,driver=ecs",
		},
		"a target with no name":       {"--target", "driver=kubernetes"},
		"a target field nobody reads": {"--target", "name=api,drivr=kubernetes"},
		"a key that is both a literal and a secret": {
			"--target", "name=api,driver=kubernetes",
			"--target-config", "api.kubeconfig=here",
			"--target-secret", "api.kubeconfig=prod/kubeconfig",
		},
		"a variable that is both a literal and a secret": {
			"--var", "DATABASE_URL=here", "--var-secret", "DATABASE_URL=prod/database-url",
		},
		"a setting with no target prefix": {
			"--target", "name=api,driver=kubernetes", "--target-config", "namespace=payments",
		},
		"a policy with no name": {"--policy", "ref=policy://change-window"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := s.run(t, append(slices.Clone(base), extra...)...)
			if err == nil {
				t.Fatal("the command accepted a specification it cannot honour")
			}
			if got := cliapi.Exit(err); got != cliapi.ExitValidation {
				t.Errorf("exit = %d, want %d", got, cliapi.ExitValidation)
			}
		})
	}
}

// §26.2 asks for decision-oriented output. What an operator reading a plan has
// to decide is whether to apply it, so the verdict leads and the operations are
// the evidence rather than the answer.
func TestAPlanLeadsWithTheVerdictAndCarriesWhatToDecideOn(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seeded := s.seed(t)

	p := answered[cliapi.PlanView](t, s, "plan", "--explain", string(seeded.plan.ID))
	if p.PlanID != string(seeded.plan.ID) {
		t.Errorf("plan_id = %q, want %q", p.PlanID, seeded.plan.ID)
	}
	if len(p.Operations) == 0 {
		t.Error("the plan carries no operations to weigh the verdict against")
	}

	// The verdict leads, on its own line, before any of the evidence.
	text := s.must(t, "plan", "--explain", string(seeded.plan.ID))
	first, _, _ := strings.Cut(text, "\n")
	if !strings.Contains(first, "allowed") {
		t.Errorf("the plan does not lead with its verdict:\n%s", text)
	}
	if !strings.Contains(text, "risk "+p.Policy.Risk.Level) {
		t.Errorf("the plan does not say how risky it is:\n%s", text)
	}

	d := answered[cliapi.DeploymentView](t, s, "status", string(seeded.deploy.ID))
	if d.DeploymentID != string(seeded.deploy.ID) {
		t.Errorf("deployment_id = %q, want %q", d.DeploymentID, seeded.deploy.ID)
	}
	if d.Status == "" {
		t.Error("the status carries no status")
	}
	if len(d.NextActions) == 0 {
		t.Error("the status says nothing about what may be done next")
	}

	status := s.must(t, "status", string(seeded.deploy.ID))
	for _, want := range []string{string(seeded.deploy.ID), d.Status, "next:"} {
		if !strings.Contains(status, want) {
			t.Errorf("the text output does not mention %q:\n%s", want, status)
		}
	}
}

// A plan says which of the three things it is — allowed, waiting on an
// approval, or refused — in the first line and in a field. The two middle cases
// differ by what the operator does next, which is why they are not one.
func TestAPlanSaysWhichOfTheThreeThingsItIs(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seed := s.seed(t)

	gated := policy.Decision{
		Reasons: []policy.Reason{{Code: "needs_approval", Message: "production is gated"}},
		Requirements: []policy.Requirement{{
			Type:   policy.RequireApproval,
			Role:   "release-manager",
			Count:  1,
			Detail: "production changes need a release manager",
		}},
		Risk: policy.RiskAssessment{
			Level:   policy.RiskHigh,
			Score:   0.8,
			Factors: []policy.RiskFactor{{Code: "blast_radius", Message: "every checkout request"}},
		},
	}
	blocked := policy.Decision{
		Reasons: []policy.Reason{{Code: "change_window", Message: "outside the window"}},
		Risk: policy.RiskAssessment{
			Level:   policy.RiskHigh,
			Score:   0.9,
			Factors: []policy.RiskFactor{{Code: "unattended", Message: "nobody is on call"}},
		},
	}

	for _, tc := range []struct {
		name     string
		decision policy.Decision
		verdict  string
		approval bool
	}{
		{"allowed", policy.Decision{
			Allowed: true,
			Risk:    policy.RiskAssessment{Level: policy.RiskLow, Score: 0.1},
		}, "allowed", false},
		{"gated", gated, "needs approval", true},
		{"blocked", blocked, "blocked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := s.storePlan(t, seed, tc.decision, nil)

			got := answered[cliapi.PlanView](t, s, "plan", "--explain", id)
			if got.Policy.Allowed != tc.decision.Allowed {
				t.Errorf("allowed = %v, want %v", got.Policy.Allowed, tc.decision.Allowed)
			}
			if got.ApprovalRequired != tc.approval {
				t.Errorf("approval_required = %v, want %v", got.ApprovalRequired, tc.approval)
			}

			text := s.must(t, "plan", "--explain", id)
			first, _, _ := strings.Cut(text, "\n")
			if !strings.Contains(first, tc.verdict) {
				t.Errorf("the plan leads with %q, want %q:\n%s", first, tc.verdict, text)
			}
			for _, r := range tc.decision.Reasons {
				if !strings.Contains(text, r.Message) {
					t.Errorf("the plan does not say why: %q\n%s", r.Message, text)
				}
			}
			for _, r := range tc.decision.Requirements {
				if !strings.Contains(text, r.Detail) {
					t.Errorf("the plan does not say what it needs: %q\n%s", r.Detail, text)
				}
			}
		})
	}
}

// INV-012: a change to a sensitive field is reported as having happened and
// never as what it became. The row stays so that nobody mistakes a withheld
// value for a field that did not change.
func TestASensitiveChangeIsNamedButNeverShown(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seed := s.seed(t)

	// Deliberately not shaped like the credential it stands for: a real-looking
	// connection string in a test file is a secret scanner's finding for as long
	// as the file exists.
	const was = "THE-VALUE-NO-PLAN-READER-SEES"

	id := s.storePlan(t, seed,
		policy.Decision{Allowed: true, Risk: policy.RiskAssessment{Level: policy.RiskLow}},
		[]plan.PlannedOperation{{
			ID:      "op-1",
			Kind:    plan.OperationApply,
			Target:  "api",
			Summary: "roll the api deployment forward",
			Diff: plan.Diff{Changes: []plan.Change{
				{
					Path: "spec.template.spec.containers[0].image",
					From: "registry.example.com/checkout:1.4.0",
					To:   "registry.example.com/checkout:2.0.0",
				},
				{Path: "env.DATABASE_URL", From: was, To: was, Sensitive: true},
			}},
		}},
	)

	for _, format := range []string{"json", "text"} {
		out := s.must(t, "plan", "--explain", id, "--output", format)
		if strings.Contains(out, was) {
			t.Errorf("--output %s showed a sensitive value:\n%s", format, out)
		}
	}

	got := answered[cliapi.PlanView](t, s, "plan", "--explain", id)
	if len(got.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(got.Operations))
	}
	changes := got.Operations[0].Changes
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want both rows kept", changes)
	}
	secret := changes[1]
	if !secret.Sensitive || secret.Path != "env.DATABASE_URL" {
		t.Errorf("the withheld change = %+v", secret)
	}
	if secret.From != "" || secret.To != "" {
		t.Errorf("the withheld change carries values: %+v", secret)
	}
	// The one beside it is shown in full, so withholding is the exception.
	if changes[0].To == "" {
		t.Errorf("an ordinary change was withheld too: %+v", changes[0])
	}
}

// Verifying reports the verdict and where it left the deployment. Both are in
// the one answer because the verdict decides the status, and a caller given
// only one of them would go and fetch the other.
func TestVerifyingReportsTheVerdictAndTheDeploymentTogether(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seed := s.seed(t)

	got := answered[cliapi.VerifyView](t, s, "verify", string(seed.deploy.ID))
	if got.Run.RunID != string(seed.run.ID) {
		t.Errorf("run_id = %q, want %q", got.Run.RunID, seed.run.ID)
	}
	if got.Run.Verdict != string(verifyv1.VerdictPass) {
		t.Errorf("verdict = %q, want %q", got.Run.Verdict, verifyv1.VerdictPass)
	}
	if len(got.Run.Checks) != 1 {
		t.Fatalf("checks = %v, want one", got.Run.Checks)
	}
	check := got.Run.Checks[0]
	if check.Name != "error-rate" || check.Kind != "prometheus" {
		t.Errorf("check = %+v, want the verifier named", check)
	}
	if len(check.Measurements) != 1 || check.Measurements[0].Name != "error_rate" {
		t.Errorf("measurements = %v, want what was measured", check.Measurements)
	}
	if len(check.Evidence) != 1 {
		t.Errorf("evidence = %v, want where to go and look", check.Evidence)
	}

	text := s.must(t, "verify", string(seed.deploy.ID))
	for _, want := range []string{"error-rate", string(verifyv1.VerdictPass), "deployment "} {
		if !strings.Contains(text, want) {
			t.Errorf("the text output does not mention %q:\n%s", want, text)
		}
	}
}

// history answers two questions with one verb, and exactly one of them at a
// time. A document carrying both would say an environment's history contained a
// timeline, which is not the relationship.
func TestHistoryAnswersOneQuestionAtATime(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seeded := s.seed(t)

	deployments := answered[cliapi.HistoryView](t, s, "history", seeded.envID)
	if len(deployments.Deployments) == 0 {
		t.Error("an environment's history lists no deployments")
	}
	if len(deployments.Events) != 0 {
		t.Errorf("an environment's history carries a timeline: %v", deployments.Events)
	}

	timeline := answered[cliapi.HistoryView](t, s, "history", "--deployment", string(seeded.deploy.ID))
	if len(timeline.Events) == 0 {
		t.Error("a deployment's history lists no events")
	}
	if len(timeline.Deployments) != 0 {
		t.Errorf("a deployment's history carries deployments: %v", timeline.Deployments)
	}
}

// A page token is carried out and back in. A listing that reported a cursor it
// would not then accept would strand a script on the first page.
func TestAPageIsAskedForAndCarriedBack(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seeded := s.seed(t)
	for _, version := range []string{"2.0.1", "2.0.2"} {
		s.must(t, "release", "create",
			"--project", seeded.projectID,
			"--version", version,
			"--artifact", "role=app,id="+seeded.artifactID,
			"--source-provider", "git",
			"--source-repository", "example/checkout",
			"--source-revision", strings.Repeat("e", 40),
		)
	}

	first := answered[cliapi.ReleasesView](t, s,
		"release", "list", "--project", seeded.projectID, "--page-size", "1")
	if len(first.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(first.Releases))
	}
	if first.NextPageToken == "" {
		t.Fatal("a truncated listing offered no cursor")
	}

	second := answered[cliapi.ReleasesView](t, s,
		"release", "list", "--project", seeded.projectID,
		"--page-size", "1", "--page-token", first.NextPageToken)
	if len(second.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(second.Releases))
	}
	if second.Releases[0].ReleaseID == first.Releases[0].ReleaseID {
		t.Error("the cursor handed back the page it came from")
	}

	// A person is told how to ask for the rest, because the cursor is only in
	// the machine document.
	text := s.must(t, "release", "list", "--project", seeded.projectID, "--page-size", "1")
	if !strings.Contains(text, "--page-token") {
		t.Errorf("the text listing does not say how to ask for the rest:\n%s", text)
	}
}

// Each write command reaches the method its name promises, carrying the caller
// as the actor. The deployer refuses everything, so these are sent for what
// they record rather than for what they return.
func TestEachWriteCommandReachesItsOwnMethodAsTheCallerWhoSentIt(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())
	seeded := s.seed(t)
	id := string(seeded.deploy.ID)

	if _, err := s.run(t, "promote", id, "--reason", "the canary held"); err == nil {
		t.Fatal("the refusing deployer promoted something")
	}
	if got := s.deployer.promote; string(got.DeploymentID) != id || got.Actor.ID != anyone().ID {
		t.Errorf("promote = %+v, want %s by %s", got, id, anyone().ID)
	}
	if got := s.deployer.promote.Reason; got != "the canary held" {
		t.Errorf("promote reason = %q", got)
	}

	if _, err := s.run(t, "rollback", id, "--reason", "the error rate rose"); err == nil {
		t.Fatal("the refusing deployer rolled something back")
	}
	if got := s.deployer.rollback; string(got.DeploymentID) != id || got.Actor.ID != anyone().ID {
		t.Errorf("rollback = %+v, want %s by %s", got, id, anyone().ID)
	}

	s.must(t, "verify", id)
	if got := s.deployer.verify; string(got.DeploymentID) != id || got.Actor.ID != anyone().ID {
		t.Errorf("verify = %+v, want %s by %s", got, id, anyone().ID)
	}

	if _, err := s.run(t, "deploy", "--plan", string(seeded.plan.ID)); err == nil {
		t.Fatal("the refusing deployer applied something")
	}
	if got := s.deployer.apply; string(got.PlanID) != string(seeded.plan.ID) || got.Actor.ID != anyone().ID {
		t.Errorf("apply = %+v, want %s by %s", got, seeded.plan.ID, anyone().ID)
	}
}

// deploy either applies a plan somebody has read or builds one first. Given a
// plan it must not build a second, because the whole point of being handed an
// id is that what gets applied is the thing that was reviewed.
func TestDeployPlansOnlyWhenItWasNotGivenAPlan(t *testing.T) {
	t.Parallel()

	t.Run("given a plan it applies that one", func(t *testing.T) {
		t.Parallel()
		s := opened(t, everyone())
		seed := s.seed(t)

		if _, err := s.run(t, "deploy", "--plan", string(seed.plan.ID)); err == nil {
			t.Fatal("the refusing deployer applied something")
		}
		if s.deployer.plan.EnvironmentID != "" {
			t.Errorf("deploy built a plan it was handed: %+v", s.deployer.plan)
		}
		if string(s.deployer.apply.PlanID) != string(seed.plan.ID) {
			t.Errorf("apply = %+v, want plan %s", s.deployer.apply, seed.plan.ID)
		}
	})

	t.Run("given an environment it plans first", func(t *testing.T) {
		t.Parallel()
		s := opened(t, everyone())
		seed := s.seed(t)

		_, err := s.run(t, "deploy", seed.envID,
			"--release", string(seed.release), "--strategy", "rolling")
		if err == nil {
			t.Fatal("the refusing deployer planned something")
		}
		if string(s.deployer.plan.EnvironmentID) != seed.envID {
			t.Errorf("plan = %+v, want environment %s", s.deployer.plan, seed.envID)
		}
		if s.deployer.plan.Actor.ID != anyone().ID {
			t.Errorf("plan actor = %q, want %q", s.deployer.plan.Actor.ID, anyone().ID)
		}
		// Nothing was applied, because there is no plan to apply.
		if s.deployer.apply.PlanID != "" {
			t.Errorf("deploy applied %q after failing to plan", s.deployer.apply.PlanID)
		}
	})

	t.Run("given neither it says so", func(t *testing.T) {
		t.Parallel()
		s := opened(t, everyone())

		_, err := s.run(t, "deploy", "--release", "rel_1", "--strategy", "rolling")
		if err == nil {
			t.Fatal("deploy accepted a command naming nothing to deploy to")
		}
		if got := cliapi.Exit(err); got != cliapi.ExitValidation {
			t.Errorf("exit = %d, want %d", got, cliapi.ExitValidation)
		}
	})
}

// Nothing is created twice when the same command is sent twice with the key
// that says so — the retry a script does after a dropped connection gets the
// thing the first call made.
func TestARepeatedCommandCarryingAKeyMakesOneThing(t *testing.T) {
	t.Parallel()
	s := opened(t, everyone())

	args := []string{"project", "create", "--name", "checkout", "--idempotency-key", "k1"}
	first := answered[cliapi.ProjectView](t, s, args...)
	second := answered[cliapi.ProjectView](t, s, args...)

	if first.ProjectID != second.ProjectID {
		t.Errorf("project ids = %q and %q, want one project", first.ProjectID, second.ProjectID)
	}
}

// seeded is a project, an artifact, a release, a plan, the deployment admitted
// for it and one timeline entry — written through the service and the store
// rather than only through commands, because the engine that would produce a
// plan and a deployment is behind the refusing deployer.
type seededShell struct {
	projectID  string
	envID      string
	artifactID string
	release    identity.ReleaseID
	plan       plan.DeploymentPlan
	deploy     deployment.Deployment
	run        verification.Run
}

// storePlan writes a plan carrying the policy decision a test wants to see
// rendered. The engine that would reach one is behind the refusing deployer, so
// what a verdict looks like on the way out is asked of a stored plan.
func (s *shell) storePlan(t *testing.T, seed seededShell, d policy.Decision, ops []plan.PlannedOperation) string {
	t.Helper()
	ctx := context.Background()
	stored, err := s.store.Environments().Get(ctx, identity.EnvironmentID(seed.envID))
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	if ops == nil {
		// A plan with nothing to apply is refused by the domain, so a test that
		// only cares about the verdict still needs something for it to be about.
		ops = []plan.PlannedOperation{{
			ID: "op-1", Kind: plan.OperationApply, Target: "api", Summary: "roll the api forward",
		}}
	}
	p, err := plan.New(identity.NewGenerator(), fixedClock{}, anyone(), time.Hour, plan.DeploymentPlan{
		ProjectID:     identity.ProjectID(seed.projectID),
		EnvironmentID: identity.EnvironmentID(seed.envID),
		ReleaseID:     seed.release,
		BaseRevision:  stored.Revision,
		Strategy:      deployment.StrategyRolling,
		Operations:    ops,
		Policy:        d,
	})
	if err != nil {
		t.Fatalf("plan.New: %v", err)
	}
	if err := s.store.Plans().Create(ctx, p); err != nil {
		t.Fatalf("Plans.Create: %v", err)
	}
	return string(p.ID)
}

func (s *shell) seed(t *testing.T) seededShell {
	t.Helper()
	ctx := context.Background()
	ids := identity.NewGenerator()

	prj := answered[cliapi.ProjectView](t, s,
		"project", "create", "--name", "checkout", "--description", "the checkout service")
	env := answered[cliapi.EnvironmentView](t, s,
		"environment", "create",
		"--project", prj.ProjectID,
		"--name", "production",
		"--kind", "production",
		"--target", "name=api,driver=kubernetes",
		"--policy", "name=change-window,ref=policy://change-window,mode=enforce",
	)

	digest := "sha256:" + strings.Repeat("c", 64)
	art := answered[cliapi.ArtifactView](t, s,
		"release", "artifact", "register",
		"--project", prj.ProjectID,
		"--kind", "oci-image",
		"--digest", digest,
		"--locator", "registry.example.com/checkout@"+digest,
		"--media-type", "application/vnd.oci.image.manifest.v1+json",
	)
	rel := answered[cliapi.ReleaseView](t, s,
		"release", "create",
		"--project", prj.ProjectID,
		"--version", "2.0.0",
		"--artifact", "role=app,id="+art.ArtifactID,
		"--source-provider", "git",
		"--source-repository", "example/checkout",
		"--source-revision", strings.Repeat("d", 40),
		"--source-ref", "refs/heads/main",
	)

	// The revision a plan pins itself to is the stored one. What the create
	// command printed describes the environment it made, not the row a
	// concurrent write would have to beat.
	stored, err := s.store.Environments().Get(ctx, identity.EnvironmentID(env.EnvironmentID))
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}

	p, err := plan.New(ids, fixedClock{}, anyone(), time.Hour, plan.DeploymentPlan{
		ProjectID:     identity.ProjectID(prj.ProjectID),
		EnvironmentID: identity.EnvironmentID(env.EnvironmentID),
		ReleaseID:     identity.ReleaseID(rel.ReleaseID),
		BaseRevision:  stored.Revision,
		Strategy:      deployment.StrategyRolling,
		Operations: []plan.PlannedOperation{{
			ID:      "op-1",
			Kind:    plan.OperationApply,
			Target:  "api",
			Summary: "roll the api deployment forward",
		}},
		Policy: policy.Decision{
			Allowed: true,
			Risk:    policy.RiskAssessment{Level: policy.RiskLow, Score: 0.1},
		},
	})
	if err != nil {
		t.Fatalf("plan.New: %v", err)
	}
	if err := s.store.Plans().Create(ctx, p); err != nil {
		t.Fatalf("Plans.Create: %v", err)
	}

	d, err := deployment.New(ids, fixedClock{}, anyone(), deployment.Deployment{
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		PlanID:        p.ID,
		Strategy:      deployment.StrategyRolling,
		Trigger:       deployment.Trigger{Type: deployment.TriggerAPI, Detail: "rollops deploy"},
	})
	if err != nil {
		t.Fatalf("deployment.New: %v", err)
	}
	if d.Revision, err = s.store.Deployments().Create(ctx, d); err != nil {
		t.Fatalf("Deployments.Create: %v", err)
	}

	vrun, err := verification.New(ids, fixedClock{}, anyone(), verification.Run{
		DeploymentID: d.ID,
		PlanID:       p.ID,
		StartedAt:    at.Add(-10 * time.Minute),
		Checks: []verification.Check{{
			Verifier: verifyv1.VerifierMetadata{Kind: "prometheus", Name: "error-rate", Version: "1.2.0"},
			Result: verifyv1.VerificationResult{
				Verdict:      verifyv1.VerdictPass,
				Reason:       "the error rate stayed under the threshold",
				Measurements: []verifyv1.Measurement{{Name: "error_rate", Value: 0.004}},
				Evidence:     []verifyv1.EvidenceRef{{Kind: "url", URI: "https://grafana.example/d/abc"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("verification.New: %v", err)
	}
	if err := s.store.VerificationRuns().Create(ctx, vrun); err != nil {
		t.Fatalf("VerificationRuns.Create: %v", err)
	}
	s.deployer.verdict = &deploy.Verification{ID: vrun.ID, Verdict: verifyv1.VerdictPass}

	ev, err := event.New(ids, fixedClock{}, anyone(), event.Event{
		Type:          event.DeploymentQueued,
		AggregateType: event.AggregateDeployment,
		AggregateID:   string(d.ID),
		Payload:       json.RawMessage(`{"strategy":"rolling"}`),
	})
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	err = s.store.WithinTransaction(ctx, func(ctx context.Context) error {
		_, err := s.store.Events().Append(ctx, ev)
		return err
	})
	if err != nil {
		t.Fatalf("Events.Append: %v", err)
	}

	return seededShell{
		projectID:  prj.ProjectID,
		envID:      env.EnvironmentID,
		artifactID: art.ArtifactID,
		release:    identity.ReleaseID(rel.ReleaseID),
		plan:       p,
		deploy:     d,
		run:        vrun,
	}
}

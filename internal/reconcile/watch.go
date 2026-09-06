package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/depgraph"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/git"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store"
)

var ErrNotLeader = errors.New("watch: not reconcile leader")

// RepoSpec declares one watched repo: where it lives, how to authenticate, the
// config location within it, and the identity reconciles are attributed to.
type RepoSpec struct {
	Name      string
	URL       string
	Ref       config.RepoRef // branch + path (defaults applied)
	Auth      git.Auth
	Initiator rollout.Identity
}

// Watcher watches N repos and reconciles each on every tick — the always-on
// brain. Each tick pulls the latest desired state from Git (poll, which
// doubles as the drift heartbeat) and reconciles. A GitHub HMAC webhook
// calls TickHint for the matching repo (or every watched repo when the
// payload does not name one). Poll remains the safety net. Repos are
// independent and serialized per repo.
type Watcher struct {
	rec       *Reconciler
	baseDir   string
	repos     []watched
	locks     *repoLocks
	leases    store.LeaseStore
	owner     string
	leaseTTL  time.Duration
	now       func() time.Time
	logf      func(format string, args ...any)
	imageAuto *ImageAuto

	// preflight asks every target in a repo whether its apply would be
	// accepted, and refuses the whole tick if any says no. Default ON: the
	// alternative is a batch that applies half of itself, which is how a
	// dangling Ingress→Middleware reference took an apex domain to 404.
	//
	// WithoutPreflight exists for the case where the extra server dry-run per
	// target per tick is genuinely unaffordable, not as a default.
	preflight bool

	// awaiting counts CONSECUTIVE cycles a config has spent waiting on Git,
	// keyed by "repo/path". See stuckAfterCycles.
	awaiting map[string]int

	// orphans notices a RolloutConfig that stopped being declared. See reap.go
	// and #154: deleting a config removes the target that --prune runs as part
	// of, so the one way to retire a service is the one way prune cannot cover.
	orphans *reaper
	// lastSeen keeps the config a key was last declared with, so a vanished
	// target can still be described after its file is gone. Bounded by the
	// number of configs the watched repos declare.
	lastSeen map[string]*config.Config

	// drifting counts consecutive ticks a target has reported drift it was not
	// allowed to correct. Under `verification: detect` the alert is the whole
	// mechanism, and one alert per tick reads the same on day one as on day
	// twenty. See driftStreak.
	drifting *driftStreak

	// failures counts consecutive IDENTICAL apply/preflight errors per key.
	// Forbidden is not transient (#182): escalate once, then hold rather than
	// logging the same refusal every reconcile interval forever. See failStreak.
	failures *failStreak
}

// stuckAfterCycles is how many consecutive waiting cycles turn a proposal from
// "in flight" into something worth acting on.
//
// #98 watched a target report the same idle-looking verdict for ~1,100
// consecutive reconciles over 18.5 hours while serving a stale image. Naming the
// wait every cycle (which the AwaitingGit branch below does) makes it visible,
// but cycle 1 and cycle 1,100 still read identically — and it is the second one
// that means something is wrong. A count is what separates them.
//
// Ten cycles is the default reconcile interval (60s) times ten: long enough that
// a proposal which auto-merges in seconds never trips it, short enough that a
// stuck one is named in minutes rather than after somebody fetches the site and
// notices an article missing.
const stuckAfterCycles = 10

type watched struct {
	spec RepoSpec
	src  *git.Source
}

type WatcherOption func(*Watcher)

// WithoutPreflight disables the batch preflight.
//
// Off means a target that cannot apply is discovered halfway through the
// batch, with the targets before it already applied — the half-applied state
// this check exists to prevent. Use it only where the extra server-side dry
// run per target per tick is measurably unaffordable.
func WithoutPreflight() WatcherOption {
	return func(w *Watcher) { w.preflight = false }
}

func WithLeaderElection(leases store.LeaseStore, owner string, ttl time.Duration) WatcherOption {
	return func(w *Watcher) {
		w.leases = leases
		w.owner = owner
		w.leaseTTL = ttl
	}
}

// WithLogger sets a logger for per-tick reconcile outcomes (errors, applied
// drifts). Without it, the loop reconciles silently.
func WithLogger(logf func(format string, args ...any)) WatcherOption {
	return func(w *Watcher) { w.logf = logf }
}

// WithImageAutomation enables registry-poll image automation: before each
// reconcile, configs with an imagePolicy are scanned for newer tags and bumped
// back to Git.
func WithImageAutomation(ia *ImageAuto) WatcherOption {
	return func(w *Watcher) { w.imageAuto = ia }
}

// NewWatcher clones each repo into baseDir and returns a ready watcher.
func NewWatcher(ctx context.Context, rec *Reconciler, baseDir string, specs []RepoSpec, opts ...WatcherOption) (*Watcher, error) {
	// preflight defaults ON — see the field comment.
	w := &Watcher{rec: rec, baseDir: baseDir, locks: newRepoLocks(), owner: "watcher", leaseTTL: 2 * time.Minute, now: time.Now, preflight: true,
		orphans: newReaper(stuckAfterCycles), lastSeen: map[string]*config.Config{}}
	for _, opt := range opts {
		opt(w)
	}
	for _, s := range specs {
		s.Ref = s.Ref.WithDefaults()
		dir := filepath.Join(baseDir, s.Name)
		src, err := git.Clone(ctx, s.URL, s.Ref.Branch, dir, s.Auth)
		if err != nil {
			// One unreachable repo must not take the whole daemon down: log and
			// skip it; the other repos still reconcile. It is retried at restart.
			if w.logf != nil {
				w.logf("watch: skip repo %q (clone failed): %v", s.Name, err)
			}
			continue
		}
		w.repos = append(w.repos, watched{spec: s, src: src})
	}
	return w, nil
}

// AddExisting registers a repo whose working tree is already present (used in
// tests and for pre-cloned trees).
func (w *Watcher) AddExisting(s RepoSpec, src *git.Source) {
	s.Ref = s.Ref.WithDefaults()
	w.repos = append(w.repos, watched{spec: s, src: src})
}

// RepoOutcome is the result of reconciling one watched repo on a tick.
type RepoOutcome struct {
	Repo    string
	Changed bool // git HEAD moved this tick
	Outcome Outcome
	Err     error
}

// Tick pulls and reconciles every watched repo once. Per-repo errors are
// captured in the result, not fatal to the others.
func (w *Watcher) Tick(ctx context.Context) []RepoOutcome {
	return w.TickHint(ctx, "")
}

// TickHint is Tick filtered by a GitHub webhook repo hint (owner/repo or a
// remote URL). An empty or unmatched hint ticks every watched repo — still
// bounded by the watch list, never an unbounded fan-out.
func (w *Watcher) TickHint(ctx context.Context, repoHint string) []RepoOutcome {
	if w.leases != nil {
		ok, err := w.leases.AcquireLease(ctx, "reconcile:leader", w.owner, w.leaseTTL, w.now().UTC())
		if err != nil {
			return []RepoOutcome{{Err: fmt.Errorf("watch: acquire leader lease: %w", err)}}
		}
		if !ok {
			return []RepoOutcome{{Err: ErrNotLeader}}
		}
	}
	repos := w.matching(repoHint)
	out := make([]RepoOutcome, 0, len(repos))
	for _, r := range repos {
		out = append(out, w.tickOne(ctx, r)...)
	}
	return out
}

func (w *Watcher) matching(hint string) []watched {
	if strings.TrimSpace(hint) == "" {
		return w.repos
	}
	matched := make([]watched, 0, 1)
	for _, r := range w.repos {
		if git.SameRepo(r.spec.URL, hint) {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return w.repos
	}
	return matched
}

func (w *Watcher) tickOne(ctx context.Context, r watched) []RepoOutcome {
	release, ok := w.locks.tryAcquire(r.spec.Name)
	if !ok {
		return []RepoOutcome{{Repo: r.spec.Name, Err: fmt.Errorf("watch: repo %s busy", r.spec.Name)}}
	}
	defer release()

	changed, _, err := r.src.Pull(ctx)
	if err != nil {
		return []RepoOutcome{{Repo: r.spec.Name, Err: fmt.Errorf("watch: pull: %w", err)}}
	}
	// A repo path may address a single config file or a directory of them
	// (manage many apps from one repo). Each config reconciles independently.
	configs, err := config.LoadAllFromDir(r.src.Dir(), r.spec.Ref)
	if err != nil {
		// Tell the reaper the load failed BEFORE returning. A tick that could
		// not read the repo taught us nothing about what it declares, and
		// silence here would let the next successful tick read the gap as a
		// deletion.
		w.ensureOrphanState()
		w.orphans.observe(r.spec.Name, nil, err)
		return []RepoOutcome{{Repo: r.spec.Name, Changed: changed, Err: fmt.Errorf("watch: load config: %w", err)}}
	}
	// Guard against duplicate target refs: drift state is keyed by target ref,
	// so two configs sharing a ref flip-flop (each reconcile overwrites the
	// other's observed fingerprint → perpetual false drift). Keep the first and
	// skip the rest with a loud warning so the collision is fixed, not silent.
	seenRef := make(map[string]string, len(configs))
	deduped := make([]config.NamedConfig, 0, len(configs))
	for _, nc := range configs {
		ref := nc.Config.Spec.Target.Ref
		if prev, dup := seenRef[ref]; dup {
			if w.logf != nil {
				w.logf("watch %s: skipping %s — duplicate target ref %q already declared by %s", r.spec.Name, nc.Path, ref, prev)
			}
			continue
		}
		seenRef[ref] = nc.Path
		deduped = append(deduped, nc)
	}
	configs = deduped

	// Orphan check on the COMPLETE, deduped set: what this repo declares now.
	// Runs before reconciling so a retirement is named on the same tick it is
	// noticed, rather than after every rollout in the repo has finished.
	w.ensureOrphanState()
	keys := make([]string, 0, len(configs))
	for _, nc := range configs {
		keys = append(keys, nc.Path)
		w.lastSeen[r.spec.Name+"/"+nc.Path] = nc.Config
	}
	for _, v := range w.orphans.observe(r.spec.Name, keys, nil) {
		w.reportOrphan(v)
	}

	// Two phases, deliberately DECOUPLED. Image automation for EVERY config first
	// (a fast registry scan + git commit), then reconcile every config (a
	// health-gated progressive rollout that can block for minutes). Interleaving
	// them per config — bump→deploy, bump→deploy — let a slow or stuck rollout for
	// one config STARVE the image bumps of the configs after it in the loop, so a
	// freshly-published tag could sit undeployed indefinitely (observed: one app
	// never auto-bumped while its siblings did, because a sibling's rollout kept
	// blocking ahead of it). Detecting a new tag and committing it to Git must not
	// depend on any other target's rollout finishing.
	cfgs := make([]*config.Config, len(configs))
	// Per-config image-automation decisions for a coverage summary. Silence used to
	// hide a starved config: an app that was never considered looked identical to
	// one that was already current (both logged nothing). The summary names EVERY
	// config and its outcome each tick, so a missing or stuck target is visible at
	// a glance — while bumps/errors still get their own detailed line.
	decisions := make([]string, 0, len(configs))
	for i, nc := range configs {
		cfg := nc.Config
		name := nc.Config.Metadata.Name
		if name == "" {
			name = nc.Path
		}
		// Best-effort: a scan/push failure is logged but never blocks reconciling
		// desired state (managing the app matters more than the image bump).
		if w.imageAuto != nil {
			bumped, ref, status, ierr := w.imageAuto.Process(ctx, r.src, nc)
			switch {
			case ierr != nil:
				decisions = append(decisions, name+"="+string(ImageOutcomeError))
				if w.logf != nil {
					w.logf("image automation %s/%s: %v (continuing)", r.spec.Name, nc.Path, ierr)
				}
			case status.Outcome.Deployed():
				w.clearAwaiting(r.spec.Name, nc.Path)
				cfg = bumped
				decisions = append(decisions, name+"="+string(status.Outcome)+status.Short())
				if w.logf != nil {
					w.logf("image automation %s/%s: bumped to %s", r.spec.Name, nc.Path, ref)
				}
			case status.Outcome.AwaitingGit():
				cycles := w.noteAwaiting(r.spec.Name, nc.Path)
				// A newer image exists that Git has not adopted. This used to be
				// reported as `current` — the same word as "nothing to do" — so a
				// proposal that never merged looked identical to a healthy target
				// for as long as nobody compared the running image by hand. Name it
				// every cycle so the wait is visible while it is still short.
				decisions = append(decisions, name+"="+string(status.Outcome)+status.Short())
				if w.logf != nil {
					w.logf("image automation %s/%s: %s — a newer image is waiting on Git, not deployed (registry offers %s, Git pins %s)",
						r.spec.Name, nc.Path, status.Outcome, shortDigest(status.Resolved), shortDigest(status.Pinned))
					// Escalate once the wait stops looking like one. Repeating the
					// line above forever is what made an 18-hour stall read the same
					// as a proposal opened a minute ago (#98).
					if cycles >= stuckAfterCycles {
						w.logf("image automation %s/%s: STUCK — %s for %d consecutive reconciles; the registry has offered %s that long and Git still pins %s. Check the proposal merged.",
							r.spec.Name, nc.Path, status.Outcome, cycles,
							shortDigest(status.Resolved), shortDigest(status.Pinned))
					}
				}
			default:
				// current / disabled. current carries what was resolved, so a
				// stale resolution is visible rather than implied: a verdict
				// recorded without its observation cannot be checked later.
				w.clearAwaiting(r.spec.Name, nc.Path)
				decisions = append(decisions, name+"="+string(status.Outcome)+status.Short())
			}
		}
		cfgs[i] = cfg
	}
	if w.imageAuto != nil && w.logf != nil && len(decisions) > 0 {
		w.logf("image automation %s: %d config(s) [%s]", r.spec.Name, len(decisions), strings.Join(decisions, " "))
	}

	ordered := orderByDependsOn(configs, cfgs)

	// Preflight the WHOLE batch before applying any of it.
	//
	// Reconciling each target independently let a batch apply half of itself. A
	// repository declared an Ingress and the Traefik Middleware its
	// router.middlewares annotation named; the Ingress applied, the Middleware
	// failed on RBAC the service account did not hold, and only the
	// Middleware's own target rolled back. Traefik could not resolve the
	// dangling reference, never built the router, and the apex domain served
	// 404 until the Middleware was applied by hand.
	//
	// Neither target was wrong on its own terms — one applied, one rolled back
	// cleanly. The breakage lived in the relationship between them, and nothing
	// modelled it.
	//
	// Compensating rollback cannot fix that: rolling back a CREATE means
	// deleting the resource, and rollopsd deliberately holds no delete verb on
	// the types where removal is destructive (a PVC loses data, a CronJob
	// silently drops scheduled work, a Middleware is often the only thing
	// enforcing a security control). So the batch fails BEFORE it applies
	// anything rather than unwinding afterwards.
	//
	// Refusing the tick is safe: nothing was applied, the previously-live state
	// is untouched. A permanent refusal (Forbidden) is held after the first
	// escalation — retrying RBAC every interval cannot heal it (#182). Anything
	// else retries on the next tick.
	w.ensureOrphanState()
	if changed {
		// Desired state moved: a previously permanent refusal deserves another look.
		w.failures.clearRepo(r.spec.Name)
	}
	if w.preflight {
		if msg, held := w.failures.held(r.spec.Name); held {
			return []RepoOutcome{{Repo: r.spec.Name, Changed: changed, Err: fmt.Errorf("%s", msg)}}
		}
		pcfgs := make([]*config.Config, 0, len(ordered))
		for _, p := range ordered {
			pcfgs = append(pcfgs, p.cfg)
		}
		root := r.src.Dir()
		if errs := w.rec.eng.Preflight(engine.WithRoot(ctx, root), pcfgs); len(errs) > 0 {
			msgs := make([]string, 0, len(errs))
			for _, e := range errs {
				msgs = append(msgs, e.Error())
			}
			err := fmt.Errorf(
				"preflight refused %s: %d of %d target(s) would not apply, so NONE were applied — %s",
				r.spec.Name, len(errs), len(ordered), strings.Join(msgs, "; "))
			// Logging is owned by logOutcomes via failStreak — logging here as
			// well would double every fresh refusal and undo the quiet hold.
			return []RepoOutcome{{Repo: r.spec.Name, Changed: changed, Err: err}}
		}
		w.failures.clear(r.spec.Name)
	}

	out := make([]RepoOutcome, 0, len(configs))
	for _, p := range ordered {
		key := r.spec.Name + "/" + p.nc.Path
		if wait, werr := w.rec.waitingOn(ctx, p.cfg); werr != nil {
			out = append(out, RepoOutcome{Repo: key, Changed: changed, Err: werr})
			continue
		} else if wait != "" {
			if w.logf != nil {
				w.logf("reconcile %s/%s: skipping this tick — dependsOn %q is not promoted", r.spec.Name, p.nc.Path, wait)
			}
			out = append(out, RepoOutcome{Repo: key, Changed: changed})
			continue
		}
		if msg, held := w.failures.held(key); held {
			out = append(out, RepoOutcome{Repo: key, Changed: changed, Err: fmt.Errorf("%s", msg)})
			continue
		}
		// Relative referenced manifest sources resolve against the config file's
		// own directory within the checkout — not the daemon CWD — so a rendered
		// kustomize/helm/path points at the polled desired state.
		root := filepath.Join(r.src.Dir(), filepath.Dir(p.nc.Path))
		o, rerr := w.rec.Reconcile(engine.WithRoot(ctx, root), p.cfg, r.spec.Initiator)
		// Only an error-free reconcile says anything about drift. A failed one
		// did not observe the target, so it must not extend OR reset a streak.
		if rerr == nil {
			w.failures.clear(key)
			if n := w.drifting.observe(key, o.Drift && !o.Reconciled); n > 0 {
				w.reportPersistentDrift(key, p.cfg, n)
			}
		}
		out = append(out, RepoOutcome{Repo: key, Changed: changed, Outcome: o, Err: rerr})
	}
	return out
}

type cfgPair struct {
	nc  config.NamedConfig
	cfg *config.Config
}

// orderByDependsOn topological-sorts configs in a repo so a dependency is
// reconciled before its dependents in the same tick. A cycle keeps file order.
func orderByDependsOn(ncs []config.NamedConfig, cfgs []*config.Config) []cfgPair {
	pairs := make([]cfgPair, 0, len(ncs))
	byRef := make(map[string]cfgPair, len(ncs))
	nodes := make([]string, 0, len(ncs))
	var deps []rollout.Dependency
	for i, nc := range ncs {
		p := cfgPair{nc: nc, cfg: cfgs[i]}
		pairs = append(pairs, p)
		ref := cfgs[i].Spec.Target.Ref
		byRef[ref] = p
		nodes = append(nodes, ref)
		for _, d := range cfgs[i].Spec.DependsOn {
			deps = append(deps, rollout.Dependency{From: d, To: ref})
		}
	}
	if len(deps) == 0 {
		return pairs
	}
	layers, err := depgraph.New(nodes, deps).Layers()
	if err != nil {
		return pairs
	}
	out := make([]cfgPair, 0, len(pairs))
	seen := make(map[string]bool, len(pairs))
	for _, layer := range layers {
		for _, ref := range layer {
			p, ok := byRef[ref]
			if !ok || seen[ref] {
				continue
			}
			out = append(out, p)
			seen[ref] = true
		}
	}
	for _, p := range pairs {
		if !seen[p.cfg.Spec.Target.Ref] {
			out = append(out, p)
		}
	}
	return out
}

// Run reconciles all repos on each interval tick until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	w.logOutcomes(w.Tick(ctx)) // reconcile immediately on start
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.logOutcomes(w.Tick(ctx))
		}
	}
}

// logOutcomes surfaces each tick's result so an operator sees reconcile errors
// and applied drifts instead of a silent loop. Not-leader is expected on the
// non-leader instances, so it is left quiet.
func (w *Watcher) logOutcomes(outcomes []RepoOutcome) {
	if w.logf == nil {
		return
	}
	w.ensureOrphanState()
	for _, o := range outcomes {
		switch {
		case o.Err != nil && !errors.Is(o.Err, ErrNotLeader):
			switch w.failures.note(o.Repo, o.Err.Error()) {
			case failEscalate:
				if permanentFailure(o.Err.Error()) {
					w.logf("reconcile %s: PERMANENT — %v (not retrying until desired state changes or the process restarts)", o.Repo, o.Err)
					w.reportPermanentFailure(o.Repo, o.Err.Error())
				} else {
					w.logf("reconcile %s: still failing after %d identical ticks — %v", o.Repo, stuckAfterCycles, o.Err)
				}
			case failFresh:
				w.logf("reconcile %s: %v", o.Repo, o.Err)
			case failQuiet:
				// Same refusal already named. Silence is the feature (#182).
			}
		case o.Outcome.Reconciled && o.Outcome.Rollout != nil:
			w.failures.clear(o.Repo)
			w.logf("reconcile %s: applied %s → %s (%s)", o.Repo, o.Outcome.Rollout.TargetRef, o.Outcome.Rollout.Phase, o.Outcome.Plan.Summary)
		case o.Outcome.Drift && o.Outcome.Rollout != nil:
			w.failures.clear(o.Repo)
			w.logf("reconcile %s: drift on %s → %s", o.Repo, o.Outcome.Rollout.TargetRef, o.Outcome.Rollout.Phase)
		}
	}
}

// repoLocks serializes reconciles per repo (independent repos run in parallel up
// to the caller's scheduling).
type repoLocks struct {
	mu   chan struct{}
	held map[string]bool
}

func newRepoLocks() *repoLocks {
	l := &repoLocks{mu: make(chan struct{}, 1), held: map[string]bool{}}
	l.mu <- struct{}{}
	return l
}

func (l *repoLocks) tryAcquire(key string) (func(), bool) {
	<-l.mu
	if l.held[key] {
		l.mu <- struct{}{}
		return nil, false
	}
	l.held[key] = true
	l.mu <- struct{}{}
	return func() {
		<-l.mu
		delete(l.held, key)
		l.mu <- struct{}{}
	}, true
}

// noteAwaiting records another consecutive cycle spent waiting on Git and
// returns the running count.
//
// Deliberately consecutive rather than cumulative: a target that alternates
// between waiting and deploying is working, and should never accumulate its way
// into an alert. Only an unbroken run means nothing is moving.
func (w *Watcher) noteAwaiting(repo, path string) int {
	if w.awaiting == nil {
		w.awaiting = make(map[string]int)
	}
	k := repo + "/" + path
	w.awaiting[k]++
	return w.awaiting[k]
}

// clearAwaiting resets the streak once a config reaches an outcome that is
// genuinely idle or genuinely done.
func (w *Watcher) clearAwaiting(repo, path string) {
	if w.awaiting == nil {
		return
	}
	delete(w.awaiting, repo+"/"+path)
}

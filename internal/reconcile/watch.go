package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/config"
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
// brain. Each tick pulls the latest desired state from Git (immediate via
// webhook, periodic via this poll which doubles as the drift heartbeat) and
// reconciles. Repos are independent and serialized per repo.
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

	// awaiting counts CONSECUTIVE cycles a config has spent waiting on Git,
	// keyed by "repo/path". See stuckAfterCycles.
	awaiting map[string]int
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
	w := &Watcher{rec: rec, baseDir: baseDir, locks: newRepoLocks(), owner: "watcher", leaseTTL: 2 * time.Minute, now: time.Now}
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
	if w.leases != nil {
		ok, err := w.leases.AcquireLease(ctx, "reconcile:leader", w.owner, w.leaseTTL, w.now().UTC())
		if err != nil {
			return []RepoOutcome{{Err: fmt.Errorf("watch: acquire leader lease: %w", err)}}
		}
		if !ok {
			return []RepoOutcome{{Err: ErrNotLeader}}
		}
	}
	out := make([]RepoOutcome, 0, len(w.repos))
	for _, r := range w.repos {
		out = append(out, w.tickOne(ctx, r)...)
	}
	return out
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

	out := make([]RepoOutcome, 0, len(configs))
	for i, nc := range configs {
		// Relative referenced manifest sources resolve against the config file's
		// own directory within the checkout — not the daemon CWD — so a rendered
		// kustomize/helm/path points at the polled desired state.
		root := filepath.Join(r.src.Dir(), filepath.Dir(nc.Path))
		o, rerr := w.rec.Reconcile(engine.WithRoot(ctx, root), cfgs[i], r.spec.Initiator)
		out = append(out, RepoOutcome{Repo: r.spec.Name + "/" + nc.Path, Changed: changed, Outcome: o, Err: rerr})
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
	for _, o := range outcomes {
		switch {
		case o.Err != nil && !errors.Is(o.Err, ErrNotLeader):
			w.logf("reconcile %s: %v", o.Repo, o.Err)
		case o.Outcome.Reconciled && o.Outcome.Rollout != nil:
			w.logf("reconcile %s: applied %s → %s (%s)", o.Repo, o.Outcome.Rollout.TargetRef, o.Outcome.Rollout.Phase, o.Outcome.Plan.Summary)
		case o.Outcome.Drift && o.Outcome.Rollout != nil:
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

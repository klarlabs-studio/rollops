package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"go.klarlabs.de/rollops/internal/config"
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
}

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
			return nil, fmt.Errorf("watch: clone %s: %w", s.Name, err)
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
	out := make([]RepoOutcome, 0, len(configs))
	for _, nc := range configs {
		cfg := nc.Config
		// Image automation runs first: a bump is committed+pushed to Git, and the
		// returned (bumped) config is what reconcile then deploys. Best-effort: a
		// scan/push failure is logged but never blocks reconciling desired state
		// (managing the app matters more than the image bump).
		if w.imageAuto != nil {
			bumped, ref, ierr := w.imageAuto.Process(ctx, r.src, nc)
			switch {
			case ierr != nil:
				if w.logf != nil {
					w.logf("image automation %s/%s: %v (continuing reconcile)", r.spec.Name, nc.Path, ierr)
				}
			case ref != "":
				cfg = bumped
				if w.logf != nil {
					w.logf("image automation %s/%s: bumped to %s", r.spec.Name, nc.Path, ref)
				}
			}
		}
		o, rerr := w.rec.Reconcile(ctx, cfg, r.spec.Initiator)
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

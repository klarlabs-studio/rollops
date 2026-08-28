package reconcile

// Orphan detection: notice when a RolloutConfig stops being declared.
//
// #154: a service was retired by deleting its manifests. Its Deployment and
// Service kept running for 19 hours, serving nothing, described by no file in
// the repository. Nothing errored and nothing warned.
//
// `--prune` cannot cover this. Pruning runs as part of APPLYING a target, so it
// needs the target to still exist; deleting the declaration removes the very
// thing that would clean up after it. The gap is not in prune.go — it is that
// nothing compares one tick's config set against the last, so a config that
// disappears becomes an absence, and absences do not reconcile.
//
// This is that comparison, kept separate from the watcher and free of I/O so
// the decision to retire something is testable on its own.

import (
	"context"
	"fmt"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// reaper tracks which target keys each repo declared, and how many consecutive
// error-free ticks a previously-declared key has been missing.
type reaper struct {
	// after is how many consecutive absent ticks turn "not in this commit" into
	// a verdict. See stuckAfterCycles for why ten.
	after int
	// seen is the set of keys each repo declared on its last error-free tick.
	seen map[string]map[string]bool
	// absent counts consecutive error-free ticks a key has been missing.
	absent map[string]int
	// fired marks keys already reported, so a target that stays deleted
	// produces one verdict rather than one per tick forever.
	fired map[string]bool
}

// verdict names a target that has been absent long enough to act on.
type verdict struct {
	Repo string
	// Key is "repo/path" — the same identity the awaiting counters use.
	Key string
	// Ticks is how many consecutive error-free ticks it was missing.
	Ticks int
}

func newReaper(after int) *reaper {
	return &reaper{
		after:  after,
		seen:   map[string]map[string]bool{},
		absent: map[string]int{},
		fired:  map[string]bool{},
	}
}

// observe records one tick's outcome for a repo and returns the targets that
// have now been absent long enough to act on.
//
// loadErr is the crux. A tick that failed to load taught us nothing about what
// exists, so it must not advance absence — and must not merely pause it either.
// Pausing would let an intermittently-failing repo accumulate absences across
// outages and retire a target that was present every time we actually looked.
// So an error RESETS progress for that repo. The cost is a delayed reap after a
// flapping repo settles; the alternative is deleting live workloads because a
// clone failed.
//
// This is affordable because config.LoadAllFromDir is all-or-nothing: any
// per-file read or parse error aborts the whole load, and an empty directory
// errors rather than returning zero configs. A successful load is therefore the
// COMPLETE set of what the repo declares, never a partial one — which is what
// makes "declared last tick, not declared now" mean what it says.
func (r *reaper) observe(repo string, keys []string, loadErr error) []verdict {
	if loadErr != nil {
		for k := range r.seen[repo] {
			delete(r.absent, k)
		}
		return nil
	}

	present := make(map[string]bool, len(keys))
	for _, k := range keys {
		present[repo+"/"+k] = true
	}

	var out []verdict
	for key := range r.seen[repo] {
		if present[key] {
			// Back before the threshold, or never gone. Either way the run of
			// absences is broken, and a target that returns must be able to
			// vanish again later and be judged afresh.
			delete(r.absent, key)
			delete(r.fired, key)
			continue
		}
		r.absent[key]++
		if r.absent[key] >= r.after && !r.fired[key] {
			r.fired[key] = true
			out = append(out, verdict{Repo: repo, Key: key, Ticks: r.absent[key]})
		}
	}

	// Union rather than replace: a key that has vanished must stay known, or
	// the next tick would forget it was ever declared and its absence would
	// stop counting. Keys leave this set only when a verdict retires them.
	if r.seen[repo] == nil {
		r.seen[repo] = map[string]bool{}
	}
	for key := range present {
		r.seen[repo][key] = true
	}
	return out
}

// forget drops a key entirely, so a target that has been dealt with stops being
// tracked. Called after a verdict has been acted on.
func (r *reaper) forget(repo, key string) {
	delete(r.seen[repo], key)
	delete(r.absent, key)
	delete(r.fired, key)
}

// reportOrphan names a target that has stopped being declared.
//
// It does NOT delete. That is the deliberate half of #154's fix, and the
// reasoning is worth keeping next to the code:
//
// A resource is only reclaimable if it carries the prune label, which only
// targets configured `prune: true` ever got. For a `prune: false` target —
// which is what #154 actually hit — a delete-by-label would select nothing,
// succeed, and log that it had cleaned up. That is the failure mode this
// codebase keeps finding elsewhere: a path that silently does nothing while
// reporting success. Saying "these resources may be orphaned, and I cannot
// reach them" is worth more than a no-op dressed as a cleanup.
//
// For a `prune: true` target the label does exist and a reap is possible, but
// it is not free: the deletion is issued against a cluster from a config that
// no longer exists in Git, so the operator cannot read the manifest to see what
// is about to go. That deserves its own change, with its own review, against a
// real cluster. Detection lands first because 19 hours of silence is the
// reported harm.
// ensureOrphanState lazily initialises the reaper's maps. The watcher is
// constructed directly in tests and by callers that do not use NewWatcher, and
// a nil map here panics on the first tick rather than at construction — the
// same reason noteAwaiting initialises its own map.
func (w *Watcher) ensureOrphanState() {
	if w.orphans == nil {
		w.orphans = newReaper(stuckAfterCycles)
	}
	if w.lastSeen == nil {
		w.lastSeen = make(map[string]*config.Config)
	}
	if w.drifting == nil {
		w.drifting = newDriftStreak(stuckAfterCycles)
	}
}

func (w *Watcher) reportOrphan(v verdict) {
	cfg := w.lastSeen[v.Key]
	ref, pruned := "unknown", false
	if cfg != nil {
		ref = cfg.Spec.Target.Ref
		pruned = targetPrunes(cfg)
	}
	reach := "its resources carry no prune label, so rollops cannot identify them — check the cluster by hand"
	if pruned {
		reach = "its resources carry the prune label, so they can be identified with " +
			"kubectl get all -l rollops.klarlabs.de/target=<value>"
	}
	if w.logf != nil {
		w.logf("orphaned target %s (ref %s): declared until %d ticks ago, absent since; %s",
			v.Key, ref, v.Ticks, reach)
	}
	if w.rec != nil {
		w.rec.record(audit.Entry{
			Action:    audit.ActionOrphan,
			TargetRef: ref,
			Detail:    "RolloutConfig no longer declared; resources may still be running",
			Fields: map[string]any{
				"key":          v.Key,
				"repo":         v.Repo,
				"absent_ticks": v.Ticks,
				"prune":        pruned,
			},
		})
	}
	// Reap only when the target asked for it. `prune: true` says "delete what
	// I stopped declaring inside a live apply"; reaping says "delete
	// everything when I stop declaring the target at all". The second does not
	// follow from the first, so it is its own opt-in.
	if reapOnDelete(cfg) {
		w.reapOrphan(v, cfg, ref)
	}
	// Reported once; stop tracking so a long-retired target does not sit in the
	// maps for the life of the process.
	w.orphans.forget(v.Repo, v.Key)
	delete(w.lastSeen, v.Key)
}

// reapOnDelete reports whether the target opted into removal when its
// declaration disappears.
func reapOnDelete(c *config.Config) bool {
	if c == nil {
		return false
	}
	v, ok := c.Spec.Target.Spec["reapOnDelete"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// reapOrphan removes the resources of a target whose RolloutConfig is gone.
//
// Everything about this is deliberately narrow. It runs only for a target that
// set reapOnDelete, only after the absence threshold, only through a target
// kind that implements pt.Reaper, and the deletion itself is scoped to the
// marker rollops applied. A failure is recorded and left alone rather than
// retried: the orphan report already named the target, and a reap that keeps
// failing should be read by a person, not hammered.
func (w *Watcher) reapOrphan(v verdict, cfg *config.Config, ref string) {
	if w.rec == nil || w.rec.eng == nil {
		return
	}
	tgt, err := w.rec.eng.BuildTarget(cfg.Spec.Target)
	if err != nil {
		w.logOrphanReap(v, ref, 0, fmt.Errorf("build target: %w", err))
		return
	}
	defer closeIfCloser(tgt)
	r, ok := tgt.(pt.Reaper)
	if !ok {
		w.logOrphanReap(v, ref, 0, fmt.Errorf("target kind %q cannot reap", cfg.Spec.Target.Kind))
		return
	}
	removed, err := r.ReapTarget(context.Background())
	w.logOrphanReap(v, ref, removed, err)
}

func (w *Watcher) logOrphanReap(v verdict, ref string, removed int, err error) {
	if w.logf != nil {
		if err != nil {
			w.logf("reap %s (ref %s) FAILED: %v — resources may still be running", v.Key, ref, err)
		} else {
			w.logf("reaped %s (ref %s): removed %d resource(s)", v.Key, ref, removed)
		}
	}
	if w.rec == nil {
		return
	}
	fields := map[string]any{"key": v.Key, "repo": v.Repo, "removed": removed}
	detail := "reaped resources of a target whose RolloutConfig was deleted"
	if err != nil {
		fields["error"] = err.Error()
		detail = "reap of a deleted target FAILED; resources may still be running"
	}
	w.rec.record(audit.Entry{
		Action: audit.ActionOrphan, TargetRef: ref, Phase: "reap",
		Detail: detail, Fields: fields,
	})
}

func closeIfCloser(t pt.Target) {
	if c, ok := t.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// targetPrunes reports whether the target opted into prune labelling, which
// decides whether its resources are identifiable after the config is gone.
func targetPrunes(c *config.Config) bool {
	if c == nil {
		return false
	}
	v, ok := c.Spec.Target.Spec["prune"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// driftStreak counts CONSECUTIVE ticks a target has reported uncorrected drift.
//
// Under `verification: detect` — the default — rollops live-diffs, alerts, and
// deliberately does not auto-correct. The alert is therefore the entire
// mechanism, and today every tick writes the same audit entry: an ActionDrift
// with the same summary, forever. Tick 1 and tick 1,100 read identically, and
// it is the second that means something is wrong.
//
// That is the lesson #98 already taught this codebase about targets waiting on
// Git (see stuckAfterCycles) and #154 taught it about targets that vanished. A
// state which is fine briefly and serious when sustained cannot be reported as
// a level; it has to be reported as a duration.
type driftStreak struct {
	after int
	n     map[string]int
	fired map[string]bool
}

func newDriftStreak(after int) *driftStreak {
	return &driftStreak{after: after, n: map[string]int{}, fired: map[string]bool{}}
}

// observe records one tick for a target and returns the streak length when it
// has just crossed the threshold, or 0 otherwise.
//
// Returning non-zero only on the crossing tick is deliberate. Escalating every
// tick after the threshold would reproduce the very problem — a wall of
// identical entries — one severity level louder.
func (d *driftStreak) observe(key string, drifting bool) int {
	if !drifting {
		delete(d.n, key)
		delete(d.fired, key)
		return 0
	}
	d.n[key]++
	if d.n[key] >= d.after && !d.fired[key] {
		d.fired[key] = true
		return d.n[key]
	}
	return 0
}

// reportPersistentDrift names drift that has persisted rather than merely
// occurred. Called once per streak, on the tick it crosses the threshold.
//
// `verification: detect` is a deliberate posture — observe and tell me, do not
// touch it — and this does not change that. It changes what "tell me" means
// when the telling has been going on for hours: an ActionDrift entry per tick
// is a level, and what an operator needs is a duration.
func (w *Watcher) reportPersistentDrift(key string, cfg *config.Config, ticks int) {
	ref, mode := "unknown", "detect"
	if cfg != nil {
		ref = cfg.Spec.Target.Ref
		if v := cfg.Spec.Verification; v != "" {
			mode = v
		}
	}
	if w.logf != nil {
		w.logf("persistent drift %s (ref %s): diverged for %d consecutive reconciles under verification=%s "+
			"— alerted every tick and never corrected; set verification=full to self-heal, or reconcile by hand",
			key, ref, ticks, mode)
	}
	if w.rec != nil {
		w.rec.record(audit.Entry{
			Action:    audit.ActionDrift,
			TargetRef: ref,
			Phase:     "persistent",
			Detail:    "drift alerted but uncorrected across consecutive reconciles",
			Fields: map[string]any{
				"key":           key,
				"ticks":         ticks,
				"verification":  mode,
				"auto_corrects": mode == "full",
			},
		})
	}
}

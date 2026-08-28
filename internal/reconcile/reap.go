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
	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
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
	// Reported once; stop tracking so a long-retired target does not sit in the
	// maps for the life of the process.
	w.orphans.forget(v.Repo, v.Key)
	delete(w.lastSeen, v.Key)
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

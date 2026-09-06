package reconcile

import (
	"strings"

	"go.klarlabs.de/rollops/internal/audit"
)

// failVerdict is what a consecutive identical failure means for this tick's
// logging and whether further attempts are worth making.
type failVerdict int

const (
	// failFresh: first sighting, or the error text changed — log once at normal
	// severity and keep trying.
	failFresh failVerdict = iota
	// failEscalate: crossed the identical-failure threshold (or is permanent on
	// first sighting) — log loudly once, then go quiet.
	failEscalate
	// failQuiet: same error already escalated — do not log again.
	failQuiet
)

// failStreak tracks consecutive IDENTICAL failures per key.
//
// #182 left Forbidden retrying every reconcile interval forever: the cause was
// RBAC, not a blip, and the log line at T+60s was indistinguishable from the
// line at T+0. Drift already learned this lesson (see driftStreak); apply
// failures need the same duration-vs-level distinction.
//
// Permanent failures (Forbidden, Unauthorized) escalate on the first tick and
// then hold: further attempts are skipped until desired state changes or the
// process restarts. Other failures keep retrying, but escalate once at `after`
// identical ticks and go quiet afterwards so a day of the same error is one
// alert, not a day's worth.
type failStreak struct {
	after int
	n     map[string]int
	msg   map[string]string
	fired map[string]bool
	perm  map[string]bool
}

func newFailStreak(after int) *failStreak {
	if after < 1 {
		after = 1
	}
	return &failStreak{
		after: after,
		n:     map[string]int{},
		msg:   map[string]string{},
		fired: map[string]bool{},
		perm:  map[string]bool{},
	}
}

// permanentFailure reports whether errText is a failure that will not heal
// itself by waiting. Matching is deliberately substring-based: kubectl wraps
// the API status in layers of "exit status 1" / "apply: …", and we only ever
// see the rendered string.
func permanentFailure(errText string) bool {
	s := strings.ToLower(errText)
	switch {
	case strings.Contains(s, "forbidden"):
		return true
	case strings.Contains(s, "unauthorized"):
		return true
	default:
		return false
	}
}

// note records one failure for key and returns how this tick should be logged.
// An empty errText clears the streak (success).
func (f *failStreak) note(key, errText string) failVerdict {
	if f == nil {
		return failFresh
	}
	if errText == "" {
		f.clear(key)
		return failFresh
	}
	perm := permanentFailure(errText)
	if f.msg[key] != errText {
		f.msg[key] = errText
		f.n[key] = 1
		f.fired[key] = false
		f.perm[key] = perm
		if perm {
			f.fired[key] = true
			return failEscalate
		}
		return failFresh
	}
	f.n[key]++
	f.perm[key] = perm
	if f.fired[key] {
		return failQuiet
	}
	if perm || f.n[key] >= f.after {
		f.fired[key] = true
		return failEscalate
	}
	return failFresh
}

// held reports whether key is in the permanent-failure hold: same Forbidden
// (etc.) already escalated, so the next tick should not spend another API
// round-trip on a refusal that cannot change without a human.
func (f *failStreak) held(key string) (string, bool) {
	if f == nil || !f.fired[key] || !f.perm[key] {
		return "", false
	}
	return f.msg[key], true
}

func (f *failStreak) clear(key string) {
	if f == nil {
		return
	}
	delete(f.n, key)
	delete(f.msg, key)
	delete(f.fired, key)
	delete(f.perm, key)
}

// clearRepo drops every streak for a repo (the repo key itself and every
// "repo/path" target under it). A Git change means desired state moved, so a
// previously permanent refusal deserves another look.
func (f *failStreak) clearRepo(repo string) {
	if f == nil {
		return
	}
	prefix := repo + "/"
	for k := range f.msg {
		if k == repo || strings.HasPrefix(k, prefix) {
			f.clear(k)
		}
	}
}

// reportPermanentFailure records the one-shot escalation for a failure that
// will not heal by waiting. Called from logOutcomes on failEscalate when the
// error is permanent.
func (w *Watcher) reportPermanentFailure(key, detail string) {
	if w.rec != nil {
		w.rec.record(audit.Entry{
			Action:    audit.ActionApply,
			TargetRef: key,
			Phase:     "permanent",
			Detail:    detail,
			Fields: map[string]any{
				"key":       key,
				"permanent": true,
			},
		})
	}
}

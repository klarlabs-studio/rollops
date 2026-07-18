// Package ui is the Rollops web console: a Vue 3 single-page app (embedded,
// self-contained — no Node build, no CDN) over a small JSON API. It shows live
// rollout state, per-target drift, an expandable resource tree, the desired→live
// diff, and history, and lets an operator approve/reject/rollback/sync — all
// interactive, no page reloads.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"time"

	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// maxBodyBytes bounds a UI request body, mirroring the REST API, so a malicious
// or buggy client can't force unbounded memory growth on a mutating call.
const maxBodyBytes = 1 << 20

//go:embed assets/*
var assetsFS embed.FS

// Backend is the engine surface the console needs. *engine.Engine satisfies it.
type Backend interface {
	List(ctx context.Context, limit int) ([]rollout.Rollout, error)
	DriftReport(ctx context.Context) ([]engine.DriftItem, error)
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)
	Diff(ctx context.Context, rolloutID string) (string, error)
	Resources(ctx context.Context, rolloutID string) ([]pt.Resource, error)
	Approve(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error)
	Reject(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error)
	Promote(ctx context.Context, id string, by rollout.Identity, force bool) (rollout.Rollout, error)
	RollbackLast(ctx context.Context, targetRef string, force bool) (rollout.Rollout, error)
	Freeze(ctx context.Context, on bool, by rollout.Identity, reason string) (bool, string, error)
	FreezeStatus() (active bool, reason string)
}

// Server serves the SPA and the JSON API.
type Server struct {
	be Backend
	// actor is retained only for backward compatibility with New's signature; it
	// is NOT used for authorization or audit attribution. Every mutating handler
	// acts as the per-request authenticated identity (see WithIdentity) so the
	// audit trail records the real principal, not a static "admin".
	actor  rollout.Identity
	policy *security.Policy
	sync   func(context.Context) error
	static http.Handler
}

// Option configures the Server.
type Option func(*Server)

// WithSync enables the "Sync now" action: an on-demand reconcile of the watched
// repos (the daemon wires this to the reconcile watcher).
func WithSync(fn func(context.Context) error) Option {
	return func(s *Server) { s.sync = fn }
}

// WithPolicy wires the RBAC policy the console enforces on every state-changing
// action — the same *security.Policy the REST API uses. When no policy is set,
// mutating handlers fail closed (deny) rather than acting as an unchecked admin.
func WithPolicy(p *security.Policy) Option {
	return func(s *Server) { s.policy = p }
}

// New builds the console server.
func New(be Backend, actor rollout.Identity, opts ...Option) *Server {
	sub, _ := fs.Sub(assetsFS, "assets")
	s := &Server{be: be, actor: actor, static: http.StripPrefix("/ui/", http.FileServerFS(sub))}
	for _, o := range opts {
		o(s)
	}
	return s
}

// identityCtxKey scopes the per-request authenticated identity in the context.
type identityCtxKey struct{}

// WithIdentity returns a context carrying the authenticated identity for a UI
// request. The daemon's auth middleware sets it (with IdP groups for RBAC) after
// verifying the caller; mutating handlers read it back via identityFrom.
func WithIdentity(ctx context.Context, id rollout.Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// identityFrom returns the authenticated identity attached by WithIdentity.
func identityFrom(ctx context.Context) (rollout.Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(rollout.Identity)
	return id, ok
}

// authorize enforces RBAC for a mutating console action. It fails closed: with
// no policy configured, every mutating action is denied rather than run as an
// unchecked admin.
func (s *Server) authorize(id rollout.Identity, perm security.Permission, scope security.Scope) error {
	if s.policy == nil {
		return errors.New("ui: no authorization policy configured")
	}
	return s.policy.Authorize(id, perm, scope)
}

// rolloutByID resolves a rollout's current target from its ID so an action can
// be scoped to that target, mirroring the REST API's per-target authorization.
func (s *Server) rolloutByID(ctx context.Context, id string) (rollout.Rollout, bool) {
	rollouts, err := s.be.List(ctx, 200)
	if err != nil {
		return rollout.Rollout{}, false
	}
	for i := range rollouts {
		if rollouts[i].ID == id {
			return rollouts[i], true
		}
	}
	return rollout.Rollout{}, false
}

// Handler returns the routed console handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", s.index)
	mux.HandleFunc("GET /ui/", s.index)
	mux.HandleFunc("GET /ui/app.js", s.asset)
	mux.HandleFunc("GET /ui/api/dashboard", s.apiDashboard)
	mux.HandleFunc("GET /ui/api/target", s.apiTarget)
	mux.HandleFunc("POST /ui/api/approve", s.apiApprove)
	mux.HandleFunc("POST /ui/api/reject", s.apiReject)
	mux.HandleFunc("POST /ui/api/promote", s.apiPromote)
	mux.HandleFunc("POST /ui/api/freeze", s.apiFreeze)
	mux.HandleFunc("POST /ui/api/rollback", s.apiRollback)
	mux.HandleFunc("POST /ui/api/sync", s.apiSync)
	return securityHeaders(mux)
}

// securityHeaders sets a tight policy for the console. The bundle is self-hosted
// (no CDN, no inline <script>), so script-src can stay 'self'; Vue's :style
// bindings and the <style> block require 'unsafe-inline' for styles only.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// The bundle is embedded in the binary and changes with every release;
		// no-cache forces revalidation so an upgraded daemon never serves a UI
		// driven by a stale cached bundle.
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	b, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) { s.static.ServeHTTP(w, r) }

// --- JSON API ---

type driftJSON struct {
	Target   string `json:"target"`
	Phase    string `json:"phase"`
	Desired  string `json:"desired"`
	Observed string `json:"observed"`
	Drifted  bool   `json:"drifted"`
}

type rolloutJSON struct {
	ID         string  `json:"id"`
	Target     string  `json:"target"`
	Phase      string  `json:"phase"`
	Strategy   string  `json:"strategy"`
	By         string  `json:"by"`
	ByKind     string  `json:"byKind"`     // human | agent | ci
	Risk       float64 `json:"risk"`       // decisionkit blast-radius score, 0 when ungated
	At         string  `json:"at"`         // last transition, RFC 3339
	StepIndex  int     `json:"stepIndex"`  // progressive step passed (1-based, 0 = not stepping)
	StepTotal  int     `json:"stepTotal"`  // total steps in the strategy plan
	StepWeight int     `json:"stepWeight"` // traffic % of the current step
}

type dashboardJSON struct {
	Counts       map[string]int `json:"counts"`
	Drift        []driftJSON    `json:"drift"`
	Rollouts     []rolloutJSON  `json:"rollouts"`
	CanSync      bool           `json:"canSync"`
	Frozen       bool           `json:"frozen"`
	FreezeReason string         `json:"freezeReason,omitempty"`
}

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	rollouts, err := s.be.List(r.Context(), 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts := map[string]int{}
	rs := make([]rolloutJSON, 0, len(rollouts))
	for _, rl := range rollouts {
		counts[string(rl.Phase)]++
		rs = append(rs, rolloutJSON{
			ID: rl.ID, Target: rl.TargetRef, Phase: string(rl.Phase), Strategy: string(rl.Strategy),
			By: rl.Initiator.Kind + "/" + rl.Initiator.Name, ByKind: rl.Initiator.Kind,
			Risk: rl.RiskScore, At: rl.UpdatedAt.UTC().Format(time.RFC3339),
			StepIndex: rl.StepIndex, StepTotal: rl.StepTotal, StepWeight: rl.StepWeight,
		})
	}
	drift, _ := s.be.DriftReport(r.Context())
	dj := make([]driftJSON, 0, len(drift))
	for _, d := range drift {
		dj = append(dj, driftJSON{Target: d.TargetRef, Phase: string(d.Phase), Desired: d.Desired, Observed: d.Observed, Drifted: d.Drifted})
	}
	frozen, freezeReason := s.be.FreezeStatus()
	writeJSON(w, http.StatusOK, dashboardJSON{Counts: counts, Drift: dj, Rollouts: rs, CanSync: s.sync != nil, Frozen: frozen, FreezeReason: freezeReason})
}

// apiFreeze toggles the emergency kill-switch from the console.
func (s *Server) apiFreeze(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.authorize(id, security.PermFreeze, security.Scope{}); err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Active bool   `json:"active"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if _, _, err := s.be.Freeze(r.Context(), body.Active, id, body.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type resourceJSON struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Parent    string `json:"parent"`
}

type historyJSON struct {
	At      string `json:"at"` // RFC 3339; the client renders relative time
	Phase   string `json:"phase"`
	Rollout string `json:"rollout"`
	By      string `json:"by"`
	ByKind  string `json:"byKind"`
	Note    string `json:"note"`
}

type targetJSON struct {
	Ref     string `json:"ref"`
	Rollout struct {
		ID         string  `json:"id"`
		Phase      string  `json:"phase"`
		Strategy   string  `json:"strategy"`
		Desired    string  `json:"desired"`
		Risk       float64 `json:"risk"`
		At         string  `json:"at"` // last transition, RFC 3339
		StepIndex  int     `json:"stepIndex"`
		StepTotal  int     `json:"stepTotal"`
		StepWeight int     `json:"stepWeight"`
	} `json:"rollout"`
	Diff      string         `json:"diff"`
	DiffNote  string         `json:"diffNote"`
	Resources []resourceJSON `json:"resources"`
	History   []historyJSON  `json:"history"`
	Awaiting  bool           `json:"awaiting"`
	Drifted   bool           `json:"drifted"`
	CanSync   bool           `json:"canSync"`
}

func (s *Server) apiTarget(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref required")
		return
	}
	rollouts, err := s.be.List(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var latest *rollout.Rollout
	for i := range rollouts {
		if rollouts[i].TargetRef == ref {
			latest = &rollouts[i]
			break
		}
	}
	if latest == nil {
		writeErr(w, http.StatusNotFound, "no rollouts for target")
		return
	}
	var out targetJSON
	out.Ref = ref
	out.Rollout.ID = latest.ID
	out.Rollout.Phase = string(latest.Phase)
	out.Rollout.Strategy = string(latest.Strategy)
	out.Rollout.Desired = latest.Desired.Checksum
	out.Rollout.Risk = latest.RiskScore
	out.Rollout.At = latest.UpdatedAt.UTC().Format(time.RFC3339)
	out.Rollout.StepIndex = latest.StepIndex
	out.Rollout.StepTotal = latest.StepTotal
	out.Rollout.StepWeight = latest.StepWeight
	out.Awaiting = latest.Phase == rollout.PhaseAwaitingApproval
	out.CanSync = s.sync != nil
	// Sync status is the authoritative drift signal (desired vs observed
	// checksum), the same source the dashboard uses — never inferred from the
	// diff text, which may carry a human "no changes" message.
	if drift, derr := s.be.DriftReport(r.Context()); derr == nil {
		for _, d := range drift {
			if d.TargetRef == ref {
				out.Drifted = d.Drifted
				break
			}
		}
	}

	if diff, derr := s.be.Diff(r.Context(), latest.ID); derr != nil {
		if errors.Is(derr, engine.ErrUnsupported) {
			out.DiffNote = "this target type does not support diff"
		} else {
			out.DiffNote = derr.Error()
		}
	} else {
		out.Diff = diff
	}
	if res, rerr := s.be.Resources(r.Context(), latest.ID); rerr == nil {
		for _, rsrc := range res {
			out.Resources = append(out.Resources, resourceJSON{Kind: rsrc.Kind, Name: rsrc.Name, Namespace: rsrc.Namespace, Status: rsrc.Status, Parent: rsrc.Parent})
		}
	}
	if recs, herr := s.be.History(r.Context(), ref); herr == nil {
		for _, rec := range recs {
			out.History = append(out.History, historyJSON{At: rec.At.UTC().Format(time.RFC3339), Phase: string(rec.Phase), Rollout: rec.RolloutID, By: rec.Initiator.Kind + "/" + rec.Initiator.Name, ByKind: rec.Initiator.Kind, Note: rec.Note})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiApprove(w http.ResponseWriter, r *http.Request) {
	s.actByID(w, r, security.PermApprove, s.be.Approve)
}
func (s *Server) apiReject(w http.ResponseWriter, r *http.Request) {
	s.actByID(w, r, security.PermApprove, s.be.Reject)
}

// apiPromote promotes a verified rollout, authorized against the rollout's
// target under PermPromote and attributed to the console caller.
//
// The console never forces: it always promotes with the post-deploy gates
// enforced. Overriding a failing gate is deliberate break-glass work and stays
// on the CLI/API, where it is an explicit flag rather than a button.
func (s *Server) apiPromote(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	rl, found := s.rolloutByID(r.Context(), body.ID)
	if !found {
		writeErr(w, http.StatusNotFound, "rollout not found")
		return
	}
	if err := s.authorize(id, security.PermPromote, security.Scope{TargetRef: rl.TargetRef}); err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := s.be.Promote(r.Context(), body.ID, id, false); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// actByID is the shared approve/reject flow: authenticate, decode {id}, scope
// authorization to the rollout's target, then act as the real principal.
func (s *Server) actByID(w http.ResponseWriter, r *http.Request, perm security.Permission, fn func(context.Context, string, rollout.Identity) (rollout.Rollout, error)) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	rl, found := s.rolloutByID(r.Context(), body.ID)
	if !found {
		writeErr(w, http.StatusNotFound, "rollout not found")
		return
	}
	if err := s.authorize(id, perm, security.Scope{TargetRef: rl.TargetRef}); err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := fn(r.Context(), body.ID, id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiRollback(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Target string `json:"target"`
		Force  bool   `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil || body.Target == "" {
		writeErr(w, http.StatusBadRequest, "target required")
		return
	}
	if err := s.authorize(id, security.PermRollback, security.Scope{TargetRef: body.Target}); err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := s.be.RollbackLast(r.Context(), body.Target, body.Force); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiSync(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.authorize(id, security.PermApply, security.Scope{}); err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.sync == nil {
		writeErr(w, http.StatusNotImplemented, "sync not available")
		return
	}
	if err := s.sync(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

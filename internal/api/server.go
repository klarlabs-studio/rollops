// Package api is the daemon's HTTP/JSON network surface. Every mutating or
// sensitive call is authenticated (bearer token by default; TLS/mTLS belongs at
// the daemon or reverse-proxy transport boundary) and authorized through RBAC:
// no anonymous calls, and apply-to-prod is a distinct grant from status.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
)

// Authenticator resolves a bearer token to an identity.
type Authenticator interface {
	Identify(token string) (rollout.Identity, bool)
}

// TokenAuth is a static token→identity map. Production deployments can replace
// it with mTLS-derived identity or an external IdP.
type TokenAuth map[string]rollout.Identity

// Identify implements Authenticator. It compares the presented token against
// each configured token in constant time (over SHA-256 digests, so token length
// isn't leaked either) rather than via a map lookup, so response timing can't be
// used to recover a valid token. An empty token is always rejected.
func (t TokenAuth) Identify(token string) (rollout.Identity, bool) {
	if token == "" {
		return rollout.Identity{}, false
	}
	sum := sha256.Sum256([]byte(token))
	var (
		match rollout.Identity
		found bool
	)
	for known, id := range t {
		knownSum := sha256.Sum256([]byte(known))
		// Compare every entry (no early return) so timing is independent of which
		// token, if any, matched.
		if subtle.ConstantTimeCompare(sum[:], knownSum[:]) == 1 {
			match, found = id, true
		}
	}
	return match, found
}

// Server is the HTTP handler over the engine.
type Server struct {
	eng    *engine.Engine
	auth   Authenticator
	policy *security.Policy
}

// New builds the API server.
func New(eng *engine.Engine, auth Authenticator, policy *security.Policy) *Server {
	return &Server{eng: eng, auth: auth, policy: policy}
}

// Handler returns the routed, authenticated http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/plan", s.handlePlan)
	mux.HandleFunc("POST /v1/apply", s.handleApply)
	mux.HandleFunc("POST /v1/rollback", s.handleRollback)
	mux.HandleFunc("POST /v1/approve", s.handleApprove)
	mux.HandleFunc("POST /v1/reject", s.handleReject)
	mux.HandleFunc("POST /v1/promote", s.handlePromote)
	mux.HandleFunc("POST /v1/pause", s.handlePause)
	mux.HandleFunc("POST /v1/resume", s.handleResume)
	mux.HandleFunc("POST /v1/abort", s.handleAbort)
	mux.HandleFunc("POST /v1/verify", s.handleVerify)
	mux.HandleFunc("POST /v1/freeze", s.handleFreeze)
	mux.HandleFunc("GET /v1/rollouts/{id}", s.handleStatus)
	mux.HandleFunc("GET /v1/fleet", s.handleFleet)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return s.authMiddleware(mux)
}

type ctxKey int

const idKey ctxKey = 0

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		token := bearer(r)
		id, ok := s.auth.Identify(token)
		if token == "" || !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), idKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func identityFrom(r *http.Request) rollout.Identity {
	if id, ok := r.Context().Value(idKey).(rollout.Identity); ok {
		return id
	}
	return rollout.Identity{}
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	data, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.LoadClusterRegistryEnv(nil); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	docs, err := config.LoadDocuments(data, "http")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var summaries []string
	changed := false
	action := ""
	var riskScore float64
	needsApproval := false
	sensitive := false
	recentFailures := 0
	reason := ""
	for _, d := range docs {
		if err := s.policy.Authorize(id, security.PermPlan, scopeOf(d.Config)); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		p, err := s.eng.Plan(r.Context(), d.Config)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		summaries = append(summaries, p.Summary)
		if p.Changed {
			changed = true
		}
		if action == "" {
			action = string(p.Action)
		} else if action != string(p.Action) {
			action = "mixed"
		}
		if p.RiskScore > riskScore {
			riskScore = p.RiskScore
		}
		needsApproval = needsApproval || p.NeedsApproval
		sensitive = sensitive || p.Sensitive
		if p.RecentFailures > recentFailures {
			recentFailures = p.RecentFailures
		}
		if p.RiskReason != "" {
			reason = p.RiskReason
		}
	}
	summary := strings.Join(summaries, "\n")
	if len(docs) > 1 {
		summary = fmt.Sprintf("RolloutSet → %d targets\n%s", len(docs), summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"action": action, "changed": changed, "summary": summary,
		"risk_score": riskScore, "needs_approval": needsApproval, "sensitive": sensitive,
		"recent_failures": recentFailures, "reason": reason,
	})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	data, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.RefuseApplyRolloutSet(data); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := config.Load(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.policy.Authorize(id, security.PermApply, scopeOf(c)); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if _, err := s.eng.Plan(r.Context(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rl, err := s.eng.Apply(r.Context(), engine.ApplyRequest{Config: c, Initiator: id, Planned: true, Risk: engine.RiskFromConfig(c)})
	if err != nil {
		if errors.Is(err, engine.ErrTargetBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef, "risk_score": rl.RiskScore})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if err := s.policy.Authorize(id, security.PermStatus, security.Scope{}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	rl, err := s.eng.Status(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef, "strategy": rl.Strategy, "note": rl.Note,
		"risk_score": rl.RiskScore, "actor_kind": rl.Initiator.Kind, "actor_name": rl.Initiator.Name,
	})
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if err := s.policy.Authorize(id, security.PermStatus, security.Scope{}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	filter := r.URL.Query().Get("prefix")
	if filter == "" {
		filter = r.URL.Query().Get("filter")
	}
	rep, err := s.eng.FleetStatus(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	members := make([]map[string]any, 0, len(rep.Members))
	for _, m := range rep.Members {
		members = append(members, map[string]any{"id": m.ID, "target": m.Target, "phase": m.Phase})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": rep.Name, "total": rep.Total, "promoted": rep.Promoted,
		"active": rep.Active, "degraded": rep.Degraded, "awaiting": rep.Awaiting,
		"members": members,
	})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApprove, func(ctx context.Context, id string, by rollout.Identity, _ bool) (rollout.Rollout, error) {
		return s.eng.Approve(ctx, id, by)
	})
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApprove, func(ctx context.Context, id string, by rollout.Identity, _ bool) (rollout.Rollout, error) {
		return s.eng.Reject(ctx, id, by)
	})
}

// handlePromote promotes a verified rollout, gated on the post-deploy checks
// (health, smoke, analysis). `{"force": true}` overrides a failing gate; the
// bypass is recorded in the audit trail.
func (s *Server) handlePromote(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermPromote, func(ctx context.Context, id string, by rollout.Identity, force bool) (rollout.Rollout, error) {
		return s.eng.Promote(ctx, id, by, force)
	})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApply, func(ctx context.Context, id string, by rollout.Identity, _ bool) (rollout.Rollout, error) {
		return s.eng.Pause(ctx, id, by)
	})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApply, func(ctx context.Context, id string, by rollout.Identity, _ bool) (rollout.Rollout, error) {
		return s.eng.Resume(ctx, id, by)
	})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApply, func(ctx context.Context, id string, by rollout.Identity, _ bool) (rollout.Rollout, error) {
		return s.eng.Abort(ctx, id, by)
	})
}

// handleVerify dry-runs the post-deploy gate and returns the report. Nothing is
// changed, but the gates really run (a smoke test executes a command on the
// daemon host), so it is authorized as PermPromote rather than a read
// permission. A failing gate is a 200 with ok=false — not an HTTP error; only
// operational failures (unknown rollout, unreadable descriptor) are errors.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	actor := identityFrom(r)
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	cur, err := s.eng.Status(r.Context(), body.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.policy.Authorize(actor, security.PermPromote, security.Scope{TargetRef: cur.TargetRef}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	rep, err := s.eng.Verify(r.Context(), body.ID)
	if err != nil {
		writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// rolloutAction is the shared approve/reject/promote flow: decode {id, force},
// scope authorization to the rollout's target, run the engine op, return its
// outcome. force is meaningful only for promote; approve/reject ignore it.
func (s *Server) rolloutAction(w http.ResponseWriter, r *http.Request, perm security.Permission, op func(context.Context, string, rollout.Identity, bool) (rollout.Rollout, error)) {
	actor := identityFrom(r)
	var body struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	cur, err := s.eng.Status(r.Context(), body.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.policy.Authorize(actor, perm, security.Scope{TargetRef: cur.TargetRef}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	rl, err := op(r.Context(), body.ID, actor, body.Force)
	if err != nil {
		writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef, "note": rl.Note})
}

func (s *Server) handleFreeze(w http.ResponseWriter, r *http.Request) {
	actor := identityFrom(r)
	if err := s.policy.Authorize(actor, security.PermFreeze, security.Scope{}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	var body struct {
		Active bool   `json:"active"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	active, reason, err := s.eng.Freeze(r.Context(), body.Active, actor, body.Reason)
	if err != nil {
		writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "reason": reason})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	var body struct {
		Target string `json:"target"`
		Force  bool   `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.Target == "" {
		writeErr(w, http.StatusBadRequest, "target required")
		return
	}
	if err := s.policy.Authorize(id, security.PermRollback, security.Scope{TargetRef: body.Target}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	rl, err := s.eng.RollbackLast(r.Context(), body.Target, body.Force)
	if err != nil {
		writeErr(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef})
}

// --- helpers ---

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

func scopeOf(c *config.Config) security.Scope {
	return security.Scope{Env: c.Spec.Target.Env, TargetRef: c.Spec.Target.Ref}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

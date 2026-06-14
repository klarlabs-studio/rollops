// Package api is the daemon's HTTP/JSON network surface. Every mutating or
// sensitive call is authenticated (bearer token by default; TLS/mTLS belongs at
// the daemon or reverse-proxy transport boundary) and authorized through RBAC:
// no anonymous calls, and apply-to-prod is a distinct grant from status.
package api

import (
	"context"
	"encoding/json"
	"errors"
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

// Identify implements Authenticator.
func (t TokenAuth) Identify(token string) (rollout.Identity, bool) {
	id, ok := t[token]
	return id, ok
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
	mux.HandleFunc("POST /v1/freeze", s.handleFreeze)
	mux.HandleFunc("GET /v1/rollouts/{id}", s.handleStatus)
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
	c, err := decodeConfig(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.policy.Authorize(id, security.PermPlan, scopeOf(c)); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	p, err := s.eng.Plan(r.Context(), c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": p.Action, "changed": p.Changed, "summary": p.Summary})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	c, err := decodeConfig(r)
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
	rl, err := s.eng.Apply(r.Context(), engine.ApplyRequest{Config: c, Initiator: id, Planned: true})
	if err != nil {
		if errors.Is(err, engine.ErrTargetBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef})
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
	writeJSON(w, http.StatusOK, map[string]any{"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef, "strategy": rl.Strategy, "note": rl.Note})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApprove, func(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error) {
		return s.eng.Approve(ctx, id, by)
	})
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermApprove, func(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error) {
		return s.eng.Reject(ctx, id, by)
	})
}

func (s *Server) handlePromote(w http.ResponseWriter, r *http.Request) {
	s.rolloutAction(w, r, security.PermPromote, func(ctx context.Context, id string, _ rollout.Identity) (rollout.Rollout, error) {
		return s.eng.Promote(ctx, id)
	})
}

// rolloutAction is the shared approve/reject/promote flow: decode {id}, scope
// authorization to the rollout's target, run the engine op, return its outcome.
func (s *Server) rolloutAction(w http.ResponseWriter, r *http.Request, perm security.Permission, op func(context.Context, string, rollout.Identity) (rollout.Rollout, error)) {
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
	if err := s.policy.Authorize(actor, perm, security.Scope{TargetRef: cur.TargetRef}); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	rl, err := op(r.Context(), body.ID, actor)
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

func decodeConfig(r *http.Request) (*config.Config, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return config.Load(data)
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

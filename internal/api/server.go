// Package api is the daemon's network surface. It wraps the engine behind an
// authenticated HTTP/JSON API — the same surface a grpc-gateway REST front
// exposes for the browser UI. Every call is authenticated (bearer token here;
// mTLS at the transport) and authorized through RBAC: no anonymous calls, and
// apply-to-prod is a distinct grant from status.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/security"
)

// Authenticator resolves a bearer token to an identity.
type Authenticator interface {
	Identify(token string) (rollout.Identity, bool)
}

// TokenAuth is a static token→identity map (mTLS or an external IdP replaces it
// in production).
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
	writeJSON(w, http.StatusOK, map[string]any{"id": rl.ID, "phase": rl.Phase, "target": rl.TargetRef, "strategy": rl.Strategy})
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
	return security.Scope{TargetRef: c.Spec.Target.Ref}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

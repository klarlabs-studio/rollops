// Package ui is the read-and-act dashboard over the REST gateway: it shows the
// live state of every rollout, highlights those awaiting approval, and lets an
// operator approve or reject them. v1 leans observe-first — the only actions are
// the human-in-the-loop approval arms of the risk gate.
package ui

import (
	"context"
	"html/template"
	"net/http"

	"go.klarlabs.de/rolloffs/internal/rollout"
)

// Backend is the engine surface the dashboard needs. *engine.Engine satisfies it.
type Backend interface {
	List(ctx context.Context, limit int) ([]rollout.Rollout, error)
	Approve(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error)
	Reject(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error)
}

// Server renders the dashboard and handles approve/reject actions.
type Server struct {
	be    Backend
	actor rollout.Identity
	tmpl  *template.Template
}

// New builds the dashboard server. actor is the authenticated operator whose
// identity is attributed to approve/reject actions.
func New(be Backend, actor rollout.Identity) *Server {
	return &Server{be: be, actor: actor, tmpl: template.Must(template.New("dash").Parse(dashboardHTML))}
}

// Handler returns the routed dashboard handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", s.dashboard)
	mux.HandleFunc("GET /ui/", s.dashboard)
	mux.HandleFunc("POST /ui/approve", s.act(s.be.Approve))
	mux.HandleFunc("POST /ui/reject", s.act(s.be.Reject))
	return mux
}

type row struct {
	rollout.Rollout
	AwaitingApproval bool
	Drift            bool
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	rollouts, err := s.be.List(r.Context(), 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]row, 0, len(rollouts))
	for _, rl := range rollouts {
		rows = append(rows, row{
			Rollout:          rl,
			AwaitingApproval: rl.Phase == rollout.PhaseAwaitingApproval,
			Drift:            rl.Phase == rollout.PhaseRolledBack,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.Execute(w, rows)
}

func (s *Server) act(fn func(context.Context, string, rollout.Identity) (rollout.Rollout, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.FormValue("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if _, err := fn(r.Context(), id, s.actor); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/ui", http.StatusSeeOther)
	}
}

const dashboardHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Rolloffs</title>
<style>
 :root{--bg:#0f1115;--card:#171a21;--ink:#e6e9ef;--muted:#8b93a7;--line:#252a35;--ok:#3fb950;--warn:#d29922;--bad:#f85149}
 body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto}
 header{padding:20px 28px;border-bottom:1px solid var(--line);display:flex;align-items:baseline;gap:12px}
 h1{font-size:18px;margin:0;letter-spacing:.3px} .sub{color:var(--muted);font-size:12px}
 main{padding:24px 28px;max-width:1100px;margin:0 auto}
 table{width:100%;border-collapse:collapse;background:var(--card);border:1px solid var(--line);border-radius:10px;overflow:hidden}
 th,td{padding:11px 14px;text-align:left;border-bottom:1px solid var(--line)}
 th{color:var(--muted);font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:.5px}
 tr:last-child td{border-bottom:0}
 .phase{font-weight:600} .mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted)}
 .pill{display:inline-block;padding:2px 9px;border-radius:999px;font-size:12px;font-weight:600}
 .p-promoted{background:rgba(63,185,80,.15);color:var(--ok)}
 .p-rolled-back{background:rgba(248,81,73,.15);color:var(--bad)}
 .p-awaiting-approval{background:rgba(210,153,34,.16);color:var(--warn)}
 .p-deploying,.p-verifying,.p-validating,.p-pending{background:rgba(139,147,167,.15);color:var(--muted)}
 .act{display:inline-flex;gap:6px} button{cursor:pointer;border:1px solid var(--line);background:#1f2430;color:var(--ink);padding:5px 11px;border-radius:7px;font-weight:600}
 button.ok{border-color:rgba(63,185,80,.4)} button.bad{border-color:rgba(248,81,73,.4)}
 .empty{color:var(--muted);padding:28px;text-align:center}
</style></head>
<body>
<header><h1>Rolloffs</h1><span class="sub">rollout operations · live state</span></header>
<main>
{{- if . }}
<table>
 <thead><tr><th>Rollout</th><th>Target</th><th>Phase</th><th>Strategy</th><th>By</th><th></th></tr></thead>
 <tbody>
 {{- range . }}
  <tr>
   <td class="mono">{{ .ID }}</td>
   <td>{{ .TargetRef }}</td>
   <td><span class="pill p-{{ .Phase }}">{{ .Phase }}</span></td>
   <td>{{ .Strategy }}</td>
   <td class="mono">{{ .Initiator.Kind }}/{{ .Initiator.Name }}</td>
   <td>
   {{- if .AwaitingApproval }}
    <span class="act">
     <form method="post" action="/ui/approve"><input type="hidden" name="id" value="{{ .ID }}"><button class="ok">Approve</button></form>
     <form method="post" action="/ui/reject"><input type="hidden" name="id" value="{{ .ID }}"><button class="bad">Reject</button></form>
    </span>
   {{- end }}
   </td>
  </tr>
 {{- end }}
 </tbody>
</table>
{{- else }}
<div class="empty">No rollouts yet.</div>
{{- end }}
</main>
</body></html>`

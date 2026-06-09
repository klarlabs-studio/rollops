// Package ui is the read-and-act dashboard over the REST gateway: it shows the
// live state of every rollout, the drift status of every target, per-target
// history, and lets an operator approve or reject rollouts awaiting approval.
// v1 leans observe-first — the only actions are the human-in-the-loop approval
// arms of the risk gate.
package ui

import (
	"context"
	"html/template"
	"net/http"

	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
)

// Backend is the engine surface the dashboard needs. *engine.Engine satisfies it.
type Backend interface {
	List(ctx context.Context, limit int) ([]rollout.Rollout, error)
	DriftReport(ctx context.Context) ([]engine.DriftItem, error)
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)
	Approve(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error)
	Reject(ctx context.Context, id string, by rollout.Identity) (rollout.Rollout, error)
}

// Server renders the dashboard and handles approve/reject actions.
type Server struct {
	be    Backend
	actor rollout.Identity
	sync  func(context.Context) error // optional: on-demand reconcile (Sync now)
	dash  *template.Template
	hist  *template.Template
}

// Option configures the Server.
type Option func(*Server)

// WithSync enables the "Sync now" button: an on-demand reconcile of the watched
// repos (the daemon wires this to the reconcile watcher). Argo-style manual sync.
func WithSync(fn func(context.Context) error) Option {
	return func(s *Server) { s.sync = fn }
}

var funcs = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"short": func(s string) string {
		if len(s) <= 12 {
			return s
		}
		return s[:12]
	},
}

// New builds the dashboard server. actor is the authenticated operator whose
// identity is attributed to approve/reject actions.
func New(be Backend, actor rollout.Identity, opts ...Option) *Server {
	s := &Server{
		be:    be,
		actor: actor,
		dash:  template.Must(template.New("dash").Funcs(funcs).Parse(dashboardHTML)),
		hist:  template.Must(template.New("hist").Funcs(funcs).Parse(historyHTML)),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Handler returns the routed dashboard handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", s.dashboard)
	mux.HandleFunc("GET /ui/", s.dashboard)
	mux.HandleFunc("GET /ui/history", s.history)
	mux.HandleFunc("POST /ui/approve", s.act(s.be.Approve))
	mux.HandleFunc("POST /ui/reject", s.act(s.be.Reject))
	mux.HandleFunc("POST /ui/sync", s.handleSync)
	return mux
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.sync == nil {
		http.Error(w, "sync not available", http.StatusNotImplemented)
		return
	}
	if err := s.sync(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui", http.StatusSeeOther)
}

type row struct {
	rollout.Rollout
	AwaitingApproval bool
}

type dashData struct {
	Rows     []row
	Drift    []engine.DriftItem
	Counts   map[string]int
	HasDrift bool
	CanSync  bool
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	rollouts, err := s.be.List(r.Context(), 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	counts := map[string]int{}
	rows := make([]row, 0, len(rollouts))
	for _, rl := range rollouts {
		counts[string(rl.Phase)]++
		rows = append(rows, row{Rollout: rl, AwaitingApproval: rl.Phase == rollout.PhaseAwaitingApproval})
	}
	drift, _ := s.be.DriftReport(r.Context())
	hasDrift := false
	for _, d := range drift {
		if d.Drifted {
			hasDrift = true
			break
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.dash.Execute(w, dashData{Rows: rows, Drift: drift, Counts: counts, HasDrift: hasDrift, CanSync: s.sync != nil})
}

type histData struct {
	TargetRef string
	Records   []rollout.RolloutRecord
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("target")
	if ref == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}
	recs, err := s.be.History(r.Context(), ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.hist.Execute(w, histData{TargetRef: ref, Records: recs})
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

const baseCSS = `
 :root{--bg:#0f1115;--card:#171a21;--ink:#e6e9ef;--muted:#8b93a7;--line:#252a35;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--accent:#539bf5}
 *{box-sizing:border-box} body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto}
 a{color:var(--accent);text-decoration:none} a:hover{text-decoration:underline}
 header{padding:18px 28px;border-bottom:1px solid var(--line);display:flex;align-items:baseline;gap:12px}
 h1{font-size:18px;margin:0;letter-spacing:.3px} .sub{color:var(--muted);font-size:12px}
 main{padding:22px 28px;max-width:1100px;margin:0 auto}
 h2{font-size:13px;text-transform:uppercase;letter-spacing:.5px;color:var(--muted);margin:26px 0 10px}
 table{width:100%;border-collapse:collapse;background:var(--card);border:1px solid var(--line);border-radius:10px;overflow:hidden}
 th,td{padding:10px 14px;text-align:left;border-bottom:1px solid var(--line)}
 th{color:var(--muted);font-weight:600;font-size:11px;text-transform:uppercase;letter-spacing:.5px}
 tr:last-child td{border-bottom:0}
 .mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted);font-size:12px}
 .pill{display:inline-block;padding:2px 9px;border-radius:999px;font-size:12px;font-weight:600}
 .p-promoted{background:rgba(63,185,80,.15);color:var(--ok)}
 .p-rolled-back{background:rgba(248,81,73,.15);color:var(--bad)}
 .p-awaiting-approval{background:rgba(210,153,34,.16);color:var(--warn)}
 .p-deploying,.p-verifying,.p-validating,.p-pending{background:rgba(139,147,167,.15);color:var(--muted)}
 .act{display:inline-flex;gap:6px} button{cursor:pointer;border:1px solid var(--line);background:#1f2430;color:var(--ink);padding:5px 11px;border-radius:7px;font-weight:600}
 button.ok{border-color:rgba(63,185,80,.4)} button.bad{border-color:rgba(248,81,73,.4)}
 .empty{color:var(--muted);padding:24px;text-align:center}
 .chips{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:6px}
 .chip{background:var(--card);border:1px solid var(--line);border-radius:999px;padding:4px 12px;font-size:12px}
 .chip b{color:var(--ink)} .ok{color:var(--ok)} .bad{color:var(--bad)} .warn{color:var(--warn)}
 .badge{font-size:11px;font-weight:700;padding:2px 8px;border-radius:6px}
 .badge.drift{background:rgba(248,81,73,.16);color:var(--bad)} .badge.sync{background:rgba(63,185,80,.12);color:var(--ok)}
 header .spacer{flex:1}
 .syncbtn{border:1px solid rgba(83,155,245,.5);background:rgba(83,155,245,.12);color:var(--accent);padding:6px 14px;border-radius:8px;font-weight:600}
 /* live phase: in-flight pills pulse so transitions read as live */
 @keyframes pulse{0%,100%{opacity:1}50%{opacity:.45}}
 .p-deploying,.p-verifying,.p-validating,.p-pending{animation:pulse 1.2s ease-in-out infinite}
 .p-awaiting-approval{animation:pulse 1.6s ease-in-out infinite}
`

const dashboardHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta http-equiv="refresh" content="5"><title>Rolloffs</title>
<style>` + baseCSS + `</style></head>
<body>
<header><h1>Rolloffs</h1><span class="sub">rollout operations · live state · auto-refresh 5s</span>
 <span class="spacer"></span>
 {{- if .CanSync }}<form method="post" action="/ui/sync"><button class="syncbtn">⟳ Sync now</button></form>{{ end }}
</header>
<main>
 <div class="chips">
  <span class="chip">promoted <b class="ok">{{ index .Counts "promoted" }}</b></span>
  <span class="chip">awaiting <b class="warn">{{ index .Counts "awaiting-approval" }}</b></span>
  <span class="chip">rolled-back <b class="bad">{{ index .Counts "rolled-back" }}</b></span>
  <span class="chip">in-flight <b>{{ add (index .Counts "deploying") (index .Counts "verifying") }}</b></span>
 </div>

 <h2>Targets {{ if .HasDrift }}<span class="badge drift">drift detected</span>{{ end }}</h2>
 {{- if .Drift }}
 <table>
  <thead><tr><th>Target</th><th>State</th><th>Phase</th><th>Desired</th><th>Observed</th><th></th></tr></thead>
  <tbody>
  {{- range .Drift }}
   <tr>
    <td>{{ .TargetRef }}</td>
    <td>{{ if .Drifted }}<span class="badge drift">DRIFT</span>{{ else }}<span class="badge sync">in sync</span>{{ end }}</td>
    <td><span class="pill p-{{ .Phase }}">{{ .Phase }}</span></td>
    <td class="mono">{{ short .Desired }}</td>
    <td class="mono">{{ short .Observed }}</td>
    <td><a href="/ui/history?target={{ .TargetRef }}">history →</a></td>
   </tr>
  {{- end }}
  </tbody>
 </table>
 {{- else }}<div class="empty">No targets yet.</div>{{ end }}

 <h2>Rollouts</h2>
 {{- if .Rows }}
 <table>
  <thead><tr><th>Rollout</th><th>Target</th><th>Phase</th><th>Strategy</th><th>By</th><th></th></tr></thead>
  <tbody>
  {{- range .Rows }}
   <tr>
    <td class="mono">{{ .ID }}</td>
    <td><a href="/ui/history?target={{ .TargetRef }}">{{ .TargetRef }}</a></td>
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
 {{- else }}<div class="empty">No rollouts yet.</div>{{ end }}
</main>
</body></html>`

const historyHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Rolloffs · history</title>
<style>` + baseCSS + `</style></head>
<body>
<header><h1>Rolloffs</h1><span class="sub">history · {{ .TargetRef }}</span></header>
<main>
 <p><a href="/ui">← dashboard</a></p>
 <h2>Transitions ({{ .TargetRef }})</h2>
 {{- if .Records }}
 <table>
  <thead><tr><th>When</th><th>Phase</th><th>Rollout</th><th>By</th><th>Note</th></tr></thead>
  <tbody>
  {{- range .Records }}
   <tr>
    <td class="mono">{{ .At.Format "2006-01-02 15:04:05" }}</td>
    <td><span class="pill p-{{ .Phase }}">{{ .Phase }}</span></td>
    <td class="mono">{{ .RolloutID }}</td>
    <td class="mono">{{ .Initiator.Kind }}/{{ .Initiator.Name }}</td>
    <td>{{ .Note }}</td>
   </tr>
  {{- end }}
  </tbody>
 </table>
 {{- else }}<div class="empty">No history for this target.</div>{{ end }}
</main>
</body></html>`

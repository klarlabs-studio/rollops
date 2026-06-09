// Rolloffs UI — a small Vue 3 SPA over the JSON API. Interactive, clickable,
// no page reloads: click a target to drill in, expand the resource tree, run
// approve/reject/rollback/sync via fetch, with live polling.
const { createApp } = Vue;

createApp({
  data() {
    return {
      view: 'dashboard',
      ref: null,
      dash: { counts: {}, drift: [], rollouts: [], canSync: false },
      detail: null,
      expanded: {},
      toast: '', toastErr: false,
      busy: false,
      lastSync: null,
    };
  },
  mounted() {
    this.refresh();
    setInterval(() => this.refresh(), 4000);
    // deep-link: #target=<ref>
    const m = location.hash.match(/target=(.+)$/);
    if (m) this.open(decodeURIComponent(m[1]));
  },
  methods: {
    async get(u) { const r = await fetch(u); if (!r.ok) throw new Error(await r.text()); return r.json(); },
    async refresh() {
      try {
        if (this.view === 'dashboard') this.dash = await this.get('/ui/api/dashboard');
        else if (this.ref) this.detail = await this.get('/ui/api/target?ref=' + encodeURIComponent(this.ref));
      } catch (e) { /* transient; keep last good state */ }
    },
    open(ref) { this.ref = ref; this.view = 'detail'; this.detail = null; this.expanded = {}; location.hash = 'target=' + encodeURIComponent(ref); this.refresh(); },
    back() { this.view = 'dashboard'; this.ref = null; location.hash = ''; this.refresh(); },
    flash(msg, err) { this.toast = msg; this.toastErr = !!err; setTimeout(() => { this.toast = ''; }, 3200); },
    async act(u, body, label) {
      this.busy = true;
      try {
        const r = await fetch(u, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
        const j = await r.json().catch(() => ({}));
        if (!r.ok || j.error) { this.flash('✗ ' + (j.error || ('HTTP ' + r.status)), true); }
        else { this.flash('✓ ' + (label || 'done')); }
      } catch (e) { this.flash('✗ ' + e.message, true); }
      this.busy = false;
      this.refresh();
    },
    approve(id) { this.act('/ui/api/approve', { id }, 'approved'); },
    reject(id) { this.act('/ui/api/reject', { id }, 'rejected'); },
    rollback(t) { if (confirm('Roll back ' + t + ' to its previous state?')) this.act('/ui/api/rollback', { target: t }, 'rollback started'); },
    sync() { this.act('/ui/api/sync', {}, 'sync triggered'); },
    toggle(name) { this.expanded[name] = !this.expanded[name]; },
    roots() { return (this.detail && this.detail.resources || []).filter(r => !r.parent); },
    children(name) { return (this.detail && this.detail.resources || []).filter(r => r.parent === name); },
    cls(p) { return 'pill p-' + p; },
    short(s) { return s && s.length > 12 ? s.slice(0, 12) : (s || ''); },
  },
  template: `
<header>
  <h1>Rolloffs</h1>
  <span class="sub" v-if="view==='dashboard'">rollout operations · live</span>
  <span class="sub" v-else>{{ ref }}</span>
  <span class="spacer"></span>
  <button v-if="dashCanSync" class="sync" :disabled="busy" @click="sync">⟳ Sync now</button>
</header>
<main>
  <!-- DASHBOARD -->
  <template v-if="view==='dashboard'">
    <div class="chips">
      <span class="chip">promoted <b class="ok">{{ dash.counts.promoted||0 }}</b></span>
      <span class="chip">awaiting <b class="warn">{{ dash.counts['awaiting-approval']||0 }}</b></span>
      <span class="chip">rolled-back <b class="bad">{{ dash.counts['rolled-back']||0 }}</b></span>
      <span class="chip">in-flight <b>{{ (dash.counts.deploying||0)+(dash.counts.verifying||0) }}</b></span>
    </div>

    <h2>Targets <span v-if="hasDrift" class="badge drift">drift detected</span></h2>
    <table v-if="dash.drift.length">
      <thead><tr><th>Target</th><th>State</th><th>Phase</th><th>Desired</th><th>Observed</th></tr></thead>
      <tbody>
        <tr v-for="d in dash.drift" :key="d.target" class="click" @click="open(d.target)">
          <td>{{ d.target }}</td>
          <td><span v-if="d.drifted" class="badge drift">DRIFT</span><span v-else class="badge sync">in sync</span></td>
          <td><span :class="cls(d.phase)">{{ d.phase }}</span></td>
          <td class="mono">{{ short(d.desired) }}</td>
          <td class="mono">{{ short(d.observed) }}</td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">No targets yet.</div>

    <h2>Rollouts</h2>
    <table v-if="dash.rollouts.length">
      <thead><tr><th>Rollout</th><th>Target</th><th>Phase</th><th>Strategy</th><th>By</th><th></th></tr></thead>
      <tbody>
        <tr v-for="r in dash.rollouts" :key="r.id" class="click" @click="open(r.target)">
          <td class="mono">{{ r.id }}</td>
          <td>{{ r.target }}</td>
          <td><span :class="cls(r.phase)">{{ r.phase }}</span></td>
          <td>{{ r.strategy }}</td>
          <td class="mono">{{ r.by }}</td>
          <td>
            <span v-if="r.phase==='awaiting-approval'" @click.stop>
              <button class="ok" :disabled="busy" @click="approve(r.id)">Approve</button>
              <button class="bad" :disabled="busy" @click="reject(r.id)">Reject</button>
            </span>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">No rollouts yet.</div>
  </template>

  <!-- DETAIL -->
  <template v-else>
    <p><a @click="back">← dashboard</a></p>
    <div v-if="!detail" class="empty">loading…</div>
    <template v-else>
      <div class="grid">
        <div class="kv"><div class="k">Phase</div><div class="v"><span :class="cls(detail.rollout.phase)">{{ detail.rollout.phase }}</span></div></div>
        <div class="kv"><div class="k">Strategy</div><div class="v">{{ detail.rollout.strategy }}</div></div>
        <div class="kv"><div class="k">Desired</div><div class="v mono">{{ short(detail.rollout.desired) }}</div></div>
        <div class="kv"><div class="k">Rollout</div><div class="v mono">{{ detail.rollout.id }}</div></div>
      </div>
      <div class="actions">
        <button v-if="detail.awaiting" class="ok" :disabled="busy" @click="approve(detail.rollout.id)">Approve</button>
        <button v-if="detail.awaiting" class="bad" :disabled="busy" @click="reject(detail.rollout.id)">Reject</button>
        <button class="bad" :disabled="busy" @click="rollback(ref)">↩ Rollback</button>
      </div>

      <h2>Live resources</h2>
      <table v-if="roots().length">
        <thead><tr><th>Kind</th><th>Name</th><th>Namespace</th><th>Status</th></tr></thead>
        <tbody>
          <template v-for="root in roots()" :key="root.name">
            <tr :class="{click: children(root.name).length}" @click="toggle(root.name)">
              <td>
                <span class="caret" v-if="children(root.name).length">{{ expanded[root.name] ? '▾' : '▸' }}</span>
                <span class="caret" v-else></span>{{ root.kind }}
              </td>
              <td class="mono">{{ root.name }}</td>
              <td class="mono">{{ root.namespace }}</td>
              <td><span class="dot" :class="root.status.includes('ready')?'up':'down'"></span>{{ root.status }}</td>
            </tr>
            <tr v-for="c in children(root.name)" v-show="expanded[root.name]" :key="c.name" class="child">
              <td><span class="tw">└─</span>{{ c.kind }}</td>
              <td class="mono">{{ c.name }}</td>
              <td class="mono">{{ c.namespace }}</td>
              <td><span class="dot" :class="c.status.includes('ready')?'up':'down'"></span>{{ c.status }}</td>
            </tr>
          </template>
        </tbody>
      </table>
      <div v-else class="note">No live resources (target may not support inspection).</div>

      <h2>Diff (desired → live)</h2>
      <pre v-if="detail.diff" class="diff">{{ detail.diff }}</pre>
      <div v-else class="note">{{ detail.diffNote || 'no diff' }}</div>

      <h2>History</h2>
      <table v-if="detail.history.length">
        <thead><tr><th>When</th><th>Phase</th><th>Rollout</th><th>By</th></tr></thead>
        <tbody>
          <tr v-for="h in detail.history" :key="h.at+h.phase">
            <td class="mono">{{ h.at }}</td>
            <td><span :class="cls(h.phase)">{{ h.phase }}</span></td>
            <td class="mono">{{ h.rollout }}</td>
            <td class="mono">{{ h.by }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="note">No history.</div>
    </template>
  </template>

  <div v-if="toast" class="toast" :class="{err:toastErr}">{{ toast }}</div>
</main>
  `,
  computed: {
    dashCanSync() { return this.view === 'dashboard' ? this.dash.canSync : (this.detail && this.detail.canSync); },
    hasDrift() { return this.dash.drift.some(d => d.drifted); },
  },
}).mount('#app');

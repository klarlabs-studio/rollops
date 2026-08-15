// Root component template. build.mjs compiles this to render.generated.ts so the
// shipped console can use runtime-only Vue under a strict CSP.
export const template = `
<div class="topbar" v-show="busy || inflight"><div class="topbar-fill"></div></div>
<header>
  <h1>Rollops</h1>
  <span class="sub" v-if="view==='dashboard'">rollout operations · live</span>
  <span class="sub" v-else>{{ ref }}</span>
  <span v-if="inflight" class="livedot" title="rollout in progress">● syncing</span>
  <span class="spacer"></span>
  <button v-if="dashCanSync" class="sync" :disabled="busy" @click="sync">
    <span class="spin" :class="{on:busy}">⟳</span> {{ busy ? 'Syncing…' : 'Sync now' }}
  </button>
  <button v-if="view==='dashboard'" class="bad" :disabled="busy" @click="toggleFreeze">{{ dash.frozen ? '❄ Unfreeze' : '❄ Freeze' }}</button>
</header>
<div v-if="dash.frozen" class="freeze-banner">❄ Rollouts are frozen — all applies are blocked.<span v-if="dash.freezeReason"> ({{ dash.freezeReason }})</span></div>
<main>
  <!-- DASHBOARD -->
  <template v-if="view==='dashboard'">
    <div class="chips" role="group" aria-label="Filter by status">
      <button class="chip" :class="{on:facet==='promoted'}" @click="toggleFacet('promoted')">promoted <b class="ok">{{ dash.counts.promoted||0 }}</b></button>
      <button class="chip" :class="{on:facet==='awaiting'}" @click="toggleFacet('awaiting')">awaiting <b class="warn">{{ dash.counts['awaiting-approval']||0 }}</b></button>
      <button class="chip" :class="{on:facet==='degraded'}" @click="toggleFacet('degraded')">rolled-back <b class="bad">{{ dash.counts['rolled-back']||0 }}</b></button>
          <button class="chip" :class="{on:facet==='active'}" @click="toggleFacet('active')">in-flight <b>{{ (dash.counts.deploying||0)+(dash.counts.paused||0)+(dash.counts.verifying||0) }}</b></button>
      <button class="chip" :class="{on:facet==='drift'}" @click="toggleFacet('drift')">drifted <b :class="hasDrift?'bad':''">{{ driftCount }}</b></button>
    </div>
    <div class="filter">
      <span aria-hidden="true">⌕</span>
      <input ref="filter" v-model="query" placeholder="Filter targets, phases, actors — press /" aria-label="Filter applications" autocomplete="off">
      <button v-if="query" @click="query=''" title="Clear filter" aria-label="Clear filter">×</button>
    </div>

    <template v-if="attention.length">
      <h2>Attention <span class="badge drift">{{ attention.length }}</span></h2>
      <div class="attention">
        <div v-for="a in attention" :key="a.key" class="attention-item" :class="'att-'+a.kind">
          <button class="att-main" @click="open(a.target)">
            <span class="att-kind">{{ a.kind }}</span>
            <span class="att-target">{{ a.target }}</span>
            <span :class="cls(a.phase)">{{ a.phase }}</span>
            <span v-if="a.by" class="mono">{{ a.by }}</span>
          </button>
          <span v-if="a.kind==='approval' && a.rolloutID" class="att-actions">
            <button class="ok" :disabled="busy" @click="approve(a.rolloutID)">Approve</button>
            <button class="bad" :disabled="busy" @click="reject(a.rolloutID)">Reject</button>
          </span>
          <span v-else-if="a.kind==='active' && a.rolloutID && (a.phase==='deploying' || a.phase==='paused')" class="att-actions">
            <button v-if="a.phase==='deploying'" :disabled="busy" @click="pause(a.rolloutID)">Pause canary</button>
            <button v-if="a.phase==='paused'" :disabled="busy" @click="resume(a.rolloutID)">Resume canary</button>
            <button class="bad" :disabled="busy" @click="abort(a.rolloutID)">Abort canary</button>
          </span>
          <span v-else-if="a.kind==='drift'" class="att-actions">
            <button v-if="dashCanSync" class="sync" :disabled="busy" @click="sync">Sync</button>
            <button @click="open(a.target)">Open</button>
          </span>
          <span v-else class="att-actions">
            <button @click="open(a.target)">Open</button>
          </span>
        </div>
      </div>
    </template>

    <h2>Applications <span v-if="hasDrift" class="badge drift">drift detected</span></h2>
    <table v-if="filteredApps.length" class="apps-table">
      <thead><tr><th>Application</th><th>Health</th><th>Sync</th><th>Phase</th><th>Operational risk</th><th>Desired</th><th>Observed</th><th>Last actor</th><th>Updated</th></tr></thead>
      <tbody>
        <tr v-for="a in filteredApps" :key="a.target" class="click" @click="open(a.target)">
          <td>
            <div class="app-name">{{ a.target }}</div>
            <div class="app-meta"><span v-if="a.strategy">{{ a.strategy }}</span><span v-if="a.rolloutID" class="mono">{{ a.rolloutID }}</span></div>
          </td>
          <td><span :class="healthClass(a.health)"><span class="dot" :class="hue(a.health)"></span>{{ a.health }}</span></td>
          <td><span class="badge" :class="a.sync==='Synced'?'sync':'drift'">{{ a.sync }}</span></td>
          <td>
            <span :class="cls(a.phase)">{{ a.phase }}</span>
            <span v-if="a.active && a.stepTotal" class="step-mini mono" :title="a.strategy+' step '+a.stepIndex+'/'+a.stepTotal+' at '+a.stepWeight+'%'">{{ a.stepIndex }}/{{ a.stepTotal }} · {{ a.stepWeight }}%</span>
          </td>
          <td><span :class="riskClass(a.risk)" :title="a.riskScore>0 ? 'risk score '+a.riskScore.toFixed(2)+' (blast-radius)' : 'situational (risk gate off)'">{{ a.risk }}<span v-if="a.riskScore>0" class="risk-score">{{ a.riskScore.toFixed(2) }}</span></span></td>
          <td class="mono">{{ short(a.desired) }}</td>
          <td class="mono">{{ short(a.observed) }}</td>
          <td><span v-if="a.by" class="actor" :title="a.byKind || 'actor'"><span class="actor-ic" aria-hidden="true">{{ actorIcon(a.byKind) }}</span><span class="mono">{{ actorName(a.by) }}</span></span></td>
          <td class="mono" :title="absTime(a.at)">{{ ago(a.at) }}</td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">{{ apps.length ? 'No applications match the filter.' : 'No applications yet.' }}</div>

    <h2>Activity</h2>
    <table v-if="filteredRollouts.length" class="activity-table">
      <thead><tr><th>Rollout</th><th>Target</th><th>Phase</th><th>Strategy</th><th>By</th><th>When</th><th></th></tr></thead>
      <tbody>
        <tr v-for="r in filteredRollouts" :key="r.id" class="click" @click="open(r.target)">
          <td class="mono">{{ r.id }}</td>
          <td>{{ r.target }}</td>
          <td><span :class="cls(r.phase)">{{ r.phase }}</span></td>
          <td>{{ r.strategy }}</td>
          <td><span class="actor" :title="r.byKind || 'actor'"><span class="actor-ic" aria-hidden="true">{{ actorIcon(r.byKind) }}</span><span class="mono">{{ actorName(r.by) }}</span></span></td>
          <td class="mono" :title="absTime(r.at)">{{ ago(r.at) }}</td>
          <td>
            <span v-if="r.phase==='awaiting-approval'" @click.stop>
              <button class="ok" :disabled="busy" @click="approve(r.id)">Approve</button>
              <button class="bad" :disabled="busy" @click="reject(r.id)">Reject</button>
            </span>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">{{ dash.rollouts.length ? 'No rollouts match the filter.' : 'No rollouts yet.' }}</div>
  </template>

  <!-- DETAIL -->
  <template v-else>
    <div v-if="!detail" class="empty">loading…</div>
    <template v-else>
      <div class="statusbar">
        <a class="crumb" @click="back">← Applications</a>
        <div class="stat"><div class="sl">Phase</div><div class="sv"><span :class="cls(detail.rollout.phase)">{{ detail.rollout.phase }}</span></div></div>
        <div class="stat"><div class="sl">Sync</div><div class="sv"><span class="badge" :class="synced?'sync':'drift'">{{ synced?'Synced':'OutOfSync' }}</span></div></div>
        <div class="stat"><div class="sl">Strategy</div><div class="sv">{{ detail.rollout.strategy }} <span class="gitsrc" title="Strategy is desired state — defined in the rollout config in Git. To change it, edit the config and Sync. (GitOps: Git is the source of truth.)">⌥ from Git</span></div></div>
        <div class="stat"><div class="sl">Desired</div><div class="sv mono">{{ short(detail.rollout.desired) }}</div></div>
        <div class="stat" v-if="detail.rollout.risk>0"><div class="sl">Risk</div><div class="sv"><span :class="riskClass(riskOf(detail.rollout.risk))" :title="'blast-radius score'">{{ riskOf(detail.rollout.risk) }} {{ detail.rollout.risk.toFixed(2) }}</span></div></div>
        <div class="stat" v-if="detail.rollout.at"><div class="sl">Updated</div><div class="sv mono" :title="absTime(detail.rollout.at)">{{ ago(detail.rollout.at) }}</div></div>
        <span class="spacer"></span>
        <div class="actions">
          <button v-if="detail.awaiting" class="ok" :disabled="busy" @click="approve(detail.rollout.id)">Approve</button>
          <button v-if="detail.awaiting" class="bad" :disabled="busy" @click="reject(detail.rollout.id)">Reject</button>
          <button v-if="detail.rollout.phase==='verifying'" class="ok" :disabled="busy" @click="promote(detail.rollout.id)">✓ Promote</button>
          <button v-if="detail.rollout.phase==='deploying'" :disabled="busy" @click="pause(detail.rollout.id)">Pause canary</button>
          <button v-if="detail.rollout.phase==='paused'" :disabled="busy" @click="resume(detail.rollout.id)">Resume canary</button>
          <button v-if="detail.rollout.phase==='deploying' || detail.rollout.phase==='paused'" class="bad" :disabled="busy" @click="abort(detail.rollout.id)">Abort canary</button>
          <button class="bad" :disabled="busy" @click="rollback(ref)">↩ Rollback</button>
          <span class="vsplit"></span>
          <button :class="{active:mode==='graph'}" @click="mode='graph'" title="Tree view">⤳</button>
          <button :class="{active:mode==='list'}" @click="mode='list'" title="List view">≣</button>
        </div>
      </div>

      <div v-if="steps.length" class="stepbar" :title="detail.rollout.strategy+' — '+detail.rollout.stepIndex+'/'+detail.rollout.stepTotal+' steps, '+detail.rollout.stepWeight+'% traffic'">
        <span class="stepbar-label">{{ detail.rollout.strategy }} steps</span>
        <span v-for="s in steps" :key="s.n" class="step" :class="s.state"></span>
        <span class="stepbar-meta mono">{{ detail.rollout.stepIndex }}/{{ detail.rollout.stepTotal }} · {{ detail.rollout.stepWeight }}%</span>
      </div>

      <div class="summary-grid">
        <div class="summary-card">
          <div class="sl">Desired from Git</div>
          <div class="sv mono">{{ short(detail.rollout.desired) || 'none' }}</div>
          <div class="hint">{{ detail.rollout.strategy }} strategy</div>
        </div>
        <div class="summary-card">
          <div class="sl">Observed live</div>
          <div class="sv"><span class="badge" :class="synced?'sync':'drift'">{{ synced?'matches Git':'drifted' }}</span></div>
          <div class="hint">{{ detail.resources.length }} resources reported</div>
        </div>
        <div class="summary-card">
          <div class="sl">Runtime state</div>
          <div class="sv"><span :class="cls(detail.rollout.phase)">{{ detail.rollout.phase }}</span></div>
          <div class="hint mono">{{ detail.rollout.id }}</div>
        </div>
        <div class="summary-card">
          <div class="sl">Operator action</div>
          <div class="sv">{{ detail.awaiting ? 'Approval required' : (detail.rollout.phase==='deploying' ? 'Pause or abort canary' : (detail.rollout.phase==='paused' ? 'Resume or abort canary' : (synced ? 'No action' : 'Sync or rollback'))) }}</div>
          <div class="hint">RBAC-checked operation surface</div>
        </div>
      </div>

      <!-- GRAPH -->
      <div v-if="mode==='graph'" class="canvas" ref="canvas">
        <div class="zoom">
          <button @click.stop="zoomBy(1.2)">+</button>
          <button @click.stop="zoomBy(0.83)">−</button>
          <button @click.stop="zoomFit" title="Reset">⊡</button>
          <span class="zlevel">{{ Math.round(zt.k*100) }}%</span>
        </div>
        <div class="viewport" :style="{transform:'translate('+zt.x+'px,'+zt.y+'px) scale('+zt.k+')'}">
          <svg class="edges" :width="graph.w" :height="graph.h">
            <path v-for="e in graph.edges" :key="e.id" :d="e.d" :class="{hot:e.hot}" />
          </svg>
          <div v-for="n in graph.nodes" :key="n.id" class="gnode" :class="[{sel:sel===n.id}]"
               :style="{left:n.x+'px', top:n.y+'px'}" @mousedown.stop @click.stop="pick(n.id)">
            <span class="gicon" :class="n.hue">{{ icon(n.kind) }}</span>
            <div class="gbody">
              <div class="gkind">{{ n.kind }}</div>
              <div class="gname mono">{{ n.name }}</div>
            </div>
            <span class="heart" :class="n.hue">♥</span>
          </div>
        </div>
        <div class="ghint">drag to pan · +/− to zoom · click a node</div>
      </div>

      <!-- LIST -->
      <template v-else>
        <h2>Live resources</h2>
        <table v-if="roots().length">
          <thead><tr><th>Kind</th><th>Name</th><th>Namespace</th><th>Status</th></tr></thead>
          <tbody>
            <template v-for="root in roots()" :key="root.name">
              <tr :class="{click: children(root.name).length}" @click="toggle(root.name)">
                <td><span class="caret" v-if="children(root.name).length">{{ expanded[root.name] ? '▾' : '▸' }}</span><span class="caret" v-else></span>{{ root.kind }}</td>
                <td class="mono">{{ root.name }}</td>
                <td class="mono">{{ root.namespace }}</td>
                <td><span class="dot" :class="hue(root.status)"></span>{{ root.status }}</td>
              </tr>
              <tr v-for="c in children(root.name)" v-show="expanded[root.name]" :key="c.name" class="child">
                <td><span class="tw">└─</span>{{ c.kind }}</td>
                <td class="mono">{{ c.name }}</td>
                <td class="mono">{{ c.namespace }}</td>
                <td><span class="dot" :class="hue(c.status)"></span>{{ c.status }}</td>
              </tr>
            </template>
          </tbody>
        </table>
        <div v-else class="note">No live resources (target may not support inspection).</div>
      </template>

      <div class="cols">
        <div>
          <h2>Diff (desired → live)</h2>
          <pre v-if="detail.diff" class="diff"><span v-for="(l,i) in diffLines" :key="i" class="dl" :class="l.c">{{ l.t }}
</span></pre>
          <div v-else class="note">{{ detail.diffNote || 'no changes — live matches desired' }}</div>
        </div>
        <div>
          <h2>Timeline</h2>
          <div v-if="detail.history.length" class="timeline">
            <div v-for="h in detail.history" :key="h.at+h.phase" class="event">
              <span class="event-dot" :class="hue(h.phase)"></span>
              <div class="event-body">
                <div><span :class="cls(h.phase)">{{ h.phase }}</span> <span class="mono">{{ h.rollout }}</span></div>
                <div v-if="h.note" class="event-note">{{ h.note }}</div>
                <div class="event-meta"><span class="mono" :title="absTime(h.at)">{{ ago(h.at) }}</span><span class="actor" :title="h.byKind || 'actor'"><span class="actor-ic" aria-hidden="true">{{ actorIcon(h.byKind) }}</span><span class="mono">{{ actorName(h.by) }}</span></span></div>
              </div>
            </div>
          </div>
          <div v-else class="note">No history.</div>
        </div>
      </div>
    </template>
  </template>

  <div v-if="toast" class="toast" :class="{err:toastErr}" role="status" aria-live="polite">{{ toast }}</div>
  <div v-if="authFailed" class="stale" role="alert">⚠ unauthorized — sign in again (reload the page) to resume live updates</div>
  <div v-else-if="failures>2 && loadedOnce" class="stale" role="alert">⚠ live updates unavailable — showing last known state</div>
  <div v-else-if="failures>2" class="stale" role="alert">⚠ can't reach the server — nothing loaded yet</div>

  <div v-if="confirmTarget" class="modal-back" @click.self="confirmTarget=''">
    <div class="modal" role="dialog" aria-modal="true" aria-label="Confirm rollback">
      <h3>Roll back {{ confirmTarget }}?</h3>
      <p>The target returns to its previous desired state. The change is recorded in history and audit.</p>
      <label class="force-opt"><input type="checkbox" v-model="confirmForce" /> Force — override the backward-compatibility gate (release ran a non-backwardCompatible migration with no reverse command)</label>
      <div class="modal-actions">
        <button @click="confirmTarget=''">Cancel</button>
        <button class="bad" :disabled="busy" @click="confirmRollback">↩ Roll back</button>
      </div>
    </div>
  </div>
</main>
`;

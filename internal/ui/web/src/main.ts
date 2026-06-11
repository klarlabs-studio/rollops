// Rollops web console — Vue 3 SPA in TypeScript over the JSON API. Every
// response is validated with Zod (schemas.ts). Interactive, no page reloads:
// drill into a target, explore the ArgoCD-style resource DAG (zoom/pan), run
// approve/reject/rollback/sync, with live 4s polling.
import { createApp, defineComponent } from 'vue';
import { stratify, tree, type HierarchyNode } from 'd3-hierarchy';
import { linkHorizontal } from 'd3-shape';
import { select, type Selection } from 'd3-selection';
import { zoom, zoomIdentity, type ZoomBehavior } from 'd3-zoom';
import {
  DashboardSchema,
  TargetSchema,
  type Dashboard,
  type Drift,
  type Rollout,
  type Target,
  type GraphNode,
  type GraphEdge,
  type Hue,
} from './schemas';

// d3-zoom behaviour + bound selection live outside Vue's reactive tree (single
// app instance). zoomEl tracks the bound element so we re-attach when the canvas
// node changes (target switch / mode toggle).
let zoomBehavior: ZoomBehavior<HTMLElement, unknown> | null = null;
let zoomSel: Selection<HTMLElement, unknown, null, undefined> | null = null;
let zoomEl: HTMLElement | null = null;

// Node record fed to d3.stratify.
interface Rec {
  id: string;
  parentId: string;
  meta: { kind: string; name: string; ns?: string; status: string };
}

interface AttentionItem {
  key: string;
  kind: 'approval' | 'drift' | 'active';
  target: string;
  phase: string;
  rolloutID?: string;
  by?: string;
}

interface ApplicationRow {
  target: string;
  phase: string;
  sync: 'Synced' | 'OutOfSync';
  health: 'Healthy' | 'Progressing' | 'Degraded' | 'Unknown';
  risk: 'Low' | 'Medium' | 'High';
  riskScore: number; // real decisionkit score (0 when the risk gate is off)
  desired: string;
  observed: string;
  rolloutID: string;
  strategy: string;
  by: string;
  byKind: string;
  at: string;
  stepIndex: number;
  stepTotal: number;
  stepWeight: number;
  changed: string;
  active: boolean;
  awaiting: boolean;
}

// ago renders an RFC 3339 timestamp as a compact relative time ("2m ago").
function ago(iso: string): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 45) return s + 's ago';
  const m = Math.round(s / 60);
  if (m < 45) return m + 'm ago';
  const h = Math.round(m / 60);
  if (h < 36) return h + 'h ago';
  return Math.round(h / 24) + 'd ago';
}

// actorIcon distinguishes the three operator kinds — agents are first-class
// operators in Rollops, so the console shows *what kind* of actor acted.
function actorIcon(kind: string): string {
  if (kind === 'agent') return '🤖';
  if (kind === 'ci') return '⚙';
  if (kind === 'human') return '👤';
  return '·';
}
// render is generated from template.ts at build time (see build.mjs) so the
// runtime needs no template compiler and no eval — strict CSP compatible.
// @ts-ignore generated at build time by build.mjs
import { render } from './render.generated';

// Layout geometry for the resource graph.
const NW = 210,
  NH = 56,
  GX = 78,
  GY = 20;

const emptyDash: Dashboard = { counts: {}, drift: [], rollouts: [], canSync: false };

interface State {
  view: 'dashboard' | 'detail';
  ref: string | null;
  dash: Dashboard;
  detail: Target | null;
  mode: 'graph' | 'list';
  zt: { x: number; y: number; k: number };
  sel: string | null;
  expanded: Record<string, boolean>;
  query: string;
  facet: string; // chip facet: '' | 'promoted' | 'awaiting' | 'degraded' | 'active' | 'drift'
  toast: string;
  toastErr: boolean;
  busy: boolean;
  confirmTarget: string; // rollback confirmation modal ('' = closed)
  failures: number; // consecutive refresh failures → stale banner
  authFailed: boolean; // last refresh got 401/403 → unauthorized banner
  loadedOnce: boolean; // any successful load yet — picks the stale message
  now: number; // ticking clock for relative timestamps
}

function hueOf(s: string): Hue {
  const x = (s || '').toLowerCase();
  if (/(ready|running|promoted|healthy|synced|ok)/.test(x)) return 'up';
  if (/(progress|deploy|verify|pending|validating|awaiting)/.test(x)) return 'warn';
  return 'down';
}

const App = defineComponent({
  data(): State {
    return {
      view: 'dashboard',
      ref: null,
      dash: emptyDash,
      detail: null,
      mode: 'graph',
      zt: { x: 0, y: 0, k: 1 },
      sel: null,
      expanded: {},
      query: '',
      facet: '',
      toast: '',
      toastErr: false,
      busy: false,
      confirmTarget: '',
      failures: 0,
      authFailed: false,
      loadedOnce: false,
      now: Date.now(),
    };
  },
  mounted() {
    void this.refresh();
    // Steady 4s poll, paused while the tab is hidden; an immediate refresh on
    // return keeps the console honest without polling in the background.
    setInterval(() => {
      if (!document.hidden) void this.refresh();
    }, 4000);
    setInterval(() => {
      this.now = Date.now();
    }, 10000);
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) void this.refresh();
    });
    window.addEventListener('keydown', (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName;
      if (e.key === '/' && tag !== 'INPUT' && tag !== 'TEXTAREA') {
        e.preventDefault();
        (this.$refs.filter as HTMLInputElement | undefined)?.focus();
      } else if (e.key === 'Escape') {
        if (this.confirmTarget) this.confirmTarget = '';
        else if (this.query || this.facet) {
          this.query = '';
          this.facet = '';
        } else if (this.view === 'detail') this.back();
      }
    });
    const m = location.hash.match(/target=(.+)$/);
    if (m && m[1]) this.open(decodeURIComponent(m[1]));
  },
  updated() {
    // Lazily bind d3-zoom once the canvas exists (after detail renders).
    if (this.view === 'detail' && this.mode === 'graph' && this.detail) this.setupZoom();
  },
  methods: {
    async fetchJSON(u: string): Promise<unknown> {
      const r = await fetch(u);
      if (!r.ok) {
        const e = new Error(await r.text()) as Error & { status?: number };
        e.status = r.status;
        throw e;
      }
      return r.json();
    },
    async refresh(): Promise<void> {
      try {
        if (this.view === 'dashboard') {
          this.dash = DashboardSchema.parse(await this.fetchJSON('/ui/api/dashboard'));
        } else if (this.ref) {
          this.detail = TargetSchema.parse(
            await this.fetchJSON('/ui/api/target?ref=' + encodeURIComponent(this.ref)),
          );
        }
        this.failures = 0;
        this.authFailed = false;
        this.loadedOnce = true;
      } catch (e) {
        // transient or validation miss; keep last good state, count for the
        // stale banner so the operator knows the view stopped updating.
        // 401/403 won't self-heal — flag it immediately and by name.
        this.failures++;
        const status = (e as { status?: number }).status;
        if (status === 401 || status === 403) this.authFailed = true;
      }
      const n = this.attention.length;
      document.title = n ? `(${n}) Rollops` : 'Rollops';
    },
    open(ref: string): void {
      this.ref = ref;
      this.view = 'detail';
      this.detail = null;
      this.sel = null;
      this.zt = { x: 0, y: 0, k: 1 };
      this.expanded = {};
      location.hash = 'target=' + encodeURIComponent(ref);
      void this.refresh();
    },
    back(): void {
      this.view = 'dashboard';
      this.ref = null;
      location.hash = '';
      void this.refresh();
    },
    flash(msg: string, err?: boolean): void {
      this.toast = msg;
      this.toastErr = !!err;
      setTimeout(() => {
        this.toast = '';
      }, 3200);
    },
    async act(u: string, body: Record<string, string>, label: string): Promise<void> {
      this.busy = true;
      try {
        const r = await fetch(u, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body || {}),
        });
        const j = (await r.json().catch(() => ({}))) as { error?: string };
        if (!r.ok || j.error) this.flash('✗ ' + (j.error || 'HTTP ' + r.status), true);
        else this.flash('✓ ' + label);
      } catch (e) {
        this.flash('✗ ' + (e as Error).message, true);
      }
      this.busy = false;
      void this.refresh();
    },
    approve(id: string): void {
      void this.act('/ui/api/approve', { id }, 'approved').then(() => this.burst());
    },
    reject(id: string): void {
      void this.act('/ui/api/reject', { id }, 'rejected').then(() => this.burst());
    },
    rollback(t: string): void {
      this.confirmTarget = t;
    },
    confirmRollback(): void {
      const t = this.confirmTarget;
      this.confirmTarget = '';
      if (t)
        void this.act('/ui/api/rollback', { target: t }, 'rollback started').then(() => this.burst());
    },
    sync(): void {
      // The server kicks off an async reconcile; burst-refresh so the UI visibly
      // moves through the resulting phase transitions instead of looking idle.
      void this.act('/ui/api/sync', {}, 'sync triggered').then(() => this.burst());
    },
    // burst polls faster than the steady 4s loop for a short window, so an
    // action's effect (a new rollout progressing) shows up promptly.
    burst(): void {
      let n = 0;
      const t = setInterval(() => {
        void this.refresh();
        if (++n >= 8) clearInterval(t);
      }, 1200);
    },

    // graph interaction
    // setupZoom binds a d3-zoom behaviour to the canvas; pan via drag, zoom via
    // wheel. The filter keeps node-card clicks and the zoom toolbar interactive.
    setupZoom(): void {
      const el = this.$refs.canvas as HTMLElement | undefined;
      if (!el || el === zoomEl) return;
      zoomEl = el;
      zoomSel = select<HTMLElement, unknown>(el);
      zoomBehavior = zoom<HTMLElement, unknown>()
        .scaleExtent([0.4, 2])
        .filter((e: Event) => {
          const t = e.target as HTMLElement;
          return !t.closest('.gnode') && !t.closest('.zoom');
        })
        .on('zoom', (e: { transform: { x: number; y: number; k: number } }) => {
          this.zt = { x: e.transform.x, y: e.transform.y, k: e.transform.k };
        });
      zoomSel.call(zoomBehavior);
      zoomSel.call(zoomBehavior.transform, zoomIdentity);
    },
    zoomBy(f: number): void {
      if (zoomSel && zoomBehavior) zoomSel.call(zoomBehavior.scaleBy, f);
    },
    zoomFit(): void {
      if (zoomSel && zoomBehavior) zoomSel.call(zoomBehavior.transform, zoomIdentity);
    },
    pick(id: string): void {
      this.sel = this.sel === id ? null : id;
    },

    // list-view tree
    toggle(name: string): void {
      this.expanded[name] = !this.expanded[name];
    },
    roots() {
      return (this.detail?.resources ?? []).filter((r) => !r.parent);
    },
    children(name: string) {
      return (this.detail?.resources ?? []).filter((r) => r.parent === name);
    },

    cls(p: string): string {
      return 'pill p-' + p;
    },
    short(s: string): string {
      return s && s.length > 12 ? s.slice(0, 12) : s || '';
    },
    riskClass(risk: string): string {
      return 'risk risk-' + risk.toLowerCase();
    },
    healthClass(health: string): string {
      return 'health h-' + health.toLowerCase();
    },
    hue: hueOf,
    ago(iso: string): string {
      void this.now; // reactive dependency: re-render as the clock ticks
      return ago(iso);
    },
    absTime(iso: string): string {
      const t = Date.parse(iso);
      return Number.isNaN(t) ? iso : new Date(t).toLocaleString();
    },
    actorIcon,
    actorName(by: string): string {
      const i = by.indexOf('/');
      return i >= 0 ? by.slice(i + 1) : by;
    },
    riskOf(score: number): 'Low' | 'Medium' | 'High' {
      // decisionkit blast-radius scale: approval threshold conventionally 0.5.
      return score < 0.34 ? 'Low' : score < 0.67 ? 'Medium' : 'High';
    },
    toggleFacet(f: string): void {
      this.facet = this.facet === f ? '' : f;
    },
    icon(kind: string): string {
      const k = (kind || '').toLowerCase();
      if (k === 'app') return '◈';
      if (k === 'deployment') return '⬡';
      if (k === 'replicaset') return '❏';
      if (k === 'pod') return '◉';
      if (k === 'service') return '⇄';
      return '▢';
    },
  },
  computed: {
    needle(): string {
      return this.query.trim().toLowerCase();
    },
    filteredDrift(): Drift[] {
      const q = this.needle;
      if (!q) return this.dash.drift;
      return this.dash.drift.filter((d) =>
        [d.target, d.phase, d.desired, d.observed, d.drifted ? 'drift' : 'synced']
          .join(' ')
          .toLowerCase()
          .includes(q),
      );
    },
    filteredRollouts(): Rollout[] {
      const q = this.needle;
      if (!q) return this.dash.rollouts;
      return this.dash.rollouts.filter((r) =>
        [r.id, r.target, r.phase, r.strategy, r.by].join(' ').toLowerCase().includes(q),
      );
    },
    apps(): ApplicationRow[] {
      const byTarget = new Map<string, Rollout>();
      for (const r of this.dash.rollouts) {
        if (!byTarget.has(r.target)) byTarget.set(r.target, r);
      }
      const targets = new Set<string>([
        ...this.dash.drift.map((d) => d.target),
        ...this.dash.rollouts.map((r) => r.target),
      ]);
      const active = (p: string) => /^(pending|validating|deploying|verifying)$/.test(p);
      const degraded = (p: string) => /^(rolled-back|failed|rejected)$/.test(p);
      return [...targets].sort().map((target) => {
        const d = this.dash.drift.find((x) => x.target === target);
        const r = byTarget.get(target);
        const phase = r?.phase || d?.phase || 'unknown';
        const isActive = active(phase);
        const isAwaiting = phase === 'awaiting-approval';
        const health: ApplicationRow['health'] = degraded(phase)
          ? 'Degraded'
          : isActive || isAwaiting
            ? 'Progressing'
            : phase === 'promoted'
              ? 'Healthy'
              : 'Unknown';
        // Real decisionkit score when the risk gate ran; situational heuristic
        // (drift / degraded / in-flight) when ungated.
        const score = r?.risk ?? 0;
        const risk: ApplicationRow['risk'] =
          score > 0
            ? this.riskOf(score)
            : d?.drifted || degraded(phase)
              ? 'High'
              : isActive || isAwaiting
                ? 'Medium'
                : 'Low';
        return {
          target,
          phase,
          sync: d?.drifted ? 'OutOfSync' : 'Synced',
          health,
          risk,
          riskScore: score,
          desired: d?.desired || '',
          observed: d?.observed || '',
          rolloutID: r?.id || '',
          strategy: r?.strategy || '',
          by: r?.by || '',
          byKind: r?.byKind || '',
          at: r?.at || '',
          stepIndex: r?.stepIndex ?? 0,
          stepTotal: r?.stepTotal ?? 0,
          stepWeight: r?.stepWeight ?? 0,
          changed: r?.id || '',
          active: isActive,
          awaiting: isAwaiting,
        };
      });
    },
    facetApps(): ApplicationRow[] {
      const f = this.facet;
      if (!f) return this.apps;
      const degraded = (p: string) => /^(rolled-back|failed|rejected)$/.test(p);
      return this.apps.filter((a) => {
        if (f === 'promoted') return a.phase === 'promoted';
        if (f === 'awaiting') return a.awaiting;
        if (f === 'degraded') return degraded(a.phase);
        if (f === 'active') return a.active;
        if (f === 'drift') return a.sync === 'OutOfSync';
        return true;
      });
    },
    filteredApps(): ApplicationRow[] {
      const q = this.needle;
      if (!q) return this.facetApps;
      return this.facetApps.filter((a) =>
        [
          a.target,
          a.phase,
          a.sync,
          a.health,
          a.risk,
          a.desired,
          a.observed,
          a.rolloutID,
          a.strategy,
          a.by,
        ]
          .join(' ')
          .toLowerCase()
          .includes(q),
      );
    },
    // steps renders the progressive plan as segments for the detail step bar.
    // stepIndex is the last step that PASSED its health gate; while deploying,
    // the next segment is the one in flight.
    steps(): { n: number; state: 'done' | 'current' | 'todo' }[] {
      const r = this.detail?.rollout;
      if (!r || !r.stepTotal) return [];
      const inFlight = r.phase === 'deploying' && r.stepIndex < r.stepTotal ? r.stepIndex + 1 : 0;
      return Array.from({ length: r.stepTotal }, (_, k) => {
        const n = k + 1;
        return { n, state: n <= r.stepIndex ? 'done' : n === inFlight ? 'current' : 'todo' };
      });
    },
    // diffLines classifies unified-diff lines for syntax colouring.
    diffLines(): { t: string; c: string }[] {
      const d = this.detail?.diff ?? '';
      if (!d) return [];
      return d.split('\n').map((t) => ({
        t,
        c: t.startsWith('+') ? 'add' : t.startsWith('-') ? 'del' : t.startsWith('@@') ? 'hunk' : '',
      }));
    },
    attention(): AttentionItem[] {
      const items: AttentionItem[] = [];
      const seen = new Set<string>();
      const active = (p: string) => /^(pending|validating|deploying|verifying)$/.test(p);

      for (const r of this.dash.rollouts) {
        if (r.phase !== 'awaiting-approval') continue;
        items.push({
          key: 'approval:' + r.id,
          kind: 'approval',
          target: r.target,
          phase: r.phase,
          rolloutID: r.id,
          by: r.by,
        });
        seen.add('approval:' + r.id);
      }
      for (const d of this.dash.drift) {
        if (!d.drifted) continue;
        const key = 'drift:' + d.target;
        if (seen.has(key)) continue;
        items.push({ key, kind: 'drift', target: d.target, phase: d.phase });
        seen.add(key);
      }
      for (const r of this.dash.rollouts) {
        if (!active(r.phase)) continue;
        const key = 'active:' + r.id;
        if (seen.has(key)) continue;
        items.push({
          key,
          kind: 'active',
          target: r.target,
          phase: r.phase,
          rolloutID: r.id,
          by: r.by,
        });
        seen.add(key);
      }

      return items.slice(0, 6);
    },
    dashCanSync(): boolean {
      return this.view === 'dashboard' ? this.dash.canSync : !!this.detail?.canSync;
    },
    hasDrift(): boolean {
      return this.dash.drift.some((d) => d.drifted);
    },
    driftCount(): number {
      return this.dash.drift.filter((d) => d.drifted).length;
    },
    synced(): boolean {
      // Authoritative drift signal from the backend (desired vs observed
      // checksum), not the diff text.
      return !!this.detail && !this.detail.drifted;
    },
    // A rollout mid-flight anywhere relevant → drive the progress bar.
    inflight(): boolean {
      const prog = (p: string) => /^(pending|validating|deploying|verifying)$/.test(p);
      if (this.view === 'detail' && this.detail) return prog(this.detail.rollout.phase);
      return this.dash.rollouts.some((r) => prog(r.phase));
    },
    graph(): { nodes: GraphNode[]; edges: GraphEdge[]; w: number; h: number } {
      if (!this.detail) return { nodes: [], edges: [], w: 0, h: 0 };
      const res = this.detail.resources;
      const appId = '__app__';
      const idOf = (r: { kind: string; name: string }) => r.kind + '/' + r.name;
      const byName: Record<string, string[]> = {};
      res.forEach((r) => {
        const ids = byName[r.name] ?? [];
        ids.push(idOf(r));
        byName[r.name] = ids;
      });
      const parentID = (r: { kind: string; name: string; parent: string }) => {
        if (!r.parent) return appId;
        // Targets often report parent by object name only. Kubernetes commonly
        // has different Kinds with the same name (Deployment + Service), so
        // pick a non-self parent to avoid a self-cycle in d3.stratify.
        return (byName[r.parent] ?? []).find((id) => id !== idOf(r)) ?? appId;
      };

      // Flat records → d3 hierarchy. App is the single synthetic root.
      const recs: Rec[] = [
        {
          id: appId,
          parentId: '',
          meta: { kind: 'App', name: this.ref ?? '', status: this.detail.rollout.phase },
        },
        ...res.map((r) => ({
          id: idOf(r),
          parentId: parentID(r),
          meta: { kind: r.kind, name: r.name, ns: r.namespace, status: r.status },
        })),
      ];

      let root: HierarchyNode<Rec>;
      try {
        root = stratify<Rec>()
          .id((d) => d.id)
          .parentId((d) => d.parentId || null)(recs);
      } catch {
        return { nodes: [], edges: [], w: 0, h: 0 };
      }
      // Horizontal tree: nodeSize is [cross-axis (vertical), depth-axis].
      tree<Rec>().nodeSize([NH + GY, NW + GX])(root);

      type P = HierarchyNode<Rec> & { x: number; y: number };
      const ds = root.descendants() as P[];
      const minX = Math.min(...ds.map((d) => d.x));
      // Screen mapping: left = depth (d.y), top = cross-axis (d.x normalised).
      const left = (d: P) => d.y;
      const top = (d: P) => d.x - minX;

      const nodes: GraphNode[] = ds.map((d) => ({
        id: d.data.id,
        kind: d.data.meta.kind,
        name: d.data.meta.name,
        ns: d.data.meta.ns,
        status: d.data.meta.status,
        hue: hueOf(d.data.meta.status),
        col: d.depth,
        row: 0,
        x: left(d),
        y: top(d),
      }));

      // d3-shape curved connectors, card-edge to card-edge.
      const lh = linkHorizontal<unknown, [number, number]>()
        .x((p) => p[0])
        .y((p) => p[1]);
      const edges: GraphEdge[] = (root.links() as { source: P; target: P }[]).map((l) => ({
        id: l.source.data.id + '>' + l.target.data.id,
        d:
          lh({
            source: [left(l.source) + NW, top(l.source) + NH / 2],
            target: [left(l.target), top(l.target) + NH / 2],
          }) ?? '',
        hot: this.sel === l.target.data.id || this.sel === l.source.data.id,
      }));

      const w = Math.max(...ds.map(left)) + NW;
      const h = Math.max(...ds.map(top)) + NH;
      return { nodes, edges, w, h };
    },
  },
  render,
});

createApp(App).mount('#app');

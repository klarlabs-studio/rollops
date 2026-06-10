// Zod schemas mirror the Go JSON API (internal/ui/ui.go). They validate every
// response at the boundary, so a backend shape change surfaces as a clear parse
// error instead of silent `undefined`s deep in the view. Types are inferred from
// the schemas — single source of truth.
import { z } from 'zod';

// Go marshals a nil slice as JSON `null`; coerce null/undefined to [].
const list = <T extends z.ZodTypeAny>(item: T) =>
  z
    .array(item)
    .nullish()
    .transform((v) => v ?? []);

export const DriftSchema = z.object({
  target: z.string(),
  phase: z.string(),
  desired: z.string(),
  observed: z.string(),
  drifted: z.boolean(),
});

export const RolloutSchema = z.object({
  id: z.string(),
  target: z.string(),
  phase: z.string(),
  strategy: z.string(),
  by: z.string(),
  byKind: z.string().nullish().transform((v) => v ?? ''),
  risk: z.number().nullish().transform((v) => v ?? 0),
  at: z.string().nullish().transform((v) => v ?? ''),
  stepIndex: z.number().nullish().transform((v) => v ?? 0),
  stepTotal: z.number().nullish().transform((v) => v ?? 0),
  stepWeight: z.number().nullish().transform((v) => v ?? 0),
});

export const DashboardSchema = z.object({
  counts: z.record(z.string(), z.number()).nullish().transform((v) => v ?? {}),
  drift: list(DriftSchema),
  rollouts: list(RolloutSchema),
  canSync: z.boolean().nullish().transform((v) => v ?? false),
});

export const ResourceSchema = z.object({
  kind: z.string(),
  name: z.string(),
  namespace: z.string(),
  status: z.string(),
  parent: z.string(),
});

export const HistorySchema = z.object({
  at: z.string(),
  phase: z.string(),
  rollout: z.string(),
  by: z.string(),
  byKind: z.string().nullish().transform((v) => v ?? ''),
  note: z.string().nullish().transform((v) => v ?? ''),
});

export const TargetSchema = z.object({
  ref: z.string(),
  rollout: z.object({
    id: z.string(),
    phase: z.string(),
    strategy: z.string(),
    desired: z.string(),
    risk: z.number().nullish().transform((v) => v ?? 0),
    at: z.string().nullish().transform((v) => v ?? ''),
    stepIndex: z.number().nullish().transform((v) => v ?? 0),
    stepTotal: z.number().nullish().transform((v) => v ?? 0),
    stepWeight: z.number().nullish().transform((v) => v ?? 0),
  }),
  diff: z.string().nullish().transform((v) => v ?? ''),
  diffNote: z.string().nullish().transform((v) => v ?? ''),
  resources: list(ResourceSchema),
  history: list(HistorySchema),
  awaiting: z.boolean().nullish().transform((v) => v ?? false),
  drifted: z.boolean().nullish().transform((v) => v ?? false),
  canSync: z.boolean().nullish().transform((v) => v ?? false),
});

export type Drift = z.infer<typeof DriftSchema>;
export type Rollout = z.infer<typeof RolloutSchema>;
export type Dashboard = z.infer<typeof DashboardSchema>;
export type Resource = z.infer<typeof ResourceSchema>;
export type History = z.infer<typeof HistorySchema>;
export type Target = z.infer<typeof TargetSchema>;

// GraphNode / GraphEdge are derived client-side for the ArgoCD-style DAG.
export interface GraphNode {
  id: string;
  kind: string;
  name: string;
  ns?: string;
  status: string;
  hue: Hue;
  parent?: string;
  col: number;
  row: number;
  x: number;
  y: number;
}
export interface GraphEdge {
  id: string;
  d: string;
  hot: boolean;
}
export type Hue = 'up' | 'warn' | 'down';

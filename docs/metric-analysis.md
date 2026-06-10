# Metric Analysis

Metric analysis is a stable optional Phase 2 feature. It adds a fourth
post-deploy gate after target health, smoke tests, and step execution errors.
It is disabled by default so the v1 path remains observability-free unless an
operator explicitly enables analysis in engine wiring.

## Contract

Analysis lives under `spec.analysis`:

```yaml
analysis:
  provider: prometheus
  address: http://prometheus.monitoring:9090
  interval: 30s
  count: 5
  failureLimit: 1
  metrics:
    - name: errorRate
      query: 'sum(rate(http_requests_total{job="api",code=~"5.."}[1m])) / sum(rate(http_requests_total{job="api"}[1m]))'
    - name: p99
      query: 'histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{job="api"}[1m])) by (le)) * 1000'
  condition: 'errorRate < 0.05 && p99 < 500'
```

Fields:

- `provider`: currently `prometheus`.
- `address`: provider endpoint. Required for Prometheus.
- `metrics`: named scalar queries. Names become CEL variables.
- `condition`: CEL boolean expression over metric names. `true` means healthy.
- `interval`: Go duration between measurements.
- `count`: number of measurements. Defaults to one.
- `failureLimit`: consecutive failed measurements tolerated before rollback.

Malformed providers, missing Prometheus addresses, bad durations, invalid metric
conditions, and non-boolean CEL conditions fail during config load.

## Rollback Behavior

Metric analysis only runs when the engine is configured with metric analysis.
When enabled:

- A passing condition lets the rollout promote.
- A failing condition participates in auto-rollback when `rollback.auto: true`.
- The rollout history note records `analysis passed: ...` or
  `analysis failed: ...`, which the UI timeline surfaces.

If `rollback.auto` is false and analysis fails, verification fails loudly without
rolling the target back.

## Provider Boundary

`internal/analysis.MetricsProvider` is the provider seam:

```go
type MetricsProvider interface {
    Query(ctx context.Context, query string) (float64, error)
}
```

Prometheus is the first supported provider. Other observability providers should
implement that interface rather than coupling the engine to a vendor SDK.

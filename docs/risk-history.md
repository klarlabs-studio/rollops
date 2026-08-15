# Historical Risk

Historical risk is an optional Phase 2 signal for the approval gate. It keeps
Git as desired state and uses only runtime rollout history from the Store:
recent `rolled-back` records for the same target increase the computed risk
score.

## Config

History lives under `spec.risk.history`:

```yaml
risk:
  threshold: 0.7
  sensitive: 'changeType == "schema" || recentFailures >= 2'
  history:
    lookback: 10
    weight: 0.15
    maxFailures: 3
```

Fields:

- `lookback`: number of recent target history records to inspect. Zero disables
  the signal.
- `weight`: additional normalized score weight assigned to rollback history.
  Defaults to `0.15` when history is configured without a weight.
- `maxFailures`: rollback count treated as maximum historical risk. Defaults to
  `3` when history is configured without a value.

The default risk weights keep history inert unless `risk.history` is configured,
so existing thresholds keep their behavior.

## CEL Variables

`risk.sensitive` can reference the standard decision variables plus:

- `recentFailures`: count of `rolled-back` records inside the lookback.
- `historyRisk`: normalized historical risk from `0` to `1`.

Malformed history bounds and CEL expressions fail during config load.

## Agent surface

When history is configured, `rollouts.plan` (and CLI/HTTP/gRPC plan) returns
`recent_failures` and a citeable `reason` alongside `needs_approval`. See
[agent-walkthrough](agent-walkthrough.md).

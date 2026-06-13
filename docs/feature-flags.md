# Feature-Flag Coupling

Rollops can drive a feature flag in lockstep with a rollout: as a canary shifts
traffic 25 → 50 → 100%, the flag's rollout percentage follows. The flag
provider is a **plugin** — the same gRPC subprocess architecture as target
plugins — so Rollops stays vendor-neutral and the lean core carries no
LaunchDarkly/Flagsmith/Unleash SDK.

A ready-to-use provider exists for Flagsmith:
[`klarlabs-studio/rollops-plugin-flagsmith`](https://github.com/klarlabs-studio/rollops-plugin-flagsmith).
Install it (`go install github.com/klarlabs-studio/rollops-plugin-flagsmith/cmd/rollops-plugin-flagsmith@latest`
or download a release binary), pin its sha256, and point `featureFlags.plugin`
at it. To target another flag service, author your own provider as below.

## Authoring a flag plugin

Implement `plugin.FlagProvider` and call `ServeFlagProvider`:

```go
package main

import "go.klarlabs.de/rollops/pkg/plugin"

type provider struct{ /* your client */ }

func (p provider) ApplyFlag(ctx context.Context, c plugin.FlagChange) error {
	// push c.Flag → c.Percentage (or c.Disabled) in c.Environment to your
	// flag service (Flagsmith, LaunchDarkly, Unleash, your own…)
	return nil
}

func main() {
	safety := plugin.Safety{NetworkHosts: []string{"flags.example.com:443"}, RiskClass: plugin.RiskActive}
	if err := plugin.ServeFlagProvider("acme/flagsmith", "1.0.0", provider{}, safety); err != nil {
		panic(err)
	}
}
```

The plugin declares the `featureflag` capability with one tool, `apply_flag`.

## Wiring it to a rollout

Add a `featureFlags` block to the spec, pointing at the sha256-pinned plugin
binary:

```yaml
spec:
  strategy:
    type: canary
    steps:
      - weight: 25
      - weight: 50
  featureFlags:
    plugin: /usr/local/lib/rollops/plugins/flagsmith
    sha256: 4f5a…              # shasum -a 256 <binary>
    flag: checkout-redesign
    environment: prod
    when: both                # step | promote | both (default both)
```

- `when: step` — drive the flag to each step's traffic weight as the canary
  bakes (25, 50, 100…).
- `when: promote` — flip the flag to 100% only once the rollout is promoted.
- `when: both` — do both (default): track the steps, then settle at 100% on
  promotion.

Delivery is best-effort: a flag-provider failure is recorded in the audit
trail but never aborts or rolls back the deploy — the rollout's own health
gates remain the source of truth. The plugin binary is sha256-verified and its
manifest validated against the plugin safety policy before it runs, exactly
like a target plugin (see `docs/target-plugins.md`).

## Provider conformance

Every flag-provider plugin should pass the shared conformance suite,
`pkg/flagconformance`. It is the flag-provider analogue of the target
`pkg/conformance` suite: it drives a provider through the contract Rollops
relies on so behavior is consistent across backends (Flagsmith, Unleash,
PostHog, GrowthBook, OpenFeature/flagd, …). A provider is correct only if it:

- accepts the full **0–100 canary range**, including the 0 and 100 boundaries;
- is **idempotent** — re-applying the identical change (a retry or re-sync) is
  not an error;
- can drive the **disabled** state (used on rollback);
- **honors context cancellation** — it threads the caller's context into its
  backend calls rather than dropping it.

Authors wire it from their `_test.go`, pointing the factory at a fake backend:

```go
func TestConformance(t *testing.T) {
	flagconformance.Run(t, func() (plugin.FlagProvider, error) {
		srv := newFakeBackend(t) // httptest server accepting the provider's writes
		return Provider{BaseURL: srv.URL, Token: "x", HTTP: srv.Client()}, nil
	}, plugin.FlagChange{Flag: "checkout", Environment: "production"})
}
```

The individual `Check*` functions return errors, so they can also run outside
the test harness.

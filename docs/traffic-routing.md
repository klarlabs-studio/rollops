# Traffic Routing

A weighted canary is only real if the weight shifts live traffic. Rollops drives
traffic at each canary step through a **traffic-router plugin** — a gRPC plugin
declaring the `trafficrouter` capability — so a step weight of 25 sends 25% of
production traffic to the canary backend and 75% to the stable backend, the way
Argo Rollouts does with its traffic-router integrations.

This is independent of, and composable with, the feature-flag coupling
(`featureFlags`): traffic routing shifts *network* traffic at the mesh/ingress
layer; feature flags shift *application* exposure. Use either or both.

## How it works

Add a `trafficRouting` block to the rollout spec. As the canary advances through
its strategy steps, Rollops calls the plugin's `set_weight` tool with the step
weight, the route object, and the stable/canary backends:

```yaml
spec:
  strategy:
    type: canary
    steps:
      - weight: 20
      - weight: 50
      - weight: 100
  trafficRouting:
    plugin: /usr/local/lib/rollops/plugins/gatewayapi
    sha256: 4f5a…              # shasum -a 256 <binary>
    route: app-route           # the router object (e.g. Gateway API HTTPRoute)
    namespace: prod
    stableService: app-stable  # backend receiving (100 - weight)%
    canaryService: app-canary  # backend receiving weight%
```

Best-effort: a routing failure is recorded in the audit trail but never aborts
or rolls back the deploy — the rollout's health gate remains the source of
truth. The plugin binary is sha256-verified and its manifest validated against
the plugin safety policy before it runs, exactly like a target or feature-flag
plugin.

## Topology

The plugin needs a stable backend and a canary backend behind one route, and a
router that splits traffic between them by weight (Gateway API `HTTPRoute`
`backendRefs`, an Istio `VirtualService`, NGINX canary annotations, …). You run
both workloads; Rollops drives the split. (Managing the canary workload's
lifecycle is the deploy target's job; the router only shifts traffic.)

## Authoring a router plugin

Implement `pkg/plugin.TrafficRouter` and serve it:

```go
type Router struct{ /* … */ }

func (r Router) SetWeight(ctx context.Context, c plugin.TrafficChange) error {
	// Set c.CanaryService to c.Weight% and c.StableService to (100-c.Weight)%
	// on route c.Route in c.Namespace.
	return nil
}

func main() {
	plugin.ServeTrafficRouter("klarlabs/gatewayapi", version, Router{}, plugin.Safety{
		RiskClass: plugin.RiskActive,
	})
}
```

The first reference implementation is
[rollops-plugin-gatewayapi](https://github.com/klarlabs-studio/rollops-plugin-gatewayapi),
which patches a Gateway API `HTTPRoute`'s `backendRefs` weights. Install it from
the marketplace:

```sh
rollops plugin install gatewayapi
```

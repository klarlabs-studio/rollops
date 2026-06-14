package trafficrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// gatewayRouter is the built-in Gateway API router: it shifts canary weight by
// patching an HTTPRoute's backendRefs — the canary service to weight%, the
// stable service to (100-weight)% — via kubectl. Built-in so progressive
// delivery works without a plugin. Gateway API is the vendor-neutral standard;
// implementations (Istio, Contour, NGINX Gateway Fabric, …) honour the weights.
type gatewayRouter struct {
	kubeconfig string
	context    string
	// run is the kubectl exec seam; tests inject a fake.
	run func(ctx context.Context, stdin []byte, args ...string) (string, error)
}

func newGatewayRouter(kubeconfig, kubeContext string) *gatewayRouter {
	r := &gatewayRouter{kubeconfig: kubeconfig, context: kubeContext}
	r.run = r.kubectl
	return r
}

func (r *gatewayRouter) baseArgs() []string {
	var args []string
	if r.kubeconfig != "" {
		args = append(args, "--kubeconfig", r.kubeconfig)
	}
	if r.context != "" {
		args = append(args, "--context", r.context)
	}
	return args
}

func (r *gatewayRouter) kubectl(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", append(r.baseArgs(), args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// httpRoute is the minimal shape we read to locate weighted backendRefs.
type httpRoute struct {
	Spec struct {
		Rules []struct {
			BackendRefs []struct {
				Name string `json:"name"`
			} `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
}

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value int    `json:"value"`
}

// SetWeight reads the HTTPRoute, finds the stable and canary backendRefs by
// service name across all rules, and JSON-patches their weights.
func (r *gatewayRouter) SetWeight(ctx context.Context, c Change) error {
	raw, err := r.run(ctx, nil, "get", "httproute", c.Route, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("trafficrouting: gateway: get httproute: %w", err)
	}
	var hr httpRoute
	if err := json.Unmarshal([]byte(raw), &hr); err != nil {
		return fmt.Errorf("trafficrouting: gateway: parse httproute: %w", err)
	}
	var ops []patchOp
	for ri, rule := range hr.Spec.Rules {
		for bi, ref := range rule.BackendRefs {
			var weight int
			switch ref.Name {
			case c.CanaryService:
				weight = c.Weight
			case c.StableService:
				weight = 100 - c.Weight
			default:
				continue
			}
			// "add" creates or replaces the weight member.
			ops = append(ops, patchOp{Op: "add", Path: fmt.Sprintf("/spec/rules/%d/backendRefs/%d/weight", ri, bi), Value: weight})
		}
	}
	if len(ops) == 0 {
		return fmt.Errorf("trafficrouting: gateway: httproute %s/%s has no backendRefs named %q or %q", c.Namespace, c.Route, c.StableService, c.CanaryService)
	}
	body, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	if _, err := r.run(ctx, nil, "patch", "httproute", c.Route, "-n", c.Namespace, "--type=json", "-p", string(body)); err != nil {
		return fmt.Errorf("trafficrouting: gateway: patch weights: %w", err)
	}
	return nil
}

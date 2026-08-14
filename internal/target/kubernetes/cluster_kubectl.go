package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// kubectlCluster drives a cluster through the external kubectl binary. No
// client-go is compiled into the Rollops core.
type kubectlCluster struct {
	kubeconfig string // explicit kubeconfig file (multi-cluster: per-target credentials)
	context    string
	namespace  string
	resource   string // e.g. deployment/api
	prune      bool   // garbage-collect resources removed from desired
	pruneVal   string // label value selecting this target's resources
	healthCond string // explicit status.conditions type to gate on (CRDs)
}

func newKubectl(s spec, ref string, conf security.Confinement) (Cluster, error) {
	ns := s.str("namespace")
	if ns == "" {
		ns = "default"
	}
	// Namespace confinement: reject an out-of-scope namespace before any kubectl
	// call so a tenant repo cannot act in another tenant's namespace. Surfaces as
	// a target build error → per-target / rollout failure.
	if err := conf.CheckNamespace(ns); err != nil {
		return nil, fmt.Errorf("kubernetes: target %q: %w", ref, err)
	}

	kubeconfig := s.str("kubeconfig")
	kctx := s.str("context")
	// Cluster confinement: ignore repo-supplied kubeconfig/context so a tenant
	// repo cannot select another cluster or credential file — the daemon uses its
	// own ambient/in-cluster credentials only. Confinement wins over repo values.
	if conf.ClusterConfined() {
		if kubeconfig != "" || kctx != "" {
			fmt.Fprintf(os.Stderr, "rollops: target %q: ROLLOPS_CONFINE_TARGET_CLUSTER set — ignoring repo-supplied kubeconfig/context\n", ref)
		}
		kubeconfig = ""
		kctx = ""
	}

	return &kubectlCluster{
		kubeconfig: kubeconfig,
		context:    kctx,
		namespace:  ns,
		resource:   s.str("resource"),
		prune:      s.boolVal("prune"),
		pruneVal:   labelValue(ref),
		healthCond: s.str("healthCondition"),
	}, nil
}

func (k *kubectlCluster) baseArgs() []string {
	args := []string{}
	// kubeconfig + context together let one daemon drive many clusters, each with
	// its own credentials file, without a central cluster registry.
	if k.kubeconfig != "" {
		args = append(args, "--kubeconfig", k.kubeconfig)
	}
	if k.context != "" {
		args = append(args, "--context", k.context)
	}
	if k.namespace != "" {
		args = append(args, "-n", k.namespace)
	}
	return args
}

func (k *kubectlCluster) run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", append(k.baseArgs(), args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (k *kubectlCluster) Apply(ctx context.Context, manifest []byte, checksum string) error {
	args := []string{"apply", "-f", "-"}
	if k.prune {
		labeled, err := labelManifest(manifest, PruneLabel, k.pruneVal)
		if err != nil {
			return fmt.Errorf("kubernetes: label for prune: %w", err)
		}
		manifest = labeled
		// Prune resources carrying our label that are no longer in the apply set.
		args = append(args, "--prune", "--selector", fmt.Sprintf("%s=%s", PruneLabel, k.pruneVal))
	}
	if _, err := k.run(ctx, manifest, args...); err != nil {
		return err
	}
	_, err := k.run(ctx, nil, "annotate", "--overwrite", k.resource,
		fmt.Sprintf("%s=%s", ChecksumAnnotation, checksum))
	return err
}

func (k *kubectlCluster) LiveChecksum(ctx context.Context) (string, error) {
	jsonpath := fmt.Sprintf(`jsonpath={.metadata.annotations.%s}`, strings.ReplaceAll(ChecksumAnnotation, ".", `\.`))
	out, err := k.run(ctx, nil, "get", k.resource, "-o", jsonpath)
	if err != nil {
		// Absent resource is not an error for drift purposes — report empty.
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

func (k *kubectlCluster) LiveYAML(ctx context.Context) ([]byte, error) {
	out, err := k.run(ctx, nil, "get", k.resource, "-o", "yaml")
	if err != nil {
		return nil, nil // absent → empty; Diff falls through
	}
	return []byte(out), nil
}

// rolloutKinds are the workload kinds `kubectl rollout status` understands.
var rolloutKinds = map[string]bool{
	"deployment": true, "deploy": true, "deployments": true,
	"statefulset": true, "sts": true, "statefulsets": true,
	"daemonset": true, "ds": true, "daemonsets": true,
}

func resourceKind(resource string) string {
	kind := resource
	if i := strings.IndexByte(resource, '/'); i >= 0 {
		kind = resource[:i]
	}
	return strings.ToLower(strings.TrimSpace(kind))
}

// Healthy reports whether the managed resource is ready. Standard workload kinds
// use `kubectl rollout status`; any other kind (a CRD) is assessed from its
// status.conditions, the way Argo CD's health checks gate on a resource's Ready/
// Available/Succeeded condition. An explicit healthCondition pins the type.
func (k *kubectlCluster) Healthy(ctx context.Context) (bool, string, error) {
	if k.healthCond == "" && rolloutKinds[resourceKind(k.resource)] {
		out, err := k.run(ctx, nil, "rollout", "status", k.resource, "--timeout=30s")
		if err != nil {
			return false, strings.TrimSpace(out), nil
		}
		return true, "", nil
	}
	return k.conditionHealthy(ctx)
}

type statusCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// conditionHealthy assesses health from the resource's status.conditions. It
// looks for the configured condition type (or, by default, the first of Ready /
// Available / Succeeded present): True is healthy, False is unhealthy with the
// reason/message, any other value is treated as still progressing. A resource
// with no conditions is considered healthy — apply already succeeded and there
// is nothing to gate on (Argo CD's default for resources without a health check).
func (k *kubectlCluster) conditionHealthy(ctx context.Context) (bool, string, error) {
	out, err := k.run(ctx, nil, "get", k.resource, "-o", "jsonpath={.status.conditions}")
	if err != nil {
		return false, strings.TrimSpace(out), nil
	}
	return evalConditions(out, k.healthCond)
}

// evalConditions is the pure assessment of a status.conditions JSON array
// (kubectl jsonpath output) against the desired condition type.
func evalConditions(raw, healthCond string) (bool, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true, "", nil // no conditions to assess
	}
	var conds []statusCondition
	if err := json.Unmarshal([]byte(raw), &conds); err != nil {
		return false, fmt.Sprintf("parse status.conditions: %v", err), nil
	}
	wanted := []string{healthCond}
	if healthCond == "" {
		wanted = []string{"Ready", "Available", "Succeeded"}
	}
	for _, want := range wanted {
		for _, c := range conds {
			if !strings.EqualFold(c.Type, want) {
				continue
			}
			switch {
			case strings.EqualFold(c.Status, "True"):
				return true, "", nil
			case strings.EqualFold(c.Status, "False"):
				return false, conditionReason(c), nil
			default:
				return false, fmt.Sprintf("%s is %s (progressing)", c.Type, c.Status), nil
			}
		}
	}
	if healthCond != "" {
		return false, fmt.Sprintf("condition %q not found", healthCond), nil
	}
	return false, "no Ready/Available/Succeeded condition present", nil
}

func conditionReason(c statusCondition) string {
	switch {
	case c.Reason != "" && c.Message != "":
		return c.Reason + ": " + c.Message
	case c.Message != "":
		return c.Message
	case c.Reason != "":
		return c.Reason
	default:
		return c.Type + " is False"
	}
}

// Diff runs `kubectl diff` of the manifest against live state. kubectl exits
// non-zero (1) when differences exist, with the diff on stdout — that is not an
// error here, it is the result.
func (k *kubectlCluster) Diff(ctx context.Context, manifest []byte) (string, error) {
	out, _ := k.run(ctx, manifest, "diff", "-f", "-")
	// Empty diff = in sync; return "" so the caller/UI owns the "no changes"
	// copy rather than treating a human message as diff content.
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	return out, nil
}

// Resources lists the managed workload and its child pods as an ownership tree
// (Deployment → Pods), each with a ready summary.
func (k *kubectlCluster) Resources(ctx context.Context) ([]pt.Resource, error) {
	out, err := k.run(ctx, nil, "get", k.resource,
		"-o", "jsonpath={.kind}|{.metadata.name}|{.metadata.namespace}|{.status.readyReplicas}/{.status.replicas}|{.spec.selector.matchLabels}")
	if err != nil {
		return nil, fmt.Errorf("kubernetes: list resources: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 5)
	if len(parts) < 4 || parts[1] == "" {
		return nil, nil
	}
	ns := parts[2]
	if ns == "" {
		ns = k.namespace
	}
	root := pt.Resource{Kind: parts[0], Name: parts[1], Namespace: ns, Status: "ready " + parts[3]}
	tree := []pt.Resource{root}

	// Child pods, selected by the workload's matchLabels.
	if len(parts) == 5 {
		if sel := selectorFromMatchLabels(parts[4]); sel != "" {
			tree = append(tree, k.pods(ctx, ns, sel, root.Name)...)
		}
	}
	return tree, nil
}

func (k *kubectlCluster) pods(ctx context.Context, ns, selector, parent string) []pt.Resource {
	out, err := k.run(ctx, nil, "get", "pods", "-n", ns, "-l", selector,
		"-o", `jsonpath={range .items[*]}{.metadata.name}|{.status.phase}|{.status.containerStatuses[0].ready}{"\n"}{end}`)
	if err != nil {
		return nil
	}
	var pods []pt.Resource
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		status := f[1]
		if len(f) > 2 && f[2] == "true" {
			status += " · ready"
		}
		pods = append(pods, pt.Resource{Kind: "Pod", Name: f[0], Namespace: ns, Status: status, Parent: parent})
	}
	return pods
}

// selectorFromMatchLabels turns `{"app":"web","tier":"fe"}` into `app=web,tier=fe`.
func selectorFromMatchLabels(j string) string {
	j = strings.TrimSpace(j)
	if j == "" || j == "{}" {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(j), &m); err != nil {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

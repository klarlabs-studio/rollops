package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// kubectlCluster drives a cluster through the external kubectl binary. No
// client-go is compiled into the Rolloffs core.
type kubectlCluster struct {
	context   string
	namespace string
	resource  string // e.g. deployment/api
	prune     bool   // garbage-collect resources removed from desired
	pruneVal  string // label value selecting this target's resources
}

func newKubectl(s spec, ref string) Cluster {
	ns := s.str("namespace")
	if ns == "" {
		ns = "default"
	}
	return &kubectlCluster{
		context:   s.str("context"),
		namespace: ns,
		resource:  s.str("resource"),
		prune:     s.boolVal("prune"),
		pruneVal:  labelValue(ref),
	}
}

func (k *kubectlCluster) baseArgs() []string {
	args := []string{}
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

func (k *kubectlCluster) Healthy(ctx context.Context) (bool, string, error) {
	out, err := k.run(ctx, nil, "rollout", "status", k.resource, "--timeout=30s")
	if err != nil {
		return false, strings.TrimSpace(out), nil
	}
	return true, "", nil
}

// Diff runs `kubectl diff` of the manifest against live state. kubectl exits
// non-zero (1) when differences exist, with the diff on stdout — that is not an
// error here, it is the result.
func (k *kubectlCluster) Diff(ctx context.Context, manifest []byte) (string, error) {
	out, _ := k.run(ctx, manifest, "diff", "-f", "-")
	if strings.TrimSpace(out) == "" {
		return "no changes — live state matches desired", nil
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

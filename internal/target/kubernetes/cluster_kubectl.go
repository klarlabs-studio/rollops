package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// kubectlCluster drives a cluster through the external kubectl binary. No
// client-go is compiled into the Rolloffs core.
type kubectlCluster struct {
	context   string
	namespace string
	resource  string // e.g. deployment/api
}

func newKubectl(s spec) Cluster {
	ns := s.str("namespace")
	if ns == "" {
		ns = "default"
	}
	return &kubectlCluster{
		context:   s.str("context"),
		namespace: ns,
		resource:  s.str("resource"),
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
	if _, err := k.run(ctx, manifest, "apply", "-f", "-"); err != nil {
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

package kubernetes

import (
	"context"
	"testing"
)

// A rollout that really failed is unhealthy; an API server that did not
// answer is an error. Both used to read as unhealthy, so a blip during
// `rollout status` failed the gate and auto-rolled-back a healthy deploy.
func TestHealthy_ProgressDeadlineIsUnhealthy(t *testing.T) {
	kubectlExiting(t, "1", "", `error: deployment "api" exceeded its progress deadline`)
	ok, reason, err := (&kubectlCluster{resource: "deployment/api"}).Healthy(context.Background())
	if err != nil || ok {
		t.Fatalf("Healthy = %v, %q, %v; want unhealthy without error", ok, reason, err)
	}
}

func TestHealthy_UnreachableAPIServerIsAnError(t *testing.T) {
	for _, stderr := range []string{
		`Unable to connect to the server: dial tcp 10.43.0.1:443: connect: connection refused`,
		`The connection to the server 10.43.0.1:443 was refused - did you specify the right host or port?`,
		`Unable to connect to the server: net/http: TLS handshake timeout`,
		`Error from server (ServiceUnavailable): the server is currently unable to handle the request`,
		`Unable to connect to the server: dial tcp 10.43.0.1:443: i/o timeout`,
	} {
		kubectlExiting(t, "1", "", stderr)
		for _, res := range []string{"deployment/api", "certificate/tls"} {
			if _, _, err := (&kubectlCluster{resource: res}).Healthy(context.Background()); err == nil {
				t.Errorf("%s, stderr %q: Healthy returned no error", res, stderr)
			}
		}
	}
}

func TestTargetHealth_PassesTheErrorOn(t *testing.T) {
	kubectlExiting(t, "1", "", `Unable to connect to the server: dial tcp 10.43.0.1:443: connect: connection refused`)
	tgt := &Target{cl: &kubectlCluster{resource: "deployment/api"}}
	if _, err := tgt.Health(context.Background()); err == nil {
		t.Fatal("Target.Health turned an unreachable API server into a verdict")
	}
}

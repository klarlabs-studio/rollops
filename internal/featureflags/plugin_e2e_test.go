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

	prov, err := BuildProvider(&config.FeatureFlags{Plugin: bin, SHA256: hex.EncodeToString(sum[:]), Flag: "checkout", Environment: "prod"})
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
	if _, err := BuildProvider(&config.FeatureFlags{Plugin: bin, SHA256: strings.Repeat("0", 64), Flag: "x"}); err == nil {
		t.Fatal("wrong pin must be rejected")
	}
}

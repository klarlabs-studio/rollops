package plugin

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
	pt "go.klarlabs.de/rollops/pkg/target"
)

var buildTestPlugin = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "rollops-testplugin-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "testplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/testplugin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &exec.ExitError{ProcessState: cmd.ProcessState, Stderr: out}
	}
	return bin, nil
})

func testPluginBinary(t *testing.T) string {
	t.Helper()
	bin, err := buildTestPlugin()
	if err != nil {
		t.Fatalf("build testplugin: %v", err)
	}
	return bin
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestBuild_TargetCapabilityEndToEnd(t *testing.T) {
	bin := testPluginBinary(t)
	ctx := context.Background()

	tgt, err := Build(config.Target{Kind: "plugin", Ref: "x/prod/exotic", Spec: map[string]any{
		"binary": bin,
		"sha256": sha256Of(t, bin),
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer tgt.(interface{ Close() error }).Close()

	res, err := tgt.Apply(ctx, pt.Manifest{Kind: "plugin", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"})
	if err != nil || !res.Changed || !strings.Contains(res.Detail, "sha:abc") {
		t.Fatalf("apply = %+v err=%v", res, err)
	}
	res2, err := tgt.Apply(ctx, pt.Manifest{Kind: "plugin", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"})
	if err != nil || res2.Changed {
		t.Errorf("reapply must be idempotent: %+v err=%v", res2, err)
	}
	fp, err := tgt.Observe(ctx)
	if err != nil || fp.Value != "sha:abc" || fp.Meta["backend"] != "mem" {
		t.Errorf("observe = %+v err=%v", fp, err)
	}
	hs, err := tgt.Health(ctx)
	if err != nil || hs.State != pt.HealthHealthy {
		t.Errorf("health = %+v err=%v", hs, err)
	}
}

func TestBuild_RejectsBadPinAndMissingBinary(t *testing.T) {
	bin := testPluginBinary(t)
	if _, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": bin, "sha256": strings.Repeat("0", 64)}}); err == nil {
		t.Error("wrong pin must be rejected")
	}
	if _, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": bin}}); err == nil {
		t.Error("missing pin must be rejected")
	}
	if _, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{}}); err == nil {
		t.Error("missing binary must be rejected")
	}
}

func TestBuild_ResolvesSymlink(t *testing.T) {
	bin := testPluginBinary(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	tgt, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": link, "sha256": sha256Of(t, bin)}})
	if err != nil {
		t.Fatalf("Build via symlink: %v", err)
	}
	_ = tgt.(interface{ Close() error }).Close()
}

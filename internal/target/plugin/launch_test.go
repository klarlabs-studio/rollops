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

// buildTestPlugin compiles the testdata plugin once per test binary.
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

func TestVerifyBinary(t *testing.T) {
	bin := testPluginBinary(t)
	if err := VerifyBinary(bin, sha256Of(t, bin)); err != nil {
		t.Errorf("matching pin rejected: %v", err)
	}
	if err := VerifyBinary(bin, strings.Repeat("0", 64)); err == nil {
		t.Error("wrong pin must be rejected")
	}
	if err := VerifyBinary(bin, ""); err == nil {
		t.Error("missing pin must be rejected")
	}
	if err := VerifyBinary(filepath.Join(t.TempDir(), "absent"), strings.Repeat("0", 64)); err == nil {
		t.Error("missing binary must be rejected")
	}
}

func TestLaunch_EndToEnd(t *testing.T) {
	bin := testPluginBinary(t)
	ctx := context.Background()

	proc, err := Launch(ctx, bin)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer proc.Close()

	tgt := proc.Target
	res, err := tgt.Apply(ctx, pt.Manifest{Kind: "plugin", Spec: []byte(`{"x":1}`), Checksum: "sha:abc"})
	if err != nil {
		t.Fatalf("Apply over subprocess gRPC: %v", err)
	}
	if !res.Changed || !strings.Contains(res.Detail, "sha:abc") {
		t.Errorf("apply = %+v", res)
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

	if err := proc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := tgt.Health(ctx); err == nil {
		t.Error("RPC after Close must fail (process gone)")
	}
}

func TestLaunch_RejectsNonPlugin(t *testing.T) {
	// /bin/echo prints no handshake and exits → launch must fail, not hang.
	if _, err := Launch(context.Background(), "/bin/echo"); err == nil {
		t.Fatal("non-plugin binary must be rejected")
	}
}

func TestBuild_FromConfigSpec(t *testing.T) {
	bin := testPluginBinary(t)
	ctx := context.Background()

	tgt, err := Build(config.Target{Kind: "plugin", Ref: "x/prod/exotic", Spec: map[string]any{
		"binary": bin,
		"sha256": sha256Of(t, bin),
		"port":   8080,
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res, err := tgt.Apply(ctx, pt.Manifest{Kind: "plugin", Spec: []byte(`{"port":8080}`), Checksum: "sha:b1"})
	if err != nil || !res.Changed {
		t.Fatalf("apply via built plugin target: %+v err=%v", res, err)
	}
	closer, ok := tgt.(interface{ Close() error })
	if !ok {
		t.Fatal("plugin-built target must be closable")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_RejectsBadPin(t *testing.T) {
	bin := testPluginBinary(t)
	_, err := Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{
		"binary": bin,
		"sha256": strings.Repeat("0", 64),
	}})
	if err == nil {
		t.Fatal("wrong sha256 pin must refuse to launch")
	}
	_, err = Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{"binary": bin}})
	if err == nil {
		t.Fatal("missing sha256 pin must refuse to launch")
	}
	_, err = Build(config.Target{Kind: "plugin", Ref: "x", Spec: map[string]any{}})
	if err == nil {
		t.Fatal("missing binary must be rejected")
	}
}

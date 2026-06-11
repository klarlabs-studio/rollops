package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBin(t *testing.T, content string) (path, sum string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "myplugin")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(content))
	return p, hex.EncodeToString(h[:])
}

func TestPluginInstall_LocalCopiesAndPrintsPin(t *testing.T) {
	src, sum := writeBin(t, "#!/bin/sh\necho hi\n")
	dir := t.TempDir()
	var buf bytes.Buffer
	app := &App{Out: &buf}
	err := app.Run(context.Background(), []string{"plugin", "install", src, "--dir", dir, "--name", "flagsmith"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	dest := filepath.Join(dir, "flagsmith")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sha256 "+sum) || !strings.Contains(out, dest) {
		t.Errorf("output missing pin/path: %q", out)
	}
}

func TestPluginInstall_CosignVerifyGate(t *testing.T) {
	src, _ := writeBin(t, "binary")
	dir := t.TempDir()

	// Failing cosign blocks the install.
	var buf bytes.Buffer
	app := &App{Out: &buf, PluginFetcher: func(_ context.Context, s string) (string, func(), error) {
		return s, func() {}, nil
	}}
	app.CosignRun = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "bad signature", os.ErrInvalid
	}
	err := app.Run(context.Background(), []string{"plugin", "install", src, "--dir", dir, "--cosign-key", "/k.pub"})
	if err == nil {
		t.Fatal("failed cosign verification must block install")
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.Base(src))); statErr == nil {
		t.Error("binary must not be installed when verification fails")
	}
}

func TestPluginInstall_RequiresOneSource(t *testing.T) {
	var buf bytes.Buffer
	app := &App{Out: &buf}
	if err := app.Run(context.Background(), []string{"plugin", "install"}); err == nil {
		t.Fatal("missing source must error")
	}
}

func TestPlugin_UnknownSubcommand(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}}
	if err := app.Run(context.Background(), []string{"plugin", "frobnicate"}); err == nil {
		t.Fatal("unknown plugin subcommand must error")
	}
}

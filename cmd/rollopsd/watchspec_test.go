package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadWatchSpecs_TokenFileAndDeployKey(t *testing.T) {
	dir := t.TempDir()
	tokenFile := writeFile(t, dir, "token", "  ghp_secret\n")
	watch := writeFile(t, dir, "watch.json", `[
	  {"name":"a","url":"https://x/a","tokenFile":"`+tokenFile+`"},
	  {"name":"b","url":"git@x:b","deployKeyPath":"/keys/b"}
	]`)
	specs, err := loadWatchSpecs(watch)
	if err != nil {
		t.Fatalf("loadWatchSpecs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("want 2 specs, got %d", len(specs))
	}
	if specs[0].Auth.Token != "ghp_secret" { // trimmed
		t.Errorf("tokenFile token = %q", specs[0].Auth.Token)
	}
	if specs[1].Auth.DeployKeyPath != "/keys/b" {
		t.Errorf("deployKeyPath = %q", specs[1].Auth.DeployKeyPath)
	}
}

func TestLoadWatchSpecs_GitHubApp(t *testing.T) {
	dir := t.TempDir()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	keyFile := writeFile(t, dir, "app.pem", string(keyPEM))
	watch := writeFile(t, dir, "watch.json", `[
	  {"name":"a","url":"https://x/a","githubAppId":"123","githubInstallationId":"456","githubAppPrivateKeyFile":"`+keyFile+`"}
	]`)
	specs, err := loadWatchSpecs(watch)
	if err != nil {
		t.Fatalf("loadWatchSpecs: %v", err)
	}
	if specs[0].Auth.TokenSource == nil {
		t.Fatal("github app config must set a TokenSource provider")
	}
	if specs[0].Auth.Token != "" {
		t.Errorf("github app must not set a static token, got %q", specs[0].Auth.Token)
	}
}

func TestLoadWatchSpecs_GitHubAppRequiresAllFields(t *testing.T) {
	dir := t.TempDir()
	watch := writeFile(t, dir, "watch.json", `[
	  {"name":"a","url":"https://x/a","githubAppId":"123"}
	]`)
	if _, err := loadWatchSpecs(watch); err == nil {
		t.Fatal("partial github app config must error")
	}
}

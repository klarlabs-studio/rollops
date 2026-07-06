package servertls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// genKeypair returns PEM-encoded self-signed cert+key for the given common name.
// Each call produces a fresh key and a unique serial so two keypairs are
// distinguishable by serial number (used by the hot-reload test).
func genKeypair(t *testing.T, cn string) (certPEM, keyPEM []byte, serial *big.Int) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		DNSNames:              []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, serial
}

// writeKeypair writes cert+key into dir and returns their paths.
func writeKeypair(t *testing.T, dir string, certPEM, keyPEM []byte) (certPath, keyPath string) {
	t.Helper()
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestFromEnv(t *testing.T) {
	cases := []struct {
		name             string
		cert, key, cliCA string
		wantNil          bool
		wantErr          bool
		wantHasClientCA  bool
	}{
		{name: "plaintext (nothing set)", wantNil: true},
		{name: "cert without key", cert: "c.pem", wantErr: true},
		{name: "key without cert", key: "k.pem", wantErr: true},
		{name: "client CA without keypair", cliCA: "ca.pem", wantErr: true},
		{name: "server TLS only", cert: "c.pem", key: "k.pem"},
		{name: "mTLS", cert: "c.pem", key: "k.pem", cliCA: "ca.pem", wantHasClientCA: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ROLLOPS_TLS_CERT", c.cert)
			t.Setenv("ROLLOPS_TLS_KEY", c.key)
			t.Setenv("ROLLOPS_TLS_CLIENT_CA", c.cliCA)

			cfg, err := FromEnv()
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got cfg=%v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantNil {
				if cfg != nil {
					t.Fatalf("want nil cfg (plaintext), got %+v", cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatal("want non-nil cfg")
			}
			if got := cfg.HasClientCA(); got != c.wantHasClientCA {
				t.Errorf("HasClientCA() = %v, want %v", got, c.wantHasClientCA)
			}
		})
	}
}

func TestServerTLS_LoadsKeypairAndPinsTLS13(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, _ := genKeypair(t, "rollopsd")
	certPath, keyPath := writeKeypair(t, dir, certPEM, keyPEM)

	cfg := &Config{certFile: certPath, keyFile: keyPath}
	tc, err := cfg.ServerTLS(false)
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	if tc.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS1.3 (%x)", tc.MinVersion, tls.VersionTLS13)
	}
	if tc.GetCertificate == nil {
		t.Fatal("GetCertificate callback must be set")
	}
	crt, err := tc.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if crt == nil || len(crt.Certificate) == 0 {
		t.Fatal("GetCertificate returned no certificate")
	}
	// No client CA → no client-auth machinery.
	if tc.ClientAuth != tls.NoClientCert || tc.ClientCAs != nil {
		t.Errorf("without client CA: ClientAuth=%v ClientCAs=%v, want none", tc.ClientAuth, tc.ClientCAs)
	}
}

func TestServerTLS_MissingKeypairErrors(t *testing.T) {
	cfg := &Config{certFile: "/nonexistent/tls.crt", keyFile: "/nonexistent/tls.key"}
	if _, err := cfg.ServerTLS(false); err == nil {
		t.Fatal("want error for missing keypair")
	}
}

func TestServerTLS_HotReloadOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	cert1, key1, serial1 := genKeypair(t, "rollopsd")
	certPath, keyPath := writeKeypair(t, dir, cert1, key1)

	cfg := &Config{certFile: certPath, keyFile: keyPath}
	tc, err := cfg.ServerTLS(false)
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}

	got1 := leafSerial(t, tc)
	if got1.Cmp(serial1) != 0 {
		t.Fatalf("initial serial = %s, want %s", got1, serial1)
	}

	// Rotate: write a distinct keypair and bump mtime forward so the change is
	// detectable regardless of filesystem mtime granularity.
	cert2, key2, serial2 := genKeypair(t, "rollopsd")
	if err := os.WriteFile(certPath, cert2, 0o600); err != nil {
		t.Fatalf("rewrite cert: %v", err)
	}
	if err := os.WriteFile(keyPath, key2, 0o600); err != nil {
		t.Fatalf("rewrite key: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(certPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got2 := leafSerial(t, tc)
	if got2.Cmp(serial2) != 0 {
		t.Fatalf("after rotation serial = %s, want %s (hot-reload did not pick up new cert)", got2, serial2)
	}
}

func TestServerTLS_ClientCASelection(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, _ := genKeypair(t, "rollopsd")
	certPath, keyPath := writeKeypair(t, dir, certPEM, keyPEM)
	caPEM, _, _ := genKeypair(t, "client-ca")
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	cfg := &Config{certFile: certPath, keyFile: keyPath, clientCAFile: caPath}
	if !cfg.HasClientCA() {
		t.Fatal("HasClientCA() = false, want true")
	}

	// requireClientCert=true → RequireAndVerifyClientCert (machine control plane).
	tcReq, err := cfg.ServerTLS(true)
	if err != nil {
		t.Fatalf("ServerTLS(true): %v", err)
	}
	if tcReq.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", tcReq.ClientAuth)
	}
	if tcReq.ClientCAs == nil {
		t.Error("ClientCAs must be populated when a client CA is configured")
	}

	// requireClientCert=false → VerifyClientCertIfGiven (shared HTTP/UI listener).
	tcOpt, err := cfg.ServerTLS(false)
	if err != nil {
		t.Fatalf("ServerTLS(false): %v", err)
	}
	if tcOpt.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("ClientAuth = %v, want VerifyClientCertIfGiven", tcOpt.ClientAuth)
	}
}

func TestServerTLS_BadClientCAErrors(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, _ := genKeypair(t, "rollopsd")
	certPath, keyPath := writeKeypair(t, dir, certPEM, keyPEM)
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte("not a pem"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	cfg := &Config{certFile: certPath, keyFile: keyPath, clientCAFile: caPath}
	if _, err := cfg.ServerTLS(true); err == nil {
		t.Fatal("want error for a client CA bundle with no valid certs")
	}
}

func TestHasClientCA_NilSafe(t *testing.T) {
	var c *Config
	if c.HasClientCA() {
		t.Error("nil Config HasClientCA() = true, want false")
	}
}

// leafSerial extracts the serial number of the leaf certificate the config's
// GetCertificate callback currently serves.
func leafSerial(t *testing.T, tc *tls.Config) *big.Int {
	t.Helper()
	crt, err := tc.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(crt.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return leaf.SerialNumber
}

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/servertls"
)

// caLeaf is a CA plus a leaf keypair signed by it, used to prove client-cert
// verification end to end.
type caLeaf struct {
	caPEM  []byte
	key    *ecdsa.PrivateKey
	caCert *x509.Certificate
}

func newCA(t *testing.T, cn string) *caLeaf {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	return &caLeaf{
		caPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:    key,
		caCert: caCert,
	}
}

// serverLeaf issues a server certificate (with 127.0.0.1 + localhost SANs)
// signed by the CA.
func (c *caLeaf) serverLeaf(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "rollopsd"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// clientCert issues a client certificate signed by the CA.
func (c *caLeaf) clientCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "cli-agent"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestEnsureTransportSecure_RefusesPlaintextNonLoopback proves a non-loopback
// bind without TLS is refused (no override).
func TestEnsureTransportSecure_RefusesPlaintextNonLoopback(t *testing.T) {
	if err := ensureTransportSecure("0.0.0.0:8443", "HTTP", nil); err == nil {
		t.Fatal("non-loopback plaintext bind must be refused")
	}
}

// TestPerSurfaceMTLS spins up a real TLS server with the same per-surface wiring
// as main.go: server TLS config from servertls (VerifyClientCertIfGiven) and the
// REST API wrapped with requireClientCertIfConfigured while the UI is not. It
// then proves: (a) API without a client cert → 401, (b) UI without a client cert
// → 200, (c) API with a valid client cert → 200.
func TestPerSurfaceMTLS(t *testing.T) {
	dir := t.TempDir()
	ca := newCA(t, "rollops-ca")
	srvCert, srvKey := ca.serverLeaf(t)
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	caPath := filepath.Join(dir, "ca.crt")
	mustWrite(t, certPath, srvCert)
	mustWrite(t, keyPath, srvKey)
	mustWrite(t, caPath, ca.caPEM)

	// Configure via env then FromEnv so we exercise the real constructor.
	t.Setenv("ROLLOPS_TLS_CERT", certPath)
	t.Setenv("ROLLOPS_TLS_KEY", keyPath)
	t.Setenv("ROLLOPS_TLS_CLIENT_CA", caPath)
	tlsCfg, err := servertls.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if !tlsCfg.HasClientCA() {
		t.Fatal("expected mTLS to be configured")
	}

	// Mirror main.go's mux: /ui is unwrapped, / (REST API) is mTLS-gated.
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api-ok"))
	})
	uiHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ui-ok"))
	})
	mux := http.NewServeMux()
	mux.Handle("/ui/", uiHandler)
	mux.Handle("/", requireClientCertIfConfigured(apiHandler, tlsCfg))

	serverTLS, err := tlsCfg.ServerTLS(false) // VerifyClientCertIfGiven, shared listener
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	// A real TLS listener (not httptest.StartTLS, which injects its own
	// self-signed cert) so the server serves the servertls GetCertificate cert.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	baseURL := "https://" + ln.Addr().String()

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(ca.caPEM) {
		t.Fatal("append CA")
	}

	// (a) API without a client cert → 401.
	noCertClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}}}
	resp, err := noCertClient.Get(baseURL + "/rollouts")
	if err != nil {
		t.Fatalf("api no-cert request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("API without client cert = %d, want 401", resp.StatusCode)
	}

	// (b) UI without a client cert → 200 (browsers can't present client certs).
	resp, err = noCertClient.Get(baseURL + "/ui/")
	if err != nil {
		t.Fatalf("ui no-cert request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("UI without client cert = %d, want 200", resp.StatusCode)
	}

	// (c) API WITH a valid client cert → 200.
	clientCert := ca.clientCert(t)
	certClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      caPool,
		Certificates: []tls.Certificate{clientCert},
		MinVersion:   tls.VersionTLS13,
	}}}
	resp, err = certClient.Get(baseURL + "/rollouts")
	if err != nil {
		t.Fatalf("api with-cert request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("API with valid client cert = %d, want 200", resp.StatusCode)
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

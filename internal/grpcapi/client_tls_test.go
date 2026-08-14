package grpcapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rollops/internal/grpcapi/rollopsv1"
)

func writeCertKey(t *testing.T, cert tls.Certificate) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestDial_PlaintextWhenTLSUnset(t *testing.T) {
	t.Setenv("ROLLOPS_TLS_CERT", "")
	t.Setenv("ROLLOPS_TLS_KEY", "")
	t.Setenv("ROLLOPS_TLS_CLIENT_CA", "")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := NewGRPCServer(newTestServer(t))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	c, err := Dial(lis.Addr().String(), "t-felix")
	if err != nil {
		t.Fatalf("Dial plaintext: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.rpc.Status(c.ctx(ctx), &rollopsv1.StatusRequest{Id: "unknown"})
	if status.Code(err) == codes.Unavailable {
		t.Fatalf("plaintext Dial must reach the RPC layer: %v", err)
	}
}

func TestDial_UsesTLSWhenConfigured(t *testing.T) {
	ca := newMTLSCA(t)
	serverCert := ca.issue(t, "localhost", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"})
	certPath, keyPath := writeCertKey(t, serverCert)
	t.Setenv("ROLLOPS_TLS_CERT", certPath)
	t.Setenv("ROLLOPS_TLS_KEY", keyPath)
	t.Setenv("ROLLOPS_TLS_CLIENT_CA", "")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverTLS := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
	}
	gs := NewGRPCServer(newTestServer(t), grpc.Creds(credentials.NewTLS(serverTLS)))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	c, err := Dial(lis.Addr().String(), "t-felix")
	if err != nil {
		t.Fatalf("Dial TLS: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.rpc.Status(c.ctx(ctx), &rollopsv1.StatusRequest{Id: "unknown"})
	if status.Code(err) == codes.Unavailable {
		t.Fatalf("TLS Dial must complete the handshake, got transport error: %v", err)
	}
}

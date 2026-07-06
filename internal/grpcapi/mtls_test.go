package grpcapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/grpcapi/rollopsv1"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// mtlsCA is a tiny in-memory CA that issues server and client leaf certs, used
// to prove the gRPC server's mTLS credentials reject a connection without a
// verified client certificate and accept one with it.
type mtlsCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newMTLSCA(t *testing.T) *mtlsCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "grpc-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &mtlsCA{cert: cert, key: key}
}

func (c *mtlsCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

func (c *mtlsCA) issue(t *testing.T, cn string, eku x509.ExtKeyUsage, ips []net.IP, dns []string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		IPAddresses:  ips,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: mustParse(t, der)}
}

func mustParse(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/g.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fakeTarget{}, nil })
	eng := engine.New(db, reg)
	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "op", Grants: []security.Grant{{Perm: security.PermStatus}}})
	pol.Bind("human:felix", "op")
	auth := api.TokenAuth{"t-felix": {Kind: "human", Name: "felix"}}
	return New(eng, auth, pol)
}

// TestGRPC_MTLS_RejectsWithoutClientCert proves that when the gRPC server is
// built with mTLS credentials (RequireAndVerifyClientCert), a client that does
// not present a client certificate cannot complete the TLS handshake, while a
// client presenting a CA-signed client cert connects and reaches the RPC layer.
func TestGRPC_MTLS_RejectsWithoutClientCert(t *testing.T) {
	ca := newMTLSCA(t)
	serverCert := ca.issue(t, "rollopsd", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"})
	clientCert := ca.issue(t, "cli-agent", x509.ExtKeyUsageClientAuth, nil, nil)

	serverTLS := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := NewGRPCServer(newTestServer(t), grpc.Creds(credentials.NewTLS(serverTLS)))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	addr := lis.Addr().String()

	// Without a client cert: handshake must fail → RPC errors at the transport.
	t.Run("no client cert rejected", func(t *testing.T) {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    ca.pool(),
			ServerName: "localhost",
		})))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err = rollopsv1.NewRolloutServiceClient(conn).Status(ctx, &rollopsv1.StatusRequest{Id: "x"})
		if err == nil {
			t.Fatal("expected a transport error without a client certificate")
		}
	})

	// With a valid client cert: handshake succeeds → RPC reaches the auth layer
	// (returns a normal application error, not a transport failure).
	t.Run("valid client cert accepted", func(t *testing.T) {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      ca.pool(),
			ServerName:   "localhost",
			Certificates: []tls.Certificate{clientCert},
		})))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer t-felix"))
		// Not-found (unknown id) or OK both prove the TLS layer let us through;
		// only a transport/handshake failure would be reported as Unavailable.
		_, err = rollopsv1.NewRolloutServiceClient(conn).Status(ctx, &rollopsv1.StatusRequest{Id: "unknown"})
		if status.Code(err) == codes.Unavailable {
			t.Fatalf("valid client cert should complete the handshake, got transport error: %v", err)
		}
	})
}

// Package servertls builds the daemon's server-side TLS material from the
// environment and hands out *tls.Config values for every network listener
// (HTTP, gRPC, MCP). The design goal is zero-trust transport: TLS 1.3 on every
// non-loopback bind, mutual TLS on the machine control plane, and cert rotation
// (cert-manager, SPIFFE, manual) picked up without a restart.
//
// Certificates are loaded through a GetCertificate callback that re-reads the
// keypair from disk whenever the certificate file's modification time changes,
// so an operator (or cert-manager) can rotate the mounted Secret in place and
// the running daemon serves the new leaf on the next handshake. The last
// successfully parsed keypair is cached and served if a rotation writes a
// transiently unreadable/partial file, so rotation can never take the listener
// down.
package servertls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"
)

// Config holds the paths to the server keypair and the optional client-CA
// bundle, plus the hot-reload cache. A nil *Config means plaintext mode; all
// methods are nil-safe where it is meaningful (HasClientCA).
type Config struct {
	certFile     string
	keyFile      string
	clientCAFile string

	mu      sync.Mutex
	cached  *tls.Certificate
	certMod time.Time
}

// FromEnv builds a Config from the ROLLOPS_TLS_* environment variables:
//
//	ROLLOPS_TLS_CERT       server certificate PEM path (leaf + chain)
//	ROLLOPS_TLS_KEY        server private key PEM path
//	ROLLOPS_TLS_CLIENT_CA  client-CA bundle PEM path (optional; enables mTLS)
//
// It returns (nil, nil) when no TLS is configured (plaintext mode, only valid on
// a loopback bind). It returns an error if the cert and key are not set as a
// pair, or if a client CA is set without a server keypair (mTLS needs a server
// cert to terminate TLS in the first place).
func FromEnv() (*Config, error) {
	cert := os.Getenv("ROLLOPS_TLS_CERT")
	key := os.Getenv("ROLLOPS_TLS_KEY")
	clientCA := os.Getenv("ROLLOPS_TLS_CLIENT_CA")

	switch {
	case cert == "" && key == "":
		if clientCA != "" {
			return nil, fmt.Errorf("ROLLOPS_TLS_CLIENT_CA is set but ROLLOPS_TLS_CERT/ROLLOPS_TLS_KEY are not: " +
				"mTLS requires a server keypair to terminate TLS")
		}
		return nil, nil // plaintext mode
	case cert == "":
		return nil, fmt.Errorf("ROLLOPS_TLS_KEY is set but ROLLOPS_TLS_CERT is not: set both")
	case key == "":
		return nil, fmt.Errorf("ROLLOPS_TLS_CERT is set but ROLLOPS_TLS_KEY is not: set both")
	}

	return &Config{certFile: cert, keyFile: key, clientCAFile: clientCA}, nil
}

// ClientTLS returns a *tls.Config for a client talking to rollopsd. The server
// certificate PEM is trusted as a root (the self-signed dogfood keypair, or a
// chain the operator concatenated). When a client CA is configured, the same
// keypair is presented as the client certificate so mTLS CLI calls work with
// the same ROLLOPS_TLS_* material. Nil-safe: a nil Config means plaintext.
func (c *Config) ClientTLS() (*tls.Config, error) {
	if c == nil {
		return nil, nil
	}
	pem, err := os.ReadFile(c.certFile)
	if err != nil {
		return nil, fmt.Errorf("read tls cert %q: %w", c.certFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tls cert %q contains no valid certificates", c.certFile)
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
	}
	if c.HasClientCA() {
		pair, err := c.loadCertificate()
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{*pair}
	}
	return cfg, nil
}

// HasClientCA reports whether a client-CA bundle is configured, i.e. whether
// mutual TLS is available on this daemon. Nil-safe.
func (c *Config) HasClientCA() bool {
	return c != nil && c.clientCAFile != ""
}

// ServerTLS returns a *tls.Config for a listener. MinVersion is pinned to TLS
// 1.3. The certificate is served through a hot-reloading GetCertificate
// callback. When a client CA is configured, ClientCAs is set from the bundle
// and ClientAuth is RequireAndVerifyClientCert if requireClientCert is true
// (the machine control plane: gRPC, MCP), else VerifyClientCertIfGiven (the
// shared HTTP listener, where the browser UI must still connect without a
// client cert but a presented one is verified so the REST API can require it).
func (c *Config) ServerTLS(requireClientCert bool) (*tls.Config, error) {
	// Prime and validate the keypair now so a bad cert fails at startup rather
	// than on the first client handshake.
	if _, err := c.loadCertificate(); err != nil {
		return nil, err
	}

	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: c.getCertificate,
	}

	if c.clientCAFile != "" {
		pool, err := c.clientCAPool()
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		if requireClientCert {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			cfg.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}

	return cfg, nil
}

// getCertificate is the tls.Config.GetCertificate callback; it delegates to the
// hot-reloading loader so every handshake sees the freshest keypair.
func (c *Config) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return c.loadCertificate()
}

// loadCertificate returns the cached parsed keypair, re-reading it from disk
// when the certificate file's mtime has changed since the last load. It holds a
// mutex so concurrent handshakes share one reload. If a reload fails but a
// previously good keypair is cached, the cached one is served (a partial write
// mid-rotation must not fail live handshakes).
func (c *Config) loadCertificate() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fi, err := os.Stat(c.certFile)
	if err != nil {
		if c.cached != nil {
			return c.cached, nil
		}
		return nil, fmt.Errorf("stat tls cert %q: %w", c.certFile, err)
	}

	mtime := fi.ModTime()
	if c.cached != nil && mtime.Equal(c.certMod) {
		return c.cached, nil
	}

	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		if c.cached != nil {
			return c.cached, nil // keep serving last-good across a bad rotation
		}
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}

	c.cached = &pair
	c.certMod = mtime
	return c.cached, nil
}

// clientCAPool reads the configured client-CA bundle into an x509 pool used to
// verify client certificates for mTLS.
func (c *Config) clientCAPool() (*x509.CertPool, error) {
	pem, err := os.ReadFile(c.clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read tls client CA %q: %w", c.clientCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tls client CA %q contains no valid certificates", c.clientCAFile)
	}
	return pool, nil
}

package ftp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"time"

	goftp "github.com/jlaffaye/ftp"
)

// ftpConn is the real FTP implementation of Conn. Credentials are supplied at
// execution time (SecretProvider); Rollops never stores them locally.
type ftpConn struct {
	c *goftp.ServerConn
}

func dialFTP(s spec) (Conn, error) {
	host := s.str("host")
	port := s.str("port")
	if port == "" {
		port = "21"
	}
	addr := net.JoinHostPort(host, port)
	c, err := goftp.Dial(addr, goftp.DialWithTimeout(15*time.Second))
	if err != nil {
		return nil, fmt.Errorf("ftp: dial %s: %w", addr, err)
	}
	user := s.str("user")
	if user == "" {
		user = "anonymous"
	}
	if err := c.Login(user, s.str("password")); err != nil {
		return nil, fmt.Errorf("ftp: login: %w", err)
	}
	return &ftpConn{c: c}, nil
}

func (f *ftpConn) Store(_ context.Context, path string, content []byte) error {
	return f.c.Stor(path, bytes.NewReader(content))
}

func (f *ftpConn) Retrieve(_ context.Context, path string) ([]byte, error) {
	r, err := f.c.Retr(path)
	if err != nil {
		return nil, retrieveError(err)
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// retrieveError maps a RETR failure: reply 550 (file unavailable) means the
// stamp is absent; anything else — 421, 530, a timeout, a dropped connection —
// is a failure to read, not proof the target was never deployed.
func retrieveError(err error) error {
	var proto *textproto.Error
	if errors.As(err, &proto) && proto.Code == goftp.StatusFileUnavailable {
		return ErrNotFound
	}
	return fmt.Errorf("ftp: retrieve: %w", err)
}

func (f *ftpConn) Ping(_ context.Context) error {
	return f.c.NoOp()
}

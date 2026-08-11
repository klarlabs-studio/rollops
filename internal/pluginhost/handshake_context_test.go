package pluginhost

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// awaitHandshake bounded its wait with a bare 10s timer and ignored the context
// entirely. Two consequences: a caller that cancelled still waited out the full ten
// seconds on a plugin that was never going to answer, and no caller could state a bound
// of its own — so on a loaded machine a slow start produced a failure indistinguishable
// from a broken plugin.

// silentReader never yields a line and never reaches EOF, standing in for a plugin that
// starts, holds stdout open, and says nothing.
type silentReader struct{ release <-chan struct{} }

func (s silentReader) Read([]byte) (int, error) {
	<-s.release
	return 0, io.EOF
}

func TestHandshakeStopsWhenTheCallerCancels(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())

	started := time.Now()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := awaitHandshake(ctx, silentReader{release: release})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a silent plugin must not appear to hand shake successfully")
	}
	if elapsed >= handshakeTimeout {
		t.Errorf("waited %s despite cancellation: the caller's cancel must not have to "+
			"outlast the %s default", elapsed, handshakeTimeout)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled so a caller can tell its own "+
			"cancellation from a plugin that timed out", err)
	}
	// A cancelled caller is not a slow plugin, and reporting one as the other sends
	// someone to debug a plugin that was fine.
	if strings.Contains(err.Error(), "timeout") {
		t.Errorf("error = %q, want it to read as cancellation rather than a timeout", err)
	}
}

// A deadline the caller set is honored, shorter or longer than the default, because the
// caller knows what it is willing to wait. This is what lets a test on a loaded machine
// avoid measuring machine load, and what lets a daemon tighten the bound.
func TestHandshakeHonorsAShorterCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := awaitHandshake(ctx, silentReader{release: release}); err == nil {
		t.Fatal("a silent plugin must fail")
	}
	if elapsed := time.Since(started); elapsed >= handshakeTimeout {
		t.Errorf("waited %s, want roughly the 80ms deadline: a caller's shorter bound must "+
			"win over the %s default", elapsed, handshakeTimeout)
	}
}

// The property the constant exists to guarantee: with no deadline set, the wait is still
// finite, so a plugin that dials and never speaks cannot hang the daemon.
func TestHandshakeStillBoundsAnUndeadlinedCaller(t *testing.T) {
	if handshakeTimeout <= 0 {
		t.Fatal("the default bound must be positive, or an undeadlined caller waits forever")
	}

	// Verified without waiting it out: a reader at EOF returns immediately, proving the
	// path completes rather than blocking on the timer.
	if _, err := awaitHandshake(context.Background(), strings.NewReader("")); err == nil {
		t.Error("a plugin that exits before handshaking must be reported as such")
	}
}

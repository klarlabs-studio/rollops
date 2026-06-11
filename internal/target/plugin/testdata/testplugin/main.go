// Command testplugin is the in-memory target used by the launcher's
// end-to-end tests: it stores the last applied manifest and reports it back
// through Observe, exercising the full subprocess + gRPC path.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"go.klarlabs.de/rollops/pkg/plugin"
	pt "go.klarlabs.de/rollops/pkg/target"
)

type memTarget struct {
	mu       sync.Mutex
	checksum string
}

func (m *memTarget) Apply(_ context.Context, man pt.Manifest) (pt.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checksum == man.Checksum {
		return pt.Result{Changed: false, Detail: "already at " + man.Checksum}, nil
	}
	m.checksum = man.Checksum
	return pt.Result{Changed: true, Detail: "applied " + man.Checksum}, nil
}

func (m *memTarget) Observe(context.Context) (pt.Fingerprint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return pt.Fingerprint{Value: m.checksum, Meta: map[string]string{"backend": "mem"}}, nil
}

func (m *memTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy, Reason: "in-memory"}, nil
}

func main() {
	fmt.Fprintln(os.Stderr, "testplugin starting") // log noise the host must skip
	fmt.Println("not a handshake line")
	if err := plugin.Serve(&memTarget{}); err != nil {
		fmt.Fprintln(os.Stderr, "testplugin:", err)
		os.Exit(1)
	}
}

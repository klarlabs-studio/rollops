package plugin

import (
	"context"
	"fmt"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Build constructs a plugin-backed target from a config target. The spec names
// the plugin binary and pins its sha256; everything else in the spec is the
// plugin's own configuration, delivered untouched through Apply's manifest:
//
//	target:
//	  kind: plugin
//	  ref: x/prod/exotic
//	  spec:
//	    binary: /usr/local/lib/rollops/plugins/exotic
//	    sha256: <hex of the binary>
//	    ... plugin-specific keys ...
//
// The binary is verified against the pin before exec, then launched as a
// subprocess for the duration of the engine operation; the engine closes the
// returned target, which tears the process down.
func Build(cfg config.Target) (pt.Target, error) {
	binary, _ := cfg.Spec["binary"].(string)
	if binary == "" {
		return nil, fmt.Errorf("plugin: target %q: spec.binary is required", cfg.Ref)
	}
	pin, _ := cfg.Spec["sha256"].(string)
	if err := VerifyBinary(binary, pin); err != nil {
		return nil, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}
	proc, err := Launch(context.Background(), binary)
	if err != nil {
		return nil, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}
	return &launched{Target: proc.Target, proc: proc}, nil
}

// launched couples the adapted target with its subprocess so the engine's
// closeTarget releases the process.
type launched struct {
	*Target
	proc *Process
}

// Close tears the plugin subprocess down.
func (l *launched) Close() error { return l.proc.Close() }

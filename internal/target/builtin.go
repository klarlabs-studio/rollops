package target

import (
	"go.klarlabs.de/rollops/internal/target/ftp"
	"go.klarlabs.de/rollops/internal/target/kubernetes"
	"go.klarlabs.de/rollops/internal/target/plugin"
	"go.klarlabs.de/rollops/internal/target/ssh"
)

// Builtin returns a Registry with all first-party targets compiled in — the
// lean common case (single binary, no plugins). The "plugin" kind launches a
// sha256-pinned third-party plugin binary per operation; community plugins can
// also register additional kinds directly.
func Builtin() *Registry {
	r := NewRegistry()
	r.Register("ssh", ssh.New)
	r.Register("ftp", ftp.New)
	r.Register("kubernetes", kubernetes.New)
	r.Register("plugin", plugin.Build)
	return r
}

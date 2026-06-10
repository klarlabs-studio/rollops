package target

import (
	"go.klarlabs.de/rollops/internal/target/ftp"
	"go.klarlabs.de/rollops/internal/target/kubernetes"
	"go.klarlabs.de/rollops/internal/target/ssh"
)

// Builtin returns a Registry with all first-party targets compiled in — the
// lean common case (single binary, no plugins). Community gRPC plugins register
// additional kinds on top of this.
func Builtin() *Registry {
	r := NewRegistry()
	r.Register("ssh", ssh.New)
	r.Register("ftp", ftp.New)
	r.Register("kubernetes", kubernetes.New)
	return r
}

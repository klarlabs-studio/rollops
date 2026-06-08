package target

import (
	"go.klarlabs.de/rolloffs/internal/target/ftp"
	"go.klarlabs.de/rolloffs/internal/target/ssh"
)

// Builtin returns a Registry with all first-party targets compiled in — the
// lean common case (single binary, no plugins). Community gRPC plugins register
// additional kinds on top of this.
func Builtin() *Registry {
	r := NewRegistry()
	r.Register("ssh", ssh.New)
	r.Register("ftp", ftp.New)
	// "kubernetes" registers here as that target lands.
	return r
}

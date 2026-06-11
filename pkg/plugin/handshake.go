// Package plugin is the public toolkit for authoring Rollops target plugins: a
// plugin is a standalone binary that implements pkg/target.Target and calls
// Serve. The host launches the binary, reads one handshake line from stdout,
// dials the advertised unix socket over gRPC, and drives the target through it.
// Every plugin must pass pkg/conformance.Run; semantics are documented in
// docs/target-plugins.md.
package plugin

import (
	"fmt"
	"strconv"
	"strings"
)

// ProtocolVersion is bumped on any breaking change to the plugin wire.
const ProtocolVersion = 1

// Cookie is the magic handshake value; a mismatch means the subprocess is not a
// Rollops target plugin.
const Cookie = "ROLLOPS_TARGET_PLUGIN_V1"

// handshakePrefix marks the handshake line among arbitrary plugin stdout.
const handshakePrefix = "ROLLOPS_PLUGIN"

// Handshake is what a plugin advertises on stdout at startup.
type Handshake struct {
	ProtocolVersion int
	Cookie          string
	Addr            string // unix socket path the plugin's gRPC server listens on
}

// Line renders the single stdout handshake line.
func (h Handshake) Line() string {
	return fmt.Sprintf("%s|%d|%s|%s", handshakePrefix, h.ProtocolVersion, h.Cookie, h.Addr)
}

// ParseHandshake parses a stdout line; ok is false for non-handshake lines so
// the host can skip plugin log output preceding the handshake.
func ParseHandshake(line string) (Handshake, bool) {
	parts := strings.Split(strings.TrimSpace(line), "|")
	if len(parts) != 4 || parts[0] != handshakePrefix {
		return Handshake{}, false
	}
	v, err := strconv.Atoi(parts[1])
	if err != nil {
		return Handshake{}, false
	}
	return Handshake{ProtocolVersion: v, Cookie: parts[2], Addr: parts[3]}, true
}

// Verify rejects a plugin built against a different protocol version or
// without the correct cookie.
func (h Handshake) Verify() error {
	if h.Cookie != Cookie {
		return fmt.Errorf("plugin: bad handshake cookie (not a target plugin)")
	}
	if h.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("plugin: protocol version mismatch: host %d, plugin %d", ProtocolVersion, h.ProtocolVersion)
	}
	if h.Addr == "" {
		return fmt.Errorf("plugin: handshake missing listen address")
	}
	return nil
}

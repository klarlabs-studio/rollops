package plugin

import "testing"

func TestHandshake_LineRoundTrip(t *testing.T) {
	in := Handshake{ProtocolVersion: 1, Cookie: Cookie, Addr: "/tmp/rollops-plugin-1/plugin.sock"}
	got, ok := ParseHandshake(in.Line())
	if !ok || got != in {
		t.Fatalf("round trip = %+v ok=%v, want %+v", got, ok, in)
	}
}

func TestParseHandshake_SkipsLogLines(t *testing.T) {
	for _, line := range []string{"", "starting up…", "ROLLOPS_PLUGIN|x|y", "OTHER|1|c|/s"} {
		if _, ok := ParseHandshake(line); ok {
			t.Errorf("line %q must not parse as handshake", line)
		}
	}
}

func TestHandshake_Verify(t *testing.T) {
	good := Handshake{ProtocolVersion: ProtocolVersion, Cookie: Cookie, Addr: "/s"}
	if err := good.Verify(); err != nil {
		t.Errorf("valid handshake rejected: %v", err)
	}
	for _, bad := range []Handshake{
		{ProtocolVersion: 99, Cookie: Cookie, Addr: "/s"},
		{ProtocolVersion: ProtocolVersion, Cookie: "nope", Addr: "/s"},
		{ProtocolVersion: ProtocolVersion, Cookie: Cookie},
	} {
		if err := bad.Verify(); err == nil {
			t.Errorf("handshake %+v must be rejected", bad)
		}
	}
}

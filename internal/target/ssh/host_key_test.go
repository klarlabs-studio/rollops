package ssh

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// hostKeyCallback decides whether Rollops will talk to a host it cannot verify, and was
// entirely untested. The design is sound — a pinned key verifies, an explicit opt-in
// skips, and the default refuses rather than trusting on first use — but one path
// undermined it: a pinned key that failed to parse fell through, and combined with a
// stale insecureSkipHostKeyCheck from dev it removed verification altogether. That is the
// one outcome neither setting expresses.

// fakeHostKey is a real parsed public key, used as the key a server presents.
func fakeHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	// A fixed ed25519 authorized_keys line. Deterministic so the test does not depend on
	// key generation.
	const authorized = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEijmr5Ivj1MvUBBGYlHYVWNbEbEBZFhpJgYYcnQoLZL rollops-test"
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		t.Fatalf("parsing the test host key: %v", err)
	}
	return pk
}

func addr(t *testing.T) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", "10.0.0.1:22")
	if err != nil {
		t.Fatalf("resolving a test address: %v", err)
	}
	return a
}

// The secure default: no pin, no opt-in, so refuse. Trust-on-first-use would accept
// whatever answered, which is the whole attack.
func TestAnUnpinnedHostIsRefused(t *testing.T) {
	err := hostKeyCallback(spec{})("host:22", addr(t), fakeHostKey(t))

	if err == nil {
		t.Fatal("a host with no pinned key was accepted: Rollops would deploy to whatever " +
			"answered on that address")
	}
	if !strings.Contains(err.Error(), "hostKey") {
		t.Errorf("error = %q, want it to name the setting that fixes it", err)
	}
}

// A pinned key that matches the presented key verifies.
func TestAPinnedHostKeyVerifies(t *testing.T) {
	key := fakeHostKey(t)
	pinned := string(ssh.MarshalAuthorizedKey(key))

	if err := hostKeyCallback(spec{"hostKey": pinned})("host:22", addr(t), key); err != nil {
		t.Errorf("the pinned key did not verify the identical presented key: %v", err)
	}
}

// A pinned key that does not match must be refused, or pinning means nothing.
func TestADifferentHostKeyIsRefused(t *testing.T) {
	const otherKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICJ3Hs0kIFOEHzTVEZC7uYbHYUgWEUAqHl0Tvz1cGmDf other"
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(otherKey))
	if err != nil {
		t.Skipf("second test key unusable: %v", err)
	}

	callbackErr := hostKeyCallback(spec{"hostKey": string(ssh.MarshalAuthorizedKey(pinned))})(
		"host:22", addr(t), fakeHostKey(t))

	if callbackErr == nil {
		t.Fatal("a host presenting a different key than the pin was accepted: the pin would " +
			"be decoration")
	}
}

// The explicit opt-in still works — it exists for throwaway hosts and removing it would
// be a different change.
func TestTheExplicitOptInSkipsVerification(t *testing.T) {
	if err := hostKeyCallback(spec{"insecureSkipHostKeyCheck": true})(
		"host:22", addr(t), fakeHostKey(t)); err != nil {
		t.Errorf("insecureSkipHostKeyCheck did not skip verification: %v", err)
	}
}

// The hole. A pin that does not parse is a configuration error, and it must not fall
// through to the insecure branch: an operator who set hostKey has asked for verification,
// and a typo there combined with a leftover dev flag would silently remove it.
func TestAnUnparseablePinIsRefusedEvenWithTheOptInSet(t *testing.T) {
	err := hostKeyCallback(spec{
		"hostKey":                  "ssh-ed25519 this-is-not-base64-and-never-was",
		"insecureSkipHostKeyCheck": true,
	})("host:22", addr(t), fakeHostKey(t))

	if err == nil {
		t.Fatal("a malformed pin plus a stale insecure opt-in accepted the host unverified. " +
			"An operator who set hostKey asked for verification; a typo must not silently " +
			"remove it, which is the one outcome neither setting expresses.")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("error = %q, want it to say the pin could not be parsed so the typo is "+
			"findable — 'no pinned host key' would be misleading when one is set", err)
	}
}

// Without the opt-in, a malformed pin must also refuse, and say why.
func TestAnUnparseablePinIsRefusedOnItsOwn(t *testing.T) {
	err := hostKeyCallback(spec{"hostKey": "garbage"})("host:22", addr(t), fakeHostKey(t))

	if err == nil {
		t.Fatal("a malformed pin was accepted")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("error = %q, want it to name the parse failure", err)
	}
}

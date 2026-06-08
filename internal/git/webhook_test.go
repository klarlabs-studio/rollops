package git

import "testing"

func TestVerifySignature_RoundTrip(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(`{"ref":"refs/heads/main"}`)
	sig := Sign(secret, body)

	if err := VerifySignature(secret, body, sig); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
}

func TestVerifySignature_Tampered(t *testing.T) {
	secret := []byte("webhook-secret")
	sig := Sign(secret, []byte("original"))
	if err := VerifySignature(secret, []byte("tampered"), sig); err == nil {
		t.Error("tampered body must fail verification")
	}
	if err := VerifySignature([]byte("wrong-secret"), []byte("original"), sig); err == nil {
		t.Error("wrong secret must fail verification")
	}
}

func TestVerifySignature_Malformed(t *testing.T) {
	for _, h := range []string{"", "abc", "sha1=deadbeef", "sha256=zzzz"} {
		if err := VerifySignature([]byte("s"), []byte("b"), h); err == nil {
			t.Errorf("malformed header %q should fail", h)
		}
	}
}

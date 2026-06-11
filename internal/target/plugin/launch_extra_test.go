package plugin

import (
	"bytes"
	"strings"
	"testing"
)

func TestCappedWriter_PrefixesAndCaps(t *testing.T) {
	var buf bytes.Buffer
	w := &cappedWriter{w: &buf, prefix: []byte("[plugin] "), remaining: 20}
	_, _ = w.Write([]byte("hello\nworld\n"))
	out := buf.String()
	if !strings.Contains(out, "[plugin] hello") || !strings.Contains(out, "[plugin] world") {
		t.Errorf("each line must be prefixed: %q", out)
	}

	var buf2 bytes.Buffer
	w2 := &cappedWriter{w: &buf2, prefix: []byte("X"), remaining: 5}
	_, _ = w2.Write([]byte("aaaaaaaaaaaaaaaaa\n")) // 17 bytes, cap 5
	_, _ = w2.Write([]byte("more"))
	if !strings.Contains(buf2.String(), "truncated") {
		t.Errorf("over-cap output must announce truncation: %q", buf2.String())
	}
	// Reported count is always the full input (an io.Writer must not short-write).
	n, _ := w2.Write([]byte("zzz"))
	if n != 3 {
		t.Errorf("Write must report full length, got %d", n)
	}
}

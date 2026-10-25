package ssh

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The remote command runs under sh; run it locally to check its exit codes.
func runLocally(t *testing.T, cmd string) (int, string) {
	t.Helper()
	c := exec.Command("sh", "-c", cmd)
	out, err := c.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, string(out)
}

func TestReadFile_MissingFileIsNotFound(t *testing.T) {
	code, out := runLocally(t, readFileCommand(filepath.Join(t.TempDir(), "absent")))
	if _, err := readFileResult(code, out); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file -> %v; want ErrNotFound", err)
	}
}

func TestReadFile_PresentFileIsRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stamp")
	if err := os.WriteFile(p, []byte("sum-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runLocally(t, readFileCommand(p))
	got, err := readFileResult(code, out)
	if err != nil || string(got) != "sum-v2" {
		t.Fatalf("readFileResult = %q, %v", got, err)
	}
}

// Any non-zero exit from `cat` read as "never deployed": a permission
// error or an I/O failure made the target look absent, and rollops rewrote
// the payload or recorded the failure as a rollback.
func TestReadFile_UnreadableFileIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read a mode-000 file")
	}
	p := filepath.Join(t.TempDir(), "stamp")
	if err := os.WriteFile(p, []byte("sum-v2"), 0o000); err != nil {
		t.Fatal(err)
	}
	code, out := runLocally(t, readFileCommand(p))
	_, err := readFileResult(code, out)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("unreadable file -> %v; want a non-NotFound error", err)
	}
	if !strings.Contains(err.Error(), "exit") {
		t.Errorf("error %q should name the exit status", err)
	}
}

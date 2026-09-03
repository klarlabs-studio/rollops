package procgroup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPIDs_CgroupV2(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pids.current"), "100\n")
	write(t, filepath.Join(root, "pids.max"), "1000\n")
	got, err := ReadPIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != 100 || got.Max != 1000 {
		t.Fatalf("got %+v, want current=100 max=1000", got)
	}
}

func TestReadPIDs_CgroupV1(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pids", "pids.current"), "50\n")
	write(t, filepath.Join(root, "pids", "pids.max"), "200\n")
	got, err := ReadPIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != 50 || got.Max != 200 {
		t.Fatalf("got %+v", got)
	}
}

func TestReadPIDs_UnlimitedMax(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pids.current"), "10\n")
	write(t, filepath.Join(root, "pids.max"), "max\n")
	got, err := ReadPIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Max != 0 {
		t.Fatalf("unlimited max should be 0, got %d", got.Max)
	}
	if UnderPressure(got, 64) {
		t.Fatal("unlimited pids.max must never report pressure")
	}
}

func TestUnderPressure(t *testing.T) {
	cases := []struct {
		name     string
		current  int
		max      int
		headroom int
		want     bool
	}{
		{"comfortable", 100, 1000, 64, false},
		{"at headroom boundary", 936, 1000, 64, true}, // 1000-64=936
		{"just below", 935, 1000, 64, false},
		{"full", 1000, 1000, 64, true},
		{"unlimited", 99999, 0, 64, false},
		{"default headroom when unset", 990, 1000, 0, true}, // DefaultHeadroom=64 → 1000-64=936
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnderPressure(PIDsStats{Current: tc.current, Max: tc.max}, tc.headroom)
			if got != tc.want {
				t.Fatalf("UnderPressure(%d/%d, headroom=%d) = %v, want %v",
					tc.current, tc.max, tc.headroom, got, tc.want)
			}
		})
	}
}

func TestLivez_FailsClosedOnPressure(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pids.current"), "980\n")
	write(t, filepath.Join(root, "pids.max"), "1000\n")
	code, body := LivezStatus(root, DefaultHeadroom)
	if code != 503 {
		t.Fatalf("status = %d body=%q, want 503", code, body)
	}
	if !strings.Contains(body, "980") || !strings.Contains(body, "1000") {
		t.Fatalf("body should name current/max: %q", body)
	}
}

func TestLivez_OKWhenComfortable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pids.current"), "10\n")
	write(t, filepath.Join(root, "pids.max"), "1000\n")
	code, _ := LivezStatus(root, DefaultHeadroom)
	if code != 200 {
		t.Fatalf("status = %d, want 200", code)
	}
}

func TestLivez_OKWhenCgroupAbsent(t *testing.T) {
	// Hosts/tests without a readable cgroup must not flap the probe.
	code, body := LivezStatus(filepath.Join(t.TempDir(), "missing"), DefaultHeadroom)
	if code != 200 {
		t.Fatalf("absent cgroup should pass open, got %d %q", code, body)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseMax(t *testing.T) {
	if n, err := parseMax("42\n"); err != nil || n != 42 {
		t.Fatalf("got %d %v", n, err)
	}
	if n, err := parseMax("max\n"); err != nil || n != 0 {
		t.Fatalf("max literal: got %d %v", n, err)
	}
	if _, err := parseMax("nope"); err == nil {
		t.Fatal("bad max must error")
	}
}

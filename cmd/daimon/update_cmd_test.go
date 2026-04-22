package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsDevBuild(t *testing.T) {
	origVersion, origCommit := version, commit
	defer func() { version, commit = origVersion, origCommit }()

	tests := []struct {
		name    string
		version string
		commit  string
		want    bool
	}{
		{"default dev build", "dev", "none", true},
		{"empty version", "", "abc123", true},
		{"missing commit even with version", "v0.7.0", "none", true},
		{"full goreleaser build", "v0.7.0", "abc123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, commit = tt.version, tt.commit
			if got := isDevBuild(); got != tt.want {
				t.Errorf("isDevBuild() = %v, want %v (version=%q commit=%q)", got, tt.want, tt.version, tt.commit)
			}
		})
	}
}

func TestLastSlash(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"/usr/local/bin/daimon", 14},
		{"daimon", -1},
		{"C:\\Users\\me\\daimon.exe", 11},
		{"", -1},
	}
	for _, tt := range tests {
		if got := lastSlash(tt.in); got != tt.want {
			t.Errorf("lastSlash(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "daimon")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	if err := checkWritable(bin); err != nil {
		t.Fatalf("expected writable, got %v", err)
	}

	// Read-only directory check is unreliable on Windows (chmod semantics differ)
	// and when running as root (root bypasses mode bits). Skip in those cases.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("read-only check skipped on this platform/user")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	if err := checkWritable(bin); err == nil {
		t.Fatal("expected writable check to fail on read-only directory")
	}
}

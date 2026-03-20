//go:build linux || darwin

package skill

// lockFile and unlockFile use syscall.Flock for cross-process mutual exclusion
// on the config file during read-modify-write operations.
// Note: these functions are duplicated from internal/mcp/service_lock_unix.go (ADR-4).

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

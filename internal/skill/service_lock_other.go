//go:build !linux && !darwin

package skill

// lockFile and unlockFile are no-ops on platforms without flock support.
// Note: these functions are duplicated from internal/mcp/service_lock_other.go (ADR-4).

import "os"

func lockFile(f *os.File) error  { return nil }
func unlockFile(f *os.File) error { return nil }

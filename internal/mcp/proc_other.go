//go:build !linux

package mcp

import "os/exec"

// setPdeathsig is a no-op on non-Linux platforms where Pdeathsig is not available.
// Subprocess lifecycle is managed exclusively by exec.CommandContext on these platforms.
func setPdeathsig(_ *exec.Cmd) {}

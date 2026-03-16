//go:build linux

package mcp

import (
	"os/exec"
	"syscall"
)

// setPdeathsig configures the subprocess to receive SIGTERM when the parent process dies.
// This is a belt-and-suspenders measure: exec.CommandContext already sends SIGKILL when
// the context is cancelled, but Pdeathsig protects against the parent being killed with
// SIGKILL (uncatchable), which would otherwise leave MCP subprocesses as orphans.
func setPdeathsig(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

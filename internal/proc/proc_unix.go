//go:build !windows

// Package proc isolates all OS-specific process handling (detaching a daemon,
// liveness checks, and killing a supervised child's whole subtree) behind a
// neutral API so the rest of the code never imports syscall directly.
package proc

import (
	"os"
	"os/exec"
	"syscall"
)

// DetachAttr returns the SysProcAttr for the detached daemon: a new session so
// it outlives the launching shell.
func DetachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// ShutdownSignals are the signals that trigger graceful shutdown.
func ShutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

// IsAlive reports whether the process is running, using the signal-0 probe.
func IsAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// KillDaemon terminates the daemon's process group (tunnel child included),
// falling back to the bare pid if the group signal fails.
func KillDaemon(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return syscall.Kill(pid, syscall.SIGTERM)
	}
	return nil
}

// Child is a supervised child process whose whole subtree can be killed.
type Child struct {
	pid int
}

// StartChild puts cmd in its own process group and starts it, so the child and
// everything it spawns (session-manager-plugin) can be signalled together.
func StartChild(cmd *exec.Cmd) (*Child, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Child{pid: cmd.Process.Pid}, nil
}

// Kill signals the child's whole process group.
func (c *Child) Kill() error {
	return syscall.Kill(-c.pid, syscall.SIGTERM)
}

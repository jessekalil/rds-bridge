//go:build windows

// Package proc isolates all OS-specific process handling (detaching a daemon,
// liveness checks, and killing a supervised child's whole subtree) behind a
// neutral API so the rest of the code never imports syscall directly.
//
// On Windows a supervised child is placed in its own Job Object with
// KILL_ON_JOB_CLOSE, so terminating the child — or the daemon that holds the
// job handle — tears down the whole subtree (aws + session-manager-plugin)
// without needing administrator rights.
package proc

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code Windows reports for a process that is still
// running (STILL_ACTIVE).
const stillActive = 259

// DetachAttr returns the SysProcAttr for the detached daemon: its own process
// group and no attached console, so it outlives the launching shell.
func DetachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// ShutdownSignals are the signals that trigger graceful shutdown. Windows only
// delivers os.Interrupt (Ctrl+C / CTRL_BREAK).
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// IsAlive reports whether the process is running by querying its exit code.
func IsAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// KillDaemon terminates the daemon process. Its supervised children live in a
// Job Object whose only handle the daemon holds, so they are killed by the OS
// as the daemon exits.
func KillDaemon(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// Child is a supervised child process whose whole subtree can be killed via a
// Job Object.
type Child struct {
	job windows.Handle
}

// StartChild starts cmd and assigns it to a fresh Job Object configured to kill
// every contained process when the job handle closes. Processes the child
// spawns after assignment (session-manager-plugin) inherit the job.
func StartChild(cmd *exec.Cmd) (*Child, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		return nil, err
	}

	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		return nil, err
	}
	defer windows.CloseHandle(ph)

	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		return nil, err
	}

	return &Child{job: job}, nil
}

// Kill terminates every process in the child's job and releases the handle.
func (c *Child) Kill() error {
	if c.job == 0 {
		return nil
	}
	err := windows.TerminateJobObject(c.job, 1)
	windows.CloseHandle(c.job)
	c.job = 0
	return err
}

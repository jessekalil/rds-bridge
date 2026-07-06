package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Dir returns the per-target state directory under XDG_STATE_HOME (or
// ~/.local/state), creating it if needed.
func Dir(target string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "rds-bridge", target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func pidFile(target string) (string, error) {
	dir, err := Dir(target)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rds-bridge.pid"), nil
}

// LogFile returns the detached log file path for the target.
func LogFile(target string) (string, error) {
	dir, err := Dir(target)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rds-bridge.log"), nil
}

// WritePID records the running detached process pid.
func WritePID(target string, pid int) error {
	path, err := pidFile(target)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

// PID returns the recorded pid and whether the process is alive.
func PID(target string) (pid int, running bool) {
	path, err := pidFile(target)
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return pid, syscall.Kill(pid, 0) == nil
}

// Stop terminates the detached process group and clears the pid file.
func Stop(target string) error {
	pid, running := PID(target)
	path, _ := pidFile(target)
	if !running {
		_ = os.Remove(path)
		return fmt.Errorf("not running")
	}
	// Negative pid signals the whole process group (tunnel child included).
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	_ = os.Remove(path)
	return nil
}

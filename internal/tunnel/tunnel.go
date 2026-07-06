package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/jessekalil/rds-bridge/internal/config"
)

const restartDelay = 5 * time.Second

// Supervisor keeps an `aws ssm start-session` port-forward alive, restarting it
// whenever it exits, until the context is cancelled.
type Supervisor struct {
	t    *config.Target
	logf func(format string, args ...any)
}

// New creates a tunnel supervisor for the target. logf receives status lines.
func New(t *config.Target, logf func(format string, args ...any)) *Supervisor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Supervisor{t: t, logf: logf}
}

// Run blocks, supervising the tunnel until ctx is cancelled. Each child process
// runs in its own process group so it (and the session-manager-plugin it spawns)
// is killed cleanly on shutdown — no orphans.
func (s *Supervisor) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		s.logf("starting SSM tunnel")
		if err := s.runOnce(ctx); err != nil && ctx.Err() == nil {
			s.logf("SSM tunnel exited: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		s.logf("restarting tunnel in %s", restartDelay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(restartDelay):
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	params, err := json.Marshal(map[string][]string{
		"host":            {s.t.SSM.RemoteHost},
		"portNumber":      {strconv.Itoa(s.t.SSM.RemotePort)},
		"localPortNumber": {strconv.Itoa(s.t.SSM.LocalPort)},
	})
	if err != nil {
		return err
	}

	cmd := exec.Command("aws",
		"--profile", s.t.SSM.Profile,
		"ssm", "start-session",
		"--target", s.t.SSM.Target,
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", string(params),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start aws ssm: %w", err)
	}

	// Kill the whole process group when ctx is cancelled.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		case <-done:
		}
	}()

	err = cmd.Wait()
	close(done)
	return err
}

// WaitReady blocks until the local tunnel port accepts a TCP connection or the
// timeout elapses.
func WaitReady(ctx context.Context, localPort int, timeout time.Duration) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tunnel not ready on %s after %s: %w", addr, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

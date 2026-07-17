package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/jessekalil/rds-bridge/internal/awsiam"
	"github.com/jessekalil/rds-bridge/internal/config"
	"github.com/jessekalil/rds-bridge/internal/runner"
	"github.com/jessekalil/rds-bridge/internal/state"
	"golang.org/x/term"
)

var version = "dev"

const usage = `rds-bridge — local Postgres proxy to RDS over SSM with IAM auth

Usage:
  rds-bridge list
  rds-bridge start <target> [--detach] [--config <path>]
  rds-bridge stop <target>
  rds-bridge status <target>
  rds-bridge logs <target> [-f]
  rds-bridge env <target> [database]
  rds-bridge version

A target is one RDS instance (one SSM tunnel) exposed on one local port; the
client picks the database via its connection (dbname). The env command takes an
optional database name only to fill DB_DATABASE for convenience.

Config discovery: --config, then $RDS_BRIDGE_CONFIG, ./rds-bridge.yaml,
~/.config/rds-bridge/config.yaml`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "list":
		err = cmdList(args)
	case "start":
		err = cmdStart(args)
	case "stop":
		err = cmdStop(args)
	case "status":
		err = cmdStatus(args)
	case "logs":
		err = cmdLogs(args)
	case "env":
		err = cmdEnv(args)
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseArgs pulls --config and flags out of args, returning the positional
// arguments and the resolved config path.
func parseArgs(args []string, flags map[string]*bool) (positionals []string, configPath string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config":
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("--config needs a value")
			}
			configPath = args[i+1]
			i++
		case flags[a] != nil:
			*flags[a] = true
		default:
			positionals = append(positionals, a)
		}
	}
	return positionals, configPath, nil
}

func arg(positionals []string, i int) string {
	if i < len(positionals) {
		return positionals[i]
	}
	return ""
}

func loadTarget(name, configPath string) (*config.Target, string, error) {
	path, err := config.Discover(configPath)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, "", err
	}
	t, err := cfg.Target(name)
	if err != nil {
		return nil, "", err
	}
	return t, path, nil
}

func cmdList(args []string) error {
	_, configPath, _ := parseArgs(args, nil)
	path, err := config.Discover(configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	for _, n := range cfg.Names() {
		fmt.Println(n)
	}
	return nil
}

func cmdStart(args []string) error {
	detach := false
	positionals, configPath, err := parseArgs(args, map[string]*bool{"--detach": &detach})
	if err != nil {
		return err
	}
	target := arg(positionals, 0)
	if target == "" {
		return fmt.Errorf("missing target")
	}
	t, path, err := loadTarget(target, configPath)
	if err != nil {
		return err
	}

	// Preflight: make sure the SSM and IAM profiles have valid SSO credentials
	// before bringing anything up. Runs in this (interactive) parent so that a
	// --detach child inherits an already-refreshed SSO cache.
	if err := awsiam.EnsureProfiles(context.Background(),
		[]string{t.SSM.Profile, t.IAM.Profile}, isTTY(),
		func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[auth] "+f+"\n", a...) }); err != nil {
		return err
	}

	if detach {
		return startDetached(target, path)
	}
	return runner.Run(t)
}

func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func startDetached(target, configPath string) error {
	if _, running := state.PID(target); running {
		return fmt.Errorf("already running")
	}
	logPath, err := state.LogFile(target)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "start", target, "--config", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := state.WritePID(target, cmd.Process.Pid); err != nil {
		return err
	}
	fmt.Printf("started %s (pid %d)\n", target, cmd.Process.Pid)
	fmt.Printf("logs:  rds-bridge logs %s -f\n", target)
	fmt.Printf("env:   eval \"$(rds-bridge env %s)\"\n", target)
	return nil
}

func cmdStop(args []string) error {
	positionals, _, err := parseArgs(args, nil)
	if err != nil {
		return err
	}
	target := arg(positionals, 0)
	if target == "" {
		return fmt.Errorf("missing target")
	}
	if err := state.Stop(target); err != nil {
		return err
	}
	fmt.Printf("stopped %s\n", target)
	return nil
}

func cmdStatus(args []string) error {
	positionals, configPath, err := parseArgs(args, nil)
	if err != nil {
		return err
	}
	target := arg(positionals, 0)
	if target == "" {
		return fmt.Errorf("missing target")
	}
	t, _, err := loadTarget(target, configPath)
	if err != nil {
		return err
	}

	pid, running := state.PID(target)
	if running {
		fmt.Printf("process: running (pid %d)\n", pid)
	} else {
		fmt.Println("process: stopped")
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(t.ListenPort))
	conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
	if derr == nil {
		_ = conn.Close()
		fmt.Printf("proxy:   accepting on %s\n", addr)
	} else {
		fmt.Printf("proxy:   not reachable on %s\n", addr)
	}
	return nil
}

func cmdLogs(args []string) error {
	follow := false
	positionals, _, err := parseArgs(args, map[string]*bool{"-f": &follow, "--follow": &follow})
	if err != nil {
		return err
	}
	target := arg(positionals, 0)
	if target == "" {
		return fmt.Errorf("missing target")
	}
	logPath, err := state.LogFile(target)
	if err != nil {
		return err
	}
	tailArgs := []string{"-n", "200"}
	if follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, logPath)
	tail := exec.Command("tail", tailArgs...)
	tail.Stdout = os.Stdout
	tail.Stderr = os.Stderr
	return tail.Run()
}

func cmdEnv(args []string) error {
	positionals, configPath, err := parseArgs(args, nil)
	if err != nil {
		return err
	}
	target := arg(positionals, 0)
	if target == "" {
		return fmt.Errorf("missing target")
	}
	t, _, err := loadTarget(target, configPath)
	if err != nil {
		return err
	}
	// Optional database name: fills DB_DATABASE for convenience. When omitted the
	// app chooses its own database (the proxy routes by whatever dbname connects).
	if db := arg(positionals, 1); db != "" {
		fmt.Printf("export DB_DATABASE=%s\n", db)
	}
	fmt.Printf("export DB_PORT=%d\n", t.ListenPort)
	fmt.Printf("export DB_SSL_REJECT_UNAUTHORIZED=false\n")
	fmt.Printf("export DB_READ_HOST=127.0.0.1\n")
	fmt.Printf("export DB_WRITE_HOST=127.0.0.1\n")
	fmt.Printf("export DB_USER=%s\n", t.Local.User)
	fmt.Printf("export DB_PASSWORD=%s\n", t.Local.Password)
	return nil
}

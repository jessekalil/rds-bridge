package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/jessekalil/rds-bridge/internal/config"
	"github.com/jessekalil/rds-bridge/internal/runner"
	"github.com/jessekalil/rds-bridge/internal/state"
)

var version = "dev"

const usage = `rds-bridge — local Postgres proxy to RDS over SSM with IAM auth

Usage:
  rds-bridge list [target]
  rds-bridge start <target> [--detach] [--config <path>]
  rds-bridge stop <target>
  rds-bridge status <target>
  rds-bridge logs <target> [-f]
  rds-bridge env <target> [database]
  rds-bridge version

A target is one RDS instance (one SSM tunnel) exposing one or more databases,
each on its own local port.

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
	positionals, configPath, _ := parseArgs(args, nil)
	path, err := config.Discover(configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	// `list <target>` shows that target's databases and ports.
	if target := arg(positionals, 0); target != "" {
		t, err := cfg.Target(target)
		if err != nil {
			return err
		}
		for _, db := range t.SortedDatabases() {
			fmt.Printf("%s\t127.0.0.1:%d\n", db.Name, db.ListenPort)
		}
		return nil
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

	if detach {
		return startDetached(target, path)
	}
	return runner.Run(t)
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

	for _, db := range t.SortedDatabases() {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(db.ListenPort))
		conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
		if derr == nil {
			_ = conn.Close()
			fmt.Printf("%-30s accepting on %s\n", db.Name, addr)
		} else {
			fmt.Printf("%-30s not reachable on %s\n", db.Name, addr)
		}
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
	db, err := t.Database(arg(positionals, 1))
	if err != nil {
		return err
	}
	local := db.EffectiveLocal(t)
	fmt.Printf("export DB_DATABASE=%s\n", db.Name)
	fmt.Printf("export DB_PORT=%d\n", db.ListenPort)
	fmt.Printf("export DB_SSL_REJECT_UNAUTHORIZED=false\n")
	fmt.Printf("export DB_READ_HOST=127.0.0.1\n")
	fmt.Printf("export DB_WRITE_HOST=127.0.0.1\n")
	fmt.Printf("export DB_USER=%s\n", local.User)
	fmt.Printf("export DB_PASSWORD=%s\n", local.Password)
	return nil
}

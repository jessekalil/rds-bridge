package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/jessekalil/rds-bridge/internal/config"
	"github.com/jessekalil/rds-bridge/internal/proc"
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
  rds-bridge env <target> [database] [--shell posix|powershell|cmd]
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
	cmd.SysProcAttr = proc.DetachAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := state.WritePID(target, cmd.Process.Pid); err != nil {
		return err
	}
	fmt.Printf("started %s (pid %d)\n", target, cmd.Process.Pid)
	fmt.Printf("logs:  rds-bridge logs %s -f\n", target)
	fmt.Printf("env:   %s\n", envHint(target))
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
	return state.Tail(logPath, follow, os.Stdout)
}

func cmdEnv(args []string) error {
	shell, args, err := extractShell(args)
	if err != nil {
		return err
	}
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
	vars := [][2]string{
		{"DB_DATABASE", db.Name},
		{"DB_PORT", strconv.Itoa(db.ListenPort)},
		{"DB_SSL_REJECT_UNAUTHORIZED", "false"},
		{"DB_READ_HOST", "127.0.0.1"},
		{"DB_WRITE_HOST", "127.0.0.1"},
		{"DB_USER", local.User},
		{"DB_PASSWORD", local.Password},
	}
	for _, kv := range vars {
		fmt.Println(formatEnv(shell, kv[0], kv[1]))
	}
	return nil
}

// defaultShell picks the env output syntax for the current OS: PowerShell on
// Windows, POSIX everywhere else.
func defaultShell() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "posix"
}

// extractShell pulls an optional `--shell <value>` out of args, returning the
// resolved shell and the remaining args. Unknown values are rejected.
func extractShell(args []string) (shell string, rest []string, err error) {
	shell = defaultShell()
	for i := 0; i < len(args); i++ {
		if args[i] == "--shell" {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--shell needs a value (posix|powershell|cmd)")
			}
			shell = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	switch shell {
	case "posix", "powershell", "cmd":
		return shell, rest, nil
	default:
		return "", nil, fmt.Errorf("unknown shell %q (want posix|powershell|cmd)", shell)
	}
}

// formatEnv renders one environment assignment in the given shell's syntax.
func formatEnv(shell, key, value string) string {
	switch shell {
	case "powershell":
		return fmt.Sprintf("$env:%s = \"%s\"", key, value)
	case "cmd":
		return fmt.Sprintf("set %s=%s", key, value)
	default: // posix
		return fmt.Sprintf("export %s=%s", key, value)
	}
}

// envHint returns the shell command that loads a target's env vars into the
// current shell, matched to the OS default shell.
func envHint(target string) string {
	switch defaultShell() {
	case "powershell":
		return fmt.Sprintf("rds-bridge env %s --shell powershell | Invoke-Expression", target)
	case "cmd":
		return fmt.Sprintf(`rds-bridge env %s --shell cmd > "%%TEMP%%\rds-env.bat" && call "%%TEMP%%\rds-env.bat"`, target)
	default: // posix
		return fmt.Sprintf("eval \"$(rds-bridge env %s)\"", target)
	}
}

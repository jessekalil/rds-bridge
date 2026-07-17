package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `
targets:
  dev:
    aws_region: us-east-1
    ssm:
      profile: p1
      target: i-123
      remote_host: rds.internal
      remote_port: 5432
      local_port: 5433
    iam:
      profile: p2
      token_host: db.rds.amazonaws.com
      token_port: 5432
    listen_port: 5488
    local:
      user: app
      password: app
`

func writeSample(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rds-bridge.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndTarget(t *testing.T) {
	cfg, err := Load(writeSample(t, sample))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Names(); len(got) != 1 || got[0] != "dev" {
		t.Fatalf("names = %v", got)
	}
	tgt, err := cfg.Target("dev")
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	if tgt.Name != "dev" || tgt.SSM.LocalPort != 5433 || tgt.ListenPort != 5488 {
		t.Fatalf("unexpected target: %+v", tgt)
	}
	if tgt.Local.User != "app" || tgt.Local.Password != "app" {
		t.Fatalf("unexpected local: %+v", tgt.Local)
	}
}

func TestTargetNotFound(t *testing.T) {
	cfg, _ := Load(writeSample(t, sample))
	if _, err := cfg.Target("nope"); err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestMissingListenPort(t *testing.T) {
	body := `
targets:
  bad:
    aws_region: us-east-1
    ssm: { profile: p, target: i, remote_host: h, remote_port: 5432, local_port: 5433 }
    iam: { profile: p, token_host: h, token_port: 5432 }
    local: { user: app, password: app }
`
	cfg, err := Load(writeSample(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := cfg.Target("bad"); err == nil {
		t.Fatal("expected missing listen_port error")
	}
}

func TestMissingCredential(t *testing.T) {
	body := `
targets:
  nocred:
    aws_region: us-east-1
    ssm: { profile: p, target: i, remote_host: h, remote_port: 5432, local_port: 5433 }
    iam: { profile: p, token_host: h, token_port: 5432 }
    listen_port: 5488
`
	cfg, _ := Load(writeSample(t, body))
	if _, err := cfg.Target("nocred"); err == nil {
		t.Fatal("expected missing credential error")
	}
}

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
    local:
      user: app
      password: app
    databases:
      one:
        listen_port: 5488
      two:
        listen_port: 5489
        local:
          user: u2
          password: p2pw
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
	if tgt.Name != "dev" || tgt.SSM.LocalPort != 5433 || len(tgt.Databases) != 2 {
		t.Fatalf("unexpected target: %+v", tgt)
	}
	if dbs := tgt.SortedDatabases(); dbs[0].Name != "one" || dbs[1].Name != "two" {
		t.Fatalf("sorted databases = %v %v", dbs[0].Name, dbs[1].Name)
	}
}

func TestEffectiveLocal(t *testing.T) {
	cfg, _ := Load(writeSample(t, sample))
	tgt, _ := cfg.Target("dev")

	one, _ := tgt.Database("one")
	if l := one.EffectiveLocal(tgt); l.User != "app" || l.Password != "app" {
		t.Fatalf("one should inherit shared cred, got %+v", l)
	}
	two, _ := tgt.Database("two")
	if l := two.EffectiveLocal(tgt); l.User != "u2" || l.Password != "p2pw" {
		t.Fatalf("two should use override, got %+v", l)
	}
}

func TestDatabaseSelection(t *testing.T) {
	cfg, _ := Load(writeSample(t, sample))
	tgt, _ := cfg.Target("dev")

	// Empty name with >1 database is an error.
	if _, err := tgt.Database(""); err == nil {
		t.Fatal("expected error for empty db name with multiple databases")
	}
	if _, err := tgt.Database("nope"); err == nil {
		t.Fatal("expected error for unknown database")
	}
	if db, err := tgt.Database("one"); err != nil || db.ListenPort != 5488 {
		t.Fatalf("Database(one) = %+v, %v", db, err)
	}
}

func TestSingleDatabaseDefault(t *testing.T) {
	body := `
targets:
  solo:
    aws_region: us-east-1
    ssm: { profile: p, target: i, remote_host: h, remote_port: 5432, local_port: 5433 }
    iam: { profile: p, token_host: h, token_port: 5432 }
    local: { user: app, password: app }
    databases:
      only:
        listen_port: 5500
`
	cfg, _ := Load(writeSample(t, body))
	tgt, _ := cfg.Target("solo")
	db, err := tgt.Database("")
	if err != nil || db.Name != "only" {
		t.Fatalf("single-db default = %+v, %v", db, err)
	}
}

func TestDuplicateListenPort(t *testing.T) {
	body := `
targets:
  dup:
    aws_region: us-east-1
    ssm: { profile: p, target: i, remote_host: h, remote_port: 5432, local_port: 5433 }
    iam: { profile: p, token_host: h, token_port: 5432 }
    local: { user: app, password: app }
    databases:
      a: { listen_port: 5500 }
      b: { listen_port: 5500 }
`
	cfg, _ := Load(writeSample(t, body))
	if _, err := cfg.Target("dup"); err == nil {
		t.Fatal("expected duplicate listen_port error")
	}
}

func TestMissingCredential(t *testing.T) {
	body := `
targets:
  nocred:
    aws_region: us-east-1
    ssm: { profile: p, target: i, remote_host: h, remote_port: 5432, local_port: 5433 }
    iam: { profile: p, token_host: h, token_port: 5432 }
    databases:
      a: { listen_port: 5500 }
`
	cfg, _ := Load(writeSample(t, body))
	if _, err := cfg.Target("nocred"); err == nil {
		t.Fatal("expected missing credential error")
	}
}

func TestTargetNotFound(t *testing.T) {
	cfg, _ := Load(writeSample(t, sample))
	if _, err := cfg.Target("nope"); err == nil {
		t.Fatal("expected error for missing target")
	}
}

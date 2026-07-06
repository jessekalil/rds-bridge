package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML document: a map of named targets.
type Config struct {
	Targets map[string]*Target `yaml:"targets"`
}

// Target holds everything needed to bring up the proxies for one RDS instance.
// A single instance (one SSM tunnel) can expose several databases, each on its
// own local listen port.
type Target struct {
	Name      string               `yaml:"-"`
	AWSRegion string               `yaml:"aws_region"`
	SSM       SSM                  `yaml:"ssm"`
	IAM       IAM                  `yaml:"iam"`
	Local     Local                `yaml:"local"` // shared static credential (default for all databases)
	Databases map[string]*Database `yaml:"databases"`
}

// Database is one database on the instance, exposed on its own listen port.
type Database struct {
	Name       string `yaml:"-"`
	ListenPort int    `yaml:"listen_port"`
	// Local overrides the target-level static credential for this database.
	Local *Local `yaml:"local"`
}

// SSM describes the port-forwarding session to the bastion.
type SSM struct {
	Profile    string `yaml:"profile"`
	Target     string `yaml:"target"`
	RemoteHost string `yaml:"remote_host"`
	RemotePort int    `yaml:"remote_port"`
	LocalPort  int    `yaml:"local_port"`
}

// IAM describes how the RDS auth token is signed.
type IAM struct {
	Profile   string `yaml:"profile"`
	TokenHost string `yaml:"token_host"`
	TokenPort int    `yaml:"token_port"`
	// User overrides the IAM DB username. When empty it is resolved from STS
	// GetCallerIdentity (the UserId field), matching the legacy shell behavior.
	User string `yaml:"user"`
}

// Local is the static credential the app uses to reach the proxy.
type Local struct {
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// Load reads and validates the config file, returning all targets.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cfg.Targets) == 0 {
		return nil, fmt.Errorf("no targets defined in %s", path)
	}
	for name, t := range cfg.Targets {
		t.Name = name
		for dbName, db := range t.Databases {
			db.Name = dbName
		}
	}
	return &cfg, nil
}

// Target loads the config and returns a single validated target.
func (c *Config) Target(name string) (*Target, error) {
	t, ok := c.Targets[name]
	if !ok {
		return nil, fmt.Errorf("target not found: %s", name)
	}
	if err := t.validate(); err != nil {
		return nil, fmt.Errorf("target %s: %w", name, err)
	}
	return t, nil
}

// Names returns the configured target names, sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Targets))
	for name := range c.Targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t *Target) validate() error {
	missing := func(field string, ok bool) error {
		if !ok {
			return fmt.Errorf("missing %s", field)
		}
		return nil
	}
	checks := []struct {
		field string
		ok    bool
	}{
		{"aws_region", t.AWSRegion != ""},
		{"ssm.profile", t.SSM.Profile != ""},
		{"ssm.target", t.SSM.Target != ""},
		{"ssm.remote_host", t.SSM.RemoteHost != ""},
		{"ssm.remote_port", t.SSM.RemotePort != 0},
		{"ssm.local_port", t.SSM.LocalPort != 0},
		{"iam.profile", t.IAM.Profile != ""},
		{"iam.token_host", t.IAM.TokenHost != ""},
		{"iam.token_port", t.IAM.TokenPort != 0},
	}
	for _, c := range checks {
		if err := missing(c.field, c.ok); err != nil {
			return err
		}
	}

	if len(t.Databases) == 0 {
		return fmt.Errorf("no databases defined")
	}
	ports := map[int]string{}
	for _, db := range t.Databases {
		if db.ListenPort == 0 {
			return fmt.Errorf("database %s: missing listen_port", db.Name)
		}
		if other, dup := ports[db.ListenPort]; dup {
			return fmt.Errorf("databases %s and %s share listen_port %d", other, db.Name, db.ListenPort)
		}
		ports[db.ListenPort] = db.Name

		local := db.EffectiveLocal(t)
		if local.User == "" || local.Password == "" {
			return fmt.Errorf("database %s: missing local.user/local.password (no shared default either)", db.Name)
		}
	}
	return nil
}

// EffectiveLocal returns the static credential for this database: its own
// override if set, otherwise the target-level shared credential.
func (d *Database) EffectiveLocal(t *Target) Local {
	if d.Local != nil {
		return *d.Local
	}
	return t.Local
}

// SortedDatabases returns the databases ordered by name for stable output.
func (t *Target) SortedDatabases() []*Database {
	dbs := make([]*Database, 0, len(t.Databases))
	for _, db := range t.Databases {
		dbs = append(dbs, db)
	}
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Name < dbs[j].Name })
	return dbs
}

// Database returns a database by name. If name is empty and the target has
// exactly one database, that one is returned.
func (t *Target) Database(name string) (*Database, error) {
	if name == "" {
		if len(t.Databases) == 1 {
			return t.SortedDatabases()[0], nil
		}
		return nil, fmt.Errorf("target %s has %d databases; specify one of: %v", t.Name, len(t.Databases), t.databaseNames())
	}
	db, ok := t.Databases[name]
	if !ok {
		return nil, fmt.Errorf("database %s not found in target %s (have: %v)", name, t.Name, t.databaseNames())
	}
	return db, nil
}

func (t *Target) databaseNames() []string {
	names := make([]string, 0, len(t.Databases))
	for _, db := range t.SortedDatabases() {
		names = append(names, db.Name)
	}
	return names
}

// Discover finds the config path using, in order: the explicit flag, the
// RDS_BRIDGE_CONFIG env var, ./rds-bridge.yaml, then ~/.config/rds-bridge/config.yaml.
func Discover(flagPath string) (string, error) {
	candidates := []string{}
	if flagPath != "" {
		candidates = append(candidates, flagPath)
	}
	if env := os.Getenv("RDS_BRIDGE_CONFIG"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "rds-bridge.yaml")
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "rds-bridge", "config.yaml"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no config file found (looked for: %v)", candidates)
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Identity is a principal written in config: who a background measurement or
// an anonymous copilot request acts as.
type Identity struct {
	User  string            `yaml:"user"`
	Role  string            `yaml:"role"`
	Attrs map[string]string `yaml:"attrs"`
}

// User is one static bearer-token identity (auth.users). It is the minimal
// form for a small deployment or a demo with two named people; a real IdP goes
// in auth.oidc. The token itself lives in the environment variable TokenEnv.
type User struct {
	Name     string            `yaml:"name"`
	Role     string            `yaml:"role"`
	TokenEnv string            `yaml:"token_env"`
	Attrs    map[string]string `yaml:"attrs"`
}

// Copilot configures the in-process agent.
type Copilot struct {
	// Principal is who the agent's tools act as when the caller is anonymous.
	// An identified caller (auth.users / OIDC) is used instead.
	Principal Identity `yaml:"principal"`
	// BriefFile is an optional domain brief appended to the generic system
	// prompt. Relative paths resolve against the config file's directory.
	BriefFile string `yaml:"brief_file"`
	// MaxTurns bounds one copilot run (default 60): a whole-business review asks dozens of
	// questions before it can record a goal, findings and a plan.
	MaxTurns int `yaml:"max_turns"`
	// Checks is the conflict-check file behind health_check; empty disables
	// the tool.
	Checks string `yaml:"checks"`
	// MCP are external MCP servers. Tools are read-only lookups the agent may
	// call; actions only ever run as part of an adopted plan.
	MCP []MCPServer `yaml:"mcp"`
}

// MCPServer is one external MCP server (streamable HTTP, Bearer token).
type MCPServer struct {
	Name     string   `yaml:"name"`
	URL      string   `yaml:"url"`
	TokenEnv string   `yaml:"token_env"`
	Tools    []string `yaml:"tools"`
	Actions  []string `yaml:"actions"`
}

// Consult configures the consulting loop (goal → finding → plan → adoption →
// acceptance) over the default database.
type Consult struct {
	// Disabled turns the loop off. It is on by default whenever the default
	// database has a semantic model.
	Disabled bool `yaml:"disabled"`
	// Store is the SQLite ledger. Empty is consult.db beside the config file.
	Store string `yaml:"store"`
	// TimeDimension is the default dimension measurement windows are cut on,
	// for metrics that reach more than one.
	TimeDimension string `yaml:"time_dimension"`
	// MeasureAs is the identity every baseline and acceptance runs as.
	MeasureAs Identity `yaml:"measure_as"`
	// AdoptRoles, when set, are the only roles allowed to adopt or reject.
	AdoptRoles []string `yaml:"adopt_roles"`
	// CheckEvery is how often due plans are accepted (Go duration, default 1h).
	CheckEvery string `yaml:"check_every"`
	// AllowAsOf lets adoption and acceptance be anchored on a past day, to
	// replay history. Every use is recorded on the decision.
	AllowAsOf bool `yaml:"allow_as_of"`
}

// Interval is CheckEvery parsed.
func (c Consult) Interval() time.Duration {
	d, err := time.ParseDuration(c.CheckEvery)
	if err != nil || d <= 0 {
		return time.Hour
	}
	return d
}

// Path resolves a path written in the config: absolute as-is, relative to the
// config file's directory otherwise. Without a file (flags mode) it is the
// working directory.
func (c *Config) Path(p string) string {
	if p == "" || filepath.IsAbs(p) || c.dir == "" {
		return p
	}
	return filepath.Join(c.dir, p)
}

// ConsultStore is the ledger path, defaulted.
func (c *Config) ConsultStore() string {
	if c.Consult.Store == "" {
		return c.Path("consult.db")
	}
	return c.Path(c.Consult.Store)
}

// resolveAdvisor finishes loading the advisor sections: it remembers the
// config directory, lets a model path that does not exist relative to the
// working directory be found beside the config file, and validates.
func (c *Config) resolveAdvisor(path string) error {
	c.dir = filepath.Dir(path)
	besides := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if alt := filepath.Join(c.dir, p); fileExists(alt) {
			return alt
		}
		return p
	}
	c.Model = besides(c.Model)
	for i := range c.Databases {
		c.Databases[i].Model = besides(c.Databases[i].Model)
	}
	if c.Consult.CheckEvery != "" {
		if d, err := time.ParseDuration(c.Consult.CheckEvery); err != nil || d <= 0 {
			return fmt.Errorf("config: consult.check_every %q is not a positive duration (e.g. 10m, 1h)", c.Consult.CheckEvery)
		}
	}
	seen := map[string]bool{}
	for i, u := range c.Auth.Users {
		if u.Name == "" || u.TokenEnv == "" {
			return fmt.Errorf("config: auth.users[%d] needs name and token_env", i)
		}
		if seen[u.Name] {
			return fmt.Errorf("config: auth.users has %q twice", u.Name)
		}
		seen[u.Name] = true
	}
	names := map[string]bool{}
	for i, m := range c.Copilot.MCP {
		if m.Name == "" || m.URL == "" {
			return fmt.Errorf("config: copilot.mcp[%d] needs name and url", i)
		}
		if names[m.Name] {
			return fmt.Errorf("config: copilot.mcp has %q twice", m.Name)
		}
		names[m.Name] = true
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

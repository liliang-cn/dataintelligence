// Package config is the single boot contract for the DataIntelligence service:
// one YAML file declares the semantic model, sources, warehouse, governance, and
// auth, and the daemon starts entirely from it. Values support ${ENV} expansion
// so secrets stay in the environment, never in the file. It is domain-neutral —
// the file points at whatever model/sources a deployment supplies.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole service configuration.
type Config struct {
	Model      string     `yaml:"model"`      // semantic model YAML path
	Sources    string     `yaml:"sources"`    // source manifest path (optional)
	IndexPath  string     `yaml:"index_path"` // grounding index sqlite path (optional; temp if empty)
	Warehouse  Warehouse  `yaml:"warehouse"`
	Auth       Auth       `yaml:"auth"`
	Governance Governance `yaml:"governance"`
	Server     Server     `yaml:"server"`
}

type Warehouse struct {
	DSN          string `yaml:"dsn"`
	AppRole      string `yaml:"app_role"`        // least-priv role for OBO sessions (RLS)
	MaxScanBytes int64  `yaml:"max_scan_bytes"`  // pre-execution byte ceiling (0 = off)
	TimeoutSecs  int    `yaml:"timeout_secs"`    // per-query statement timeout
	MaxRows      int    `yaml:"max_rows"`        // hard row cap
}

type Auth struct {
	OIDC *OIDC `yaml:"oidc"` // nil → open (no bearer required); set → every request verified
}

type OIDC struct {
	Issuer       string `yaml:"issuer"`
	Audience     string `yaml:"audience"`
	JWKSURL      string `yaml:"jwks_url"`       // preferred for real IdPs
	PublicKeyPEM string `yaml:"public_key_pem"` // OR a static key (PEM, supports ${ENV})
}

type Governance struct {
	TenantBudgetBytes int64 `yaml:"tenant_budget_bytes"` // per-tenant spend cap (0 = unlimited)
}

type Server struct {
	RESTAddr string `yaml:"rest_addr"` // /v1 REST API listen addr
	MCPAddr  string `yaml:"mcp_addr"`  // MCP (streamable HTTP) listen addr
	OTel     bool   `yaml:"otel"`      // emit OpenTelemetry spans
}

// Load reads, env-expands, validates, and defaults a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if c.Model == "" {
		return nil, fmt.Errorf("config: model is required")
	}
	if c.Warehouse.DSN == "" {
		return nil, fmt.Errorf("config: warehouse.dsn is required")
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.RESTAddr == "" {
		c.Server.RESTAddr = ":41900"
	}
	if c.Server.MCPAddr == "" {
		c.Server.MCPAddr = ":41910"
	}
	if c.Warehouse.TimeoutSecs == 0 {
		c.Warehouse.TimeoutSecs = 30
	}
	if c.Warehouse.MaxRows == 0 {
		c.Warehouse.MaxRows = 10000
	}
}

// Timeout returns the warehouse statement timeout as a Duration.
func (c *Config) Timeout() time.Duration { return time.Duration(c.Warehouse.TimeoutSecs) * time.Second }

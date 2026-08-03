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
	// Engagement names the customer this deployment serves. Every audit row is
	// stamped with it.
	//
	// One deployment serving several customers writes all their trails into one
	// table, and a trail that cannot be filtered to a customer cannot be shown
	// to that customer — which is the moment somebody exports the whole thing
	// and hands over another customer's questions along with it. It is also
	// what lets an eval set be mined per customer instead of from everyone's
	// questions at once.
	Engagement string     `yaml:"engagement"`
	Model      string     `yaml:"model"`      // semantic model YAML path (single-database form)
	Sources   string     `yaml:"sources"`    // source manifest path (optional)
	IndexPath string     `yaml:"index_path"` // grounding index sqlite path (optional; temp if empty)
	Databases []Database `yaml:"databases"`  // multi-database form; see Defs
	// DatabasesFile enables runtime registration (POST /v1/databases) and is
	// where those registrations are stored. Empty (the default) means the only
	// databases are the ones declared here — an endpoint that opens a DSN the
	// caller supplies is not something to turn on by accident on a networked
	// service. A product that ships its own DI sets it.
	DatabasesFile string `yaml:"databases_file"`
	// ModelsDir is where generated semantic models are written. Empty puts them
	// beside databases_file, so a product that enables runtime registration gets
	// model generation with it — the two halves of "connect a database, then
	// govern it" arrive together rather than one working and the other 403-ing.
	ModelsDir  string     `yaml:"models_dir"`
	Warehouse  Warehouse  `yaml:"warehouse"`
	Auth       Auth       `yaml:"auth"`
	Governance Governance `yaml:"governance"`
	Server     Server     `yaml:"server"`
}

// Database is one governed database: a semantic model over a warehouse.
//
//	databases:
//	  - id: conglomerate
//	    model: models/conglomerate.yaml
//	    dsn: postgres://…/conglomerate
//	  - id: shop
//	    model: models/shop.yaml
//	    dsn: mysql://…/shop
//
// The id appears in URLs (the MCP endpoint for a database is /db/{id}), so it
// is restricted to letters, digits, '-' and '_'.
type Database struct {
	ID    string `yaml:"id"`
	Model string `yaml:"model"` // empty → unmodelled: direct SQL only, no governed tools
	DSN   string `yaml:"dsn"`

	// AllowRawSQL opens POST /v1/sql on a *modelled* database. Off by default:
	// modelling a warehouse means answers come from declared metrics, and an
	// open SQL path beside them quietly makes that optional.
	AllowRawSQL bool `yaml:"allow_raw_sql"`
}

type Warehouse struct {
	DSN          string `yaml:"dsn"`
	AppRole      string `yaml:"app_role"`       // least-priv role for OBO sessions (RLS)
	MaxScanBytes int64  `yaml:"max_scan_bytes"` // pre-execution byte ceiling (0 = off)
	TimeoutSecs  int    `yaml:"timeout_secs"`   // per-query statement timeout
	MaxRows      int    `yaml:"max_rows"`       // hard row cap
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
	// Zero databases is legitimate only when they can be registered later:
	// that is a product shipping DI before its user has connected anything.
	// Without databases_file it is a misconfiguration that would otherwise
	// surface as an empty service nobody can explain.
	if len(c.Databases) == 0 && c.DatabasesFile == "" {
		if c.Model == "" {
			return nil, fmt.Errorf("config: model is required (or declare databases:, or set databases_file to register them at runtime)")
		}
		if c.Warehouse.DSN == "" {
			return nil, fmt.Errorf("config: warehouse.dsn is required (or declare databases:, or set databases_file to register them at runtime)")
		}
	}
	return &c, nil
}

// Defs normalises both config shapes into the registry's input.
//
// The single-database form (top-level model: + warehouse.dsn:) still works and
// becomes one database named "default", so existing configs and every
// `di serve -model … -dsn …` invocation keep behaving exactly as before. When
// both forms appear, the explicit list wins and the top-level pair is ignored:
// silently merging them would make the default database depend on which key the
// reader noticed first.
func (c *Config) Defs() []Database {
	if len(c.Databases) > 0 {
		return c.Databases
	}
	if c.Warehouse.DSN == "" {
		return nil // nothing declared; databases arrive by registration
	}
	return []Database{{ID: "default", Model: c.Model, DSN: c.Warehouse.DSN}}
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

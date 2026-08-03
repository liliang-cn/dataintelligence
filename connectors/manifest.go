package connectors

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// A Manifest describes an integration as a list of typed, configured sources.
// The platform stays domain-neutral: it knows source TYPES (mysql, mongo, redis,
// s3, kafka, csv, postgres-cdc, crm) but nothing about any specific business —
// the manifest (an example/customer artifact) supplies the specifics. Config
// values may reference env vars as ${VAR} so secrets never live in the manifest.
type SourceSpec struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	// PrimaryKey names the source fields that identify a row. It belongs to the
	// source, not to any one source TYPE: a CSV export, an ERP's OData service
	// and a Mongo collection all have a natural key that the transport does not
	// carry. Without one the landed table cannot be modelled at all.
	PrimaryKey []string          `yaml:"primary_key"`
	Config     map[string]string `yaml:"config"`
}

type Manifest struct {
	Sources []SourceSpec `yaml:"sources"`
}

// LoadManifest reads and env-expands a sources manifest.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for i := range m.Sources {
		for k, v := range m.Sources[i].Config {
			m.Sources[i].Config[k] = os.ExpandEnv(v)
		}
	}
	return &m, nil
}

// Names lists the configured source names.
func (m *Manifest) Names() []string {
	out := make([]string, len(m.Sources))
	for i, s := range m.Sources {
		out[i] = s.Name
	}
	return out
}

// Spec returns the named source spec.
func (m *Manifest) Spec(name string) *SourceSpec {
	for i := range m.Sources {
		if m.Sources[i].Name == name {
			return &m.Sources[i]
		}
	}
	return nil
}

// Build instantiates a generic Source from a spec by its type. Adding a domain
// never touches this — only adding a new source TYPE does.
func Build(spec SourceSpec) (Source, error) {
	c := spec.Config
	switch strings.ToLower(spec.Type) {
	case "mysql":
		return &MySQLSource{DSN: c["dsn"], Query: c["query"], Name: spec.Name}, nil
	case "mssql", "sqlserver":
		return &MSSQLSource{DSN: c["dsn"], Query: c["query"], Name: spec.Name}, nil
	case "mongo", "mongodb":
		return &MongoSource{URI: c["uri"], Database: c["database"], Collection: c["collection"], Limit: atoiDefault(c["limit"], 1000)}, nil
	case "redis":
		return &RedisSource{Addr: c["addr"], Password: c["password"], Match: c["match"], Name: spec.Name}, nil
	case "s3", "minio":
		return &S3Source{Endpoint: c["endpoint"], AccessKey: c["access_key"], SecretKey: c["secret_key"], Bucket: c["bucket"], Prefix: c["prefix"], UseSSL: c["use_ssl"] == "true", Name: spec.Name}, nil
	case "kafka", "redpanda":
		return &KafkaSource{Brokers: c["brokers"], Topic: c["topic"], Max: atoiDefault(c["max"], 100)}, nil
	case "http", "rest", "odata", "openapi":
		return &HTTPSource{
			Name: spec.Name, URL: c["url"], Method: strings.ToUpper(c["method"]),
			Header: prefixed(c, "header."),

			Auth: c["auth"], Username: c["username"], Password: c["password"],
			Token: c["token"], HeaderName: c["header_name"],
			TokenURL: c["token_url"], ClientID: c["client_id"],
			ClientSecret: c["client_secret"], Scope: c["scope"],

			RecordsPath: c["records_path"],
			Page:        c["page"],
			PageSize:    atoiDefault(c["page_size"], defaultPageSize),
			PageParam:   c["page_param"], SizeParam: c["size_param"],
			NextPath: c["next_path"], NextParam: c["next_param"],
			MaxPages:   atoiDefault(c["max_pages"], defaultMaxPages),
			MaxRecords: atoiDefault(c["max_records"], 0),

			CursorField: c["cursor_field"],
			RPS:         atofDefault(c["rps"], defaultRPS),
		}, nil
	case "csv":
		return &CSVSource{Path: c["path"]}, nil
	case "xlsx", "excel", "xls":
		return &XLSXSource{
			Path:      c["path"],
			Sheet:     c["sheet"],
			HeaderRow: atoiDefault(c["header_row"], 1),
			Name:      spec.Name,
		}, nil
	default:
		return nil, fmt.Errorf("unknown source type %q", spec.Type)
	}
}

// BuildByName builds the named source from a manifest.
func (m *Manifest) BuildByName(name string) (Source, error) {
	s := m.Spec(name)
	if s == nil {
		return nil, fmt.Errorf("source %q not in manifest", name)
	}
	return Build(*s)
}

// prefixed pulls the config keys under a prefix into their own map, so a
// manifest can carry arbitrary headers (SAP needs `sap-client`, most Chinese
// OpenAPIs need a tenant id) without the platform knowing any of their names.
func prefixed(c map[string]string, prefix string) map[string]string {
	var out map[string]string
	for k, v := range c {
		if strings.HasPrefix(k, prefix) {
			if out == nil {
				out = map[string]string{}
			}
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

func atofDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil || f <= 0 {
		return def
	}
	return f
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n == 0 {
		return def
	}
	return n
}

var _ = context.Background

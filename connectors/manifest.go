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
	Name   string            `yaml:"name"`
	Type   string            `yaml:"type"`
	Config map[string]string `yaml:"config"`
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

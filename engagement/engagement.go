// Package engagement is the unit of work for a forward-deployed engineer.
//
// The rest of this codebase is organised around a database. An engineer
// standing up a customer is organised around a customer: several databases, one
// semantic model per modelled database, a reconciliation set that proves each
// metric, a labelled question set, a report to hand over — and a record of what
// had to be worked around to make the core fit.
//
// Without something binding those together, forty subcommands each take their
// own flags and nobody can reproduce a delivery from what is checked in. This
// file is that binding.
package engagement

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Database is one of the customer's databases.
//
// Model empty means unmodelled: direct read-only SQL only, no metrics. That is
// the honest state of a database in week one, and the engagement file should be
// able to say so rather than pretending everything is modelled from day one.
type Database struct {
	ID    string `yaml:"id"`
	DSN   string `yaml:"dsn"`
	Model string `yaml:"model"` // semantic model YAML; empty → unmodelled
	Recon string `yaml:"recon"` // reconciliation set; defaults to <model>.recon.yaml

	// AllowRawSQL opens direct SQL on a modelled database. Off by default.
	AllowRawSQL bool `yaml:"allow_raw_sql"`
}

// Deliver names the artefacts handed over.
type Deliver struct {
	Report string   `yaml:"report"` // where `di report` writes
	Roles  []string `yaml:"roles"`  // the roles the customer actually has
}

// DeltaItem is one thing this engagement needed that the core did not have.
//
// This is the difference between a forward-deployed engineer and a consultant:
// a consultant finishes and leaves; an engineer makes the next engagement
// cheaper by feeding the gap back. Nothing feeds back that nobody wrote down,
// so the file carries the gaps alongside the work.
//
// Kinds are open on purpose — this is a note to a human, not a schema to
// validate. The useful ones so far: "missing-feature", "transform", "manual",
// "workaround".
type DeltaItem struct {
	Kind       string `yaml:"kind"`
	What       string `yaml:"what"`                 // what was needed, in plain words
	Workaround string `yaml:"workaround,omitempty"` // file or command that papers over it
	Cost       string `yaml:"cost,omitempty"`       // roughly what it cost, if worth saying
}

// Engagement is one customer delivery.
type Engagement struct {
	Customer  string      `yaml:"customer"`
	Started   string      `yaml:"started"`
	Owner     string      `yaml:"owner"`
	Databases []Database  `yaml:"databases"`
	Evalset   string      `yaml:"evalset"`
	Deliver   Deliver     `yaml:"deliver"`
	Delta     []DeltaItem `yaml:"delta"`

	dir string // directory of the file, for resolving relative paths
}

// Load reads an engagement file, expands ${ENV} so credentials stay out of it,
// and resolves every path relative to the file itself.
//
// Relative-to-the-file matters: an engineer runs these commands from wherever
// they happen to be, and a model path that only works from one directory turns
// a reproducible delivery into a personal one.
func Load(path string) (*Engagement, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Report missing variables by name, before expansion. os.ExpandEnv turns an
	// unset ${ERP_DSN} into an empty string, and an empty DSN surfaces as
	// "database erp needs a dsn" — true, unhelpful, and pointing at the file
	// rather than at the environment that is actually wrong.
	if missing := unsetVars(string(raw)); len(missing) > 0 {
		return nil, fmt.Errorf("%s: these variables are not set: %s", path, strings.Join(missing, ", "))
	}
	var e Engagement
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &e); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	e.dir = filepath.Dir(abs)

	if e.Customer == "" {
		return nil, fmt.Errorf("%s: customer is required — it names the delivery", path)
	}
	if len(e.Databases) == 0 {
		return nil, fmt.Errorf("%s: at least one database is required", path)
	}
	seen := map[string]bool{}
	for i := range e.Databases {
		d := &e.Databases[i]
		if d.ID == "" {
			return nil, fmt.Errorf("%s: database %d needs an id", path, i+1)
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("%s: duplicate database id %q", path, d.ID)
		}
		seen[d.ID] = true
		if d.DSN == "" {
			return nil, fmt.Errorf("%s: database %q needs a dsn", path, d.ID)
		}
		d.Model = e.resolve(d.Model)
		d.Recon = e.resolve(d.Recon)
		if d.Model != "" && d.Recon == "" {
			d.Recon = reconFor(d.Model)
		}
	}
	e.Evalset = e.resolve(e.Evalset)
	e.Deliver.Report = e.resolve(e.Deliver.Report)
	return &e, nil
}

// Dir is the engagement file's directory; artefacts are written relative to it.
func (e *Engagement) Dir() string { return e.dir }

// Resolve makes a path relative to the engagement file.
func (e *Engagement) Resolve(p string) string { return e.resolve(p) }

func (e *Engagement) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.dir, p)
}

// Database returns one database by id ("" → the first, which is the default).
func (e *Engagement) Database(id string) (*Database, error) {
	if id == "" {
		return &e.Databases[0], nil
	}
	for i := range e.Databases {
		if e.Databases[i].ID == id {
			return &e.Databases[i], nil
		}
	}
	var ids []string
	for i := range e.Databases {
		ids = append(ids, e.Databases[i].ID)
	}
	return nil, fmt.Errorf("unknown database %q in engagement %q — declared: %s",
		id, e.Customer, strings.Join(ids, ", "))
}

// Modelled reports how far the engagement has got: a delivery is finished when
// every database a customer asks questions of has a model behind it.
func (e *Engagement) Modelled() (modelled, total int) {
	for i := range e.Databases {
		if e.Databases[i].Model != "" {
			modelled++
		}
	}
	return modelled, len(e.Databases)
}

// varRef matches ${NAME} and $NAME, the two forms os.ExpandEnv understands.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// unsetVars lists the environment variables the file references and the
// environment does not define, in order, without duplicates.
func unsetVars(text string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, m := range varRef.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := os.LookupEnv(name); !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

func reconFor(modelPath string) string {
	ext := filepath.Ext(modelPath)
	return strings.TrimSuffix(modelPath, ext) + ".recon" + ext
}

// Find locates an engagement file: the given path, else ./engagement.yaml, else
// the nearest one in a parent directory. The walk upward is why a command run
// from a subdirectory of the engagement still knows which customer it is in.
func Find(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, "engagement.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no engagement.yaml here or in any parent directory — pass -engagement, or create one")
		}
		dir = parent
	}
}

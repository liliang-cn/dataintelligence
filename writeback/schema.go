// Package writeback is the production write-path: the AI agent turns a natural
// language request into a TYPED, validated change proposal (never raw SQL),
// confined to a declared allowlist of writable tables/columns/operations. A
// proposal is dry-run (before-image + affected-row count), persisted, and only
// applied after approval by a different principal — then it is atomic, audited,
// and reversible from its before-image. This is the "write-back danger zone"
// done safely: typed params, mandatory predicates, row caps, red-lines,
// separation of duties, and rollback.
package writeback

import (
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Column is a writable column with an optional type and value allowlist.
type Column struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"` // text | int | numeric | date
	Required bool     `yaml:"required"`
	Enum     []string `yaml:"enum,omitempty"` // if set, value must be one of these
}

// WritableTable declares what may change on one table.
type WritableTable struct {
	Name             string   `yaml:"name"`
	PrimaryKey       string   `yaml:"primary_key"`
	AllowInsert      bool     `yaml:"allow_insert"`
	AllowUpdate      bool     `yaml:"allow_update"`
	AllowDelete      bool     `yaml:"allow_delete"`
	RequirePredicate bool     `yaml:"require_predicate"`
	MaxAffected      int      `yaml:"max_affected"`
	Columns          []Column `yaml:"columns"`
}

func (t *WritableTable) column(name string) *Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

func (t *WritableTable) allows(op Op) bool {
	switch op {
	case OpInsert:
		return t.AllowInsert
	case OpUpdate:
		return t.AllowUpdate
	case OpDelete:
		return t.AllowDelete
	}
	return false
}

// Governance lists which roles may propose vs approve. Proposer ≠ approver is
// always enforced on top of these.
type Governance struct {
	ProposerRoles []string `yaml:"proposer_roles"`
	ApproverRoles []string `yaml:"approver_roles"`
}

// Schema is the full write-back contract.
type Schema struct {
	Tables     []WritableTable `yaml:"tables"`
	Governance Governance      `yaml:"governance"`
}

// LoadSchema reads the write-back allowlist YAML.
func LoadSchema(path string) (*Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Schema
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	for i := range s.Tables {
		if s.Tables[i].MaxAffected == 0 {
			s.Tables[i].MaxAffected = 100 // a cap is always present
		}
	}
	return &s, nil
}

// Table returns the writable table by name (nil if not on the allowlist).
func (s *Schema) Table(name string) *WritableTable {
	for i := range s.Tables {
		if s.Tables[i].Name == name {
			return &s.Tables[i]
		}
	}
	return nil
}

// CanPropose / CanApprove check the role lists.
func (s *Schema) CanPropose(role string) bool { return roleIn(role, s.Governance.ProposerRoles) }
func (s *Schema) CanApprove(role string) bool { return roleIn(role, s.Governance.ApproverRoles) }

func roleIn(role string, roles []string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// redLine blocks destructive / schema-altering SQL even if it somehow gets built
// — defense in depth under the typed, parameterized builder.
var redLine = regexp.MustCompile(`(?i)\b(drop|truncate|alter|grant|revoke|create|delete\s+from\s+\w+\s*;?\s*$|;\s*\w)`)

func redLineViolation(sql string) string {
	if redLine.MatchString(sql) {
		return "red-line: statement contains a destructive/DDL/stacked pattern"
	}
	if strings.Count(sql, ";") > 0 {
		return "red-line: multiple statements are not allowed"
	}
	return ""
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func safeIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

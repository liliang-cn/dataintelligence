package writeback

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Op is a data-change operation.
type Op string

const (
	OpInsert Op = "insert"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
)

// Status is the proposal lifecycle.
type Status string

const (
	StatusPending    Status = "pending"
	StatusRejected   Status = "rejected"
	StatusCommitted  Status = "committed"
	StatusRolledBack Status = "rolled_back"
)

// Predicate is one WHERE clause term (parameterized at build time).
type Predicate struct {
	Column   string `json:"column"`
	Operator string `json:"operator"` // = != > >= < <= in
	Value    any    `json:"value"`
}

// DataChange is a typed CRUD operation — never raw SQL.
type DataChange struct {
	Op    Op             `json:"op"`
	Table string         `json:"table"`
	Set   map[string]any `json:"set,omitempty"`   // insert/update column→value
	Where []Predicate    `json:"where,omitempty"` // update/delete predicate
}

// ModelChange is a transform proposal: a new/updated metric or dimension.
type ModelChange struct {
	Kind string `json:"kind"` // metric | dimension
	Name string `json:"name"`
	YAML string `json:"yaml"` // the fragment to merge into the model
}

// Preview is the dry-run result shown before approval.
type Preview struct {
	SQL          string           `json:"sql"`
	Args         []any            `json:"args"`
	AffectedRows int              `json:"affected_rows"`
	Before       []map[string]any `json:"before,omitempty"` // rows as they are now (for update/delete)
	Note         string           `json:"note,omitempty"`
}

// Proposal is one reviewable change request through its whole lifecycle.
type Proposal struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"` // data | model
	Question  string       `json:"question"`
	Rationale string       `json:"rationale,omitempty"`
	Data      *DataChange  `json:"data,omitempty"`
	Model     *ModelChange `json:"model,omitempty"`

	Status   Status   `json:"status"`
	Proposer string   `json:"proposer"`
	Approver string   `json:"approver,omitempty"`
	Preview  *Preview `json:"preview,omitempty"`

	CreatedNs   int64  `json:"created_ns"`
	DecidedNs   int64  `json:"decided_ns,omitempty"`
	CommittedNs int64  `json:"committed_ns,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Validate checks a data change against the write-back allowlist: writable table,
// allowed op, writable columns, value enums, a predicate when required, and that
// inserts set every required column. It returns a clear, user-facing error.
func (d *DataChange) Validate(s *Schema) error {
	if d == nil {
		return fmt.Errorf("no data change")
	}
	t := s.Table(d.Table)
	if t == nil {
		return fmt.Errorf("table %q is not writable (not on the allowlist)", d.Table)
	}
	if !t.allows(d.Op) {
		return fmt.Errorf("operation %q is not allowed on %q", d.Op, d.Table)
	}
	switch d.Op {
	case OpInsert:
		if len(d.Set) == 0 {
			return fmt.Errorf("insert needs column values")
		}
		for _, c := range t.Columns {
			if c.Required {
				if _, ok := d.Set[c.Name]; !ok {
					return fmt.Errorf("insert missing required column %q", c.Name)
				}
			}
		}
	case OpUpdate:
		if len(d.Set) == 0 {
			return fmt.Errorf("update needs column values to set")
		}
	case OpDelete:
		// nothing to set
	default:
		return fmt.Errorf("unknown op %q", d.Op)
	}
	// Every column written must be on the allowlist with a valid value.
	for col, val := range d.Set {
		c := t.column(col)
		if c == nil {
			return fmt.Errorf("column %q is not writable on %q", col, d.Table)
		}
		if err := checkValue(c, val); err != nil {
			return err
		}
	}
	// Predicate rules: required for update/delete on guarded tables; columns must
	// exist (PK is always a valid predicate target).
	if (d.Op == OpUpdate || d.Op == OpDelete) && t.RequirePredicate && len(d.Where) == 0 {
		return fmt.Errorf("%s on %q requires a WHERE predicate (refusing an unbounded mutation)", d.Op, d.Table)
	}
	for _, p := range d.Where {
		if !safeIdent(p.Column) {
			return fmt.Errorf("invalid predicate column %q", p.Column)
		}
		if !validOperator(p.Operator) {
			return fmt.Errorf("invalid predicate operator %q", p.Operator)
		}
	}
	return nil
}

func checkValue(c *Column, val any) error {
	s := fmt.Sprintf("%v", val)
	switch c.Type {
	case "int":
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return fmt.Errorf("column %q expects int, got %q", c.Name, s)
		}
	case "numeric":
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return fmt.Errorf("column %q expects numeric, got %q", c.Name, s)
		}
	case "date":
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return fmt.Errorf("column %q expects date YYYY-MM-DD, got %q", c.Name, s)
		}
	}
	if len(c.Enum) > 0 {
		for _, e := range c.Enum {
			if e == s {
				return nil
			}
		}
		return fmt.Errorf("column %q must be one of %v, got %q", c.Name, c.Enum, s)
	}
	return nil
}

func validOperator(op string) bool {
	switch strings.ToLower(op) {
	case "=", "!=", ">", ">=", "<", "<=", "in":
		return true
	}
	return false
}

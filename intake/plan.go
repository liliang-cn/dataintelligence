package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liliang-cn/dataintelligence/modelgen"
)

// treatmentsFor is the classifier's first pass over a catalogue, in a stable
// order: tables by name, columns in the order the table declares them — the
// order a person reading the table reads it in.
func treatmentsFor(schema *modelgen.Schema) []Treatment {
	tables := append([]modelgen.Table(nil), schema.Tables...)
	sort.SliceStable(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	var out []Treatment
	for _, t := range tables {
		for _, c := range t.Columns {
			tr := Treatment{Table: t.Name, Column: c.Name, Type: c.Type, Action: Keep, By: "rule"}
			if modelgen.PIIColumn(c.Name) {
				tr.Personal, tr.Action = true, Mask
				tr.Reason = "the name reads as contact or identity data"
			}
			out = append(out, tr)
		}
	}
	return out
}

// canonicalPlan is what the hash is taken over: which database, and what was
// decided about each of its columns — and nothing else.
//
// A struct marshalled by encoding/json rather than a map or a hand-rolled
// string, livedb's reasoning: the field order is written in this file and
// cannot drift with somebody else's implementation note.
//
// Reason and By are outside it. Who said so is provenance; what was decided is
// the decision. A signature that went stale because somebody reworded a reason
// would teach people to re-sign without reading.
//
// Columns are sorted by (table, column) rather than taken in declared order.
// A column moved within its table is the same decision, and a hash that moved
// with it would ask for a signature nobody owes.
type canonicalPlan struct {
	Source  string               `json:"source"`
	Driver  string               `json:"driver"`
	Columns []canonicalTreatment `json:"columns"`
}

type canonicalTreatment struct {
	Table    string `json:"table"`
	Column   string `json:"column"`
	Type     string `json:"type"`
	Personal bool   `json:"personal"`
	Action   Action `json:"action"`
}

// planHash is what a signature names, and what makes "I signed this"
// checkable.
func planHash(p Plan) string {
	c := canonicalPlan{Source: p.Source, Driver: p.Driver,
		Columns: make([]canonicalTreatment, 0, len(p.Columns))}
	for _, t := range p.Columns {
		c.Columns = append(c.Columns, canonicalTreatment{
			Table: t.Table, Column: t.Column, Type: t.Type, Personal: t.Personal, Action: t.Action,
		})
	}
	sort.Slice(c.Columns, func(i, j int) bool {
		if c.Columns[i].Table != c.Columns[j].Table {
			return c.Columns[i].Table < c.Columns[j].Table
		}
		return c.Columns[i].Column < c.Columns[j].Column
	})
	body, err := json.Marshal(c)
	if err != nil {
		// Strings and bools; Marshal cannot fail on it. A panic is better
		// than a hash nobody can tell from a real one.
		panic(fmt.Sprintf("intake: canonical plan will not marshal: %v", err))
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// amend applies changes to a plan's treatments in memory. A change naming a
// table or column the plan does not cover is an error rather than a no-op:
// the caller believed they were tightening something, and a request that
// quietly did nothing would then be signed as though it had.
func amend(p *Plan, changes []Change, by string) error {
	for _, ch := range changes {
		if !ch.Action.valid() {
			return fmt.Errorf("intake: %q is not a treatment — keep, mask or redact", ch.Action)
		}
		hit := false
		for i := range p.Columns {
			t := &p.Columns[i]
			if t.Table != ch.Table || (ch.Column != "" && t.Column != ch.Column) {
				continue
			}
			hit = true
			if t.Action == ch.Action {
				continue
			}
			t.Action, t.By = ch.Action, by
			t.Reason = ch.Reason
		}
		if !hit {
			what := ch.Table
			if ch.Column != "" {
				what += "." + ch.Column
			}
			return fmt.Errorf("intake: plan %s does not cover %s", p.ID, what)
		}
	}
	p.Hash = planHash(*p)
	return nil
}

// drift compares a signed plan against the catalogue as it is now.
func drift(p Plan, schema *modelgen.Schema) (unclassified, gone, retyped []string) {
	live := map[string]string{}
	for _, t := range schema.Tables {
		for _, c := range t.Columns {
			name := t.Name + "." + c.Name
			live[name] = c.Type
			if tr, ok := p.Treatment(t.Name, c.Name); !ok {
				unclassified = append(unclassified, name)
			} else if tr.Type != c.Type {
				retyped = append(retyped, fmt.Sprintf("%s %s→%s", name, tr.Type, c.Type))
			}
		}
	}
	for _, t := range p.Columns {
		if _, ok := live[t.Table+"."+t.Column]; !ok {
			gone = append(gone, t.Table+"."+t.Column)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(gone)
	sort.Strings(retyped)
	return unclassified, gone, retyped
}

// Package nodes is the field-level rule engine (whiteboard "Data Node"): when the
// same entity arrives from multiple sources, field rules decide the surviving
// value (conflict resolution, source-based or value-based), enforce required
// fields, and raise field-level alerts. It is pure (no DB) and deterministic.
package nodes

import (
	"fmt"
	"strconv"
	"strings"
)

// Rule kinds.
const (
	KindConflict = "conflict" // resolve when sources disagree
	KindRequire  = "require"  // field must be non-empty in the result
	KindUpdate   = "update"   // update policy when there is no conflict
	KindAlert    = "alert"    // raise an event when a condition holds
)

// Rule is one field-level rule.
type Rule struct {
	Field    string
	Kind     string
	Strategy string         // see resolve(); for conflict/update
	Params   map[string]any // e.g. {"min":0,"max":100000} for an alert range
}

// Source is one record with provenance (name + sortable time for "latest").
type Source struct {
	Name string
	Time string // RFC3339 (lexicographic == chronological); optional
	Rec  map[string]string
}

// Event is a field-level finding (audit / alert / reporting).
type Event struct {
	Field    string `json:"field"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // info | warning | error
	Message  string `json:"message"`
}

// Node applies a set of field rules.
type Node struct {
	Rules    []Rule
	Priority []string // source name priority (highest first) for source-based conflict
}

// Merge folds incoming into existing per the field rules, returning the resolved
// record and the events raised.
func (n *Node) Merge(existing, incoming Source) (map[string]string, []Event) {
	result := map[string]string{}
	for k, v := range existing.Rec {
		result[k] = v
	}
	var events []Event

	for _, f := range unionKeys(existing.Rec, incoming.Rec) {
		rule := n.ruleFor(f, KindConflict)
		if rule == nil {
			rule = n.ruleFor(f, KindUpdate)
		}
		val, ev := n.resolve(f, existing, incoming, rule)
		result[f] = val
		events = append(events, ev...)
	}

	// require checks
	for _, r := range n.Rules {
		if r.Kind == KindRequire && strings.TrimSpace(result[r.Field]) == "" {
			events = append(events, Event{r.Field, KindRequire, "error", "required field is empty"})
		}
	}
	// alert checks
	for _, r := range n.Rules {
		if r.Kind == KindAlert {
			if ev, ok := n.alert(r, existing, incoming, result); ok {
				events = append(events, ev)
			}
		}
	}
	return result, events
}

func (n *Node) resolve(field string, existing, incoming Source, rule *Rule) (string, []Event) {
	e, i := existing.Rec[field], incoming.Rec[field]

	// no real conflict
	if e == "" {
		return i, nil
	}
	if i == "" || e == i {
		return e, nil
	}

	strat := ""
	if rule != nil {
		strat = rule.Strategy
	}
	mk := func(sev, msg string) []Event { return []Event{{field, KindConflict, sev, msg}} }

	switch strat {
	case "prefer_incoming":
		return i, mk("info", "conflict → took incoming")
	case "keep_existing":
		return e, mk("info", "conflict → kept existing")
	case "latest":
		if incoming.Time >= existing.Time {
			return i, mk("info", fmt.Sprintf("conflict → latest (incoming %s)", incoming.Time))
		}
		return e, mk("info", fmt.Sprintf("conflict → latest (existing %s)", existing.Time))
	case "max", "min":
		ef, eerr := strconv.ParseFloat(e, 64)
		iff, ierr := strconv.ParseFloat(i, 64)
		if eerr == nil && ierr == nil {
			if (strat == "max" && iff > ef) || (strat == "min" && iff < ef) {
				return i, mk("info", "conflict → "+strat)
			}
			return e, mk("info", "conflict → "+strat)
		}
		return e, mk("warning", "conflict → "+strat+" failed (non-numeric), kept existing")
	case "longest":
		if len(i) > len(e) {
			return i, mk("info", "conflict → longest")
		}
		return e, mk("info", "conflict → longest")
	case "source_priority":
		if n.rank(incoming.Name) < n.rank(existing.Name) {
			return i, mk("info", "conflict → source priority ("+incoming.Name+")")
		}
		return e, mk("info", "conflict → source priority ("+existing.Name+")")
	default:
		return e, mk("warning", "unresolved conflict (kept existing); add a conflict rule")
	}
}

func (n *Node) alert(r Rule, existing, incoming Source, result map[string]string) (Event, bool) {
	switch r.Strategy {
	case "changed":
		if existing.Rec[r.Field] != "" && incoming.Rec[r.Field] != "" && existing.Rec[r.Field] != incoming.Rec[r.Field] {
			return Event{r.Field, KindAlert, "warning",
				fmt.Sprintf("value changed %q → %q", existing.Rec[r.Field], incoming.Rec[r.Field])}, true
		}
	case "range":
		v, err := strconv.ParseFloat(result[r.Field], 64)
		if err != nil {
			return Event{}, false
		}
		if lo, ok := toFloat(r.Params["min"]); ok && v < lo {
			return Event{r.Field, KindAlert, "warning", fmt.Sprintf("%g below min %g", v, lo)}, true
		}
		if hi, ok := toFloat(r.Params["max"]); ok && v > hi {
			return Event{r.Field, KindAlert, "warning", fmt.Sprintf("%g above max %g", v, hi)}, true
		}
	}
	return Event{}, false
}

func (n *Node) ruleFor(field, kind string) *Rule {
	for i := range n.Rules {
		if n.Rules[i].Field == field && n.Rules[i].Kind == kind {
			return &n.Rules[i]
		}
	}
	return nil
}

func (n *Node) rank(source string) int {
	for i, s := range n.Priority {
		if s == source {
			return i
		}
	}
	return len(n.Priority) + 1 // unknown sources rank lowest
}

func unionKeys(a, b map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]string{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

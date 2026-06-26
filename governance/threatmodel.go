package governance

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Threat model as code. The security posture is not a wiki page that rots — it
// is a VCS-tracked file the CI gate enforces: every threat must name a control,
// an owner, and a status, and no threat may sit unaddressed. A new attack path
// is a failing build until someone owns it.

// Statuses a threat may carry. A threat is "addressed" only when mitigated or
// explicitly, deliberately accepted.
const (
	ThreatMitigated = "mitigated" // a control is in place
	ThreatAccepted  = "accepted"  // residual risk consciously signed off
	ThreatOpen      = "open"      // known, not yet handled — fails the gate
)

// Threat is one entry in the model.
type Threat struct {
	ID       string `yaml:"id"`
	Title    string `yaml:"title"`
	Vector   string `yaml:"vector"`   // how the attack happens
	Control  string `yaml:"control"`  // the control that addresses it
	Owner    string `yaml:"owner"`    // who is accountable
	Status   string `yaml:"status"`   // mitigated | accepted | open
	Evidence string `yaml:"evidence"` // where to verify the control (test, code, run)
}

// ThreatModel is the loaded set.
type ThreatModel struct {
	Threats []Threat `yaml:"threats"`
}

// LoadThreatModel reads a threat model YAML.
func LoadThreatModel(path string) (*ThreatModel, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tm ThreatModel
	if err := yaml.Unmarshal(b, &tm); err != nil {
		return nil, err
	}
	return &tm, nil
}

// Check validates the model and returns one issue string per problem. A clean
// model (every threat has id/title/vector/control/owner/evidence and a status
// of mitigated or accepted) returns nil — the CI gate passes only then.
func (tm *ThreatModel) Check() []string {
	var issues []string
	seen := map[string]bool{}
	for i, t := range tm.Threats {
		where := t.ID
		if where == "" {
			where = fmt.Sprintf("#%d", i+1)
		}
		req := map[string]string{"title": t.Title, "vector": t.Vector, "control": t.Control, "owner": t.Owner, "evidence": t.Evidence}
		// stable order for deterministic output
		keys := make([]string, 0, len(req))
		for k := range req {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if t.ID == "" {
			issues = append(issues, fmt.Sprintf("%s: missing id", where))
		} else if seen[t.ID] {
			issues = append(issues, fmt.Sprintf("%s: duplicate id", t.ID))
		}
		seen[t.ID] = true
		for _, k := range keys {
			if req[k] == "" {
				issues = append(issues, fmt.Sprintf("%s: missing %s", where, k))
			}
		}
		switch t.Status {
		case ThreatMitigated, ThreatAccepted:
		case ThreatOpen, "":
			issues = append(issues, fmt.Sprintf("%s: UNADDRESSED (status=%q) — assign a control or accept the risk", where, t.Status))
		default:
			issues = append(issues, fmt.Sprintf("%s: bad status %q (mitigated|accepted|open)", where, t.Status))
		}
	}
	return issues
}

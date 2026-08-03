package handover

import (
	"fmt"
	"io"
	"strings"

	"github.com/liliang-cn/dataintelligence/engagement"
)

// Runbook is what the customer's team is left holding.
//
// It is written for the person who inherits this and was not in any of the
// meetings: what the pieces are, what to run when someone doubts a number, what
// breaks and where to look, and which decisions are theirs rather than the
// tool's. Everything in it is generated from the engagement, so it cannot drift
// away from what was actually delivered the way a hand-written document does.
type Runbook struct {
	E        *engagement.Engagement
	CoreAddr string // where the service runs, for the commands to be copy-pasteable
}

// WriteMarkdown renders the runbook.
func (r *Runbook) WriteMarkdown(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	e := r.E
	modelled, total := e.Modelled()

	p("# Runbook — %s", e.Customer)
	p("")
	p("Written for whoever inherits this and was not in the meetings.")
	if e.Owner != "" {
		p("")
		p("Delivered by %s%s.", e.Owner, started(e.Started))
	}

	p("")
	p("## What you have")
	p("")
	p("%d database(s), %d of them modelled.", total, modelled)
	p("")
	p("| Database | Mode | What that means |")
	p("|---|---|---|")
	for i := range e.Databases {
		d := &e.Databases[i]
		if d.Model != "" {
			p("| `%s` | governed | questions are answered from declared metrics; raw SQL is refused |", d.ID)
		} else {
			p("| `%s` | unmodelled | direct read-only SQL; there are no metrics yet |", d.ID)
		}
	}
	p("")
	p("A **governed** database answers only in terms of metrics somebody defined and")
	p("checked. That is the point of it: the same question gives the same number to")
	p("everyone, and \"revenue\" cannot quietly mean three things. An **unmodelled**")
	p("one is still being explored — useful, but nothing vouches for the answers.")

	p("")
	p("## The three commands")
	p("")
	p("```bash")
	p("di drift      # has anything changed underneath us?   ← run this on a schedule")
	p("di eval       # does every metric still match its control query?")
	p("di report     # regenerate the delivery document")
	p("```")
	p("")
	p("Run them from the directory holding `engagement.yaml`. None of them takes")
	p("arguments: the engagement file is the configuration, so what a colleague runs")
	p("is what you ran.")

	p("")
	p("## When someone says a number looks wrong")
	p("")
	p("1. `di eval` — if a metric stopped matching its control query, that is the answer.")
	p("2. `di drift` — a column renamed, or a feed that stopped, will show here and")
	p("   nowhere else. Both produce clean-looking wrong answers rather than errors.")
	p("3. Ask which number they expected and where it comes from. If it is a figure")
	p("   your business publishes, add it as a control query with `source:` naming it")
	p("   — see below. That converts an argument into a check that runs forever.")

	p("")
	p("## Adding or changing a metric")
	p("")
	p("A metric lives in the semantic model; the check that it is right lives beside it.")
	p("")
	for i := range e.Databases {
		d := &e.Databases[i]
		if d.Model == "" {
			continue
		}
		p("- `%s` → model `%s`, checks `%s`", d.ID, rel(e, d.Model), rel(e, d.Recon))
	}
	p("")
	p("```yaml")
	p("# in the .recon.yaml, beside the model")
	p("cases:")
	p("  - metric: net_revenue")
	p("    control: SELECT sum(amount) - sum(refunds) FROM …")
	p("    source: customer-report      # WHERE the expected number comes from")
	p("    note: the Q2 board pack, page 3")
	p("```")
	p("")
	p("`source` is the part people skip and the part that matters. A control query")
	p("written by whoever wrote the metric proves only that the model agrees with")
	p("itself. A control anchored to a number your business already publishes proves")
	p("it is right. `di report` counts the two separately and says so.")

	p("")
	p("## Run the checks automatically")
	p("")
	p("`di drift` exits non-zero when something needs attention, so it works as a")
	p("scheduled job or a CI step. A gate nobody runs is a document; see")
	p("`.github/workflows/di-gate.yml`, generated beside this file.")

	p("")
	p("## What is yours to decide, not the tool's")
	p("")
	p("- **What a metric means.** The tool guarantees the same definition is applied")
	p("  everywhere. It cannot tell you whether revenue should be net of refunds.")
	p("- **Who may see what.** Roles gate metrics%s. Adding a person to a", rolesNote(e.Deliver.Roles))
	p("  role is a business decision.")
	p("- **Whether a feed is allowed to stop.** `di drift` reports one that has. Only")
	p("  you know whether that is a broken pipeline or a shop that closed.")

	if len(e.Delta) > 0 {
		p("")
		p("## Known rough edges")
		p("")
		p("Things this delivery needed that the platform does not do natively. They")
		p("work, and they are the parts most likely to surprise you:")
		p("")
		for _, d := range e.Delta {
			p("- **%s** — %s", orUnknown(d.Kind), d.What)
			if d.Workaround != "" {
				p("  Handled by `%s`.", d.Workaround)
			}
		}
	}

	p("")
	p("## Where things are")
	p("")
	p("| | |")
	p("|---|---|")
	p("| Engagement | `%s/engagement.yaml` |", e.Dir())
	if r.CoreAddr != "" {
		p("| Service | %s |", r.CoreAddr)
	}
	if e.Deliver.Report != "" {
		p("| Delivery report | `%s` |", rel(e, e.Deliver.Report))
	}
	if e.Evalset != "" {
		p("| Labelled questions | `%s` |", rel(e, e.Evalset))
	}
}

// WorkflowYAML is a CI job that fails the build when the model no longer
// matches the warehouse. A gate that has to be remembered is not a gate.
func (r *Runbook) WorkflowYAML() string {
	return `name: Semantic model gate

# Runs the checks the delivery was signed off with. A metric that stops matching
# its control query, a column that was renamed, or a feed that stopped sending
# all produce clean-looking wrong answers rather than errors — this is what
# turns them into a failed build instead.

on:
  push:
  pull_request:
  schedule:
    - cron: "0 6 * * *"   # daily: drift arrives without a commit

jobs:
  gate:
    runs-on: ubuntu-latest
    env:
      # Warehouse credentials come from repository secrets, never the file.
` + secretsEnv(r.E) + `    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.25" }
      - run: go install github.com/liliang-cn/dataintelligence/cmd/di@latest

      - name: Metrics still match their control queries
        run: di eval

      - name: Nothing changed underneath the model
        run: di drift
`
}

// secretsEnv wires each database's DSN variable to a repository secret of the
// same name, so the workflow is usable without editing it.
func secretsEnv(e *engagement.Engagement) string {
	var b strings.Builder
	seen := map[string]bool{}
	for i := range e.Databases {
		for _, name := range e.Databases[i].Vars {
			if seen[name] {
				continue
			}
			seen[name] = true
			fmt.Fprintf(&b, "      %s: ${{ secrets.%s }}\n", name, name)
		}
	}
	if b.Len() == 0 {
		b.WriteString("      # (no ${VAR} in the engagement's DSNs — add them here)\n")
	}
	return b.String()
}

func started(s string) string {
	if s == "" {
		return ""
	}
	return ", starting " + s
}

func rolesNote(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	return " — this delivery uses " + strings.Join(roles, ", ")
}

func orUnknown(s string) string {
	if s == "" {
		return "note"
	}
	return s
}

func rel(e *engagement.Engagement, p string) string {
	return strings.TrimPrefix(strings.TrimPrefix(p, e.Dir()), "/")
}

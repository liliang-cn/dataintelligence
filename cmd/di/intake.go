package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/intake"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/platform"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/warehouse"
)

// runIntake is the signature that comes before anything reads a customer's
// database.
//
//	di intake propose -by 王工                      # classify every column, read no rows
//	di intake show                                  # the plan and its hash
//	di intake amend -id P -by 王工 customer.phone=redact batch=keep
//	di intake sign  -id P -hash <hash> -by 王工 -note "..."
//	di intake check                                 # has the schema moved since?
//	di intake ledger
//
// `model gen` and `survey` refuse to run against a database with no signed
// plan. That is the point of the command, not a side effect of it: both read
// the customer's data — counts, distinct values, extremes, samples — and both
// used to do it before anybody had agreed that they could.
func runIntake(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di intake <propose|show|amend|sign|check|ledger> [flags]")
		os.Exit(2)
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("intake "+sub, flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "the customer's warehouse")
	id := fs.String("id", "", "plan id (amend, sign; default: the current plan)")
	hash := fs.String("hash", "", "the plan hash you read (sign)")
	by := fs.String("by", "", "who is doing this")
	note := fs.String("note", "", "why")
	_ = fs.Parse(sortFlagsFirst(fs, rest))

	ctx := context.Background()
	store, done := openIntake(ctx)
	defer done()

	// Amend and sign act on the draft somebody is reviewing, and Current is
	// the plan in force — a signed one. Asking Current for the plan to amend
	// found nothing the first time this ran, because the only plan there was
	// the draft. The newest draft is what a reviewer means by "the plan".
	planID := func() string {
		if *id != "" {
			return *id
		}
		p, err := latest(ctx, store, *dsn, intake.Draft)
		if err != nil {
			fail(err)
		}
		return p.ID
	}

	switch sub {
	case "propose":
		wh := openCustomer(ctx, *dsn)
		defer wh.Close()
		p, err := store.Propose(ctx, wh, *dsn, *by, *note)
		if err != nil {
			fail(err)
		}
		printPlan(p)
		fmt.Printf("\nnext: di intake sign -id %s -hash %s -by <who>\n", p.ID, p.Short())
	case "show":
		// A draft under review is what somebody wants to see before signing;
		// the signed plan is what is in force. Show the draft when there is
		// one, and say which it is.
		p, err := latest(ctx, store, *dsn, intake.Draft)
		if err != nil {
			if p, err = store.Current(ctx, *dsn); err != nil {
				fail(err)
			}
		}
		printPlan(p)
	case "amend":
		var changes []intake.Change
		for _, arg := range fs.Args() {
			c, err := parseChange(arg)
			if err != nil {
				fail(err)
			}
			changes = append(changes, c)
		}
		if len(changes) == 0 {
			fail(fmt.Errorf("amend needs at least one table.column=keep|mask|redact"))
		}
		p, err := store.Amend(ctx, planID(), changes, *by)
		if err != nil {
			fail(err)
		}
		printPlan(p)
	case "sign":
		p, err := store.Sign(ctx, planID(), *hash, *by, *note)
		if err != nil {
			fail(err)
		}
		fmt.Printf("plan %s signed by %s (hash %s) — %s may now be read\n", p.ID, p.SignedBy, p.Short(), p.Source)
	case "check":
		wh := openCustomer(ctx, *dsn)
		defer wh.Close()
		r, err := store.Check(ctx, wh, *dsn)
		if err != nil {
			fail(err)
		}
		fmt.Printf("plan %s (%s), signed by %s\n", r.Plan.ID, r.Plan.Short(), orDashStr(r.Plan.SignedBy))
		for _, c := range r.Unclassified {
			fmt.Printf("  NOT CLASSIFIED  %s — nobody decided how this is treated; reads are refused until they do\n", c)
		}
		for _, c := range r.Gone {
			fmt.Printf("  gone            %s\n", c)
		}
		for _, c := range r.Retyped {
			fmt.Printf("  retyped         %s\n", c)
		}
		if len(r.Unclassified)+len(r.Gone)+len(r.Retyped) == 0 {
			fmt.Println("  the live schema is what was signed")
		}
	case "ledger":
		es, err := store.Ledger(ctx, *dsn)
		if err != nil {
			fail(err)
		}
		for _, e := range es {
			line := fmt.Sprintf("%s  %-9s %s by %-10s %s", e.At.Format("2006-01-02 15:04:05"), e.Act, e.Plan, e.By, shortHash(e.ToHash))
			if e.Note != "" {
				line += "  — " + e.Note
			}
			fmt.Println(line)
		}
	default:
		fail(fmt.Errorf("unknown intake subcommand %q", sub))
	}
}

// requireIntake is the gate `model gen` and `survey` call before their first
// read. It returns the signed plan so the caller can honour it — a gate that
// said yes and was then ignored about which columns may be read would be a
// signature on paper only.
func requireIntake(ctx context.Context, wh *warehouse.Warehouse, dsn string) intake.Plan {
	store, done := openIntake(ctx)
	defer done()
	p, err := store.Require(ctx, wh, dsn)
	if err != nil {
		fail(fmt.Errorf("%w\n\nnobody has signed off on reading %s. Propose a plan, review it, sign it:\n"+
			"  di intake propose -dsn <dsn> -by <who>\n  di intake sign -id <plan> -hash <hash> -by <who>",
			err, intake.SourceOf(dsn)))
	}
	return p
}

// withholdRedacted removes what the plan says must not be read from the schema
// before anything is generated from it. A redacted column never reaches the
// draft model, and so never reaches the model that refines it — its name
// included, because a column called `id_card_no` says something on its own.
func withholdRedacted(s *modelgen.Schema, p intake.Plan) (withheld []string) {
	for i := range s.Tables {
		t := &s.Tables[i]
		kept := t.Columns[:0]
		for _, c := range t.Columns {
			if tr, ok := p.Treatment(t.Name, c.Name); ok && tr.Action == intake.Redact {
				withheld = append(withheld, t.Name+"."+c.Name)
				continue
			}
			kept = append(kept, c)
		}
		t.Columns = kept
	}
	return withheld
}

// maskPlanned makes the generated model mask what the plan says to mask.
//
// The generator masks what looks like personal data by name; the plan is what a
// person decided. They usually agree, and where they do not — a column the
// reviewer masked by hand because its name gives nothing away — the person
// wins. The gate is the same one curate applies, admin only.
func maskPlanned(m *semantic.Model, p intake.Plan) {
	table := map[string]string{}
	for _, e := range m.Entities {
		table[e.Name] = e.Table
	}
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		tr, ok := p.Treatment(table[d.Entity], d.Column)
		if !ok || tr.Action != intake.Mask || d.Mask != "" {
			continue
		}
		d.Mask = `'***'`
		if len(d.Roles) == 0 {
			d.Roles = []string{"admin"}
		}
	}
}

func openIntake(ctx context.Context) (*intake.Store, func()) {
	p, err := platform.Open(ctx, platform.Config{BrainPath: defaultBrain(), Embedder: corpusEmbedder()})
	if err != nil {
		fail(err)
	}
	if p.Intake == nil {
		p.Close()
		fail(fmt.Errorf("the intake plans live in the brain, and it could not be opened: %s", strings.Join(p.Missing, "; ")))
	}
	return p.Intake, p.Close
}

func openCustomer(ctx context.Context, dsn string) *warehouse.Warehouse {
	wh, err := warehouse.Open(ctx, dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	return wh
}

func parseChange(arg string) (intake.Change, error) {
	target, action, ok := strings.Cut(arg, "=")
	if !ok {
		return intake.Change{}, fmt.Errorf("%q: want table.column=keep|mask|redact (or table=… for a whole table)", arg)
	}
	table, column, _ := strings.Cut(target, ".")
	return intake.Change{Table: table, Column: column, Action: intake.Action(strings.ToLower(action))}, nil
}

func printPlan(p intake.Plan) {
	signed := strings.ToUpper(string(p.State))
	if p.SignedBy != "" {
		signed = "signed by " + p.SignedBy + " at " + p.SignedAt.Format("2006-01-02 15:04")
	}
	fmt.Printf("plan %s  %s  hash %s  %s\n\n", p.ID, p.Source, p.Short(), signed)
	counts := map[intake.Action]int{}
	for _, t := range p.Columns {
		counts[t.Action]++
		if t.Action == intake.Keep {
			continue
		}
		why := t.Reason
		if t.Personal && why == "" {
			why = "personal data"
		}
		fmt.Printf("  %-7s %s.%s  %s\n", t.Action, t.Table, t.Column, why)
	}
	fmt.Printf("\n%d column(s): %d keep, %d mask, %d redact\n",
		len(p.Columns), counts[intake.Keep], counts[intake.Mask], counts[intake.Redact])
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func orDashStr(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// attachBrain gives a registry the brain, so every signature is also a
// decision there. A brain that cannot be opened is reported and skipped, not
// fatal: the warehouse ledger is the source of truth and signs without it.
func attachBrain(ctx context.Context, reg *rollout.Registry) func() {
	p, err := platform.Open(ctx, platform.Config{BrainPath: defaultBrain(), Embedder: corpusEmbedder()})
	if err != nil || p.Brain == nil {
		if p != nil {
			p.Close()
		}
		fmt.Fprintf(os.Stderr, "-- no brain at %s: signatures go to the warehouse ledger only\n", defaultBrain())
		return func() {}
	}
	reg.WithBrain(p.Brain)
	return p.Close
}

// latest is the newest plan for a database in the given state.
func latest(ctx context.Context, store *intake.Store, dsn string, state intake.State) (intake.Plan, error) {
	ps, err := store.List(ctx, dsn)
	if err != nil {
		return intake.Plan{}, err
	}
	for _, p := range ps {
		if p.State == state {
			return p, nil
		}
	}
	return intake.Plan{}, fmt.Errorf("no %s intake plan for %s — `di intake propose` makes one", state, intake.SourceOf(dsn))
}

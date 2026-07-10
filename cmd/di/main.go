// Command di drives the DataIntelligence platform.
//
//	di query -metrics net_revenue -by store_region
//	di query -metrics total_revenue,order_count,avg_order_value
//
// It compiles a semantic query (semantic-go) to fanout/chasm-safe SQL and runs
// it against the live warehouse. (NL `ask` lands in the next slice.)
package main

import (
	"bufio"
	"context"
	"crypto/rsa"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	oteltrace "go.opentelemetry.io/otel/trace"

	agentpkg "github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/llm"
	"github.com/liliang-cn/agent-go/v2/pkg/providers"
	semantic "github.com/liliang-cn/semantic-go"
	"github.com/spf13/cobra"

	"github.com/liliang-cn/dataintelligence/connectors"
	"github.com/liliang-cn/dataintelligence/convo"
	"github.com/liliang-cn/dataintelligence/copilot"
	"github.com/liliang-cn/dataintelligence/critic"
	"github.com/liliang-cn/dataintelligence/destinations"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/flow"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/grounding"
	"github.com/liliang-cn/dataintelligence/config"
	"github.com/liliang-cn/dataintelligence/ingest"
	mcpserver "github.com/liliang-cn/dataintelligence/mcp"
	"github.com/liliang-cn/dataintelligence/modelgen"
	"github.com/liliang-cn/dataintelligence/obs"
	"github.com/liliang-cn/dataintelligence/nleval"
	"github.com/liliang-cn/dataintelligence/reconcile"
	"github.com/liliang-cn/dataintelligence/nodes"
	"github.com/liliang-cn/dataintelligence/rollout"
	"github.com/liliang-cn/dataintelligence/spiderbench"
	"github.com/liliang-cn/dataintelligence/runtime"
	"github.com/liliang-cn/dataintelligence/runtime/ui"
	"github.com/liliang-cn/dataintelligence/warehouse"
	"github.com/liliang-cn/dataintelligence/writeback"
)

const defaultDSN = "postgres://meridian:meridian@localhost:39632/meridian?sslmode=disable"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// rootCmd builds the Cobra command tree. Each leaf wraps an existing run* handler
// that still parses its own flags (DisableFlagParsing), so the CLI gains Cobra's
// grouped help / version / shell completion with zero change to command behavior.
func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "di",
		Short: "DataIntelligence — governed semantic layer + MCP gateway for your warehouse",
		Long: "DataIntelligence makes a data warehouse safe for AI agents: a semantic layer,\n" +
			"grounded text-to-SQL, governance on every hop, and a governed MCP server.",
		Version:       "0.1.0",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Real OpenTelemetry, opt-in via DI_OTEL=1 (spans are no-ops otherwise).
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if os.Getenv("DI_OTEL") != "" {
				if _, err := obs.InitOTel("dataintelligence"); err != nil {
					return err
				}
			}
			return nil
		},
	}

	leaf := func(use, group, short string, run func([]string)) *cobra.Command {
		c := &cobra.Command{
			Use: use, Short: short, GroupID: group,
			DisableFlagParsing: true, // each handler parses its own flags
			Run:                func(_ *cobra.Command, args []string) { run(args) },
		}
		return c
	}

	root.AddGroup(
		&cobra.Group{ID: "ai", Title: "AI:"},
		&cobra.Group{ID: "query", Title: "Query & explore:"},
		&cobra.Group{ID: "model", Title: "Model & onboarding:"},
		&cobra.Group{ID: "gov", Title: "Governance & security:"},
		&cobra.Group{ID: "ops", Title: "Service & operations:"},
		&cobra.Group{ID: "data", Title: "Data movement:"},
	)

	root.AddCommand(
		// AI
		leaf("copilot", "ai", "Autonomous agent: audits + answers + recommends (agent-go)", runCopilot),
		leaf("agent", "ai", "Run an LLM agent over the MCP tools", runAgent),
		// query & explore
		leaf("query", "query", "Run a governed semantic query", runQuery),
		leaf("ask", "query", "Ask a question in natural language", runAsk),
		leaf("chat", "query", "Conversational BI with cross-turn memory", runChat),
		leaf("chain", "query", "Multi-metric chained query", runChain),
		leaf("explain", "query", "Compile a query to SQL for a dialect (no execution)", runExplain),
		// model & onboarding
		leaf("model", "model", "Generate (gen) or lint a semantic model", runModel),
		leaf("exemplar", "model", "Manage the few-shot exemplar bank", runExemplar),
		leaf("eval", "model", "Reconciliation gate (metrics vs control SQL)", runEval),
		leaf("nleval", "model", "NL accuracy gate over the labeled set", runNLEval),
		leaf("bench", "model", "Public benchmark (Spider): coverage + correctness", runBench),
		leaf("shadow", "model", "Diff a query across two model versions", runShadow),
		leaf("rollout", "model", "Version registry, canary, auto-rollback", runRollout),
		// governance & security
		leaf("threats", "gov", "Threat-model-as-code gate", runThreats),
		leaf("pentest", "gov", "MCP security regression (forged-token battery)", runPentest),
		leaf("token", "gov", "Dev token issuer (gen-key / mint)", runToken),
		leaf("obo", "gov", "On-behalf-of identity propagation demo", runOBO),
		leaf("spend", "gov", "Per-tenant spend ledger", runSpend),
		leaf("reconcile", "gov", "Detect cross-source data conflicts (AI triage)", runReconcile),
		leaf("trace", "gov", "OpenTelemetry trace-propagation demo", runTrace),
		// service & operations
		leaf("serve", "ops", "Run the service daemon (REST /v1 + MCP + /ui)", runServe),
		leaf("mcp", "ops", "Run the MCP server (stdio or HTTP)", runMCP),
		leaf("dashboard", "ops", "Print the execution dashboard", runDashboard),
		leaf("flow", "ops", "Run a config-driven DataFlow saga", runFlow),
		leaf("node", "ops", "Run a single pipeline node", runNode),
		// data movement & write-back
		leaf("ingest", "data", "Ingest a CSV into the warehouse", runIngest),
		leaf("source", "data", "Read/ingest from a configured source", runSource),
		leaf("crm", "data", "Sync from a CRM source", runCRM),
		leaf("webhook", "data", "Run the webhook receiver", runWebhook),
		leaf("cdc", "data", "Watch a table for changes (CDC)", runCDC),
		leaf("propose", "data", "Propose a typed write-back change", runPropose),
		leaf("proposals", "data", "List write-back proposals", runProposals),
		leaf("approve", "data", "Approve a write-back proposal", func(a []string) { runWriteDecision("approve", a) }),
		leaf("reject", "data", "Reject a write-back proposal", func(a []string) { runWriteDecision("reject", a) }),
		leaf("revert", "data", "Revert a committed write-back", func(a []string) { runWriteDecision("revert", a) }),
	)
	return root
}

// runServe starts the control-plane HTTP API (execution dashboard + query +
// explorer + lineage).
// runServe is the production core-service daemon: one config boots the shared
// engine/grounding/governance core and serves it over two contracts — the
// versioned REST /v1 API and the MCP server — with OIDC identity propagation,
// OpenTelemetry, health/readiness, and graceful shutdown.
func runServe(argv []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("DI_CONFIG", "config.yaml"), "service config YAML")
	model := fs.String("model", "models/meridian.yaml", "semantic model (used only if no config file)")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN (used only if no config file)")
	_ = fs.Parse(argv)

	// Config-driven when the file exists; otherwise synthesize from flags/env so
	// `di serve` still works out of the box.
	var cfg *config.Config
	if _, statErr := os.Stat(*cfgPath); statErr == nil {
		c, err := config.Load(*cfgPath)
		if err != nil {
			fail(err)
		}
		cfg = c
		fmt.Fprintf(os.Stderr, "-- loaded config %s\n", *cfgPath)
	} else {
		cfg = &config.Config{
			Model: *model, Sources: envOr("DI_SOURCES", "examples/meridian/sources.yaml"),
			Warehouse: config.Warehouse{DSN: *dsn, AppRole: os.Getenv("DI_DB_APP_ROLE"), MaxScanBytes: envBytes("DI_MAX_SCAN_BYTES")},
			Governance: config.Governance{TenantBudgetBytes: envBytes("DI_TENANT_BUDGET_BYTES")},
			Server:     config.Server{RESTAddr: envOr("DI_ADDR", ":41900"), MCPAddr: envOr("DI_MCP_ADDR", ":41910"), OTel: os.Getenv("DI_OTEL") != ""},
		}
		cfg.Server.RESTAddr = orDefaultStr(cfg.Server.RESTAddr, ":41900")
		cfg.Server.MCPAddr = orDefaultStr(cfg.Server.MCPAddr, ":41910")
		fmt.Fprintf(os.Stderr, "-- no config file at %s; using flags/env defaults\n", *cfgPath)
	}

	if cfg.Server.OTel {
		if _, err := obs.InitOTel("dataintelligence"); err != nil {
			fail(err)
		}
	}

	// Warehouse options travel via env that engine.New reads.
	if cfg.Warehouse.AppRole != "" {
		_ = os.Setenv("DI_DB_APP_ROLE", cfg.Warehouse.AppRole)
	}
	if cfg.Warehouse.MaxScanBytes > 0 {
		_ = os.Setenv("DI_MAX_SCAN_BYTES", fmt.Sprintf("%d", cfg.Warehouse.MaxScanBytes))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eng, err := engine.New(ctx, cfg.Model, cfg.Warehouse.DSN)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	idx := cfg.IndexPath
	if idx == "" {
		dir, _ := os.MkdirTemp("", "di-serve-")
		defer os.RemoveAll(dir)
		idx = filepath.Join(dir, "idx.db")
	}
	gr, err := grounding.New(ctx, eng.Model, idx)
	if err != nil {
		fail(err)
	}
	defer gr.Close()
	if bank, err := grounding.LoadExemplars(ctx, "models/exemplars.yaml"); err == nil {
		gr.WithExemplars(bank)
	}

	pol := governance.DefaultPolicy()
	pol.TenantBudgetBytes = cfg.Governance.TenantBudgetBytes
	verifier, authNote := verifierFromConfig(cfg)

	fe, _ := newFlowEngine(ctx, cfg.Warehouse.DSN)

	// One parent mux: stable /v1 data-plane API + the existing control-plane API
	// + the embedded web console at /ui.
	v1 := &runtime.V1{Eng: eng, Gr: gr, Pol: pol, Verify: verifier}
	mux := http.NewServeMux()
	mux.Handle("/v1/", v1.Handler())
	if console, uerr := ui.New(eng, pol, fe); uerr == nil {
		console.Mount(mux)
	} else {
		fmt.Fprintf(os.Stderr, "-- web console disabled: %v\n", uerr)
	}
	mux.Handle("/", runtime.NewServer(eng, fe))

	rest := &http.Server{Addr: cfg.Server.RESTAddr, Handler: mux}
	mcpSrv := buildMCPHTTPServer(cfg.Server.MCPAddr, eng, verifier)

	errc := make(chan error, 2)
	go func() { errc <- serveNamed("REST /v1", rest) }()
	go func() { errc <- serveNamed("MCP", mcpSrv) }()
	fmt.Fprintf(os.Stderr, "DataIntelligence service up:\n  Console  → %s/ui\n  REST /v1 → %s  (GET /v1/metrics /v1/metrics/{m}/dimensions ; POST /v1/query /v1/ground /v1/ask ; /v1/healthz /v1/readyz)\n  MCP      → %s  (%s)\n  auth: %s · otel: %v\n",
		cfg.Server.RESTAddr, cfg.Server.RESTAddr, cfg.Server.MCPAddr, "list_metrics/get_dimensions/query_metric", authNote, cfg.Server.OTel)

	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "\n-- shutting down gracefully…")
	case err := <-errc:
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = rest.Shutdown(sctx)
	_ = mcpSrv.Shutdown(sctx)
}

func serveNamed(name string, s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// buildMCPHTTPServer wires the MCP server over streamable HTTP behind bearer auth
// (or open when verifier is nil) and continues inbound W3C traces.
func buildMCPHTTPServer(addr string, eng *engine.Engine, verifier auth.TokenVerifier) *http.Server {
	opts := &mcpserver.Options{Default: mcpserver.Principal{User: "local", Role: "analyst", Scopes: []string{"metrics:read", "data:write"}}, Burst: 5, ChecksPath: envOr("DI_CHECKS", "examples/meridian/conflicts.yaml")}
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return mcpserver.NewServer(eng, opts) }, nil)
	var h http.Handler = handler
	if verifier != nil {
		h = auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{Scopes: []string{"metrics:read"}})(handler)
	}
	traced := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(obs.ExtractHTTP(r.Context(), r.Header)))
	})
	return &http.Server{Addr: addr, Handler: traced}
}

// verifierFromConfig builds the OIDC verifier from config; nil (open) when no
// auth block is present.
func verifierFromConfig(cfg *config.Config) (auth.TokenVerifier, string) {
	if cfg.Auth.OIDC == nil {
		return nil, "open (no auth — dev only; set auth.oidc to require tokens)"
	}
	o := cfg.Auth.OIDC
	oc := mcpserver.OIDCConfig{Issuer: o.Issuer, Audience: o.Audience, JWKSURL: o.JWKSURL}
	if o.PublicKeyPEM != "" {
		oc.PublicKeyPEM = []byte(o.PublicKeyPEM)
	}
	v, err := mcpserver.NewOIDC(oc)
	if err != nil {
		fail(err)
	}
	src := o.JWKSURL
	if src == "" {
		src = "static-key"
	}
	return v.Verifier(), fmt.Sprintf("OIDC iss=%q aud=%q keys=%s", o.Issuer, o.Audience, src)
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// runExplain compiles a semantic query to SQL for a chosen warehouse dialect
// WITHOUT executing it (M6): the same intent → Postgres / Snowflake / Databricks
// SQL, proving the dialect abstraction. No warehouse connection needed.
func runExplain(argv []string) {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dialect := fs.String("dialect", "postgres", "postgres | snowflake | databricks | ansi")
	metrics := fs.String("metrics", "", "comma-separated metrics (required)")
	by := fs.String("by", "", "group-by dimensions")
	grain := fs.String("grain", "", "time grain (day|month|quarter|year)")
	_ = fs.Parse(argv)
	if *metrics == "" {
		fail(fmt.Errorf("explain: -metrics is required"))
	}
	d, ok := semantic.DialectByName(*dialect)
	if !ok {
		fail(fmt.Errorf("unknown dialect %q (postgres|snowflake|databricks|ansi)", *dialect))
	}
	m, err := semantic.LoadFile(*model)
	if err != nil {
		fail(err)
	}
	c, err := semantic.Compile(m, semantic.Query{Metrics: split(*metrics), GroupBy: split(*by), TimeGrain: *grain}, d)
	if err != nil {
		fail(err)
	}
	fmt.Printf("-- dialect: %s\n%s\n", d.Name(), c.SQL)
	if len(c.Args) > 0 {
		fmt.Printf("-- args: %v\n", c.Args)
	}
}

// runModel is the metadata gate (M4): `di model lint` enforces that every
// metric describes itself and declares how it rolls up. Exits 1 on any error so
// it can guard a merge in CI. The rules live in the neutral core (semantic.Lint).
// runBench dispatches the public-benchmark harness. Spider is the first backend;
// it reports coverage (how many questions are expressible as a semantic query)
// alongside correctness on that slice — never a single misleading leaderboard number.
func runBench(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di bench spider [-data DIR]")
		os.Exit(2)
	}
	switch argv[0] {
	case "spider":
		runBenchSpider(argv[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: di bench spider [-data DIR]")
		os.Exit(2)
	}
}

// runBenchSpider is M1: classify the Spider dev set by expressibility and print
// coverage. Needs only dev.json — no warehouse, no LLM.
func runBenchSpider(argv []string) {
	fs := flag.NewFlagSet("bench spider", flag.ExitOnError)
	dir := fs.String("data", envOr("DI_SPIDER_DIR", "testdata/spider"), "Spider data dir (holds dev.json)")
	_ = fs.Parse(argv)

	xs, err := spiderbench.LoadDev(*dir)
	if err != nil {
		fail(err)
	}
	spiderbench.Cover(xs).Print(os.Stdout)
}

func runModel(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di model <lint|gen> [flags]")
		os.Exit(2)
	}
	switch argv[0] {
	case "lint":
		runModelLint(argv[1:])
	case "gen":
		runModelGen(argv[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: di model <lint|gen> [flags]")
		os.Exit(2)
	}
}

func runModelLint(argv []string) {
	fs := flag.NewFlagSet("model lint", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	_ = fs.Parse(argv)

	m, err := semantic.LoadFile(*model)
	if err != nil {
		fail(err)
	}
	issues := semantic.Lint(m)
	errs := 0
	for _, i := range issues {
		fmt.Println(i)
		if i.Severity == "error" {
			errs++
		}
	}
	fmt.Fprintf(os.Stderr, "-- lint %s: %d issue(s), %d error(s)\n", *model, len(issues), errs)
	if errs > 0 {
		os.Exit(1)
	}
}

// runModelGen is self-serve onboarding: introspect a warehouse and emit a
// semantic-model draft (heuristic, optionally LLM-refined) for a human to review.
//
//	di model gen -dsn ... -out draft.yaml          # heuristic
//	LLM_BASE_URL/LLM_API_KEY/LLM_MODEL set → adds AI-refined descriptions/metrics
func runModelGen(argv []string) {
	fs := flag.NewFlagSet("model gen", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN to introspect")
	out := fs.String("out", "", "write the draft YAML here (default: stdout)")
	useLLM := fs.Bool("llm", true, "refine with the LLM when LLM_* env is set")
	_ = fs.Parse(argv)

	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()

	schema, err := modelgen.Introspect(ctx, wh)
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "-- introspected %d table(s)\n", len(schema.Tables))

	var ask modelgen.AskFunc
	mode := "heuristic"
	if *useLLM {
		if svc, lerr := llm.NewOpenAIFromEnv(); lerr == nil {
			ask = svc.Ask
			mode = "heuristic + LLM refine"
		}
	}
	model, issues, err := modelgen.Generate(ctx, schema, ask)
	if err != nil {
		fail(err)
	}
	yamlOut, err := modelgen.ToYAML(model)
	if err != nil {
		fail(err)
	}
	if *out == "" {
		fmt.Print(string(yamlOut))
	} else {
		if err := os.WriteFile(*out, yamlOut, 0o644); err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "-- wrote %s\n", *out)
	}
	fmt.Fprintf(os.Stderr, "-- mode: %s · %d entities, %d joins, %d dimensions, %d metrics · %d lint note(s)\n",
		mode, len(model.Entities), len(model.Joins), len(model.Dimensions), len(model.Metrics), len(issues))
	fmt.Fprintln(os.Stderr, "-- review the draft, then run: di model lint -model <file>  and  di eval")
}

// runCopilot is a real agent-go agent driving the whole platform: given a goal,
// the LLM autonomously calls governed platform tools (describe the warehouse,
// list metrics, check dimensions, run a governed query, health-check for
// cross-source conflicts) and synthesizes an answer + a recommended governed fix.
// The agent decides WHAT to call; the deterministic tools guarantee each result.
func runCopilot(argv []string) {
	fs := flag.NewFlagSet("copilot", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	goal := fs.String("goal", "Describe this warehouse, run a health check for cross-source conflicts, answer: which store region has the highest net revenue, then recommend the single highest-priority governed fix.", "the agent's goal")
	checks := fs.String("checks", "examples/meridian/conflicts.yaml", "conflict checks YAML")
	_ = fs.Parse(argv)

	if !copilot.Available() {
		fail(fmt.Errorf("copilot is the AI showcase — set LLM_BASE_URL/LLM_API_KEY/LLM_MODEL first"))
	}
	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	agent, err := copilot.New(eng, governance.DefaultPolicy(), *checks)
	if err != nil {
		fail(err)
	}
	defer agent.Close()

	fmt.Fprintf(os.Stderr, "-- copilot goal: %s\n\n", *goal)
	res, err := agent.Run(ctx, *goal)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%s\n", res.Answer)
	fmt.Fprintf(os.Stderr, "\n-- agent: steps=%d tool_calls=%d tools=%v\n", res.Steps, res.ToolCalls, res.Tools)
}

// runReconcile detects cross-source data conflicts (deterministic SQL checks)
// and, when LLM_* is set, has the LLM triage each conflict — likely cause,
// system-of-record, recommended fix. Detection is reliable; AI adds judgment.
func runReconcile(argv []string) {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	checks := fs.String("checks", "examples/meridian/conflicts.yaml", "conflict checks YAML")
	ai := fs.Bool("ai", true, "LLM triage of each conflict when LLM_* is set")
	_ = fs.Parse(argv)

	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()
	cs, err := reconcile.Load(*checks)
	if err != nil {
		fail(err)
	}

	var ask reconcile.AskFunc
	mode := "detection only"
	if *ai {
		if svc, lerr := llm.NewOpenAIFromEnv(); lerr == nil {
			ask = svc.Ask
			mode = "detection + LLM triage"
		}
	}
	results, err := reconcile.Run(ctx, wh, cs, ask)
	if err != nil {
		fail(err)
	}

	total := 0
	for _, r := range results {
		mark := "✓"
		if r.Count() > 0 {
			mark = "✗"
			total += r.Count()
		}
		fmt.Printf("\n%s [%s] %s — %d conflict(s)\n", mark, r.Check.Severity, r.Check.Name, r.Count())
		for i, row := range r.Rows {
			if i >= 5 {
				fmt.Printf("    … and %d more\n", r.Count()-5)
				break
			}
			cells := make([]string, len(row))
			for j, v := range row {
				cells[j] = fmt.Sprintf("%v", v)
			}
			fmt.Printf("    %s\n", strings.Join(cells, " | "))
		}
		if r.Triage != "" {
			fmt.Printf("  🧠 AI triage: %s\n", r.Triage)
		}
	}
	fmt.Fprintf(os.Stderr, "\n-- %s · %d total conflict(s) across %d check(s)\n", mode, total, len(results))
}

// runSpend shows or resets the per-tenant spend ledger (M13/M21):
//
//	di spend            # list cumulative cost per tenant
//	di spend reset -tenant acme
func runSpend(argv []string) {
	fs := flag.NewFlagSet("spend", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	tenant := fs.String("tenant", "", "tenant id (for reset)")
	reset := len(argv) > 0 && argv[0] == "reset"
	if reset {
		argv = argv[1:]
	}
	_ = fs.Parse(argv)

	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()
	ledger := governance.NewSpendLedger(wh)

	if reset {
		if *tenant == "" {
			fail(fmt.Errorf("reset needs -tenant"))
		}
		if err := ledger.Reset(ctx, *tenant); err != nil {
			fail(err)
		}
		fmt.Printf("reset spend for %q\n", *tenant)
		return
	}
	rows, err := ledger.All(ctx)
	if err != nil {
		fail(err)
	}
	if len(rows) == 0 {
		fmt.Println("(no spend recorded)")
		return
	}
	fmt.Printf("%-16s %14s %9s\n", "tenant", "bytes", "queries")
	for _, r := range rows {
		fmt.Printf("%-16s %14d %9d\n", r.Tenant, r.Bytes, r.Queries)
	}
}

// runTrace demonstrates real OpenTelemetry with W3C trace-context propagation
// (M20): a client span injects a traceparent into a carrier; the "server" side
// extracts it and runs a governed query whose compile/plan/execute spans nest
// under the SAME trace_id — the cross-process linkage, proven end to end.
func runTrace(argv []string) {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	metrics := fs.String("metrics", "total_revenue", "metrics to query")
	by := fs.String("by", "store_region", "group-by dimensions")
	role := fs.String("role", "finance", "caller role")
	_ = fs.Parse(argv)

	if _, err := obs.InitOTel("dataintelligence"); err != nil {
		fail(err)
	}
	ctx := context.Background()

	// --- client side: start a span and inject its traceparent into a carrier ---
	cctx, client := obs.Tracer().Start(ctx, "client_request")
	carrier := map[string]string{}
	obs.InjectMap(cctx, carrier)
	clientTrace := oteltrace.SpanContextFromContext(cctx).TraceID().String()
	fmt.Printf("client span trace_id = %s\n", clientTrace)
	fmt.Printf("propagated traceparent = %s\n", carrier["traceparent"])
	client.End()

	// --- server side: extract the remote context, then run a real query ---
	sctx := obs.ExtractMap(context.Background(), carrier)
	serverTrace := oteltrace.SpanContextFromContext(sctx).TraceID().String()
	fmt.Printf("server extracted trace_id = %s  (match=%v)\n", serverTrace, serverTrace == clientTrace)

	eng, err := engine.New(sctx, "models/meridian.yaml", *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()
	_, err = governance.Query(sctx, eng, semantic.Query{Metrics: split(*metrics), GroupBy: split(*by)},
		governance.Principal{User: "cli", Role: *role}, governance.DefaultPolicy())
	if err != nil {
		fail(err)
	}
	fmt.Fprintln(os.Stderr, "-- the otel spans above (governed_query→compile/plan/execute) share the client's trace_id")
}

// runPentest is the automated MCP-security regression (M17, lesson 75): it
// fires a battery of forged tokens at the real OIDC verifier and asserts every
// attack is rejected and the one good token is accepted. Exit 1 if any attack
// gets through — a CI gate so a security control can never silently regress.
func runPentest(argv []string) {
	_ = argv // no flags; the gate is self-contained
	ctx := context.Background()
	const issuer, aud = "https://di-issuer.local", "warehouse"

	priv, err := mcpserver.GenerateKey(2048)
	if err != nil {
		fail(err)
	}
	pubPEM, err := mcpserver.MarshalPublicKeyPEM(&priv.PublicKey)
	if err != nil {
		fail(err)
	}
	evil, _ := mcpserver.GenerateKey(2048) // attacker's key, not trusted

	oidc, err := mcpserver.NewOIDC(mcpserver.OIDCConfig{Issuer: issuer, Audience: aud, PublicKeyPEM: pubPEM, KeyID: "k1"})
	if err != nil {
		fail(err)
	}
	verify := oidc.Verifier()
	now := time.Now()
	base := func() map[string]any {
		return map[string]any{"iss": issuer, "aud": aud, "sub": "u1", "role": "analyst",
			"exp": now.Add(time.Hour).Unix(), "nbf": now.Add(-time.Minute).Unix()}
	}
	sign := func(p *rsa.PrivateKey, kid string, c map[string]any) string {
		t, e := mcpserver.SignJWT(p, kid, c)
		if e != nil {
			fail(e)
		}
		return t
	}

	type attack struct {
		name       string
		token      string
		wantAccept bool
	}
	var attacks []attack
	attacks = append(attacks, attack{"valid token (control)", sign(priv, "k1", base()), true})
	attacks = append(attacks, attack{"no token", "", false})
	attacks = append(attacks, attack{"malformed jwt", "not.a.jwt.token", false})
	// expired
	c := base()
	c["exp"] = now.Add(-time.Hour).Unix()
	attacks = append(attacks, attack{"expired token", sign(priv, "k1", c), false})
	// not yet valid
	c = base()
	c["nbf"] = now.Add(time.Hour).Unix()
	attacks = append(attacks, attack{"not-yet-valid (nbf future)", sign(priv, "k1", c), false})
	// wrong audience — the confused-deputy attack
	c = base()
	c["aud"] = "some-other-service"
	attacks = append(attacks, attack{"wrong audience (confused deputy)", sign(priv, "k1", c), false})
	// wrong issuer
	c = base()
	c["iss"] = "https://evil-issuer.local"
	attacks = append(attacks, attack{"untrusted issuer", sign(priv, "k1", c), false})
	// forged signature (attacker key)
	attacks = append(attacks, attack{"forged signature (attacker key)", sign(evil, "k1", base()), false})
	// tampered signature
	good := sign(priv, "k1", base())
	attacks = append(attacks, attack{"tampered signature", good[:len(good)-2] + "AA", false})

	fmt.Println("MCP security pen-test — forged tokens vs the real OIDC verifier:")
	failures := 0
	for _, a := range attacks {
		_, verr := verify(ctx, a.token, nil)
		accepted := verr == nil
		ok := accepted == a.wantAccept
		mark := "✓"
		if !ok {
			mark = "✗"
			failures++
		}
		verdict := "REJECTED"
		if accepted {
			verdict = "ACCEPTED"
		}
		fmt.Printf("  %s %-34s → %s\n", mark, a.name, verdict)
	}

	// Authorization probe: an unauthorized metric must be refused at the
	// governance boundary even with a perfectly valid token.
	eng, err := engine.New(ctx, "models/meridian.yaml", envOr("DI_DSN", defaultDSN))
	if err == nil {
		defer eng.Close()
		_, qerr := governance.Query(ctx, eng, semantic.Query{Metrics: []string{"net_revenue"}},
			governance.Principal{User: "u1", Role: "analyst"}, governance.DefaultPolicy())
		mark := "✓"
		if qerr == nil {
			mark = "✗"
			failures++
		}
		fmt.Printf("  %s %-34s → %s\n", mark, "unauthorized metric (net_revenue)", boolStr(qerr != nil, "REFUSED", "LEAKED"))
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\nPEN-TEST FAILED: %d control(s) breached\n", failures)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\n-- pen-test passed: all %d attacks rejected, controls intact\n", len(attacks))
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// runRollout drives the production change-management plane (M21): version
// registry, canary traffic-split, lineage-driven invalidation.
//
//	di rollout register -name v2 -model models/candidate.yaml
//	di rollout list
//	di rollout canary  -name v2 -pct 10
//	di rollout promote -name v2     # retires old active, prints lineage delta
//	di rollout rollback             # panic button
//	di rollout simulate -pct 10 -n 1000
func runRollout(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di rollout <register|list|canary|promote|rollback|simulate> [flags]")
		os.Exit(2)
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("rollout "+sub, flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	name := fs.String("name", "", "version name")
	model := fs.String("model", "", "model YAML path (register)")
	pct := fs.Int("pct", 0, "canary percentage 0..100")
	n := fs.Int("n", 1000, "number of synthetic requests (simulate)")
	minHealth := fs.Float64("min", 1.0, "canary health floor (watch): auto-rollback below this")
	_ = fs.Parse(rest)

	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()
	reg := rollout.New(wh, func() string { return time.Now().UTC().Format(time.RFC3339) })

	switch sub {
	case "register":
		if *name == "" || *model == "" {
			fail(fmt.Errorf("register needs -name and -model"))
		}
		v, err := reg.Register(ctx, *name, *model)
		if err != nil {
			fail(err)
		}
		fmt.Printf("registered %s (hash %s) as %s\n", v.Name, v.Hash, v.Status)
	case "list":
		vs, err := reg.List(ctx)
		if err != nil {
			fail(err)
		}
		for _, v := range vs {
			pctStr := ""
			if v.Status == rollout.StatusCanary {
				pctStr = fmt.Sprintf(" @%d%%", v.CanaryPct)
			}
			fmt.Printf("%-10s %-10s%s  hash=%s  %s\n", v.Name, v.Status, pctStr, v.Hash, v.Path)
		}
	case "canary":
		v, err := reg.Canary(ctx, *name, *pct)
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s now canary @ %d%% of traffic\n", v.Name, v.CanaryPct)
	case "promote":
		changed, err := reg.Promote(ctx, *name)
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s is now ACTIVE\n", *name)
		if len(changed) == 0 {
			fmt.Println("lineage: no metric definitions changed — no caches to invalidate")
		} else {
			fmt.Printf("lineage: %d metric(s) changed → invalidate caches for: %v\n", len(changed), changed)
		}
	case "rollback":
		v, err := reg.Rollback(ctx)
		if err != nil {
			fail(err)
		}
		if v == nil {
			fmt.Println("rollback: nothing to restore")
		} else {
			fmt.Printf("rolled back — %s restored to ACTIVE\n", v.Name)
		}
	case "simulate":
		counts := map[string]int{}
		for i := 0; i < *n; i++ {
			v, err := reg.Route(ctx, fmt.Sprintf("req-%d", i))
			if err != nil {
				fail(err)
			}
			counts[v.Name+" ("+v.Status+")"]++
		}
		fmt.Printf("routed %d requests:\n", *n)
		for k, c := range counts {
			fmt.Printf("  %-24s %5d  (%.1f%%)\n", k, c, 100*float64(c)/float64(*n))
		}
	case "watch":
		// Health-check the live canary; auto-rollback if it regresses below the
		// floor — the unattended guard so a bad canary self-heals.
		cv, err := reg.CanaryVersion(ctx)
		if err != nil {
			fail(fmt.Errorf("no canary to watch: %w", err))
		}
		score, failed, err := healthScore(ctx, cv.Path, *dsn)
		if err != nil {
			fail(err)
		}
		fmt.Printf("canary %s health=%.0f%% (floor %.0f%%)", cv.Name, score*100, *minHealth*100)
		if len(failed) > 0 {
			fmt.Printf(" · failing: %v", failed)
		}
		fmt.Println()
		if score < *minHealth {
			rb, rerr := reg.Rollback(ctx)
			if rerr != nil {
				fail(rerr)
			}
			if rb != nil {
				fmt.Printf("REGRESSION → auto-rolled back; %s is ACTIVE, canary demoted\n", rb.Name)
			} else {
				fmt.Println("REGRESSION → canary demoted (no active version)")
			}
			os.Exit(1)
		}
		fmt.Println("canary healthy — safe to keep promoting")
	default:
		fail(fmt.Errorf("unknown rollout subcommand %q", sub))
	}
}

// runThreats is the threat-model-as-code gate (M11): every threat must name a
// control + owner + evidence and be mitigated or accepted. Exits 1 on any
// unaddressed or under-specified threat so it can guard a merge in CI.
func runThreats(argv []string) {
	fs := flag.NewFlagSet("threats", flag.ExitOnError)
	path := fs.String("file", envOr("DI_THREATS", "examples/meridian/threats.yaml"), "threat model YAML")
	_ = fs.Parse(argv)

	tm, err := governance.LoadThreatModel(*path)
	if err != nil {
		fail(err)
	}
	issues := tm.Check()
	for _, t := range tm.Threats {
		mark := "✓"
		if t.Status != governance.ThreatMitigated && t.Status != governance.ThreatAccepted {
			mark = "✗"
		}
		fmt.Printf("%s %-22s %-10s %s\n", mark, t.ID, t.Status, t.Title)
	}
	if len(issues) > 0 {
		fmt.Fprintln(os.Stderr, "\nTHREAT MODEL GATE FAILED:")
		for _, i := range issues {
			fmt.Fprintln(os.Stderr, "  - "+i)
		}
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "-- threat model OK: %d threat(s), all addressed\n", len(tm.Threats))
}

// runEval is the reconciliation / regression / drift gate: every metric must
// match a hand-written control query. Non-zero exit on fail.
func runEval(argv []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	_ = fs.Parse(argv)

	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	cases := reconCases()
	pass := 0
	for _, c := range cases {
		ans, err := eng.Query(ctx, semantic.Query{Metrics: []string{c.metric}})
		if err != nil {
			fmt.Printf("  [FAIL] %-14s compile/run: %v\n", c.name, err)
			continue
		}
		got := scalar(ans.Rows)
		ctrl, err := eng.WH.Query(ctx, c.control)
		if err != nil {
			fail(err)
		}
		want := scalar(ctrl.Rows)
		if floatEq(got, want) {
			fmt.Printf("  [PASS] %-14s %s\n", c.name, got)
			pass++
		} else {
			fmt.Printf("  [FAIL] %-14s got=%s want=%s\n", c.name, got, want)
		}
	}
	fmt.Printf("eval: %d/%d passed\n", pass, len(cases))
	if pass != len(cases) {
		os.Exit(1) // regression / drift detected
	}
}

type reconCase struct{ name, metric, control string }

// reconCases are the deterministic metric→control reconciliation checks shared
// by `di eval` and the canary health watcher.
func reconCases() []reconCase {
	return []reconCase{
		{"total_revenue", "total_revenue", "SELECT sum(quantity*unit_price) FROM order_items"},
		{"units_sold", "units_sold", "SELECT sum(quantity) FROM order_items"},
		{"order_count", "order_count", "SELECT count(DISTINCT order_id) FROM orders"},
		{"refund_total", "refund_total", "SELECT sum(refund_amount) FROM refunds"},
		{"net_revenue", "net_revenue", "SELECT (SELECT sum(quantity*unit_price) FROM order_items)-(SELECT sum(refund_amount) FROM refunds)"},
	}
}

// healthScore runs the reconciliation checks against a model and returns the
// fraction that match their control query — the canary's health signal.
func healthScore(ctx context.Context, modelPath, dsn string) (float64, []string, error) {
	eng, err := engine.New(ctx, modelPath, dsn)
	if err != nil {
		return 0, nil, err
	}
	defer eng.Close()
	cases := reconCases()
	pass := 0
	var failed []string
	for _, c := range cases {
		ans, err := eng.Query(ctx, semantic.Query{Metrics: []string{c.metric}})
		if err != nil {
			failed = append(failed, c.name)
			continue
		}
		ctrl, err := eng.WH.Query(ctx, c.control)
		if err != nil {
			return 0, nil, err
		}
		if floatEq(scalar(ans.Rows), scalar(ctrl.Rows)) {
			pass++
		} else {
			failed = append(failed, c.name)
		}
	}
	return float64(pass) / float64(len(cases)), failed, nil
}

// runNLEval is the natural-language evaluation closed-loop:
// it grades a labeled question set on the three axes (semantic / execution /
// result) + governance probes, prints per-category accuracy + a metric-confusion
// matrix, persists to the accuracy dashboard, and fails CI on a regression.
func runNLEval(argv []string) {
	fs := flag.NewFlagSet("nleval", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	set := fs.String("set", "models/nl_evalset.yaml", "labeled eval set YAML")
	min := fs.Float64("min", 0.8, "overall accuracy floor for the CI gate")
	floors := fs.String("floor", "governance=1.0", "per-category floors, e.g. governance=1.0,grouped=0.9")
	judge := fs.Bool("judge", true, "run the LLM-judge groundedness layer when creds are present")
	save := fs.Bool("save", true, "persist results to the accuracy dashboard tables")
	jsonOut := fs.String("json", "", "also write the machine-readable report to this path")
	_ = fs.Parse(argv)

	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	ds, err := nleval.Load(*set)
	if err != nil {
		fail(err)
	}

	dir, _ := os.MkdirTemp("", "di-nleval-")
	defer os.RemoveAll(dir)
	g, err := grounding.New(ctx, eng.Model, filepath.Join(dir, "idx.db"))
	if err != nil {
		fail(err)
	}
	defer g.Close()
	if bank, err := grounding.LoadExemplars(ctx, "models/exemplars.yaml"); err == nil {
		g.WithExemplars(bank)
	}
	llmWired := strings.Contains(g.Mode(), "llm")
	fmt.Fprintf(os.Stderr, "-- grounding=%s · set=%s · cases=%d\n", g.Mode(), *set, len(ds.Cases))

	grader := &nleval.Grader{Eng: eng, Gr: g, Pol: governance.DefaultPolicy()}
	rep := grader.Run(ctx, ds, llmWired)
	rep.WriteConsole(os.Stdout)

	if *judge {
		jr, err := nleval.Judge(ctx, rep)
		if err != nil {
			fmt.Fprintf(os.Stderr, "judge: %v\n", err)
		} else {
			jr.WriteConsole(os.Stdout)
		}
	}

	if *save {
		runID, err := rep.Save(ctx, eng.WH, time.Now().UnixNano())
		if err != nil {
			fmt.Fprintf(os.Stderr, "save: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "-- saved run %s to _nl_eval_runs/_nl_eval_cases\n", runID)
		}
	}
	if *jsonOut != "" {
		f, err := os.Create(*jsonOut)
		if err != nil {
			fail(err)
		}
		_ = rep.WriteJSON(f)
		_ = f.Close()
	}

	ok, fails := rep.Gate(*min, parseFloors(*floors))
	if !ok {
		fmt.Fprintf(os.Stderr, "\nGATE FAILED:\n")
		for _, f := range fails {
			fmt.Fprintf(os.Stderr, "  - %s\n", f)
		}
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\nGATE PASSED ✅  (accuracy %.0f%% ≥ %.0f%%)\n", rep.Acc*100, *min*100)
}

// runExemplar promotes a (question → semantic query) pair into the few-shot bank
// ("promote every fixed miss into the bank"). It appends to the
// YAML so the fix is durable and embeds the new question for retrieval.
func runExemplar(argv []string) {
	fs := flag.NewFlagSet("exemplar", flag.ExitOnError)
	path := fs.String("bank", "models/exemplars.yaml", "exemplar bank YAML")
	metrics := fs.String("metrics", "", "comma-separated metrics (required)")
	by := fs.String("by", "", "comma-separated group-by dimensions")
	grain := fs.String("grain", "", "time grain")
	_ = fs.Parse(argv)
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" || *metrics == "" {
		fmt.Fprintln(os.Stderr, `di exemplar -metrics net_revenue -by store_region "net revenue by region"`)
		os.Exit(2)
	}
	ctx := context.Background()
	bank, err := grounding.LoadExemplars(ctx, *path)
	if err != nil {
		fail(err)
	}
	q := semantic.Query{Metrics: split(*metrics), GroupBy: split(*by), TimeGrain: *grain}
	if err := bank.Promote(ctx, question, q); err != nil {
		fail(err)
	}
	fmt.Printf("promoted exemplar → %s (now %d in bank)\n", *path, bank.Len())
}

// parseFloors parses "cat=0.9,other=1.0" into a map.
func parseFloors(s string) map[string]float64 {
	out := map[string]float64{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64); err == nil {
			out[strings.TrimSpace(kv[0])] = f
		}
	}
	return out
}

// runDashboard renders a multi-panel dashboard. Panels are preset here; the
// NL→dashboard hook (agent-go LLM → panel specs) plugs in at panelsFor().
func runDashboard(argv []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	role := fs.String("role", "analyst", "caller role")
	_ = fs.Parse(argv)

	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	type panel struct {
		title string
		q     semantic.Query
	}
	panels := []panel{
		{"Revenue by region", semantic.Query{Metrics: []string{"total_revenue"}, GroupBy: []string{"store_region"}}},
		{"Revenue by category", semantic.Query{Metrics: []string{"total_revenue"}, GroupBy: []string{"product_category"}}},
		{"Orders by segment", semantic.Query{Metrics: []string{"order_count"}, GroupBy: []string{"customer_segment"}}},
	}
	for _, p := range panels {
		fmt.Printf("\n### %s\n", p.title)
		ans, err := governance.Query(ctx, eng, p.q, governance.Principal{User: "cli", Role: *role}, governance.DefaultPolicy())
		if err != nil {
			fmt.Printf("  (error: %v)\n", err)
			continue
		}
		printAnswer(ans)
	}
}

// runAgent runs a multi-step analyst using agent-go's loop (plan → call tools →
// reason → answer) over our governed MCP server. agent-go owns the loop; the MCP
// tools own correctness + governance. The "critique" discipline is in the prompt.
func runAgent(argv []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	role := fs.String("role", "analyst", "role the MCP server runs queries as")
	_ = fs.Parse(argv)
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `di agent: provide a question, e.g. di agent "which region has the highest revenue, and its AOV?"`)
		os.Exit(2)
	}
	base, key, mdl := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if base == "" || key == "" || mdl == "" {
		fail(fmt.Errorf("agent loop needs an LLM: set LLM_BASE_URL / LLM_API_KEY / LLM_MODEL"))
	}

	// MCP server config: spawn THIS binary in `mcp` mode as a stdio tool server.
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	absModel, _ := filepath.Abs(*model)
	cfg := map[string]any{"mcpServers": map[string]any{
		"dataintelligence": map[string]any{
			"type": "stdio", "command": exe,
			"args": []string{"mcp", "-model", absModel, "-dsn", *dsn, "-role", *role},
		},
	}}
	dir, _ := os.MkdirTemp("", "di-agent-")
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "mcpServers.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		fail(err)
	}

	llm, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{BaseURL: base, APIKey: key, LLMModel: mdl})
	if err != nil {
		fail(err)
	}

	const sys = `You are a data analyst. Answer ONLY using the warehouse MCP tools
(list_metrics, get_dimensions, query_metric). Never write SQL.
Plan: discover metrics with list_metrics; check valid dimensions with get_dimensions
BEFORE grouping; call query_metric; then sanity-check (right metric? right grain?
plausible range?). For multi-part questions, query step by step and chain results.
If a query is refused or a metric is missing, say so honestly — never fabricate a number.`

	ctx := context.Background()
	svc, err := agentpkg.New("di-analyst").
		WithLLM(llm).
		WithPTC(false). // native tool-calling over the MCP tools (not code-execution)
		WithSystemPrompt(sys).
		WithMCP(agentpkg.WithMCPConfigPaths(cfgPath)).
		Build()
	if err != nil {
		fail(err)
	}
	defer svc.Close()

	res, err := svc.Chat(ctx, question)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%v\n", res.FinalResult)
	fmt.Fprintf(os.Stderr, "\n-- agent: steps=%d tool_calls=%d tools=%v\n", res.StepsTotal, res.ToolCalls, res.ToolsUsed)
}

// runCDC watches a table for new rows (change-data-capture) and streams events.
func runCDC(argv []string) {
	fs := flag.NewFlagSet("cdc", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	table := fs.String("table", "order_items", "table to watch")
	cursor := fs.String("cursor", "item_id", "monotonic cursor column")
	secs := fs.Int("for", 8, "seconds to watch")
	_ = fs.Parse(argv)

	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()

	cdc := &connectors.PostgresCDC{WH: wh, Table: *table, CursorCol: *cursor, Interval: time.Second}
	if err := cdc.StartCursor(ctx); err != nil {
		fail(err)
	}
	fmt.Printf("watching %q (cursor %q) for %ds...\n", *table, *cursor, *secs)
	wctx, cancel := context.WithTimeout(ctx, time.Duration(*secs)*time.Second)
	defer cancel()
	ch, err := cdc.Subscribe(wctx)
	if err != nil {
		fail(err)
	}
	n := 0
	for e := range ch {
		n++
		fmt.Printf("  [%s cursor=%d] %v\n", e.Op, e.Cursor, e.Record)
	}
	fmt.Printf("captured %d change event(s)\n", n)
}

// runShadow compiles+runs a query through two model versions and diffs the
// result — the shadow step before a canary rollout (M21).
func runShadow(argv []string) {
	fs := flag.NewFlagSet("shadow", flag.ExitOnError)
	a := fs.String("a", "models/meridian.yaml", "model A (current)")
	b := fs.String("b", "", "model B (candidate) — required")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	metrics := fs.String("metrics", "", "comma-separated metrics (required)")
	by := fs.String("by", "", "group-by dimensions")
	grain := fs.String("grain", "", "time grain")
	_ = fs.Parse(argv)
	if *b == "" || *metrics == "" {
		fmt.Fprintln(os.Stderr, "di shadow: -b and -metrics are required")
		os.Exit(2)
	}
	ctx := context.Background()
	q := semantic.Query{Metrics: split(*metrics), GroupBy: split(*by), TimeGrain: *grain}

	engA, err := engine.New(ctx, *a, *dsn)
	if err != nil {
		fail(err)
	}
	defer engA.Close()
	engB, err := engine.New(ctx, *b, *dsn)
	if err != nil {
		fail(err)
	}
	defer engB.Close()

	ansA, errA := engA.Query(ctx, q)
	ansB, errB := engB.Query(ctx, q)
	if errA != nil || errB != nil {
		fmt.Printf("shadow: A err=%v · B err=%v (DIFFER)\n", errA, errB)
		os.Exit(1)
	}
	da, db := dumpRows(ansA), dumpRows(ansB)
	if da == db {
		fmt.Printf("shadow: MATCH (%d rows) — safe to promote B\n", len(ansA.Rows))
		return
	}
	fmt.Printf("shadow: DIFFER — A=%d rows, B=%d rows. Do NOT promote until reconciled.\n", len(ansA.Rows), len(ansB.Rows))
	os.Exit(1)
}

func dumpRows(a *engine.Answer) string {
	var b strings.Builder
	b.WriteString(strings.Join(a.Columns, "|") + "\n")
	for _, r := range a.Rows {
		for i, c := range r {
			if i > 0 {
				b.WriteByte('|')
			}
			fmt.Fprintf(&b, "%v", c)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func scalar(rows [][]any) string {
	if len(rows) == 0 || len(rows[0]) == 0 || rows[0][0] == nil {
		return ""
	}
	return fmt.Sprintf("%v", rows[0][0])
}

func floatEq(a, b string) bool {
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea != nil || eb != nil {
		return a == b
	}
	d := fa - fb
	if d < 0 {
		d = -d
	}
	return d < 0.01
}

// runNode demonstrates the field-level rule engine: merge the same customer from
// two sources under conflict/require/alert rules.
func runNode(_ []string) {
	n := &nodes.Node{
		Priority: []string{"crm", "import"}, // crm wins source-priority conflicts
		Rules: []nodes.Rule{
			{Field: "email", Kind: nodes.KindConflict, Strategy: "latest"},
			{Field: "segment", Kind: nodes.KindConflict, Strategy: "source_priority"},
			{Field: "ltv", Kind: nodes.KindConflict, Strategy: "max"},
			{Field: "name", Kind: nodes.KindRequire},
			{Field: "ltv", Kind: nodes.KindAlert, Strategy: "range", Params: map[string]any{"max": 100000.0}},
			{Field: "email", Kind: nodes.KindAlert, Strategy: "changed"},
		},
	}
	existing := nodes.Source{Name: "crm", Time: "2024-01-01T00:00:00Z", Rec: map[string]string{
		"name": "Ada Lovelace", "email": "ada@old.com", "segment": "smb", "ltv": "5000",
	}}
	incoming := nodes.Source{Name: "import", Time: "2025-01-01T00:00:00Z", Rec: map[string]string{
		"name": "Ada Lovelace", "email": "ada@new.com", "segment": "enterprise", "ltv": "250000",
	}}

	merged, events := n.Merge(existing, incoming)
	fmt.Printf("existing (%s): %v\n", existing.Name, existing.Rec)
	fmt.Printf("incoming (%s): %v\n", incoming.Name, incoming.Rec)
	fmt.Println("merged:")
	for _, k := range []string{"name", "email", "segment", "ltv"} {
		fmt.Printf("  %-8s = %s\n", k, merged[k])
	}
	fmt.Println("events:")
	for _, e := range events {
		fmt.Printf("  [%s] %s/%s: %s\n", e.Severity, e.Field, e.Rule, e.Message)
	}
}

// newFlowEngine loads workflows from FILES (DI_FLOWS_DIR) and resolves their
// `ingest` sources from the sources manifest (DI_SOURCES). The platform binary
// carries NO domain flow logic — flows are data supplied by the example/customer.
func newFlowEngine(ctx context.Context, dsn string) (*flow.Engine, *warehouse.Warehouse) {
	wh, err := warehouse.OpenPostgres(ctx, dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	e := flow.NewEngine(wh)
	sourcesPath := envOr("DI_SOURCES", "examples/meridian/sources.yaml")
	deps := flow.Deps{ResolveSource: func(name string) (connectors.Source, error) {
		man, err := connectors.LoadManifest(sourcesPath)
		if err != nil {
			return nil, err
		}
		return man.BuildByName(name)
	}}
	flows, err := flow.LoadDir(envOr("DI_FLOWS_DIR", "examples/meridian/flows"), deps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: load flows: %v\n", err)
	}
	for name, steps := range flows {
		e.Register(name, steps)
	}
	return e, wh
}

func runFlow(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di flow <run|approve|reject|rollback|list|show> ...")
		os.Exit(2)
	}
	ctx := context.Background()
	sub, rest := argv[0], argv[1:]

	switch sub {
	case "run":
		fs := flag.NewFlagSet("flow run", flag.ExitOnError)
		dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
		_ = fs.Parse(rest)
		e, wh := newFlowEngine(ctx, *dsn)
		defer wh.Close()
		name := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if name == "" {
			fmt.Fprintf(os.Stderr, "di flow run <name>  (loaded flows: %s)\n", strings.Join(e.Names(), ", "))
			os.Exit(2)
		}
		run, err := e.Start(ctx, name, map[string]any{})
		if err != nil {
			fail(err)
		}
		printRun(run)
	case "approve", "reject", "rollback", "show":
		if len(rest) < 1 {
			fmt.Fprintf(os.Stderr, "di flow %s <run-id>\n", sub)
			os.Exit(2)
		}
		e, wh := newFlowEngine(ctx, envOr("DI_DSN", defaultDSN))
		defer wh.Close()
		var run *flow.Run
		var err error
		switch sub {
		case "approve":
			run, err = e.Approve(ctx, rest[0])
		case "reject":
			run, err = e.Reject(ctx, rest[0])
		case "rollback":
			run, err = e.Rollback(ctx, rest[0])
		case "show":
			run, err = e.Get(ctx, rest[0])
		}
		if err != nil {
			fail(err)
		}
		printRun(run)
	case "list":
		e, wh := newFlowEngine(ctx, envOr("DI_DSN", defaultDSN))
		defer wh.Close()
		runs, err := e.List(ctx)
		if err != nil {
			fail(err)
		}
		for _, r := range runs {
			fmt.Printf("%-24s %-16s %s\n", r.ID, r.Flow, r.Status)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown flow subcommand %q\n", sub)
		os.Exit(2)
	}
}

func printRun(r *flow.Run) {
	fmt.Printf("run %s  flow=%s  status=%s  cursor=%d\n", r.ID, r.Flow, r.Status, r.Cursor)
	for k, v := range r.State {
		fmt.Printf("  state.%s = %v\n", k, v)
	}
	for _, s := range r.Steps {
		info := ""
		if s.Info != "" {
			info = "  (" + s.Info + ")"
		}
		fmt.Printf("  [%s] %s%s\n", s.Status, s.Name, info)
	}
}

func runMCP(argv []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	httpAddr := fs.String("http", "", "if set, serve over HTTP with bearer auth (e.g. :41955) instead of stdio")
	role := fs.String("role", envOr("DI_ROLE", "analyst"), "default role for local stdio (no token)")
	rps := fs.Float64("rps", 0, "per-principal rate limit (0 = off)")
	oidc := fs.Bool("oidc", false, "verify real JWTs via DI_OIDC_* (issuer/audience/JWKS or pubkey) instead of demo tokens")
	_ = fs.Parse(argv)

	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	opts := &mcpserver.Options{
		Default:    mcpserver.Principal{User: "local", Role: *role, Scopes: []string{"metrics:read", "data:write"}},
		RPS:        *rps, Burst: 5,
		ChecksPath: envOr("DI_CHECKS", "examples/meridian/conflicts.yaml"),
	}

	if *httpAddr != "" {
		// Remote: streamable HTTP behind bearer-token auth (per-user identity).
		verifier, note := mcpVerifier(*oidc)
		handler := mcpsdk.NewStreamableHTTPHandler(
			func(*http.Request) *mcpsdk.Server { return mcpserver.NewServer(eng, opts) }, nil)
		authed := auth.RequireBearerToken(verifier,
			&auth.RequireBearerTokenOptions{Scopes: []string{"metrics:read"}})(handler)
		// Continue any trace the client carried in (W3C traceparent header), so
		// server-side query spans nest under the caller's distributed trace.
		traced := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authed.ServeHTTP(w, r.WithContext(obs.ExtractHTTP(r.Context(), r.Header)))
		})
		fmt.Fprintf(os.Stderr, "dataintelligence MCP server on http %s (%s)\n", *httpAddr, note)
		if err := http.ListenAndServe(*httpAddr, traced); err != nil {
			fail(err)
		}
		return
	}

	srv := mcpserver.NewServer(eng, opts)
	fmt.Fprintln(os.Stderr, "dataintelligence MCP server on stdio (tools: list_metrics, get_dimensions, query_metric, ingest_csv)")
	if err := srv.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		fail(err)
	}
}

// mcpVerifier picks the token verifier: a real OIDC/JWT verifier when -oidc is
// set (config from DI_OIDC_ISSUER / DI_OIDC_AUDIENCE / DI_OIDC_JWKS|DI_OIDC_PUBKEY),
// else the demo verifier.
func mcpVerifier(useOIDC bool) (auth.TokenVerifier, string) {
	if !useOIDC {
		return mcpserver.DemoVerifier("dataintelligence"), "demo bearer auth; tokens: analyst-/finance-/admin-token"
	}
	cfg := mcpserver.OIDCConfig{
		Issuer:   os.Getenv("DI_OIDC_ISSUER"),
		Audience: envOr("DI_OIDC_AUDIENCE", "dataintelligence"),
		JWKSURL:  os.Getenv("DI_OIDC_JWKS"),
		KeyID:    os.Getenv("DI_OIDC_KID"),
	}
	if p := os.Getenv("DI_OIDC_PUBKEY"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			fail(err)
		}
		cfg.PublicKeyPEM = b
	}
	o, err := mcpserver.NewOIDC(cfg)
	if err != nil {
		fail(err)
	}
	src := cfg.JWKSURL
	if src == "" {
		src = "pubkey:" + os.Getenv("DI_OIDC_PUBKEY")
	}
	return o.Verifier(), fmt.Sprintf("OIDC: iss=%q aud=%q keys=%s", cfg.Issuer, cfg.Audience, src)
}

// runToken is the local dev issuer (a real IdP owns this in production):
//
//	di token gen-key -dir testdata/oidc           # RSA key + JWKS
//	di token mint -key testdata/oidc/priv.pem ...  # signed RS256 JWT
func runToken(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "di token <gen-key|mint> ...")
		os.Exit(2)
	}
	switch argv[0] {
	case "gen-key":
		fs := flag.NewFlagSet("gen-key", flag.ExitOnError)
		dir := fs.String("dir", "testdata/oidc", "output directory for priv.pem / pub.pem / jwks.json")
		kid := fs.String("kid", "k1", "key id")
		bits := fs.Int("bits", 2048, "RSA key size")
		_ = fs.Parse(argv[1:])
		priv, err := mcpserver.GenerateKey(*bits)
		if err != nil {
			fail(err)
		}
		if err := os.MkdirAll(*dir, 0o755); err != nil {
			fail(err)
		}
		pubPEM, err := mcpserver.MarshalPublicKeyPEM(&priv.PublicKey)
		if err != nil {
			fail(err)
		}
		jwks, err := mcpserver.JWKSJSON(*kid, &priv.PublicKey)
		if err != nil {
			fail(err)
		}
		writeFile(filepath.Join(*dir, "priv.pem"), mcpserver.MarshalPrivateKeyPEM(priv))
		writeFile(filepath.Join(*dir, "pub.pem"), pubPEM)
		writeFile(filepath.Join(*dir, "jwks.json"), jwks)
		fmt.Printf("wrote %s/{priv.pem,pub.pem,jwks.json} (kid=%s)\n", *dir, *kid)

	case "mint":
		fs := flag.NewFlagSet("mint", flag.ExitOnError)
		keyPath := fs.String("key", "testdata/oidc/priv.pem", "signing private key PEM")
		kid := fs.String("kid", "k1", "key id (must match JWKS)")
		iss := fs.String("iss", "https://idp.local/di", "issuer")
		aud := fs.String("aud", "dataintelligence", "audience (the MCP server)")
		sub := fs.String("sub", "alice", "subject (user id)")
		roleC := fs.String("role", "finance", "role claim")
		tenant := fs.String("tenant", "acme", "tenant claim")
		region := fs.String("region", "", "region claim (for region-scoped roles, e.g. manager)")
		scopes := fs.String("scopes", "metrics:read", "space- or comma-separated scopes")
		ttl := fs.Duration("ttl", time.Hour, "token lifetime")
		_ = fs.Parse(argv[1:])
		pemBytes, err := os.ReadFile(*keyPath)
		if err != nil {
			fail(err)
		}
		priv, err := mcpserver.ParsePrivateKeyPEM(pemBytes)
		if err != nil {
			fail(err)
		}
		now := time.Now()
		claims := map[string]any{
			"iss": *iss, "sub": *sub, "aud": *aud, "role": *roleC, "tenant": *tenant, "region": *region,
			"scope": strings.Join(strings.FieldsFunc(*scopes, func(r rune) bool { return r == ',' || r == ' ' }), " "),
			"iat":   now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(*ttl).Unix(),
		}
		tok, err := mcpserver.SignJWT(priv, *kid, claims)
		if err != nil {
			fail(err)
		}
		fmt.Println(tok)
	default:
		fmt.Fprintf(os.Stderr, "unknown token subcommand %q (gen-key|mint)\n", argv[0])
		os.Exit(2)
	}
}

func writeFile(path string, b []byte) {
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fail(err)
	}
}

// openWriteback builds the write-back engine over the warehouse + allowlist.
func openWriteback(ctx context.Context, model, dsn, schemaPath string) (*engine.Engine, *writeback.Engine) {
	eng, err := engine.New(ctx, model, dsn)
	if err != nil {
		fail(err)
	}
	sch, err := writeback.LoadSchema(schemaPath)
	if err != nil {
		fail(err)
	}
	return eng, &writeback.Engine{WH: eng.WH, Schema: sch, ModelPath: model}
}

// runPropose: NL → typed change proposal (generated, validated, dry-run, persisted
// pending). Nothing is applied — it must be approved by a different principal.
func runPropose(argv []string) {
	fs := flag.NewFlagSet("propose", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	schemaPath := fs.String("writeback", "models/writeback.yaml", "write-back allowlist YAML")
	role := fs.String("role", "analyst", "proposer role")
	_ = fs.Parse(argv)
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `di propose "mark order 1001 as refunded"`)
		os.Exit(2)
	}
	ctx := context.Background()
	eng, wb := openWriteback(ctx, *model, *dsn, *schemaPath)
	defer eng.Close()

	gen, err := writeback.NewGenerator(wb.Schema)
	if err != nil {
		fail(fmt.Errorf("propose needs an LLM (set LLM_*): %w", err))
	}
	catalog := metricCatalog(eng)
	prop, err := gen.Generate(ctx, question, catalog)
	if err != nil {
		fail(err)
	}
	saved, err := wb.Propose(ctx, writeback.Principal{User: "cli", Role: *role}, prop)
	if err != nil {
		fail(err)
	}
	printProposal(saved)
	fmt.Fprintf(os.Stderr, "\nproposed %s (pending). Approve with: di approve -role <approver> %s\n", saved.ID, saved.ID)
}

func runProposals(argv []string) {
	fs := flag.NewFlagSet("proposals", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	schemaPath := fs.String("writeback", "models/writeback.yaml", "write-back allowlist YAML")
	_ = fs.Parse(argv)
	ctx := context.Background()
	eng, wb := openWriteback(ctx, *model, *dsn, *schemaPath)
	defer eng.Close()

	if id := strings.TrimSpace(strings.Join(fs.Args(), " ")); id != "" {
		prop, err := wb.Get(ctx, id)
		if err != nil {
			fail(err)
		}
		printProposal(prop)
		return
	}
	list, err := wb.List(ctx, 50)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%-22s %-7s %-11s %-22s %s\n", "ID", "KIND", "STATUS", "PROPOSER", "SUMMARY")
	for _, p := range list {
		fmt.Printf("%-22s %-7s %-11s %-22s %s\n", p.ID, p.Kind, p.Status, p.Proposer, proposalSummary(p))
	}
}

func runWriteDecision(action string, argv []string) {
	fs := flag.NewFlagSet(action, flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	schemaPath := fs.String("writeback", "models/writeback.yaml", "write-back allowlist YAML")
	role := fs.String("role", "admin", "approver role")
	_ = fs.Parse(argv)
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintf(os.Stderr, "di %s -role <approver> <proposal-id>\n", action)
		os.Exit(2)
	}
	ctx := context.Background()
	eng, wb := openWriteback(ctx, *model, *dsn, *schemaPath)
	defer eng.Close()
	p := writeback.Principal{User: "cli", Role: *role}

	var prop *writeback.Proposal
	var err error
	switch action {
	case "approve":
		prop, err = wb.Approve(ctx, p, id)
	case "reject":
		prop, err = wb.Reject(ctx, p, id)
	case "revert":
		prop, err = wb.Rollback(ctx, p, id)
	}
	if err != nil {
		fail(err)
	}
	fmt.Printf("%s → %s\n", action, prop.Status)
	if prop.Note != "" {
		fmt.Printf("  %s\n", prop.Note)
	}
}

func printProposal(p *writeback.Proposal) {
	fmt.Printf("proposal %s  [%s · %s]\n", p.ID, p.Kind, p.Status)
	fmt.Printf("  request : %s\n", p.Question)
	if p.Rationale != "" {
		fmt.Printf("  rationale: %s\n", p.Rationale)
	}
	if p.Data != nil {
		fmt.Printf("  change  : %s %s set=%v where=%v\n", p.Data.Op, p.Data.Table, p.Data.Set, p.Data.Where)
	}
	if p.Model != nil {
		fmt.Printf("  model   : %s %s\n%s\n", p.Model.Kind, p.Model.Name, p.Model.YAML)
	}
	if p.Preview != nil {
		if p.Preview.SQL != "" {
			fmt.Printf("  sql     : %s  args=%v\n", p.Preview.SQL, p.Preview.Args)
		}
		fmt.Printf("  preview : affects %d row(s). %s\n", p.Preview.AffectedRows, p.Preview.Note)
		for i, r := range p.Preview.Before {
			if i >= 5 {
				fmt.Printf("            … (%d more)\n", len(p.Preview.Before)-5)
				break
			}
			fmt.Printf("            before: %v\n", r)
		}
	}
}

func proposalSummary(p *writeback.Proposal) string {
	if p.Data != nil {
		n := 0
		if p.Preview != nil {
			n = p.Preview.AffectedRows
		}
		return fmt.Sprintf("%s %s (%d rows)", p.Data.Op, p.Data.Table, n)
	}
	if p.Model != nil {
		return fmt.Sprintf("model %s %s", p.Model.Kind, p.Model.Name)
	}
	return p.Question
}

func metricCatalog(eng *engine.Engine) string {
	var b strings.Builder
	for i := range eng.Model.Metrics {
		m := &eng.Model.Metrics[i]
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
	}
	return b.String()
}

// runOBO demonstrates on-behalf-of identity propagation to the warehouse:
//
//	di obo setup                 # install the Postgres row-security policy on stores
//	di obo demo -region South    # prove the DB itself scopes a per-user session
//	di obo chain -token <jwt>    # full chain: verify → RFC 8693 exchange → DB session
func runOBO(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "di obo <setup|demo|chain> ...")
		os.Exit(2)
	}
	ctx := context.Background()
	switch argv[0] {
	case "setup":
		fs := flag.NewFlagSet("setup", flag.ExitOnError)
		dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
		file := fs.String("file", "deploy/schema/03_obo_rls.sql", "RLS policy SQL")
		_ = fs.Parse(argv[1:])
		wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
		if err != nil {
			fail(err)
		}
		defer wh.Close()
		sqlBytes, err := os.ReadFile(*file)
		if err != nil {
			fail(err)
		}
		if _, err := wh.Exec(ctx, string(sqlBytes)); err != nil {
			fail(err)
		}
		fmt.Println("installed warehouse RLS policy (stores_region_isolation, FORCE RLS)")

	case "demo":
		fs := flag.NewFlagSet("demo", flag.ExitOnError)
		dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
		region := fs.String("region", "South", "region to scope the session to")
		_ = fs.Parse(argv[1:])
		wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{AppRole: "di_app"})
		if err != nil {
			fail(err)
		}
		defer wh.Close()
		// A raw cross-region query — NO app-layer filter. Only the DB session
		// identity decides what comes back, proving warehouse-level enforcement.
		const raw = `SELECT region, count(*) FROM stores GROUP BY region ORDER BY region`
		admin, err := wh.QueryAs(ctx, warehouse.Session{User: "admin", Role: "admin"}, raw)
		if err != nil {
			fail(err)
		}
		mgr, err := wh.QueryAs(ctx, warehouse.Session{User: "mgr", Role: "manager", Region: *region}, raw)
		if err != nil {
			fail(err)
		}
		fmt.Printf("same SQL %q, only the DB session identity differs:\n", raw)
		fmt.Printf("  admin (app.region unset) → regions: %v\n", flatten(admin.Rows))
		fmt.Printf("  manager (app.region=%s)  → regions: %v\n", *region, flatten(mgr.Rows))

	case "chain":
		fs := flag.NewFlagSet("chain", flag.ExitOnError)
		token := fs.String("token", "", "caller JWT (from `di token mint`)")
		key := fs.String("key", "testdata/oidc/priv.pem", "warehouse-token signing key")
		kid := fs.String("kid", "k1", "key id")
		whAud := fs.String("warehouse-aud", "meridian-warehouse", "downstream warehouse audience")
		dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
		_ = fs.Parse(argv[1:])
		if *token == "" {
			fmt.Fprintln(os.Stderr, "di obo chain -token <jwt>")
			os.Exit(2)
		}
		// 1) Verify the caller token (same verifier the MCP server uses).
		verifier, _ := mcpVerifier(true)
		ti, err := verifier(ctx, *token, nil)
		if err != nil {
			fail(fmt.Errorf("verify caller token: %w", err))
		}
		fmt.Printf("1) verified caller: sub=%s role=%v region=%v\n", ti.UserID, ti.Extra["role"], ti.Extra["region"])
		// 2) RFC 8693 exchange → a warehouse-audience token + identity.
		priv, err := mcpserver.ParsePrivateKeyPEM(mustRead(*key))
		if err != nil {
			fail(err)
		}
		whTok, id, err := mcpserver.ExchangeToken(priv, *kid, "dataintelligence-mcp", *whAud, ti, 5*time.Minute)
		if err != nil {
			fail(err)
		}
		fmt.Printf("2) exchanged (RFC 8693) → warehouse token aud=%q sub=%s region=%s (%d-char JWT)\n", *whAud, id.User, id.Region, len(whTok))
		// 3) Open the DB session AS that identity and run a raw cross-region query.
		wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{AppRole: "di_app"})
		if err != nil {
			fail(err)
		}
		defer wh.Close()
		res, err := wh.QueryAs(ctx, warehouse.Session{User: id.User, Role: id.Role, Tenant: id.Tenant, Region: id.Region},
			`SELECT region, count(*) FROM stores GROUP BY region ORDER BY region`)
		if err != nil {
			fail(err)
		}
		fmt.Printf("3) warehouse session (app.region=%q) sees regions: %v\n", id.Region, flatten(res.Rows))

	default:
		fmt.Fprintf(os.Stderr, "unknown obo subcommand %q (setup|demo|chain)\n", argv[0])
		os.Exit(2)
	}
}

func flatten(rows [][]any) []string {
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%v=%v", r[0], r[len(r)-1]))
	}
	return out
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	return b
}

// runSource is the neutral, manifest-driven multi-source connector: it reads any
// configured source (mysql/mongo/redis/s3/kafka/csv) and can ingest it into the
// warehouse. The platform knows source TYPES; the manifest supplies the domain.
//
//	di source list   -manifest examples/meridian/sources.yaml
//	di source read   -manifest ... <name>
//	di source ingest -manifest ... <name> [-table _src_<name>]
func runSource(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: di source <list|read|ingest> -manifest <f> [name]")
		os.Exit(2)
	}
	sub := argv[0]
	fs := flag.NewFlagSet("source", flag.ExitOnError)
	manifest := fs.String("manifest", "examples/meridian/sources.yaml", "sources manifest YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN (for ingest)")
	table := fs.String("table", "", "target table for ingest (default _src_<name>)")
	limit := fs.Int("limit", 10, "rows to print for read")
	_ = fs.Parse(argv[1:])
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))

	man, err := connectors.LoadManifest(*manifest)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()

	if sub == "list" {
		fmt.Printf("%-18s %-10s\n", "NAME", "TYPE")
		for _, s := range man.Sources {
			fmt.Printf("%-18s %-10s\n", s.Name, s.Type)
		}
		return
	}
	if name == "" {
		fmt.Fprintf(os.Stderr, "di source %s needs a source name (one of: %s)\n", sub, strings.Join(man.Names(), ", "))
		os.Exit(2)
	}
	src, err := man.BuildByName(name)
	if err != nil {
		fail(err)
	}
	batch, err := src.Read(ctx)
	if err != nil {
		fail(err)
	}

	switch sub {
	case "read":
		fmt.Fprintf(os.Stderr, "-- %s: %d rows, fields=%d\n", name, len(batch.Rows), len(batch.Schema.Fields))
		printRecords(batch, *limit)
	case "ingest":
		tbl := *table
		if tbl == "" {
			tbl = "_src_" + name
		}
		wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
		if err != nil {
			fail(err)
		}
		defer wh.Close()
		n, err := connectors.Stage(ctx, wh, tbl, batch)
		if err != nil {
			fail(err)
		}
		fmt.Printf("ingested %d rows from source %q (%s) into %s\n", n, name, man.Spec(name).Type, tbl)
	default:
		fmt.Fprintf(os.Stderr, "unknown source subcommand %q (list|read|ingest)\n", sub)
		os.Exit(2)
	}
}

// printRecords prints up to n records as a table over the union of fields.
func printRecords(b connectors.Batch, n int) {
	cols := make([]string, 0, len(b.Schema.Fields))
	for _, f := range b.Schema.Fields {
		cols = append(cols, f.Name)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	for i, r := range b.Rows {
		if i >= n {
			break
		}
		cells := make([]string, len(cols))
		for j, c := range cols {
			cells[j] = truncCell(r[c], 28)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	w.Flush()
}

func truncCell(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// runWebhook starts the real-time push receiver: an external system (Twenty CRM)
// POSTs change events here; each is signature-checked and recorded to _crm_events.
func runWebhook(argv []string) {
	fs := flag.NewFlagSet("webhook", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	addr := fs.String("addr", envOr("DI_WEBHOOK_ADDR", ":34200"), "listen address")
	secret := fs.String("secret", os.Getenv("DI_WEBHOOK_SECRET"), "HMAC secret to verify deliveries")
	_ = fs.Parse(argv)

	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()

	srv := &connectors.WebhookServer{WH: wh, Secret: *secret, OnEvent: func(e connectors.WebhookEvent) {
		fmt.Fprintf(os.Stderr, "← webhook: %s %s id=%s verified=%s\n", e.Event, e.Object, e.RecordID, e.Verified)
	}}
	secNote := "unsigned (no secret)"
	if *secret != "" {
		secNote = "HMAC-verified"
	}
	fmt.Fprintf(os.Stderr, "CRM webhook receiver on %s  POST /webhook (%s) → _crm_events\n", *addr, secNote)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		fail(err)
	}
}

// runCRM syncs contacts from a real Twenty CRM into the warehouse and joins them
// to the existing Meridian customers on email — a real "CRM client" connector.
func runCRM(argv []string) {
	fs := flag.NewFlagSet("crm", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	base := fs.String("url", envOr("TWENTY_URL", "http://localhost:34100"), "Twenty base URL")
	key := fs.String("key", os.Getenv("TWENTY_API_KEY"), "Twenty API key (or TWENTY_API_KEY)")
	since := fs.String("since", "", "incremental: only sync People updated after this ISO8601 cursor")
	join := fs.Bool("join", true, "after sync, join CRM people to Meridian customers on email")
	_ = fs.Parse(argv)
	if *key == "" {
		fmt.Fprintln(os.Stderr, "di crm: set TWENTY_API_KEY (or -key)")
		os.Exit(2)
	}
	ctx := context.Background()
	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()
	src := &connectors.TwentyCRM{BaseURL: *base, APIKey: *key}

	var rows []connectors.Record
	if *since != "" {
		rows, err = src.Poll(ctx, *since)
	} else {
		var b connectors.Batch
		b, err = src.Read(ctx)
		rows = b.Rows
	}
	if err != nil {
		fail(err)
	}

	if _, err := wh.Exec(ctx, `CREATE TABLE IF NOT EXISTS _crm_people (
		crm_id text PRIMARY KEY, name text, first_name text, last_name text,
		email text, job_title text, city text, updated_at text, synced_at timestamptz DEFAULT now())`); err != nil {
		fail(err)
	}
	for _, r := range rows {
		if _, err := wh.Exec(ctx, `INSERT INTO _crm_people (crm_id,name,first_name,last_name,email,job_title,city,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (crm_id) DO UPDATE SET name=$2,first_name=$3,last_name=$4,email=$5,job_title=$6,city=$7,updated_at=$8,synced_at=now()`,
			r["crm_id"], r["name"], r["first_name"], r["last_name"], r["email"], r["job_title"], r["city"], r["updated_at"]); err != nil {
			fail(err)
		}
	}
	fmt.Printf("synced %d CRM people from %s into _crm_people\n", len(rows), *base)

	if !*join {
		return
	}
	// The payoff: CRM ⋈ warehouse on email. The CRM was seeded from Meridian
	// customers, so this enriches the warehouse's customers with CRM attributes.
	res, err := wh.Query(ctx, `
		SELECT c.customer_id, c.customer_name, c.segment, cp.job_title AS crm_segment, cp.crm_id
		FROM customers c JOIN _crm_people cp ON lower(c.email) = lower(cp.email)
		ORDER BY c.customer_id LIMIT 20`)
	if err != nil {
		fail(err)
	}
	cnt, _ := wh.Query(ctx, `SELECT count(*) FROM customers c JOIN _crm_people cp ON lower(c.email)=lower(cp.email)`)
	fmt.Printf("\n-- enrichment join: Meridian customers ⋈ CRM people on email (%s matched) --\n", scalar(cnt.Rows))
	printResult(res.Columns, res.Rows)
}

func runIngest(argv []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	csvPath := fs.String("csv", "", "path to a CSV file (required)")
	table := fs.String("table", "", "target table name (required)")
	fields := fs.String("fields", "", "target columns to map to (optional; default = cleaned CSV headers)")
	required := fs.String("required", "", "comma-separated target columns that must be non-empty")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	_ = fs.Parse(argv)
	if *csvPath == "" || *table == "" {
		fmt.Fprintln(os.Stderr, "di ingest: -csv and -table are required")
		os.Exit(2)
	}

	ctx := context.Background()
	src := &connectors.CSVSource{Path: *csvPath}
	schema, err := src.Discover(ctx)
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "-- discovered %d columns:\n", len(schema.Fields))
	for _, f := range schema.Fields {
		fmt.Fprintf(os.Stderr, "   %-16s %s\n", f.Name, f.Type)
	}

	plan := ingest.InferMapping(schema, *table, split(*fields))
	plan.Required = split(*required)
	fmt.Fprintln(os.Stderr, "-- mapping (source → target):")
	for _, fm := range plan.Fields {
		fmt.Fprintf(os.Stderr, "   %-16s → %-16s [%s]\n", fm.Source, fm.Target, fm.Type)
	}

	wh, err := warehouse.OpenPostgres(ctx, *dsn, warehouse.Options{})
	if err != nil {
		fail(err)
	}
	defer wh.Close()

	batch, err := src.Read(ctx)
	if err != nil {
		fail(err)
	}
	rep, err := ingest.Run(ctx, wh, batch, plan)
	if err != nil {
		fail(err)
	}
	fmt.Printf("ingested into %q: read=%d landed=%d skipped=%d\n", rep.Table, rep.RowsRead, rep.RowsLanded, rep.RowsSkipped)
	for _, d := range rep.Diff {
		fmt.Printf("  diff: %s\n", d)
	}
	for _, e := range rep.Errors {
		fmt.Printf("  check: %s\n", e)
	}
}

func runAsk(argv []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	role := fs.String("role", "analyst", "caller role (governance)")
	useCritic := fs.Bool("critic", true, "run the plan-query-critique loop: self-verify + bounded revise")
	retries := fs.Int("retries", 2, "max critic-driven revisions before graceful degradation")
	_ = fs.Parse(argv)
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `di ask: provide a question, e.g. di ask "net revenue by region"`)
		os.Exit(2)
	}

	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	dir, _ := os.MkdirTemp("", "di-ground-")
	defer os.RemoveAll(dir)
	g, err := grounding.New(ctx, eng.Model, filepath.Join(dir, "idx.db"))
	if err != nil {
		fail(err)
	}
	defer g.Close()
	if bank, err := grounding.LoadExemplars(ctx, "models/exemplars.yaml"); err == nil {
		g.WithExemplars(bank)
	}
	fmt.Fprintf(os.Stderr, "-- grounding=%s\n", g.Mode())
	principal := governance.Principal{User: "cli", Role: *role}

	if *useCritic {
		runAskWithCritic(ctx, eng, g, principal, question, *retries)
		return
	}

	q, retrieved, clarify, err := g.Ground(ctx, question)
	if err != nil {
		fail(err)
	}
	// Receipt: which metrics were retrieved (pruned context) — auditable.
	var rnames []string
	for _, r := range retrieved {
		rnames = append(rnames, r.Name)
	}
	fmt.Fprintf(os.Stderr, "-- retrieved top-%d=%v\n", len(rnames), rnames)

	if clarify != nil {
		fmt.Printf("clarify: %s\n  candidates: %s\n", clarify.Question, strings.Join(clarify.Candidates, ", "))
		return
	}
	fmt.Fprintf(os.Stderr, "-- grounded → metrics=%v group_by=%v grain=%q\n", q.Metrics, q.GroupBy, q.TimeGrain)

	ans, err := governance.Query(ctx, eng, q, principal, governance.DefaultPolicy())
	if err != nil {
		fail(err)
	}
	printAnswer(ans)
}

// runAskWithCritic runs the plan-query-critique loop: ground →
// govern+execute → critique (rule, then LLM) → revise or answer, bounded.
func runAskWithCritic(ctx context.Context, eng *engine.Engine, g *grounding.Grounder, p governance.Principal, question string, retries int) {
	chain := critic.Chain{Rule: critic.RuleCritic{}}
	if lc, err := critic.NewLLMCritic(); err == nil {
		chain.LLM = lc
	}
	loop := &critic.Loop{Gr: g, Eng: eng, Pol: governance.DefaultPolicy(), Critic: chain, MaxRetries: retries}

	res, err := loop.Resolve(ctx, question, p)
	if err != nil {
		fail(err)
	}

	// Trace: every plan-query-critique cycle, auditable.
	for _, a := range res.Attempts {
		v := a.Verdict
		if a.Feedback != "" {
			fmt.Fprintf(os.Stderr, "-- attempt %d (revised: %s)\n", a.N, a.Feedback)
		} else {
			fmt.Fprintf(os.Stderr, "-- attempt %d\n", a.N)
		}
		fmt.Fprintf(os.Stderr, "   plan → metrics=%v group_by=%v grain=%q\n", a.Query.Metrics, a.Query.GroupBy, a.Query.TimeGrain)
		detail := v.Feedback
		if detail == "" {
			detail = strings.Join(v.Reasons, "; ")
		}
		fmt.Fprintf(os.Stderr, "   critic[%s] → %s", v.By, v.Decision)
		if v.Dimension != "" {
			fmt.Fprintf(os.Stderr, " (%s)", v.Dimension)
		}
		if detail != "" {
			fmt.Fprintf(os.Stderr, ": %s", detail)
		}
		fmt.Fprintln(os.Stderr)
	}

	switch res.Outcome {
	case "answered":
		fmt.Fprintf(os.Stderr, "-- outcome: answered after %d attempt(s)\n", len(res.Attempts))
		printAnswer(res.Answer)
	case "clarify":
		fmt.Printf("clarify: %s\n", res.Clarify.Question)
		if len(res.Clarify.Candidates) > 0 {
			fmt.Printf("  candidates: %s\n", strings.Join(res.Clarify.Candidates, ", "))
		}
	case "refused":
		fmt.Printf("refused: %s\n", res.Note)
	case "gave_up":
		fmt.Printf("could not fully verify (%s). best-effort answer below:\n", res.Note)
		if res.Answer != nil {
			printAnswer(res.Answer)
		}
	}
}

// runChat is the conversational session with typed cross-turn memory: each line
// on stdin is a turn that refines (merge) or replaces (reset) the running state.
func runChat(argv []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	role := fs.String("role", "analyst", "caller role (governance)")
	useCritic := fs.Bool("critic", true, "verify each turn with the critic")
	_ = fs.Parse(argv)

	ctx := context.Background()
	eng, g := openGrounder(ctx, *model, *dsn)
	defer eng.Close()
	defer g.Close()

	sess := convo.New(g, eng, governance.DefaultPolicy(), governance.Principal{User: "cli", Role: *role})
	if *useCritic {
		chain := critic.Chain{Rule: critic.RuleCritic{}}
		if lc, err := critic.NewLLMCritic(); err == nil {
			chain.LLM = lc
		}
		sess.Critic = chain
	}
	fmt.Fprintf(os.Stderr, "-- chat (%s) · one question per line, Ctrl-D to end\n", g.Mode())

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		q := strings.TrimSpace(sc.Text())
		if q == "" {
			continue
		}
		res, err := sess.Ask(ctx, q)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		switch res.Kind {
		case "clarify":
			fmt.Printf("» %s\n  clarify: %s\n", q, res.Clarify.Question)
		case "refused":
			fmt.Printf("» %s\n  refused: %s\n", q, res.Note)
		default:
			fmt.Printf("» %s  [%s · state: metrics=%v group_by=%v grain=%q where=%d]\n",
				q, res.Kind, res.State.Metrics, res.State.GroupBy, res.State.TimeGrain, len(res.State.Where))
			printAnswer(res.Answer)
		}
	}
}

// runChain plans and executes a multi-metric question whose later step filters on
// an earlier step's result (explicit dependency edges, sub-result caching).
func runChain(argv []string) {
	fs := flag.NewFlagSet("chain", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	role := fs.String("role", "analyst", "caller role (governance)")
	_ = fs.Parse(argv)
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `di chain "refund total for the top region by revenue"`)
		os.Exit(2)
	}

	ctx := context.Background()
	eng, g := openGrounder(ctx, *model, *dsn)
	defer eng.Close()
	defer g.Close()
	sess := convo.New(g, eng, governance.DefaultPolicy(), governance.Principal{User: "cli", Role: *role})

	res, multi, err := sess.RunChain(ctx, question)
	if err != nil {
		fail(err)
	}
	if !multi {
		fmt.Fprintln(os.Stderr, "-- single-step question; use `di ask` instead")
		r, err := sess.Ask(ctx, question)
		if err != nil {
			fail(err)
		}
		printAnswer(r.Answer)
		return
	}
	for _, st := range res.Steps {
		fmt.Fprintf(os.Stderr, "-- step %s: metrics=%v group_by=%v where=%v", st.Step.ID, st.Query.Metrics, st.Query.GroupBy, st.Query.Where)
		if st.Picked != "" {
			fmt.Fprintf(os.Stderr, " → picked %s=%q", st.Step.Pick.Dimension, st.Picked)
		}
		fmt.Fprintln(os.Stderr)
	}
	if res.Note != "" {
		fmt.Println(res.Note)
	}
	if res.Final != nil {
		printAnswer(res.Final)
	}
}

// openGrounder builds an engine + grounder (with exemplars) — shared by ask/chat/chain.
func openGrounder(ctx context.Context, model, dsn string) (*engine.Engine, *grounding.Grounder) {
	eng, err := engine.New(ctx, model, dsn)
	if err != nil {
		fail(err)
	}
	dir, _ := os.MkdirTemp("", "di-ground-")
	g, err := grounding.New(ctx, eng.Model, filepath.Join(dir, "idx.db"))
	if err != nil {
		fail(err)
	}
	if bank, err := grounding.LoadExemplars(ctx, "models/exemplars.yaml"); err == nil {
		g.WithExemplars(bank)
	}
	return eng, g
}

func runQuery(argv []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	model := fs.String("model", "models/meridian.yaml", "semantic model YAML")
	dsn := fs.String("dsn", envOr("DI_DSN", defaultDSN), "warehouse DSN")
	metrics := fs.String("metrics", "", "comma-separated metrics (required)")
	by := fs.String("by", "", "comma-separated group-by dimensions")
	grain := fs.String("grain", "", "time grain (day|month|quarter|year)")
	limit := fs.Int("limit", 0, "row limit")
	role := fs.String("role", "analyst", "caller role (governance: RBAC + masking)")
	region := fs.String("region", "", "caller region attribute (row-level security for role=manager)")
	tenant := fs.String("tenant", "", "tenant id (per-tenant spend budget)")
	to := fs.String("to", "", "destination sink: log | json:path | table:name | alert:col:min:max")
	_ = fs.Parse(argv)

	if *metrics == "" {
		fmt.Fprintln(os.Stderr, "di query: -metrics is required")
		os.Exit(2)
	}
	ctx := context.Background()
	eng, err := engine.New(ctx, *model, *dsn)
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	principal := governance.Principal{User: "cli", Role: *role}
	if *region != "" || *tenant != "" {
		principal.Attrs = map[string]string{"region": *region, "tenant": *tenant}
	}
	pol := governance.DefaultPolicy()
	pol.TenantBudgetBytes = envBytes("DI_TENANT_BUDGET_BYTES") // 0 = unlimited
	ans, err := governance.Query(ctx, eng, semantic.Query{
		Metrics:   split(*metrics),
		GroupBy:   split(*by),
		TimeGrain: *grain,
		Limit:     *limit,
	}, principal, pol)
	if err != nil {
		fail(err)
	}
	printAnswer(ans)

	if *to != "" {
		sink, err := parseSink(eng.WH, *to)
		if err != nil {
			fail(err)
		}
		res, err := sink.Write(ctx, ans.Columns, ans.Rows)
		if err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "-- delivered to %s: %+v\n", sink.Name(), res)
	}
}

func parseSink(wh *warehouse.Warehouse, spec string) (destinations.Sink, error) {
	parts := strings.SplitN(spec, ":", 2)
	switch parts[0] {
	case "log":
		return destinations.LogSink{}, nil
	case "json":
		if len(parts) < 2 {
			return nil, fmt.Errorf("json sink needs a path: json:out.jsonl")
		}
		return destinations.JSONFileSink{Path: parts[1]}, nil
	case "table":
		if len(parts) < 2 {
			return nil, fmt.Errorf("table sink needs a name: table:foo")
		}
		return destinations.TableSink{WH: wh, Table: parts[1]}, nil
	case "alert":
		f := strings.Split(spec, ":") // alert:col:min:max
		if len(f) < 4 {
			return nil, fmt.Errorf("alert sink: alert:col:min:max")
		}
		mn, _ := strconv.ParseFloat(f[2], 64)
		mx, _ := strconv.ParseFloat(f[3], 64)
		return destinations.AlertSink{Column: f[1], Min: mn, Max: mx, HasMin: true, HasMax: true}, nil
	case "es":
		if len(parts) < 2 {
			return nil, fmt.Errorf("es sink needs an index: es:my_index")
		}
		return destinations.ESSink{URL: os.Getenv("DI_ES_URL"), Index: parts[1]}, nil // URL empty = dry-run
	case "snowflake":
		if len(parts) < 2 {
			return nil, fmt.Errorf("snowflake sink needs a table: snowflake:db.schema.table")
		}
		return destinations.SnowflakeSink{Table: parts[1]}, nil
	default:
		return nil, fmt.Errorf("unknown sink %q", parts[0])
	}
}

func printAnswer(ans *engine.Answer) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(ans.Columns, "\t"))
	for _, row := range ans.Rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = fmt.Sprintf("%v", c)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	w.Flush()
	if ans.TraceID != "" {
		fmt.Fprintf(os.Stderr, "\n-- trace=%s  compile=%dms execute=%dms rows=%d\n", ans.TraceID, ans.CompileMs, ans.ExecMs, len(ans.Rows))
	}
	fmt.Fprintf(os.Stderr, "-- compiled SQL --\n%s\n", ans.SQL)
}

// printResult renders a raw column/row result as a table.
func printResult(cols []string, rows [][]any) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = fmt.Sprintf("%v", c)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	w.Flush()
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envBytes reads a byte budget from the environment, 0 (unlimited) when unset.
func envBytes(k string) int64 {
	v := os.Getenv(k)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "di:", err)
	os.Exit(1)
}

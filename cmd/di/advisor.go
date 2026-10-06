package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/liliang-cn/dataintelligence/config"
	"github.com/liliang-cn/dataintelligence/consult"
	"github.com/liliang-cn/dataintelligence/copilot"
	"github.com/liliang-cn/dataintelligence/engine"
	"github.com/liliang-cn/dataintelligence/governance"
	"github.com/liliang-cn/dataintelligence/remotemcp"
	"github.com/liliang-cn/dataintelligence/runtime"
)

// advisor is the copilot, the consulting loop and the external MCP servers,
// built from one config. Both `di serve` and `di copilot -config` use it, so
// the agent a person talks to in the console and the one on the command line
// have the same tools and the same rules.
type advisor struct {
	remote  *remotemcp.Set
	store   *consult.Store
	consult *consult.Service
	cop     *copilot.Agent
	users   []runtime.StaticUser
}

func (a *advisor) Close() {
	if a.cop != nil {
		_ = a.cop.Close()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
	a.remote.Close()
}

func identity(id config.Identity, def string, engagement string) governance.Principal {
	p := governance.Principal{User: id.User, Role: id.Role, Attrs: id.Attrs, Engagement: engagement}
	if p.User == "" {
		p.User = def
	}
	if p.Role == "" {
		p.Role = "analyst"
	}
	if p.Attrs == nil {
		p.Attrs = map[string]string{}
	}
	return p
}

// staticUsers reads auth.users' tokens from the environment. A user whose
// token variable is empty cannot sign in, and finding that out at the first
// adoption is too late — so it stops the boot.
func staticUsers(cfg *config.Config) ([]runtime.StaticUser, error) {
	var out []runtime.StaticUser
	for _, u := range cfg.Auth.Users {
		tok := os.Getenv(u.TokenEnv)
		if tok == "" {
			return nil, fmt.Errorf("auth.users %q: token_env %s is empty", u.Name, u.TokenEnv)
		}
		out = append(out, runtime.StaticUser{Name: u.Name, Role: u.Role, Token: tok, Attrs: u.Attrs})
	}
	return out, nil
}

// buildAdvisor wires everything that hangs off the default engine. A nil or
// unmodelled engine gets neither the loop nor the copilot.
func buildAdvisor(ctx context.Context, cfg *config.Config, eng *engine.Engine, pol governance.Policy, checks string) (*advisor, error) {
	a := &advisor{}
	var err error
	if a.users, err = staticUsers(cfg); err != nil {
		return nil, err
	}
	var servers []remotemcp.Server
	for _, m := range cfg.Copilot.MCP {
		servers = append(servers, remotemcp.Server{Name: m.Name, URL: m.URL, TokenEnv: m.TokenEnv, Tools: m.Tools, Actions: m.Actions})
	}
	if len(servers) > 0 {
		if a.remote, err = remotemcp.New(servers); err != nil {
			return nil, err
		}
	}
	if eng == nil || !eng.Governed() {
		return a, nil
	}
	if !cfg.Consult.Disabled {
		if a.store, err = consult.OpenStore(cfg.ConsultStore(), cfg.Engagement); err != nil {
			return nil, err
		}
		a.consult = &consult.Service{
			Store: a.store, Model: eng.Model, Dialect: eng.Dialect,
			Query:         consult.Governed(eng, pol),
			MeasureAs:     identity(cfg.Consult.MeasureAs, "consult", cfg.Engagement),
			TimeDimension: cfg.Consult.TimeDimension,
			AdoptRoles:    cfg.Consult.AdoptRoles,
			AllowAsOf:     cfg.Consult.AllowAsOf,
			Audit: func(ctx context.Context, p governance.Principal, note string, refused bool) {
				p.Engagement = cfg.Engagement
				governance.AuditNote(ctx, eng, p, note, refused)
			},
		}
		if a.remote != nil {
			a.consult.Actions = a.remote
		}
	}
	if copilot.Available() {
		brief := ""
		if cfg.Copilot.BriefFile != "" {
			b, err := os.ReadFile(cfg.Path(cfg.Copilot.BriefFile))
			if err != nil {
				return nil, fmt.Errorf("copilot.brief_file: %w", err)
			}
			brief = string(b)
		}
		a.cop, err = copilot.New(ctx, eng, pol, copilot.Options{
			Principal:  identity(cfg.Copilot.Principal, "copilot", cfg.Engagement),
			ChecksPath: checks, Brief: brief, Consult: a.consult, Remote: a.remote,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "-- copilot disabled: %v\n", err)
		}
	}
	return a, nil
}

// checksFor is the health_check source: copilot.checks from a config, or — for
// the flag-driven `di serve` with no config — DI_CHECKS / the bundled example
// when that file exists.
func checksFor(cfg *config.Config, fromFile bool) string {
	if fromFile {
		return cfg.Path(cfg.Copilot.Checks)
	}
	p := envOr("DI_CHECKS", "examples/meridian/conflicts.yaml")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// acceptLoop accepts plans whose window has closed, every interval, until ctx
// ends. Each acceptance re-measures; nothing here takes anyone's word.
func (a *advisor) acceptLoop(ctx context.Context, every time.Duration) {
	if a.consult == nil {
		return
	}
	tick := func() {
		done, errs := a.consult.AcceptDue(ctx)
		for _, acc := range done {
			fmt.Fprintf(os.Stderr, "-- consult: accepted %s → %s: %s\n", acc.Plan, acc.Outcome, acc.Says)
		}
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "-- consult: acceptance failed: %v\n", err)
		}
	}
	go func() {
		t := time.NewTimer(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick()
				t.Reset(every)
			}
		}
	}()
}

func (a *advisor) describe() string {
	var parts []string
	if a.consult != nil {
		parts = append(parts, "consult on")
	}
	if a.cop != nil {
		parts = append(parts, fmt.Sprintf("copilot %d tools", len(a.cop.ToolNames())))
	}
	if n := a.remote.Names(); len(n) > 0 {
		parts = append(parts, "mcp "+strings.Join(n, ","))
	}
	if len(a.users) > 0 {
		parts = append(parts, fmt.Sprintf("%d static users", len(a.users)))
	}
	if len(parts) == 0 {
		return "off"
	}
	return strings.Join(parts, " · ")
}

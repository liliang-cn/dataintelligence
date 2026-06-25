package critic

import (
	"context"
	"testing"

	semantic "github.com/liliang-cn/semantic-go"

	"github.com/liliang-cn/dataintelligence/engine"
)

func model(t *testing.T) *semantic.Model {
	t.Helper()
	m, err := semantic.LoadFile("../models/meridian.yaml")
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	return m
}

func ans(cols []string, rows ...[]any) *engine.Answer {
	return &engine.Answer{Columns: cols, Rows: rows}
}

func TestRuleCriticCoverageBreakdown(t *testing.T) {
	m := model(t)
	in := Input{
		Question: "revenue by store region",
		Query:    semantic.Query{Metrics: []string{"total_revenue"}}, // missing group_by
		Model:    m,
		Answer:   ans([]string{"total_revenue"}, []any{100.0}),
	}
	v := RuleCritic{}.Critique(context.Background(), in)
	if v.Decision != Revise || v.Dimension != "coverage" {
		t.Fatalf("want revise/coverage, got %s/%s (%v)", v.Decision, v.Dimension, v.Reasons)
	}
}

func TestRuleCriticCoverageTime(t *testing.T) {
	m := model(t)
	in := Input{
		Question: "total revenue last quarter",
		Query:    semantic.Query{Metrics: []string{"total_revenue"}}, // no time grain/filter
		Model:    m,
		Answer:   ans([]string{"total_revenue"}, []any{100.0}),
	}
	v := RuleCritic{}.Critique(context.Background(), in)
	if v.Decision != Revise || v.Dimension != "coverage" {
		t.Fatalf("want revise/coverage for time, got %s/%s", v.Decision, v.Dimension)
	}
}

func TestRuleCriticMetricIdentity(t *testing.T) {
	m := model(t)
	in := Input{
		Question: "what is net revenue", // says "net revenue" → net_revenue
		Query:    semantic.Query{Metrics: []string{"total_revenue"}},
		Model:    m,
		Answer:   ans([]string{"total_revenue"}, []any{100.0}),
	}
	v := RuleCritic{}.Critique(context.Background(), in)
	if v.Decision != Revise || v.Dimension != "metric-identity" {
		t.Fatalf("want revise/metric-identity, got %s/%s (%v)", v.Decision, v.Dimension, v.Reasons)
	}
}

func TestRuleCriticSanityNegative(t *testing.T) {
	m := model(t)
	in := Input{
		Question: "total revenue",
		Query:    semantic.Query{Metrics: []string{"total_revenue"}},
		Model:    m,
		Answer:   ans([]string{"total_revenue"}, []any{-5.0}), // additive measure can't be negative
	}
	v := RuleCritic{}.Critique(context.Background(), in)
	if v.Decision != Revise || v.Dimension != "sanity" {
		t.Fatalf("want revise/sanity, got %s/%s (%v)", v.Decision, v.Dimension, v.Reasons)
	}
}

func TestRuleCriticEmptyAsksUser(t *testing.T) {
	m := model(t)
	in := Input{
		Question: "total revenue",
		Query:    semantic.Query{Metrics: []string{"total_revenue"}},
		Model:    m,
		Answer:   ans([]string{"total_revenue"}),
	}
	v := RuleCritic{}.Critique(context.Background(), in)
	if v.Decision != AskUser {
		t.Fatalf("empty result should ask_user, got %s", v.Decision)
	}
}

func TestRuleCriticPass(t *testing.T) {
	m := model(t)
	in := Input{
		Question: "total revenue by store region",
		Query:    semantic.Query{Metrics: []string{"total_revenue"}, GroupBy: []string{"store_region"}},
		Model:    m,
		Answer:   ans([]string{"store_region", "total_revenue"}, []any{"South", 100.0}, []any{"North", 200.0}),
	}
	v := RuleCritic{}.Critique(context.Background(), in)
	if v.Decision != Pass {
		t.Fatalf("want pass, got %s (%v)", v.Decision, v.Reasons)
	}
}

func TestChainRuleShortCircuits(t *testing.T) {
	m := model(t)
	// Rule critic fails → chain returns it without consulting the (nil) LLM.
	in := Input{
		Question: "revenue by region",
		Query:    semantic.Query{Metrics: []string{"total_revenue"}},
		Model:    m,
		Answer:   ans([]string{"total_revenue"}, []any{100.0}),
	}
	v := Chain{Rule: RuleCritic{}}.Critique(context.Background(), in)
	if v.Decision != Revise {
		t.Fatalf("chain should surface the rule revise, got %s", v.Decision)
	}
}

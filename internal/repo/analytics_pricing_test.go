package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

func TestAnalyticsPublicPricesAcrossCurrencies(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	checkAnalyticsPublicPrices(t, s)
}

func TestAnalyticsPublicPricesMySQL(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_BUSINESS_DELIVERY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local isolated MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	checkAnalyticsPublicPrices(t, s)
}

func checkAnalyticsPublicPrices(t *testing.T, s *Store) {
	id := fmt.Sprintf("priced-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, table := range []string{"run_nodes", "run_events", "runs"} {
			column := "run_id"
			if table == "runs" {
				column = "id"
			}
			if err := s.DB.Table(table).Where(column+" = ?", id).Delete(nil).Error; err != nil {
				t.Error(err)
			}
		}
	})
	run := domain.Run{ID: id, WorkflowID: "priced", ExecutionContext: domain.TelemetryContext{ProjectID: id}, Status: "succeeded", Usage: domain.TokenUsage{InputTokens: 2e6, OutputTokens: 2e6, TotalTokens: 4e6, CostUSD: .002, CostStatus: "reported"}, Nodes: map[string]*domain.NodeRun{
		"cn":  {Model: "glm-5.3-flash-ioa", Usage: domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, TotalTokens: 2e6, CostUSD: .001, CostStatus: "estimated"}},
		"usd": {Model: "claude-opus-4-6", Usage: domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, TotalTokens: 2e6, CostUSD: .001, CostStatus: "estimated"}},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "overview"} {
		got := s.Analytics(domain.AnalyticsFilter{Scope: scope, ProjectID: id})
		want := 3.6 + 30*7
		if math.Abs(got.Summary.TotalCostCNY-want) > 1e-8 || got.Summary.CostUnknownRuns != 0 || got.Summary.CostCurrency != "CNY" {
			t.Fatalf("scope %s: %+v", scope, got.Summary)
		}
		if len(got.Models) != 2 || math.Abs(got.Models[0].CostCNY+got.Models[1].CostCNY-want) > 1e-8 || got.RunTraces[0].CostCNY != got.Summary.TotalCostCNY {
			t.Fatalf("models/traces must reconcile: %+v", got)
		}
	}
	stored, _ := s.Run(run.ID)
	if stored.Usage.CostUSD != .002 || stored.Nodes["cn"].Usage.CostUSD != .001 {
		t.Fatal("analytics modified raw billing facts")
	}
	partial := run
	partial.Nodes = map[string]*domain.NodeRun{"cn": run.Nodes["cn"], "unknown": {Model: "unknown", Usage: run.Nodes["usd"].Usage}}
	priced := analyticsPricedRun(partial)
	if usageCostKnown(priced.Usage) || math.Abs(priced.Usage.CostUSD*7-3.6) > 1e-8 {
		t.Fatalf("partial recognized amount must survive with incomplete coverage: %+v", priced.Usage)
	}
}

func TestAnalyticsReportedCostsAndFallbackReconcile(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	u := domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, TotalTokens: 2e6}
	zero, reported := u, u
	zero.CostStatus = "reported"
	reported.CostStatus, reported.CostUSD = "reported", .25
	run := domain.Run{ID: "reported-priority", Status: "succeeded", WorkflowID: "reported-priority", ExecutionContext: domain.TelemetryContext{ProjectID: "reported-priority"},
		Usage: domain.TokenUsage{InputTokens: 3e6, OutputTokens: 3e6, TotalTokens: 6e6}, Nodes: map[string]*domain.NodeRun{
			"free":     {Model: "glm-5.3-flash", Usage: zero},
			"reported": {Model: "unknown-model", Usage: reported},
			"fallback": {Model: "claude-opus-4-6", Usage: u},
		}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "overview"} {
		out := s.Analytics(domain.AnalyticsFilter{ProjectID: "reported-priority", Scope: scope})
		want := (.25 + 30) * 7
		if math.Abs(out.Summary.TotalCostCNY-want) > 1e-9 || out.Summary.CostUnknownRuns != 0 {
			t.Fatalf("scope %q: %+v", scope, out.Summary)
		}
		modelCost := 0.0
		for _, model := range out.Models {
			modelCost += model.CostCNY
		}
		if math.Abs(modelCost-want) > 1e-9 || out.RunTraces[0].CostCNY != out.Summary.TotalCostCNY || out.ProjectUsage[0].CostCNY != out.Summary.TotalCostCNY {
			t.Fatalf("reported and estimated parts do not reconcile: %+v", out)
		}
	}
	stored, _ := s.Run(run.ID)
	if stored.Nodes["reported"].Usage.CostUSD != .25 || stored.Nodes["free"].Usage.CostStatus != "reported" {
		t.Fatal("analytics rewrote original billing facts")
	}
}

func TestAnalyticsCodeBuddyPlaceholderZeroFallsBack(t *testing.T) {
	u := domain.TokenUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostStatus: "reported"}
	run := domain.Run{Usage: u, Nodes: map[string]*domain.NodeRun{"work": {Provider: "codebuddy", Model: "claude-sonnet-4-6", Usage: u}}}
	got := analyticsPricedRun(run)
	if got.Usage.CostStatus != "estimated" || math.Abs(got.Usage.CostUSD*7-.00315) > 1e-10 {
		t.Fatalf("historical compatibility zero was treated as free: %+v", got.Usage)
	}
	if run.Nodes["work"].Usage.CostStatus != "reported" {
		t.Fatal("analytics modified the persisted source fact")
	}
}

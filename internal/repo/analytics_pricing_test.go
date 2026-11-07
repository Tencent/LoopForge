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
		"cn":  {Model: "glm-5.3-flash-ioa", Usage: domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, TotalTokens: 2e6, CostUSD: .001, CostStatus: "reported"}},
		"usd": {Model: "claude-opus-4-6", Usage: domain.TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, TotalTokens: 2e6, CostUSD: .001, CostStatus: "reported"}},
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

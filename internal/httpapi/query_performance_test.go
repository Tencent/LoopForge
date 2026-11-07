package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Log pages must never hydrate nodes, artifacts or snapshots. These handlers
// only need the run's existence and cursor metadata.
func TestLogQueriesDoNotHydrateRunView(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(domain.Run{ID: "query-run", Status: "succeeded", LogSequence: 1, TelemetrySequence: 1, Logs: []domain.LogRecord{{Sequence: 1, Body: "hello"}}, Telemetry: []domain.TelemetryEvent{{Sequence: 1, Message: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: &deps{repo: s}}
	g := gin.New()
	for _, path := range []string{"/api/runs/:id/logs", "/api/runs/:id/telemetry", "/api/runs/:id/logs.txt"} {
		g.GET(path, h.run)
	}
	if err := s.DB.Callback().Query().Before("gorm:query").Register("assert_no_run_view", func(tx *gorm.DB) {
		if tx.Statement.Table == "run_nodes" || tx.Statement.Table == "run_artifacts" {
			t.Errorf("page hydrated %s", tx.Statement.Table)
		}
		if tx.Statement.Table == "runs" && (len(tx.Statement.Selects) == 0 || strings.Contains(strings.Join(tx.Statement.Selects, ","), "snapshot")) {
			t.Error("page loaded full run header")
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"logs", "telemetry", "logs.txt"} {
		t.Run(suffix, func(t *testing.T) {
			rec := httptest.NewRecorder()
			g.ServeHTTP(rec, httptest.NewRequest("GET", "/api/runs/query-run/"+suffix, nil))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
				t.Fatalf("query failed: %d %s", rec.Code, rec.Body.String())
			}
			rec = httptest.NewRecorder()
			g.ServeHTTP(rec, httptest.NewRequest("GET", "/api/runs/missing/"+suffix, nil))
			if rec.Code != 404 {
				t.Fatalf("missing run: %d", rec.Code)
			}
		})
	}
}

func TestRunViewProjectionPreservesHTTPClipping(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, text := range []string{"", "short", strings.Repeat("x", 2500), strings.Repeat("x", 10000), strings.Repeat("中文测试", 3000)} {
		run := domain.Run{ID: "detail-query", Status: "succeeded", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Output: text, Error: text, Feedback: text, Activity: text, SessionID: "session"}}}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		view, ok := s.RunView(run.ID)
		if !ok {
			t.Fatal("missing view")
		}
		want := publicRun(run, false).Nodes["n"]
		got := publicRun(view, false).Nodes["n"]
		if *got != *want {
			t.Fatalf("detail clipping changed for %d bytes", len(text))
		}
		if len(view.Nodes["n"].Output) > 2501*4 {
			t.Fatal("view hydrated unbounded output")
		}
	}
}

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestRunWriteGuardDoesNotHydrateRunAggregate(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "guard-outsider"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("guard-outsider")
	runs := []domain.Run{
		{ID: "guard-personal", Kind: domain.RunKindWorkspace, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}},
		{ID: "guard-sop", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}},
	}
	for _, run := range runs {
		if err := h.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.DB.Callback().Query().Before("gorm:query").Register("assert_guard_header_only", func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "run_nodes", "run_events", "run_artifacts", "run_logs", "run_telemetry":
			t.Errorf("run access guard hydrated %s", tx.Statement.Table)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.repo.DB.Callback().Query().Remove("assert_guard_header_only") })
	for _, suffix := range []string{"stream", "logs", "telemetry", "workspace/tree", "workspace/file", "browser/stream", "preview"} {
		t.Run(suffix, func(t *testing.T) {
			g := gin.New()
			g.Use(h.deps.runWriteGuard())
			g.GET("/api/runs/:id/"+suffix, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			for _, run := range runs {
				for _, user := range []domain.User{owner, other} {
					req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/"+suffix, nil)
					req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
					rec := httptest.NewRecorder()
					g.ServeHTTP(rec, req)
					want := http.StatusNoContent
					if run.IsWorkspace() && user.ID != owner.ID {
						want = http.StatusForbidden
					}
					if rec.Code != want {
						t.Fatalf("run %s user %d: got %d want %d", run.ID, user.ID, rec.Code, want)
					}
				}
			}
		})
	}
}

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

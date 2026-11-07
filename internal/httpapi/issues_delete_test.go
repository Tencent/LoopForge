package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestDeleteIssueEndpoint(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "ws"))), tracker.NewService(), "", "")
	for _, analyzing := range []bool{true, false} {
		issue := domain.Issue{ID: "delete-api", ProjectID: "platform", Title: "delete", Status: "backlog"}
		if analyzing {
			issue.Clarification.Status = "analyzing"
		}
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/issues/delete-api", nil))
		expected := http.StatusNoContent
		if analyzing {
			expected = http.StatusConflict
		}
		if rr.Code != expected {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/issues/delete-api", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("deleted issue GET=%d", rr.Code)
	}
}

func TestDeleteIssueExecutionHistory(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "cascade", true: "success blocks all cleanup"}[success], func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "ws"))), tracker.NewService(), "", "")
			if err := st.PutIssue(domain.Issue{ID: "history", ProjectID: "platform", Title: "history", Status: "backlog"}); err != nil {
				t.Fatal(err)
			}
			statuses := []string{"failed", "cancelled"}
			if success {
				statuses = append(statuses, "succeeded")
			}
			for _, status := range statuses {
				if err := st.PutTask(domain.Task{ID: status, IssueID: "history", RunID: status, Status: status}); err != nil {
					t.Fatal(err)
				}
				// The cancelled run has no issue context: deletion must find it via its Task.
				ctx := domain.TelemetryContext{IssueID: "history", TaskID: status}
				if status == "cancelled" {
					ctx.IssueID = ""
				}
				if err := st.PutRun(domain.Run{ID: status, Status: status, ExecutionContext: ctx, Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: status}}, Logs: []domain.LogRecord{{ID: status + "-log", Body: "log"}}, Events: []domain.RunEvent{{ID: status + "-event", RunID: status, Type: "test"}}, Artifacts: []domain.Artifact{{ID: status + "-artifact", RunID: status, Name: "test.txt", Content: "artifact"}}}); err != nil {
					t.Fatal(err)
				}
			}
			detail := requestJSON[struct {
				Issue issueDeletionView `json:"issue"`
			}](t, h, http.MethodGet, "/api/issues/history", nil, http.StatusOK)
			list := requestJSON[[]issueDeletionView](t, h, http.MethodGet, "/api/issues", nil, http.StatusOK)
			if detail.Issue.DeletionAllowed == success || len(list) != 1 || list[0].DeletionAllowed == success {
				t.Fatal("incorrect deletion affordance")
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/issues/history", nil))
			expected := http.StatusNoContent
			if success {
				expected = http.StatusConflict
			}
			if rr.Code != expected {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			for _, status := range statuses {
				_, runExists := st.Run(status)
				_, taskExists := st.Task(status)
				if runExists != success || taskExists != success {
					t.Fatalf("unexpected remaining history: run=%v task=%v", runExists, taskExists)
				}
			}
			if !success {
				for _, table := range []string{"run_nodes", "run_events", "run_telemetry", "run_logs", "run_artifacts", "run_evals"} {
					var n int64
					if err := st.DB.Table(table).Count(&n).Error; err != nil || n != 0 {
						t.Fatalf("remaining %s: %d %v", table, n, err)
					}
				}
			}
		})
	}
}

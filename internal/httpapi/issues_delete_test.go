package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestDeleteIssueEndpoint(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "ws"))), tracker.NewService(), "", "")
	issue := domain.Issue{ID: "delete-api", ProjectID: "platform", Title: "delete", Status: "backlog"}
	issue.Clarification.Status = "analyzing"
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?force=true"} {
		conflict := requestEmptyJSON[struct {
			Block *repo.IssueDeletionBlock `json:"deletionBlock"`
		}](t, h, http.MethodDelete, "/api/issues/delete-api"+query, http.StatusConflict)
		if conflict.Block == nil || conflict.Block.Code != "clarifying" {
			t.Fatalf("missing clarification block: %+v", conflict)
		}
	}
	issue.Clarification.Status = ""
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/issues/delete-api", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	requestEmptyJSON[map[string]string](t, h, http.MethodGet, "/api/issues/delete-api", http.StatusNotFound)
}

func TestDeleteIssueExecutionHistory(t *testing.T) {
	for _, withSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed and cancelled", true: "including success"}[withSuccess], func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			sandboxes := runtime.NewManager(filepath.Join(t.TempDir(), "ws"))
			h := New(st, engine.New(st, sandboxes), tracker.NewService(), "", "")
			const issueID, path = "force-delete-history", "/api/issues/force-delete-history"
			issue := domain.Issue{ID: issueID, ProjectID: "platform", Title: "history", Status: "backlog"}
			issue.Clarification.Summary = "summary"
			if err := st.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			if err := st.PutComment(domain.IssueComment{ID: issueID + "-comment", IssueID: issueID, Body: "comment"}); err != nil {
				t.Fatal(err)
			}
			statuses := []string{"failed", "cancelled"}
			if withSuccess {
				statuses = append(statuses, "succeeded")
			}
			runIDs := make([]string, 0, len(statuses))
			for _, status := range statuses {
				id := issueID + "-" + status
				runIDs = append(runIDs, id)
				if err := st.PutTask(domain.Task{ID: id, IssueID: issueID, RunID: id, Status: taskStatusFromRun(status)}); err != nil {
					t.Fatal(err)
				}
				ctx := domain.TelemetryContext{IssueID: issueID, TaskID: id}
				if status == "cancelled" {
					ctx.IssueID = "" // legacy task-linked run
				}
				if err := st.PutRun(domain.Run{
					ID: id, Status: status, ExecutionContext: ctx,
					Nodes:     map[string]*domain.NodeRun{"work": {NodeID: "work", Status: status}},
					Logs:      []domain.LogRecord{{ID: id + "-log", Sequence: 1, Body: "log"}},
					Events:    []domain.RunEvent{{ID: id + "-event", RunID: id, Type: "test"}},
					Artifacts: []domain.Artifact{{ID: id + "-artifact", RunID: id, Name: "test.txt", Content: "artifact"}},
				}); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(sandboxes.RunWorkspace(id), 0700); err != nil {
					t.Fatal(err)
				}
			}
			const unrelatedRun = "unrelated-to-force-delete"
			if err := st.PutRun(domain.Run{ID: unrelatedRun, Status: "succeeded", ExecutionContext: domain.TelemetryContext{IssueID: "other"}}); err != nil {
				t.Fatal(err)
			}
			detail := requestEmptyJSON[struct {
				Issue issueDeletionView `json:"issue"`
			}](t, h, http.MethodGet, path, http.StatusOK)
			list := requestEmptyJSON[[]issueDeletionView](t, h, http.MethodGet, "/api/issues", http.StatusOK)
			if len(list) != 1 {
				t.Fatalf("incorrect issue list: %+v", list)
			}
			for _, view := range []issueDeletionView{detail.Issue, list[0]} {
				if !view.DeletionAllowed || !view.DeletionRequiresForce || view.DeletionBlock != nil {
					t.Fatalf("incorrect deletion affordance: %+v", view)
				}
			}
			conflict := requestEmptyJSON[struct {
				RequiresForce bool `json:"deletionRequiresForce"`
			}](t, h, http.MethodDelete, path, http.StatusConflict)
			if !conflict.RequiresForce {
				t.Fatal("missing force confirmation requirement")
			}
			const waitingRun = "force-delete-history-waiting"
			if err := st.PutRun(domain.Run{ID: waitingRun, Status: "waiting", ExecutionContext: domain.TelemetryContext{IssueID: issueID}}); err != nil {
				t.Fatal(err)
			}
			busy := requestEmptyJSON[struct {
				Block *repo.IssueDeletionBlock `json:"deletionBlock"`
			}](t, h, http.MethodDelete, path+"?force=true", http.StatusConflict)
			if busy.Block == nil || busy.Block.RunID != waitingRun {
				t.Fatalf("missing active run conflict: %+v", busy)
			}
			for _, id := range runIDs {
				_, hasRun := st.Run(id)
				_, hasTask := st.Task(id)
				_, err := os.Stat(sandboxes.RunWorkspace(id))
				if !hasRun || !hasTask || err != nil {
					t.Fatalf("conflict removed history for %s: run=%v task=%v workspace=%v", id, hasRun, hasTask, err)
				}
			}
			if err := st.PutRun(domain.Run{ID: waitingRun, Status: "cancelled", ExecutionContext: domain.TelemetryContext{IssueID: issueID}}); err != nil {
				t.Fatal(err)
			}
			runIDs = append(runIDs, waitingRun)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path+"?force=true", nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			requestEmptyJSON[map[string]string](t, h, http.MethodGet, path, http.StatusNotFound)
			if comments := st.Comments(issueID); len(comments) != 0 {
				t.Fatalf("remaining comments: %+v", comments)
			}
			for _, id := range runIDs {
				_, hasRun := st.Run(id)
				_, hasTask := st.Task(id)
				_, err := os.Stat(sandboxes.RunWorkspace(id))
				if hasRun || hasTask || !os.IsNotExist(err) {
					t.Fatalf("remaining history for %s: run=%v task=%v workspace=%v", id, hasRun, hasTask, err)
				}
			}
			for _, table := range []string{"run_nodes", "run_events", "run_telemetry", "run_logs", "run_artifacts", "run_evals"} {
				var n int64
				if err := st.DB.Table(table).Where("run_id IN ?", runIDs).Count(&n).Error; err != nil || n != 0 {
					t.Fatalf("remaining %s: %d %v", table, n, err)
				}
			}
			if _, ok := st.Run(unrelatedRun); !ok {
				t.Fatal("force deleted an unrelated run")
			}
		})
	}
}

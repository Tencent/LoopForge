package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestCloseAndFilterIssues(t *testing.T) {
	root := t.TempDir()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := engine.New(st, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(st, e, tracker.NewService(), "", "")

	now := time.Now().UTC()
	if err := st.PutProject(domain.Project{ID: "filter-proj", Name: "Filter Test Project", OwnerID: 0}); err != nil {
		t.Fatal(err)
	}

	iss1 := domain.Issue{
		ID:             "iss-close-1",
		ProjectID:      "filter-proj",
		Number:         101,
		Title:          "待规划任务1",
		Status:         "backlog",
		AssigneeUserID: 42,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	iss2 := domain.Issue{
		ID:        "iss-close-2",
		ProjectID: "filter-proj",
		Number:    102,
		Title:     "未指派任务2",
		Status:    "todo",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.PutIssue(iss1); err != nil {
		t.Fatalf("put issue 1: %v", err)
	}
	if err := st.PutIssue(iss2); err != nil {
		t.Fatalf("put issue 2: %v", err)
	}

	// 1. 测试按照 assignee 筛选
	filtered42 := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=filter-proj&assignee=42", nil, http.StatusOK)
	if len(filtered42) != 1 || filtered42[0].ID != "iss-close-1" {
		t.Fatalf("expected 1 issue for assignee=42, got %d", len(filtered42))
	}
	filteredUnassigned := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=filter-proj&assignee=unassigned", nil, http.StatusOK)
	if len(filteredUnassigned) != 1 || filteredUnassigned[0].ID != "iss-close-2" {
		t.Fatalf("expected 1 unassigned issue, got %d", len(filteredUnassigned))
	}

	// 2. 测试按照 status 筛选
	backlogIssues := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=filter-proj&status=backlog", nil, http.StatusOK)
	if len(backlogIssues) != 1 || backlogIssues[0].ID != "iss-close-1" {
		t.Fatalf("expected 1 backlog issue, got %d", len(backlogIssues))
	}
	todoIssues := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=filter-proj&status=todo", nil, http.StatusOK)
	if len(todoIssues) != 1 || todoIssues[0].ID != "iss-close-2" {
		t.Fatalf("expected 1 todo issue, got %d", len(todoIssues))
	}

	// 3. 测试手动关闭 POST /api/issues/:id/close
	closed := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+iss1.ID+"/close", map[string]any{}, http.StatusOK)
	if closed.Status != "closed" {
		t.Fatalf("expected closed status to be closed, got %s", closed.Status)
	}
	if closed.CloseSource != "manual" {
		t.Fatalf("manual close source = %q, want manual", closed.CloseSource)
	}
	if closed.AutomationState != "ignored" {
		t.Fatalf("expected automation state ignored, got %s", closed.AutomationState)
	}
	if closed.DeliveryState != "" {
		t.Fatalf("manual close must not forge delivery state, got %s", closed.DeliveryState)
	}
	if closed.DeliveryMRState != "" {
		t.Fatalf("manual close must not forge MR state, got %s", closed.DeliveryMRState)
	}
	if closed.DeliveryLockedLabel() != "关闭" {
		t.Fatalf("expected delivery locked label '关闭', got %s", closed.DeliveryLockedLabel())
	}
	if len(closed.Operations) != 1 || closed.Operations[0].Action != "close" {
		t.Fatalf("expected one close operation, got %+v", closed.Operations)
	}

	// 验证已关闭与已完成使用不同的数据库状态。
	doneIssues := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=filter-proj&status=done", nil, http.StatusOK)
	if len(doneIssues) != 0 {
		t.Fatalf("expected no closed issue in done status query, got %d", len(doneIssues))
	}
	closedIssues := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=filter-proj&status=closed", nil, http.StatusOK)
	if len(closedIssues) != 1 || closedIssues[0].ID != iss1.ID {
		t.Fatalf("expected 1 issue in closed status query, got %d", len(closedIssues))
	}

	// 4. 测试关闭后可以重新打开 POST /api/issues/:id/reopen
	reopened := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+iss1.ID+"/reopen", map[string]any{}, http.StatusOK)
	if reopened.Status != "todo" {
		t.Fatalf("expected reopened status to be todo, got %s", reopened.Status)
	}
	if reopened.CloseSource != "" {
		t.Fatalf("reopen must clear close source, got %q", reopened.CloseSource)
	}
	if reopened.DeliveryState != "" {
		t.Fatalf("reopening an undelivered issue must not forge delivery state, got %s", reopened.DeliveryState)
	}
	if len(reopened.Operations) != 2 || reopened.Operations[0].Action != "close" || reopened.Operations[1].Action != "reopen" {
		t.Fatalf("expected close then reopen operations, got %+v", reopened.Operations)
	}
}

func TestCloseIssueRejectsActiveWork(t *testing.T) {
	for _, status := range []string{"running", "waiting", "queued", "analyzing", "failed", "cancelled", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			issue := domain.Issue{ID: "close-boundary", ProjectID: "platform", Title: "closure boundary", Status: "backlog"}
			if status == "analyzing" {
				issue.Clarification.Status = "analyzing"
			}
			if err := st.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			if status != "analyzing" {
				task := domain.Task{ID: "close-task", IssueID: issue.ID, Status: status}
				if status != "queued" {
					task.RunID = "close-run"
					if err := st.PutRun(domain.Run{ID: task.RunID, Status: status}); err != nil {
						t.Fatal(err)
					}
				}
				if err := st.PutTask(task); err != nil {
					t.Fatal(err)
				}
			}
			h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "ws"))), tracker.NewService(), "", "")
			busy := status == "running" || status == "waiting" || status == "queued" || status == "analyzing"
			detail := requestJSON[struct {
				Issue issueDeletionView `json:"issue"`
			}](t, h, http.MethodGet, "/api/issues/close-boundary", nil, http.StatusOK)
			if detail.Issue.ClosureAllowed == busy {
				t.Fatal("incorrect close visibility")
			}
			expected := http.StatusOK
			if busy {
				expected = http.StatusConflict
			}
			requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/close-boundary/close", map[string]any{}, expected)
			saved, _ := st.Issue(issue.ID)
			if busy {
				if saved.Status != issue.Status || saved.CloseSource != "" {
					t.Fatal("rejected closure changed issue")
				}
			} else if saved.Status != "closed" || saved.CloseSource != "manual" {
				t.Fatal("settled issue not closed")
			}
		})
	}
}

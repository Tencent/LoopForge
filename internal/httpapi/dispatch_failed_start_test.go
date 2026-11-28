package httpapi

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// A dispatch whose LOOP cannot start must not leave the requirement blocked or
// a failed task behind: nothing ran, so the user has to be able to fix the LOOP
// and simply dispatch again. Scheduling relies on the same invariant, because a
// phantom task makes every later scheduled dispatch answer "该需求已经跑过 LOOP".
func TestDispatchUnknownWorkflowLeavesNoTrace(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	d := &deps{repo: f.s, engine: f.e}
	for _, tc := range []struct {
		name  string
		issue domain.Issue
		rerun bool
	}{
		{
			name:  "bound workflow",
			issue: domain.Issue{ID: "ghost-wf-bound", ProjectID: "platform", Title: "绑定到已删除 LOOP", Status: "todo", Kind: "issue", AssigneeUserID: f.user.ID, WorkflowID: "ghost-loop"},
		},
		{
			name:  "explicit workflow",
			issue: domain.Issue{ID: "ghost-wf-explicit", ProjectID: "platform", Title: "对话框选了已删除 LOOP", Status: "todo", Kind: "issue", AssigneeUserID: f.user.ID},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.s.PutIssue(tc.issue); err != nil {
				t.Fatal(err)
			}
			req := dispatchRequest{IssueID: tc.issue.ID, Rerun: tc.rerun}
			if tc.issue.WorkflowID == "" {
				req.WorkflowID = "ghost-loop"
			}
			_, err := dispatchIssue(d, req)
			if err == nil {
				t.Fatal("dispatch with an unknown LOOP succeeded")
			}
			if !strings.Contains(err.Error(), "LOOP") && !strings.Contains(err.Error(), "workflow") {
				t.Fatalf("error = %v", err)
			}
			stored, ok := f.s.Issue(tc.issue.ID)
			if !ok {
				t.Fatal("issue disappeared")
			}
			if stored.Status != "todo" {
				t.Errorf("issue status = %q, want todo (nothing ran)", stored.Status)
			}
			if stored.LatestTaskID != "" {
				t.Errorf("issue latest task = %q, want none", stored.LatestTaskID)
			}
			if f.s.IssueHasTask(tc.issue.ID) {
				t.Errorf("phantom task left for the issue: %+v", f.s.Tasks())
			}
		})
	}
}

// An engine error after the reservation (execution disabled) is the same
// user-visible situation: the run never existed, so the issue keeps its status
// and the reservation is released so a later dispatch is not treated as a rerun.
func TestDispatchStartFailureReleasesReservation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	// The fixture has no default LOOP. Create one on the fixture's own native
	// sandbox: the LOOP is valid, and the start fails deterministically because
	// the engine no longer accepts runs (Drain below), which is the same
	// user-visible situation: reserved, then no run exists.
	if _, ok := f.s.Workflow("default"); !ok {
		if err := f.s.PutWorkflow(domain.WorkflowDefinition{
			ID: "default", Name: "Default", ProjectID: "platform", Status: "published",
			SandboxProfileID: repo.TestNativeSandboxID,
			Nodes:            []domain.WorkflowNode{{ID: "step", Name: "Step", Type: "script", ScriptLanguage: "shell", Script: "true"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.PutIssue(domain.Issue{ID: "start-fail", ProjectID: "platform", Title: "启动失败", Status: "todo", Kind: "issue", AssigneeUserID: f.user.ID, WorkflowID: "default"}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: f.s, engine: f.e}
	if err := f.e.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: "start-fail"}); err == nil {
		t.Fatal("dispatch succeeded with a missing sandbox profile")
	}
	stored, _ := f.s.Issue("start-fail")
	if stored.Status != "todo" || stored.LatestTaskID != "" {
		t.Fatalf("after a failed start: status=%q latest=%q", stored.Status, stored.LatestTaskID)
	}
	if f.s.IssueHasTask("start-fail") {
		t.Fatalf("reservation kept after a failed start: %+v", f.s.Tasks())
	}
}

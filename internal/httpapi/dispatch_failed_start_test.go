package httpapi

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
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
	if _, ok := f.s.Workflow("default"); !ok {
		t.Skip("fixture has no default workflow")
	}
	if err := f.s.PutIssue(domain.Issue{ID: "start-fail", ProjectID: "platform", Title: "启动失败", Status: "todo", Kind: "issue", AssigneeUserID: f.user.ID, WorkflowID: "default"}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: f.s, engine: f.e}
	if err := f.e.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: "start-fail"}); err == nil {
		t.Fatal("dispatch succeeded while the engine drains")
	}
	stored, _ := f.s.Issue("start-fail")
	if stored.Status != "todo" || stored.LatestTaskID != "" {
		t.Fatalf("after a failed start: status=%q latest=%q", stored.Status, stored.LatestTaskID)
	}
	if f.s.IssueHasTask("start-fail") {
		t.Fatalf("reservation kept after a failed start: %+v", f.s.Tasks())
	}
}

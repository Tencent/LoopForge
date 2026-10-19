package repo

import (
	"agentflow/internal/domain"
	"testing"
)

func TestRestoreRunSOPIssueRollback(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale revision", true: "externally closed"}[blocked], func(t *testing.T) {
			s, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			issue := domain.Issue{ID: "sop-rollback", ProjectID: "p", Status: "done", DeliveryState: "merged"}
			if blocked {
				issue.Source = "tapd"
				issue.ExternalRef = &domain.ExternalRef{ExternalState: "closed"}
			}
			if err := s.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			run := domain.Run{ID: "sop-rollback", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "p", IssueID: issue.ID}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, Nodes: map[string]*domain.NodeRun{}}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			run, _ = s.RunState(run.ID)
			revision := run.Revision
			if !blocked {
				revision--
			}
			run.WorkflowSnapshot.CollaborationMode = ""
			run.PauseRequested = true
			if err := s.RestoreRunSOP(run, revision, false); err == nil {
				t.Fatal("invalid transition accepted")
			}
			after, _ := s.RunState(run.ID)
			got, _ := s.Issue(issue.ID)
			if after.WorkflowSnapshot.CollaborationMode != "group" || after.PauseRequested || !got.DeliveryLocked() {
				t.Fatalf("partial commit: %+v %+v", after, got)
			}
		})
	}
}

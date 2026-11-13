package httpapi

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// settleIssueTasks must not finish a Task while its terminal Run still has a
// node in flight (e.g. a cancel that has not yet stopped the running step).
func TestSettleIssueTasksWaitsForActiveNodes(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutIssue(domain.Issue{ID: "settle-issue", ProjectID: "platform", Title: "Settle", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	h := &tasksHandlers{deps: &deps{repo: s}}
	for _, active := range []string{"running", "pending_input"} {
		runID, taskID := "settle-run-"+active, "settle-task-"+active
		if err := s.PutRun(domain.Run{ID: runID, Status: "cancelled", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{
			"done": {NodeID: "done", Status: "succeeded"},
			"busy": {NodeID: "busy", Status: active},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutTask(domain.Task{ID: taskID, IssueID: "settle-issue", RunID: runID, Status: "running"}); err != nil {
			t.Fatal(err)
		}
		h.settleIssueTasks("settle-issue")
		if task, ok := s.Task(taskID); !ok || task.Status != "running" {
			t.Fatalf("%s node: task settled while run still active: %+v, found=%v", active, task, ok)
		}
		run, _ := s.Run(runID)
		run.Nodes["busy"].Status = "cancelled"
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		h.settleIssueTasks("settle-issue")
		if task, ok := s.Task(taskID); !ok || task.Status != "cancelled" {
			t.Fatalf("%s node: task not settled after nodes stopped: %+v, found=%v", active, task, ok)
		}
	}
}

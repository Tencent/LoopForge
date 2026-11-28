package httpapi

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"
)

func scheduledExecutionIDOnWire(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		ID string `json:"scheduleExecutionId"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields.ID
}

func TestScheduleExecutionAttributionSeparatesOccurrences(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	wf := domain.WorkflowDefinition{ID: "execution-attribution", Name: "Attribution", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "gate", Type: "approval"}}}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := &scheduleHandlers{deps: &deps{repo: st, engine: e}}
	svc := schedule.New(st, tracker.NewService(), nil)
	svc.SetRunDispatcher(h.dispatchScheduledRun)
	job := domain.ScheduleJob{ID: "attribution-job", Name: "same job", ProjectID: "platform", WorkflowID: wf.ID, Enabled: true, MaxDispatch: 1}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var firstTask domain.Task
	var executionIDs []string
	// The scheduler excludes issues that already have a task. Two occurrences
	// therefore dispatch different issues, and must retain distinct attribution.
	for _, issueID := range []string{"occurrence-one", "occurrence-two"} {
		issue := domain.Issue{ID: issueID, ProjectID: "platform", Title: issueID, Status: "todo", WorkflowID: wf.ID}
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
		finished, err := svc.RunNow(job.ID)
		if err != nil || finished.LastDispatched != 1 {
			t.Fatalf("schedule did not dispatch exactly once: %+v, %v", finished, err)
		}
		want := domain.ScheduleJobExecutionID(finished.LastTraceID)
		executionIDs = append(executionIDs, want)
		execution, ok := st.ScheduleJobExecution(want)
		if !ok || len(execution.Runs) != 1 {
			t.Fatalf("execution missing its downstream run: %+v", execution)
		}
		task, ok := st.Task(primaryTaskID(issueID, wf.ID))
		if !ok || task.RunID != execution.Runs[0].RunID {
			t.Fatalf("task/run link is missing: %+v", task)
		}
		t.Cleanup(func() { _ = e.Cancel(task.RunID) })
		run, ok := st.RunState(task.RunID)
		if !ok || run.ExecutionContext.TraceID != run.ID {
			t.Fatalf("existing Run trace contract changed: %+v", run.ExecutionContext)
		}
		if got := scheduledExecutionIDOnWire(t, task); got != want {
			t.Errorf("task execution = %q, want %q", got, want)
		}
		if got := scheduledExecutionIDOnWire(t, run.ExecutionContext); got != want {
			t.Errorf("run execution = %q, want %q", got, want)
		}
		if firstTask.ID == "" {
			firstTask = task
		}
	}
	if executionIDs[0] == executionIDs[1] {
		t.Fatal("separate occurrences reused the same execution")
	}
	retained, _ := st.Task(firstTask.ID)
	if got := scheduledExecutionIDOnWire(t, retained); got != executionIDs[0] {
		t.Errorf("second occurrence rewrote the first task's attribution: %q", got)
	}
	for _, legacy := range []bool{false, true} {
		id := "manual-no-attribution"
		if legacy {
			id = "legacy-no-trace"
		}
		issue := domain.Issue{ID: id, ProjectID: "platform", Title: id, Status: "todo", WorkflowID: wf.ID}
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
		var runID string
		if legacy {
			runID, err = h.dispatchScheduledRun(job, issue, "timer")
		} else {
			var task domain.Task
			task, err = dispatchIssue(h.deps, dispatchRequest{IssueID: id, WorkflowID: wf.ID})
			runID = task.RunID
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.Cancel(runID) })
		task, _ := st.Task(primaryTaskID(id, wf.ID))
		run, _ := st.RunState(runID)
		if scheduledExecutionIDOnWire(t, task) != "" || scheduledExecutionIDOnWire(t, run.ExecutionContext) != "" {
			t.Fatal("manual or legacy dispatch fabricated an execution ID")
		}
	}
}

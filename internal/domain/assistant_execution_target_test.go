package domain

import "testing"

func TestAssistantSessionExecutionDefaultsToCloud(t *testing.T) {
	if got := (AssistantSession{}).Execution(); got != (AssistantExecutionTarget{Pool: ExecutionModeCloud}) {
		t.Fatalf("legacy session execution = %+v, want cloud pool", got)
	}
	want := AssistantExecutionTarget{Pool: "runner", RunnerID: "r1"}
	if got := (AssistantSession{ExecutionTarget: &want}).Execution(); got != want {
		t.Fatalf("explicit execution = %+v, want %+v", got, want)
	}
}

func TestAssistantSessionExecutionLocked(t *testing.T) {
	if (AssistantSession{}).ExecutionLocked() {
		t.Fatal("an empty draft must still allow choosing the machine")
	}
	for name, s := range map[string]AssistantSession{
		"flag":           {ExecutionTargetLocked: true},
		"messages":       {Messages: []AssistantMessage{{Role: "user"}}},
		"run":            {RunID: "run-1"},
		"workspace":      {WorkspaceRunID: "ws-1"},
		"own workspaces": {OwnWorkspaceRunIDs: []string{"ws-2"}},
		"subagent":       {ParentSessionID: "parent"},
		"task":           {TaskID: "task-1"},
	} {
		if !s.ExecutionLocked() {
			t.Errorf("%s: session should lock its execution machine", name)
		}
	}
}

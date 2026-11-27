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

func TestClonedPageReferencesAreIndependent(t *testing.T) {
	source := []AssistantReference{{Element: &AssistantPageElement{Selector: "old", Rect: &ElementRect{Width: 12}, Viewport: &ElementRect{Height: 20}, Styles: map[string]string{"color": "red"}}}, {}}
	copied := CloneAssistantReferences(source)
	element := copied[0].Element
	element.Selector = "new"
	element.Rect.Width = 10
	element.Viewport.Height = 15
	element.Styles["color"] = "blue"
	original := source[0].Element
	if original.Selector != "old" || original.Rect.Width != 12 || original.Viewport.Height != 20 || original.Styles["color"] != "red" || copied[1].Element != nil {
		t.Fatalf("clone changed source reference: %+v", original)
	}
}

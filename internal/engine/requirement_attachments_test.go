package engine

import (
	"fmt"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestLocalLOOPRestagesRequirementAttachments(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	issue := domain.Issue{ID: "script-attachment-issue", ProjectID: "platform", Title: "Requirement document"}
	file := repo.IssueAttachment{ID: "spec", Name: "spec.md", MediaType: "application/octet-stream", Data: []byte("original document")}
	if err := store.CreateIssueWithAttachments(issue, []repo.IssueAttachment{file}); err != nil {
		t.Fatal(err)
	}
	rel := file.InputPath()
	readOriginal := fmt.Sprintf(`test "$(cat '%s')" = 'original document'`, rel)
	wf := domain.WorkflowDefinition{
		ID: "script-attachments", Name: "Requirement attachments", SandboxProfileID: "test-native",
		Nodes: []domain.WorkflowNode{
			{ID: "first", Name: "Read", Type: "script", ScriptLanguage: "shell", Script: readOriginal + fmt.Sprintf(" && rm '%s'", rel), MaxAttempts: 1},
			{ID: "second", Name: "Read again", Type: "script", ScriptLanguage: "shell", Script: readOriginal, MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "next", Source: "first", Target: "second", When: "success"}},
	}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(store, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{
		IssueID: issue.ID, ProjectID: issue.ProjectID,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, store, run.ID, "succeeded")
}

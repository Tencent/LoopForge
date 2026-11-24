package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/attachment"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestRequirementAttachmentsReachEverySOPNode(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := domain.IssueAttachment{AssistantAttachment: domain.AssistantAttachment{ID: strings.Repeat("a", 32), Name: "需求.md", Size: int64(len("实现附件里的需求")), MimeType: "text/markdown"}, UploadedBy: 7}
	dir := attachment.OwnerDir(7)
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, a.ID+".bin"), []byte("实现附件里的需求"), 0600)
	raw, _ := json.Marshal(attachment.Record{AssistantAttachment: a.AssistantAttachment, ProjectID: "p"})
	os.WriteFile(filepath.Join(dir, a.ID+".json"), raw, 0600)
	st.PutIssue(domain.Issue{ID: "requirement", ProjectID: "p", Attachments: []domain.IssueAttachment{a}})
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ProjectID: "p", IssueID: "requirement"}}
	e := &Engine{repo: st}
	files, err := e.requirementAttachments(run)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	workspace := t.TempDir()
	if err := materializeRequirementAttachments(workspace, files, "p"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(attachment.WorkspacePath(a.AssistantAttachment))))
	if err != nil || string(data) != "实现附件里的需求" {
		t.Fatalf("%v %s", err, data)
	}
	for _, id := range []string{"clarify", "design", "implement", "review", "test"} {
		prompt := renderPromptWithStore(st, domain.WorkflowDefinition{}, domain.WorkflowNode{ID: id}, run)
		if !strings.Contains(prompt, attachment.WorkspacePath(a.AssistantAttachment)) || !strings.Contains(prompt, "先读取") {
			t.Fatalf("%s missing requirement evidence: %s", id, prompt)
		}
	}
	run.ExecutionContext.ProjectID = "other"
	if _, err := e.requirementAttachments(run); err == nil {
		t.Fatal("cross project attachments accepted")
	}
}

func TestRequirementAttachmentsRequireCapableRunner(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.PutIssue(domain.Issue{ID: "i", ProjectID: "p", Attachments: []domain.IssueAttachment{{AssistantAttachment: domain.AssistantAttachment{ID: strings.Repeat("d", 32), Name: "spec.pdf", Size: 100}, UploadedBy: 1}}})
	run := domain.Run{ExecutionContext: domain.TelemetryContext{IssueID: "i", ProjectID: "p"}}
	e := &Engine{repo: st}
	st.PutRunner(domain.Runner{ID: "old", RunnerVersion: "0.12.0"})
	if _, err := e.runnerRequirementAttachments("old", run); err == nil || !strings.Contains(err.Error(), "0.13.0") {
		t.Fatalf("old runner was not blocked: %v", err)
	}
	st.PutRunner(domain.Runner{ID: "new", RunnerVersion: "0.13.0"})
	if files, err := e.runnerRequirementAttachments("new", run); err != nil || len(files) != 1 {
		t.Fatalf("new runner: %v %+v", err, files)
	}
}

func TestAttachmentOnlySOPReadsOriginalDocumentAcrossNodes(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	body := "# Requirement\nImplement the uploaded document without a typed description.\n"
	a := domain.IssueAttachment{AssistantAttachment: domain.AssistantAttachment{ID: strings.Repeat("e", 32), Name: "requirement.md", MimeType: "text/markdown", Size: int64(len(body))}, UploadedBy: 7}
	dir := attachment.OwnerDir(a.UploadedBy)
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, a.ID+".bin"), []byte(body), 0600)
	raw, _ := json.Marshal(attachment.Record{AssistantAttachment: a.AssistantAttachment, ProjectID: "platform"})
	os.WriteFile(filepath.Join(dir, a.ID+".json"), raw, 0600)
	st.PutIssue(domain.Issue{ID: "document-only-sop", ProjectID: "platform", Title: a.Name, Attachments: []domain.IssueAttachment{a}})
	path := attachment.WorkspacePath(a.AssistantAttachment)
	wf := domain.WorkflowDefinition{ID: "document-input-sop", Name: "Document input SOP", SandboxProfileID: "test-native", Nodes: []domain.WorkflowNode{
		{ID: "analyze", Type: "script", ScriptLanguage: "shell", Script: "cat " + path + " > analysis.md\nchmod u+w " + path + "\nprintf 'changed by prior node' > " + path, MaxAttempts: 1, ArtifactOutputs: []domain.ArtifactContract{{Name: "analysis", Path: "analysis.md", Required: true}}},
		{ID: "verify", Type: "script", ScriptLanguage: "shell", Script: "cmp " + path + " analysis.md && cat " + path + " > acceptance.md", MaxAttempts: 1, ArtifactOutputs: []domain.ArtifactContract{{Name: "acceptance", Path: "acceptance.md", Required: true}}},
	}, Edges: []domain.WorkflowEdge{{ID: "next", Source: "analyze", Target: "verify", When: "success"}}}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "platform", IssueID: "document-only-sop"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "succeeded")
	finished, _ := st.Run(run.ID)
	var found bool
	for _, a := range finished.Artifacts {
		if a.Name == "acceptance" {
			found = true
			if a.Content != body {
				t.Fatalf("document changed: %q", a.Content)
			}
		}
	}
	if !found {
		t.Fatal("downstream node did not read requirement document")
	}
}

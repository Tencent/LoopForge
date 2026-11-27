package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestNestedSOPWorkspaceReviewAndRestore(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.e.Drain(ctx)
	})
	project := domain.Project{ID: "sop-workspace-review", Name: "LOOP workspace review", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}

	repoNames := []string{"api", "site", "worker"}
	sources := t.TempDir()
	repositories := make([]domain.RepositoryContext, 0, len(repoNames))
	for _, name := range repoNames {
		dir := filepath.Join(sources, name)
		mustWriteWorkspaceFile(t, filepath.Join(dir, "source.txt"), "base\n")
		mustRunWorkspaceGit(t, dir, "init", "-b", "main")
		mustRunWorkspaceGit(t, dir, "add", "source.txt")
		mustRunWorkspaceGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--no-gpg-sign", "-m", "initial")
		repositories = append(repositories, domain.RepositoryContext{
			RepositoryID: name, Name: name, LocalPath: dir, BaseBranch: "main", WorkBranch: "feature/review",
		})
	}
	child := domain.WorkflowDefinition{
		ID: "workspace-child", ProjectID: project.ID, Name: "Child", SandboxProfileID: "test-native",
		Nodes: []domain.WorkflowNode{
			{ID: "edit", Name: "Edit", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
				Script: "set -e; for repo in api site worker; do printf 'change-%s\n' \"$repo\" > \"$repo/source.txt\"; done"},
			{ID: "done", Name: "Finish child", Type: "approval", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "edit-done", Source: "edit", Target: "done", When: "success"}},
	}
	parent := domain.WorkflowDefinition{
		ID: "workspace-parent", ProjectID: project.ID, Name: "Parent", SandboxProfileID: "test-native",
		Nodes: []domain.WorkflowNode{
			{ID: "child", Name: "Child", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1},
			{ID: "review", Name: "Review Git diffs", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
				Script: "set -e; for repo in api site worker; do git -C \"$repo\" diff HEAD -- source.txt | grep -F \"+change-$repo\"; done"},
			{ID: "gate", Name: "Wait for review", Type: "approval", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "child-review", Source: "child", Target: "review", When: "success"},
			{ID: "review-gate", Source: "review", Target: "gate", When: "success"},
		},
	}
	for _, workflow := range []domain.WorkflowDefinition{child, parent} {
		if err := f.s.PutWorkflow(workflow); err != nil {
			t.Fatal(err)
		}
	}
	parentRun, err := f.e.StartWithRepositoriesContext(parent.ID, "review changes", repositories, domain.TelemetryContext{
		ProjectID: project.ID, ResponsibleUserID: f.user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	waitRun := func(id string, ready func(domain.Run) bool) domain.Run {
		t.Helper()
		var run domain.Run
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			var found bool
			run, found = f.s.RunState(id)
			if found && ready(run) {
				return run
			}
			if run.Status == "failed" {
				break
			}
		}
		for _, node := range run.Nodes {
			t.Logf("%s: %s (%s)", node.NodeID, node.Status, node.Error)
		}
		t.Fatalf("run %s did not reach the expected state: %s", id, run.Status)
		return domain.Run{}
	}
	workspace := func(method, id, suffix string, want int) []byte {
		t.Helper()
		rec := cvBRequest(t, f, method, "/api/runs/"+id+"/workspace"+suffix, nil, true)
		if rec.Code != want {
			t.Fatalf("%s %s/workspace%s: got %d, want %d: %s", method, id, suffix, rec.Code, want, rec.Body)
		}
		return rec.Body.Bytes()
	}
	assertDiffs := func(id string) {
		t.Helper()
		raw := workspace(http.MethodGet, id, "", http.StatusOK)
		var info struct {
			Available, ReadOnly bool
			Repositories        []string
			Changes             []workspaceChange
		}
		if err := json.Unmarshal(raw, &info); err != nil || !info.Available || !info.ReadOnly || len(info.Repositories) != 3 {
			t.Fatalf("expected a readable shared workspace with three repositories: %s (%v)", raw, err)
		}
		diff := string(workspace(http.MethodGet, id, "/diff", http.StatusOK))
		for _, name := range repoNames {
			if !workspaceChangeExists(info.Changes, name+"/source.txt") || !strings.Contains(diff, "+change-"+name) {
				t.Fatalf("missing Git change for %s: %s", name, diff)
			}
		}
	}

	parentRun = waitRun(parentRun.ID, func(run domain.Run) bool { return run.Nodes["child"].ChildRunID != "" })
	childID := parentRun.Nodes["child"].ChildRunID
	waitRun(childID, func(run domain.Run) bool { return run.Nodes["done"].Status == "waiting_approval" })
	assertDiffs(childID)
	workspace(http.MethodPost, childID, "/use", http.StatusOK)
	if _, ok := f.s.WorkspaceCheckpoint(parentRun.ID); !ok {
		t.Fatal("browsing the child did not record workspace activity on the parent")
	}
	if _, ok := f.s.WorkspaceCheckpoint(childID); ok {
		t.Fatal("browsing created a separate workspace checkpoint on the child")
	}
	if err := f.e.Approve(childID, "done", ""); err != nil {
		t.Fatal(err)
	}
	waitRun(childID, func(run domain.Run) bool { return run.Status == "succeeded" && !run.WorkspaceCleanedAt.IsZero() })
	waitRun(parentRun.ID, func(run domain.Run) bool {
		return run.Nodes["review"].Status == "succeeded" && run.Nodes["gate"].Status == "waiting_approval"
	})
	assertDiffs(childID)
	assertDiffs(parentRun.ID)

	if err := f.e.ReleaseConversationWorkspace(parentRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.e.Approve(parentRun.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	waitRun(parentRun.ID, func(run domain.Run) bool { return run.Status == "succeeded" && !run.WorkspaceCleanedAt.IsZero() })
	for _, id := range []string{parentRun.ID, childID} {
		raw := workspace(http.MethodGet, id, "", http.StatusOK)
		var info struct {
			Available, Restorable bool
			CheckpointSavedAt     *time.Time
		}
		if err := json.Unmarshal(raw, &info); err != nil || info.Available || !info.Restorable || info.CheckpointSavedAt == nil {
			t.Fatalf("run %s should advertise the parent's saved workspace: %s (%v)", id, raw, err)
		}
		workspace(http.MethodGet, id, "/diff", http.StatusGone)
	}

	restorePath := "/api/runs/" + childID + "/workspace/restore"
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		request := httptest.NewRequest(http.MethodPost, restorePath, nil)
		request.Header.Set("Prefer", "respond-async")
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, authedRequest(t, request, f.user))
		if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
			break
		}
		var failure struct{ Code string }
		if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil || rec.Code != http.StatusConflict ||
			failure.Code != domain.WorkspaceErrorBusy || time.Now().After(deadline) {
			t.Fatalf("restoring through the child failed: %d %s", rec.Code, rec.Body)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		raw := workspace(http.MethodGet, childID, "/restore", http.StatusOK)
		var state domain.WorkspaceRestoreStatus
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.Status == "ready" {
			break
		}
		if state.Status != "restoring" || time.Now().After(deadline) {
			t.Fatalf("shared workspace did not restore: %s", raw)
		}
	}
	assertDiffs(parentRun.ID)
	assertDiffs(childID)
}

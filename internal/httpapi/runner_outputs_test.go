package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestValidateRunnerTaskOutputsRejectsEscapingPaths(t *testing.T) {
	for _, bad := range []string{"../x.md", "/etc/passwd", "a/../../x", "a\\..\\x", "."} {
		if msg := validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{Files: map[string]string{bad: "x"}}); msg == "" {
			t.Fatalf("file path %q accepted", bad)
		}
	}
	if msg := validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{Patches: map[string]string{"../svc": "x"}}); msg == "" {
		t.Fatal("patch dir escaping the workspace accepted")
	}
	big := strings.Repeat("x", 1<<20+1)
	if msg := validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{Files: map[string]string{"a.md": big}}); msg == "" {
		t.Fatal("oversized file accepted")
	}
	ok := &domain.RunnerTaskOutputs{Files: map[string]string{"svc/notes.md": "x"}, Patches: map[string]string{"": "p", "svc": "p"}}
	if msg := validateRunnerTaskOutputs(ok); msg != "" {
		t.Fatalf("valid outputs rejected: %s", msg)
	}
}

func TestLegacyRunnerCanCompleteWithMultipleInlineArtifacts(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	eng := engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	handler := New(store, eng, tracker.NewService(), "", "")
	id, session := registerTestRunner(t, handler)
	task := domain.RunnerTask{ID: "legacy-outputs", RunnerID: id, Provider: "mock", Status: domain.RunnerTaskQueued}
	if err := store.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimRunnerTask(id, []string{"mock"}, time.Now().UTC(), time.Minute); !ok || err != nil {
		t.Fatalf("could not claim legacy task: %v", err)
	}
	file := strings.Repeat("x", domain.MaxInlineRunnerArtifactBytes)
	result := requestWithHeaders(t, handler, http.MethodPost, "/api/runners/tasks/"+task.ID+"/complete", map[string]any{
		"status": domain.RunnerTaskSuccess, "outputs": domain.RunnerTaskOutputs{Files: map[string]string{"one.md": file, "two.md": file}},
	}, map[string]string{"Authorization": "Bearer " + session})
	if result.Code != http.StatusOK {
		t.Fatalf("complete: HTTP %d %s", result.Code, result.Body)
	}
	saved, ok := store.RunnerTask(task.ID)
	if !ok || saved.Status != domain.RunnerTaskSuccess || saved.Outputs == nil || saved.Outputs.Files["one.md"] != file || saved.Outputs.Files["two.md"] != file {
		t.Fatal("task did not persist both inline artifacts")
	}
}

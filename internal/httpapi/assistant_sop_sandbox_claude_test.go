package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// issue #1518: an account with only a Claude Code key must let the sandbox
// selector call Claude Code. The built-in Claude executor has ID "claude" but
// kind "claude-code"; passing the ID to the credential helpers silently
// dropped the key and fell back to generic-docker.
func TestAutomaticSOPSandboxUsesClaudeCodeCredential(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	const key = "claude-selector-test-key"
	if _, err := store.UpdateUserCredentials(1, repo.UserCredentialUpdate{
		CodeBuddyAPIKeyClear: true, CodexAPIKeyClear: true, ClaudeCodeAPIKey: key,
	}); err != nil {
		t.Fatal(err)
	}
	const projectID = "java-app"
	if err := store.PutProject(domain.Project{
		ID: projectID, Key: projectID, Name: "Java Project", OwnerID: 1,
		Description: "JDK 21 and Maven",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSandbox(domain.SandboxProfile{
		ID: "java-box", Name: "Java Box", ProjectID: projectID, Driver: domain.SandboxDriverDocker,
		Toolchain: "JDK 21 and Maven", Dockerfile: &domain.SandboxDockerfile{Content: "FROM eclipse-temurin:21"},
	}); err != nil {
		t.Fatal(err)
	}

	// A fake claude CLI that answers the selection prompt. It also proves the
	// key never leaks into the model input.
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	selector := `#!/bin/sh
for arg in "$@"; do input=$arg; done
case "$input" in
  *claude-selector-test-key*) exit 2 ;;
  *java-box*'JDK 21 and Maven'*) ;;
  *) exit 4 ;;
esac
printf '{"type":"result","result":"{\\"sandboxProfileId\\":\\"java-box\\"}"}\n'
`
	if err := os.WriteFile(bin, []byte(selector), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CLAUDE_BIN", bin)

	e := engine.New(store, runtime.NewManager(filepath.Join(dir, "workspace")))
	e.ConfigureExecution("api-only", "test", 30*time.Second, false)
	api := New(store, e, tracker.NewService(), "", "")
	wf := requestJSON[domain.WorkflowDefinition](t, api, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "claude-auto", Name: "Claude auto sandbox", ProjectID: projectID, AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", ScriptLanguage: "shell", Script: "true"}},
	}, http.StatusOK)
	run := requestJSON[domain.Run](t, api, http.MethodPost, "/api/runs", map[string]string{
		"workflowId": wf.ID, "projectId": projectID, "input": "Validate JDK 21 and Maven toolchain",
	}, http.StatusCreated)
	run = requestJSON[domain.Run](t, api, http.MethodGet, "/api/runs/"+run.ID, nil, http.StatusOK)

	if got := run.WorkflowSnapshot.SandboxProfileID; got != "java-box" {
		t.Fatalf("sandbox = %q, want java-box (Claude Code key must drive selection); events=%+v", got, run.Events)
	}
	if len(run.Events) != 1 || run.Events[0].Message != "Agent 选择沙箱 java-box" {
		t.Fatalf("events = %+v, want the agent-selection event", run.Events)
	}
}

// Control: with no model credentials the selector still falls back to the
// default sandbox exactly as before.
func TestAutomaticSOPSandboxFallbackWithoutCredentials(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	if _, err := store.UpdateUserCredentials(1, repo.UserCredentialUpdate{
		CodeBuddyAPIKeyClear: true, CodexAPIKeyClear: true, ClaudeCodeAPIKeyClear: true,
	}); err != nil {
		t.Fatal(err)
	}
	const projectID = "java-app"
	if err := store.PutProject(domain.Project{
		ID: projectID, Key: projectID, Name: "Java Project", OwnerID: 1,
		Description: "JDK 21 and Maven",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSandbox(domain.SandboxProfile{
		ID: "java-box", Name: "Java Box", ProjectID: projectID, Driver: domain.SandboxDriverDocker,
		Toolchain: "JDK 21 and Maven", Dockerfile: &domain.SandboxDockerfile{Content: "FROM eclipse-temurin:21"},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	e := engine.New(store, runtime.NewManager(filepath.Join(dir, "workspace")))
	e.ConfigureExecution("api-only", "test", 30*time.Second, false)
	api := New(store, e, tracker.NewService(), "", "")
	wf := requestJSON[domain.WorkflowDefinition](t, api, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "claude-fallback", Name: "No credential fallback", ProjectID: projectID, AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", ScriptLanguage: "shell", Script: "true"}},
	}, http.StatusOK)
	run := requestJSON[domain.Run](t, api, http.MethodPost, "/api/runs", map[string]string{
		"workflowId": wf.ID, "projectId": projectID, "input": "Validate JDK 21 and Maven toolchain",
	}, http.StatusCreated)
	run = requestJSON[domain.Run](t, api, http.MethodGet, "/api/runs/"+run.ID, nil, http.StatusOK)

	if got := run.WorkflowSnapshot.SandboxProfileID; got != repo.GenericDockerSandboxID {
		t.Fatalf("sandbox = %q, want %q fallback", got, repo.GenericDockerSandboxID)
	}
}

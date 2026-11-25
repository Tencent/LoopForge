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

func TestAutomaticSOPSandboxUsesEachProjectsToolchain(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const key = "selector-test-model-key"
	if _, err := store.UpdateUserCredentials(1, repo.UserCredentialUpdate{CodeBuddyAPIKey: key}); err != nil {
		t.Fatal(err)
	}
	projects := []struct{ id, name, toolchain, image string }{
		{"python-app", "Python Project", "Python 3.12 and uv", "python:3.12"},
		{"java-app", "Java Project", "JDK 21 and Maven", "eclipse-temurin:21"},
	}
	for _, project := range projects {
		if err := store.PutProject(domain.Project{ID: project.id, Key: project.id, Name: project.name, OwnerID: 1, Description: project.toolchain}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSandbox(domain.SandboxProfile{
			ID: project.id + "-box", Name: project.name, ProjectID: project.id, Driver: domain.SandboxDriverDocker,
			Toolchain: project.toolchain, Dockerfile: &domain.SandboxDockerfile{Content: "FROM " + project.image},
			Environment: map[string]string{"TEST_TOKEN": "environment-value-must-stay-private"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codebuddy")
	selector := `#!/bin/sh
for arg in "$@"; do input=$arg; done
if [ "$input" = -- ]; then input=$(cat); fi
case "$input" in
  *environment-value-must-stay-private*|*selector-test-model-key*|*'FROM '*) exit 2 ;;
  *python-app-box*java-app-box*|*java-app-box*python-app-box*) exit 3 ;;
  *python-app-box*'Python 3.12 and uv'*) id=python-app-box ;;
  *java-app-box*'JDK 21 and Maven'*) id=java-app-box ;;
  *) exit 4 ;;
esac
printf '{"type":"result","result":"{\\"sandboxProfileId\\":\\"%s\\"}"}\n' "$id"
`
	if err := os.WriteFile(bin, []byte(selector), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	e := engine.New(store, runtime.NewManager(filepath.Join(dir, "workspace")))
	e.ConfigureExecution("api-only", "test", 30*time.Second, false)
	api := New(store, e, tracker.NewService(), "", "")
	wf := requestJSON[domain.WorkflowDefinition](t, api, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "shared-auto", Name: "共享验证流程", ProjectID: "platform", Shared: true, AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", ScriptLanguage: "shell", Script: "true"}},
	}, http.StatusOK)
	if !wf.AutoSelectSandbox || wf.SandboxProfileID != "" {
		t.Fatalf("automatic workflow was saved with a fixed profile: %+v", wf)
	}
	for _, project := range projects {
		want := project.id + "-box"
		var run domain.Run
		if project.id == "python-app" {
			run = requestJSON[domain.Run](t, api, http.MethodPost, "/api/runs", map[string]string{
				"workflowId": wf.ID, "projectId": project.id, "input": "Validate toolchain. " + key,
			}, http.StatusCreated)
		} else {
			issue := domain.Issue{ID: "auto-sandbox-issue", ProjectID: project.id, Title: "Validate toolchain", Description: key, WorkflowID: wf.ID, AssigneeUserID: 1}
			if err := store.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			task := requestJSON[domain.Task](t, api, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]string{}, http.StatusCreated)
			run.ID = task.RunID
		}
		run = requestJSON[domain.Run](t, api, http.MethodGet, "/api/runs/"+run.ID, nil, http.StatusOK)
		if run.WorkflowSnapshot.SandboxProfileID != want || run.WorkflowSnapshot.SandboxSnapshot == nil || run.WorkflowSnapshot.SandboxSnapshot.Dockerfile == nil ||
			run.WorkflowSnapshot.SandboxSnapshot.Dockerfile.Content != "FROM "+project.image {
			t.Fatalf("project %s selected %q, want %q", project.id, run.WorkflowSnapshot.SandboxProfileID, want)
		}
		if len(run.Events) != 1 || run.Events[0].Type != domain.RunEventSandboxSelected || run.Events[0].Message != "Agent 选择沙箱 "+want {
			t.Fatalf("run detail events = %+v", run.Events)
		}
		stream, ok := store.RunStreamView(run.ID)
		if !ok {
			t.Fatal("run stream view missing")
		}
		events := publicLiveRun(stream).Events
		if len(events) != 1 || events[0] != run.Events[0] {
			t.Fatalf("run stream lost the sandbox selection: %+v", events)
		}
	}
}

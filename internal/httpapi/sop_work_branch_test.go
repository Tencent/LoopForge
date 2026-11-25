package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSOPWorkBranchNamingAcrossRepositories(t *testing.T) {
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const key = "branch-model-fixture-key"
	user, err := store.UpdateUserCredentials(1, repo.UserCredentialUpdate{
		CodeBuddyAPIKey: key, CodexAPIKey: "branch-personal-cli-fixture-key",
		AssistantDefaults: &domain.AssistantDefaults{Executor: "codex", Model: "personal-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.AssistantDefaults.Executor != "codex" {
		t.Fatal("test requires a different personal default CLI")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codebuddy")
	const generator = `#!/bin/sh
for arg in "$@"; do
  if [ "${previous:-}" = --model ]; then model=$arg; fi
  previous=$arg
  input=$arg
done
[ "${model:-}" = hy4std ] || exit 5
if [ "$input" = -- ]; then input=$(cat); fi
case "$input" in
  *branch-model-fixture-key*|*github.com*|*localhost*|*continued-repo*) exit 2 ;;
  *ai-repo*'payment timeout'*'use the dev namespace'*) ;;
  *) exit 3 ;;
esac
case "$input" in
  *'"feedback":'*) name=dev/payment-timeout-1234 ;;
  *) name=main ;;
esac
printf '{"type":"result","result":"{\\"branch\\":\\"%s\\"}"}\n' "$name"
`
	if err := os.WriteFile(bin, []byte(generator), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	e := engine.New(store, runtime.NewManager(filepath.Join(root, "workspaces")))
	e.ConfigureExecution("api-only", "test", 30*time.Second, false)
	api := New(store, e, nil, "", "")

	save := func(id, method string, naming *domain.WorkBranchNaming, status int) domain.Repository {
		cloneURL := "https://github.com/acme/" + id + ".git"
		if id == "ai-repo" {
			cloneURL = "https://localhost:1/acme/ai-repo.git"
		}
		return requestJSON[domain.Repository](t, api, method, "/api/repositories", domain.Repository{
			ID: id, ProjectID: "platform", Name: id, CloneURL: cloneURL, DefaultBranch: "main",
			WorkBranchNaming: naming,
		}, status)
	}
	ai := &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingAI, ExecutorID: "codebuddy", Model: "codebuddy:hy4std", Prompt: "use the dev namespace"}
	noPrefix := &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingPrefix}
	if saved := save("ai-repo", http.MethodPost, ai, http.StatusOK); saved.WorkBranchNaming == nil || saved.WorkBranchNaming.ExecutorID != "codebuddy" || saved.WorkBranchNaming.Model != "hy4std" {
		t.Fatalf("repository did not persist the selected CLI and raw model ID: %+v", saved.WorkBranchNaming)
	}
	save("prefix-repo", http.MethodPost, &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingPrefix, Prefix: "bugfix/"}, http.StatusOK)
	save("plain-repo", http.MethodPost, noPrefix, http.StatusOK)
	save("legacy-repo", http.MethodPost, nil, http.StatusOK)
	save("continued-repo", http.MethodPost, ai, http.StatusOK)
	if saved := save("plain-repo", http.MethodPut, nil, http.StatusOK); saved.WorkBranchNaming == nil || *saved.WorkBranchNaming != *noPrefix {
		t.Fatalf("older repository editor cleared no-prefix mode: %+v", saved.WorkBranchNaming)
	}
	save("bad-prefix", http.MethodPost, &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingPrefix, Prefix: "invalid//"}, http.StatusBadRequest)
	for _, naming := range []domain.WorkBranchNaming{
		{Mode: domain.WorkBranchNamingAI},
		{Mode: domain.WorkBranchNamingAI, ExecutorID: "mock", Model: "hy4std"},
		{Mode: domain.WorkBranchNamingAI, ExecutorID: "codebuddy", Model: "codex:personal-model"},
		{Mode: domain.WorkBranchNamingAI, ExecutorID: "codebuddy", Model: "codebuddy:default"},
	} {
		save("invalid-ai", http.MethodPost, &naming, http.StatusBadRequest)
	}

	workflow := requestJSON[domain.WorkflowDefinition](t, api, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "branch-naming", Name: "Branch naming", ProjectID: "platform", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", ScriptLanguage: "shell", Script: "true"}},
	}, http.StatusOK)
	issue := domain.Issue{
		ID: "branch-naming-issue", Number: 1234, ProjectID: "platform", Kind: "requirement", Source: "local",
		Title: "Fix payment timeout", Description: key, WorkflowID: workflow.ID, AssigneeUserID: 1,
		RepositoryIDs:      []string{"ai-repo", "prefix-repo", "plain-repo", "legacy-repo", "continued-repo"},
		RepositoryBranches: map[string]string{"continued-repo": "users/existing"},
	}
	if err := store.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	task := requestJSON[domain.Task](t, api, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]string{}, http.StatusCreated)
	run, ok := store.RunState(task.RunID)
	if !ok || len(run.Repositories) != 5 {
		t.Fatalf("dispatch did not persist all repository branches: %+v", run.Repositories)
	}
	byID := make(map[string]domain.RepositoryContext)
	for _, repository := range run.Repositories {
		byID[repository.RepositoryID] = repository
	}
	plain := byID["plain-repo"].WorkBranch
	if !strings.HasPrefix(plain, "requirement-1234-") ||
		byID["prefix-repo"].WorkBranch != "bugfix/"+plain ||
		byID["legacy-repo"].WorkBranch != "feature/"+plain {
		t.Fatalf("prefix generation did not use the repository's configuration: %+v", byID)
	}
	if got := byID["ai-repo"]; got.WorkBranch != "dev/payment-timeout-1234" || !got.CreateNewWorkBranch {
		t.Fatalf("AI candidate was not validated and frozen: %+v", got)
	}
	if got := byID["continued-repo"]; got.WorkBranch != "users/existing" || !got.RequireExistingWorkBranch {
		t.Fatalf("selected existing branch was replaced: %+v", got)
	}
	save("ai-repo", http.MethodPut, noPrefix, http.StatusOK)
	current := requestJSON[domain.Run](t, api, http.MethodGet, "/api/runs/"+run.ID, nil, http.StatusOK)
	if current.Repository.WorkBranch != run.Repository.WorkBranch || current.Repositories[0].WorkBranch != run.Repositories[0].WorkBranch {
		t.Fatalf("changing the naming configuration changed an existing run: %+v", current.Repositories)
	}
}

func TestSOPWorkBranchNamingRequiresSelectedModelOnSharedCodeBuddyKey(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{
		APIKey: "branch-shared-key-fixture", Models: []string{"another-model"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	// Model access is checked before invoking a CLI.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &assistantHandlers{deps: &deps{repo: store}}
	_, err = h.generateSOPWorkBranch(ctx, engine.SOPWorkBranchRequest{
		Execution: domain.TelemetryContext{ResponsibleUserID: 1, ProjectID: "platform"},
		Executor:  domain.AssistantDefaults{Executor: "codebuddy", Model: "hy4std"},
	})
	if err == nil || !strings.Contains(err.Error(), "所选模型未在平台共享 CodeBuddy Key 中启用") {
		t.Fatalf("expected rejection of the selected model, got %v", err)
	}
}

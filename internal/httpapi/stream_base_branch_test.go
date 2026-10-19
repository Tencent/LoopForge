package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestComposerClarifyCarriesAndValidatesBaseBranch(t *testing.T) {
	seen := make(chan domain.Issue, 1)
	setClarifyExecForTest(func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		seen <- issue
		parsed := map[string]any{"summary": "请补充验收标准", "ready": false}
		raw, _ := json.Marshal(parsed)
		return parsed, string(raw), map[string]any{"analyzed": false}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })
	st, h := clarifyTestServer(t)
	if err := st.PutRepository(domain.Repository{ID: "repo-branch", ProjectID: "platform", Name: "Repo", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "repositoryId": "repo-branch", "baseBranch": " feature/continue ", "message": "基于已有功能继续开发"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clarify status=%d: %s", rec.Code, rec.Body.String())
	}
	select {
	case issue := <-seen:
		if issue.BaseBranch != "feature/continue" || issue.RepositoryID != "repo-branch" {
			t.Fatalf("clarification lost selected branch: %+v", issue)
		}
	default:
		t.Fatal("clarification did not execute")
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/chat-clarify", map[string]any{
		"projectId": "platform", "repositoryId": "repo-branch", "baseBranch": "feature/x~1", "message": "非法分支",
	}, http.StatusBadRequest)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/chat-clarify", map[string]any{
		"projectId": "platform", "baseBranch": "feature/continue", "message": "没有仓库",
	}, http.StatusBadRequest)
}

func TestClarifySelectedBranchHasIsolatedCacheAndDoesNotUseStaleCodeOnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, _ := clarifyTestServer(t)
	if err := st.PutUser(domain.User{ID: 22, LoginName: "branch-owner", GitToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	mustRunWorkspaceGit(t, source, "init", "-b", "master")
	mustRunWorkspaceGit(t, source, "config", "user.name", "Test")
	mustRunWorkspaceGit(t, source, "config", "user.email", "test@example.com")
	writeCommit := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		mustRunWorkspaceGit(t, source, "add", ".")
		mustRunWorkspaceGit(t, source, "commit", "-m", content)
	}
	writeCommit("master")
	mustRunWorkspaceGit(t, source, "checkout", "-b", "feature/continue")
	writeCommit("feature")
	mustRunWorkspaceGit(t, source, "checkout", "master")
	repository := domain.Repository{ID: "branch-repo", Name: "Same repo", CloneURL: source, DefaultBranch: "master"}
	h := &streamHandlers{deps: &deps{repo: st}}
	featureDir, analyzed, reason := h.prepareClarifyRepo(context.Background(), repository, "feature/continue", 22, true)
	if !analyzed || featureDir == "" {
		t.Fatalf("selected branch preparation failed: %s", reason)
	}
	defaultDir, analyzed, reason := h.prepareClarifyRepo(context.Background(), repository, "", 22, true)
	if !analyzed || defaultDir == "" || defaultDir == featureDir {
		t.Fatalf("default and selected branches must use separate checkouts: %q %q %s", defaultDir, featureDir, reason)
	}
	content, err := os.ReadFile(filepath.Join(featureDir, "version.txt"))
	if err != nil || string(content) != "feature" {
		t.Fatalf("default clarification replaced feature code: %q, %v", content, err)
	}
	mustRunWorkspaceGit(t, source, "branch", "-D", "feature/continue")
	dir, analyzed, reason := h.prepareClarifyRepo(context.Background(), repository, "feature/continue", 22, true)
	if analyzed || dir != "" || reason == "" {
		t.Fatalf("failed selected branch fetch must not report stale checkout as analyzed: %q %t %q", dir, analyzed, reason)
	}
}

func TestClarifyLocalGitUsesConfiguredMainBranchWithoutToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, _ := clarifyTestServer(t)
	if err := st.PutUser(domain.User{ID: 23, LoginName: "local-branch-owner", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	mustRunWorkspaceGit(t, source, "init", "-b", "master")
	mustRunWorkspaceGit(t, source, "config", "user.name", "Test")
	mustRunWorkspaceGit(t, source, "config", "user.email", "test@example.com")
	writeCommit := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		mustRunWorkspaceGit(t, source, "add", ".")
		mustRunWorkspaceGit(t, source, "commit", "-m", content)
	}
	writeCommit("master code")
	mustRunWorkspaceGit(t, source, "checkout", "-b", "release/configured-main")
	writeCommit("configured main code")
	mustRunWorkspaceGit(t, source, "checkout", "master")
	repository := domain.Repository{ID: "local-default-repo", Name: "Same repo", LocalPath: source, CloneURL: "https://example.invalid/unneeded.git", DefaultBranch: "release/configured-main"}
	preparedSource, directSnapshot := clarifyRepositorySource(repository, "")
	if directSnapshot || preparedSource.CloneURL != source {
		t.Fatalf("local Git must be cloned, never analyzed from its current branch: %+v, direct=%v", preparedSource, directSnapshot)
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	configuredDir, analyzed, reason := h.prepareClarifyRepo(context.Background(), preparedSource, "", 23, true)
	if !analyzed || configuredDir == "" || configuredDir == source {
		t.Fatalf("configured local main branch requires a separate credential-free checkout: %q %t %q", configuredDir, analyzed, reason)
	}
	content, err := os.ReadFile(filepath.Join(configuredDir, "version.txt"))
	if err != nil || string(content) != "configured main code" {
		t.Fatalf("blank baseline ignored configured main branch: %q %v", content, err)
	}
	current, err := os.ReadFile(filepath.Join(source, ".git", "HEAD"))
	if err != nil || strings.TrimSpace(string(current)) != "ref: refs/heads/master" {
		t.Fatalf("analysis switched the user's source checkout: %q %v", current, err)
	}
	preparedSource.DefaultBranch = "master"
	masterDir, analyzed, reason := h.prepareClarifyRepo(context.Background(), preparedSource, "", 23, false)
	if !analyzed || masterDir == configuredDir {
		t.Fatalf("changed configured main branch reused the previous cache: %q %t %q", masterDir, analyzed, reason)
	}
	content, err = os.ReadFile(filepath.Join(masterDir, "version.txt"))
	if err != nil || string(content) != "master code" {
		t.Fatalf("changed main branch content is stale: %q %v", content, err)
	}
	preparedSource.ID = "another-repo-with-same-name"
	otherDir, analyzed, reason := h.prepareClarifyRepo(context.Background(), preparedSource, "", 23, false)
	if !analyzed || otherDir == masterDir {
		t.Fatalf("different repositories with equal names shared a cache: %q %t %q", otherDir, analyzed, reason)
	}

	// Resuming a blank-baseline analysis must announce the configured main
	// branch too; the CLI session may remember code from an earlier default.
	repository.ProjectID = "platform"
	if err := st.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	cliRoot := t.TempDir()
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(cliRoot, "sessions"))
	mustWriteClarifyFile(t, filepath.Join(clarifyCLIHome(23), ".codebuddy", "projects", "old-workspace", "old-session.jsonl"), "old conversation")
	bin := filepath.Join(cliRoot, "codebuddy")
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> '` + filepath.Join(cliRoot, "args") + `'
printf '%s\n' '{"type":"result","result":"{\"summary\":\"已确认需求\",\"ready\":true,\"questions\":[]}"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ProjectID: "platform", RepositoryID: repository.ID, AssigneeUserID: 23,
		Clarification: domain.ClarificationAssessment{SessionID: "old-session", SessionUserID: 23}}
	request := chatClarifyRequest{SessionID: "old-session", Message: "继续确认范围"}
	request.History = append(request.History, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: "继续确认范围"})
	if _, _, _, err := h.executeClarify(context.Background(), issue, request, nil); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(cliRoot, "args"))
	if err != nil || !strings.Contains(string(args), "--resume\n") || !strings.Contains(string(args), "Same repo: release/configured-main") {
		t.Fatalf("resumed analysis lost the configured main branch: %s %v", args, err)
	}
}

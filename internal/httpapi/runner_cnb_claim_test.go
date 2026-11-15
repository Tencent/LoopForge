package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

const (
	testCNBToken    = "cnb-fake-token-0000"
	testGlobalToken = "ghp_FAKEGLOBALPATxxxxxxxx"
)

func claimCNBTask(t *testing.T, version string) (domain.RunnerTask, *httpResponse, cvBHTTPFixture, string, string) {
	t.Helper()
	f := cvBNewHTTPFixture(t)
	actor := domain.User{LoginName: "cnb-delivery-actor"}
	if err := f.s.PutUser(actor); err != nil {
		t.Fatal(err)
	}
	actor, _ = f.s.UserByLoginName(actor.LoginName)
	if err := setCNBToken(f.s, actor.ID, testCNBToken); err != nil {
		t.Fatal(err)
	}
	runnerID, session := registerTestRunner(t, f.h)
	registered, _ := f.s.Runner(runnerID)
	now := time.Now().UTC()
	if err := f.s.RunnerHeartbeat(runnerID, registered.Runtimes, version, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{ID: "cnb-task", RunnerID: runnerID, Provider: "mock", Status: domain.RunnerTaskQueued, MaxAttempts: 1, Invocation: &domain.RunnerInvocation{
		GitHTTPSUserID: actor.ID,
		Repositories: []domain.RunnerRepository{
			{CloneURL: "https://cnb.cool/org/repo.git", ScopedHTTPS: true, GitToken: testGlobalToken},
			{CloneURL: "https://github.com/org/repo.git", GitToken: testGlobalToken},
		},
	}}
	if err := f.s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	resp := requestWithHeaders(t, f.h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"mock"}, "noWait": true}, map[string]string{"Authorization": "Bearer " + session})
	return task, &httpResponse{resp.Code, resp.Body.String(), resp.Header().Get("Cache-Control")}, f, session, runnerID
}

type httpResponse struct {
	code         int
	body         string
	cacheControl string
}

func TestOldRunnerClaimGetsNoGitCredentialsForCNBTask(t *testing.T) {
	task, resp, _, _, _ := claimCNBTask(t, "0.10.1")
	var result struct{ Task domain.RunnerTask }
	if err := json.Unmarshal([]byte(resp.body), &result); err != nil || resp.code != http.StatusOK || result.Task.ID != task.ID {
		t.Fatalf("claim failed (HTTP %d): %s", resp.code, resp.body)
	}
	if strings.Contains(resp.body, testCNBToken) || strings.Contains(resp.body, testGlobalToken) {
		t.Fatalf("an old runner received a token: %s", resp.body)
	}
	if len(result.Task.Invocation.GitHTTPSCredentials) != 0 || result.Task.Invocation.GitHTTPSUserID != 0 {
		t.Fatalf("old runner got CNB credentials: %+v", result.Task.Invocation)
	}
	for _, repository := range result.Task.Invocation.Repositories {
		if repository.GitToken != "" {
			t.Fatalf("old runner kept a Git token for %s", repository.CloneURL)
		}
	}
}

func TestCNBRunnerClaimGetsScopedCredentialUncached(t *testing.T) {
	task, resp, f, session, _ := claimCNBTask(t, domain.RunnerVersionCNB)
	var result struct{ Task domain.RunnerTask }
	if err := json.Unmarshal([]byte(resp.body), &result); err != nil || resp.code != http.StatusOK || result.Task.ID != task.ID {
		t.Fatalf("claim failed (HTTP %d): %s", resp.code, resp.body)
	}
	creds := result.Task.Invocation.GitHTTPSCredentials
	if len(creds) != 1 || creds[0].Token != testCNBToken || creds[0].Username != "cnb" || creds[0].BaseURL != "https://cnb.cool" {
		t.Fatalf("credentials = %+v", creds)
	}
	if resp.cacheControl != "no-store" || result.Task.Invocation.GitHTTPSUserID != 0 {
		t.Fatalf("Cache-Control=%q userId=%d", resp.cacheControl, result.Task.Invocation.GitHTTPSUserID)
	}
	if stored, _ := f.s.RunnerTask(task.ID); stored.Invocation == nil || len(stored.Invocation.GitHTTPSCredentials) != 0 {
		t.Fatal("claimed CNB token was persisted")
	}
	status := requestWithHeaders(t, f.h, http.MethodGet, "/api/runners/tasks/"+task.ID, nil, map[string]string{"Authorization": "Bearer " + session})
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), testCNBToken) || strings.Contains(status.Body.String(), "gitHttps") {
		t.Fatalf("runner status response must omit CNB credentials (HTTP %d)", status.Code)
	}
}

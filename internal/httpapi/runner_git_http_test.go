package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestSOPGitCredentialsAreHydratedOnlyOnAuthenticatedRunnerClaim(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	runner, _ := f.s.Runner(runnerID)
	runner.RunnerVersion = domain.RunnerVersionGitHTTP
	if err := f.s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	const host, token = "unfamiliar.example.org", "fake-runner-http-secret"
	if err := f.s.SaveGitHTTPCredential(f.user.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Host: host, Platform: domain.GitPlatformOther},
		Token:             token,
	}); err != nil {
		t.Fatal(err)
	}
	credentials := f.s.GitHTTPCredentialSecrets(context.Background(), f.user.ID)
	task := domain.RunnerTask{ID: "http-credential-task", RunnerID: runnerID, Provider: "mock", Invocation: &domain.RunnerInvocation{
		GitHTTPUserID: f.user.ID, GitHTTPCredentials: credentials,
	}}
	if err := f.s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	stored, _ := f.s.RunnerTask(task.ID)
	if stored.Invocation.GitHTTPUserID != f.user.ID || stored.Invocation.GitHTTPCredentials != nil {
		t.Fatal("the queue must persist only the responsible user reference")
	}
	public, _ := json.Marshal(domain.PublicRunnerTask(task))
	if strings.Contains(string(public), token) || strings.Contains(string(public), "gitHttpUserId") {
		t.Fatal("management responses exposed execution credentials")
	}
	if task.Invocation.GitHTTPCredentials[host].Token != token {
		t.Fatal("stripping public credentials mutated the execution task")
	}
	resp := requestWithHeaders(t, f.h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"}, "noWait": true,
	}, map[string]string{"Authorization": "Bearer " + session})
	var claimed struct {
		Task domain.RunnerTask `json:"task"`
	}
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &claimed) != nil {
		t.Fatalf("claim: HTTP %d %s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("Cache-Control") != "no-store" || claimed.Task.Invocation.GitHTTPUserID != 0 || claimed.Task.Invocation.GitHTTPCredentials[host].Token != token {
		t.Fatal("the authenticated claim lost the transient host credential")
	}
	stored, _ = f.s.RunnerTask(task.ID)
	if stored.Invocation.GitHTTPCredentials != nil {
		t.Fatal("hydrated credentials were written back into the queue")
	}
}

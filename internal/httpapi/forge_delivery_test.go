package httpapi

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
	"agentflow/internal/gitwork"
	"agentflow/internal/tracker"
)

func TestForgejoConversationPushAndDeliveryRefresh(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	// Go caches the system TLS roots. Exercise the production transport in a
	// subprocess with httptest's certificate added to its trust store.
	if os.Getenv("LOOPFORGE_TEST_FORGE_HTTPS") != "1" {
		probe := httptest.NewTLSServer(http.NotFoundHandler())
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: probe.Certificate().Raw})
		probe.Close()
		certFile := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(certFile, certificate, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestForgejoConversationPushAndDeliveryRefresh$")
		cmd.Env = append(os.Environ(), "LOOPFORGE_TEST_FORGE_HTTPS=1", "SSL_CERT_FILE="+certFile)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Forgejo delivery subprocess: %v\n%s", err, output)
		}
		return
	}
	t.Run("conversation workspace", func(t *testing.T) { testForgejoConversationPush(t, false) })
	t.Run("completed SOP workspace", func(t *testing.T) { testForgejoConversationPush(t, true) })
}

func testForgejoConversationPush(t *testing.T, borrowSOP bool) {
	f := cvBNewHTTPFixture(t)
	remote, privateKey, bare, knownHosts := localSSHGit(t, nil)
	t.Setenv(gitwork.SSHKnownHostsEnv, knownHosts)
	host, _ := gitwork.SSHHostScope(remote)
	if err := f.s.SaveGitSSHKey(f.user.ID, "", host, "Forgejo", privateKey); err != nil {
		t.Fatal(err)
	}
	const secret = "test-submitter-credential"
	const branch = "feature/forgejo-test"
	var created, merged atomic.Bool
	var calls atomic.Int32
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "token "+secret {
			t.Error("Forgejo API did not receive the submitter's token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const repository = "/api/v1/repos/team/app"
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/user":
			_, _ = w.Write([]byte(`{"id":13,"login":"submitter"}`))
		case "GET " + repository:
			_, _ = w.Write([]byte(`{"id":27,"full_name":"team/app"}`))
		case "GET " + repository + "/pulls":
			_, _ = w.Write([]byte("[]"))
		case "POST " + repository + "/pulls", "GET " + repository + "/pulls/4":
			if r.Method == http.MethodPost {
				created.Store(true)
				w.WriteHeader(http.StatusCreated)
			}
			output, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/"+branch).Output()
			if err != nil {
				t.Error("PR request arrived before Git had pushed its branch")
			}
			state := "open"
			var at *time.Time
			if merged.Load() {
				state = "closed"
				now := time.Now().UTC()
				at = &now
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 4, "state": state, "merged": merged.Load(), "merged_at": at,
				"head": map[string]any{"label": branch, "ref": "refs/pull/4/head", "repo_id": 27, "sha": strings.TrimSpace(string(output))},
				"base": map[string]any{"label": "main", "repo_id": 27},
			})
		default:
			t.Errorf("unexpected Forgejo request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	t.Setenv(forge.InstancesEnv, api.URL)
	tokenURL := fmt.Sprintf("/api/users/%d/forge-tokens", f.user.ID)
	res := cvBRequest(t, f, http.MethodPut, tokenURL, map[string]string{"instance": api.URL, "token": secret}, true)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), secret) {
		t.Fatalf("token verification: HTTP %d", res.Code)
	}
	webURL := api.URL + "/team/app"
	res = cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{
		"projectId": "platform", "provider": "ssh", "repoUrl": remote, "webUrl": webURL,
	}, true)
	var repository domain.Repository
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &repository) != nil {
		t.Fatalf("SSH binding: HTTP %d %s", res.Code, res.Body)
	}
	run := domain.Run{
		ID: "run-forgejo", Kind: domain.RunKindWorkspace, Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: f.user.ID},
		Repositories: []domain.RepositoryContext{{
			RepositoryID: repository.ID, Name: repository.Name, Provider: repository.Provider,
			CloneURL: repository.CloneURL, WebURL: repository.WebURL, GongfengProjectID: repository.GongfengProjectID,
			BaseBranch: "main", DefaultBranch: "main", WorkBranch: branch, CreateNewWorkBranch: true, WorkspaceDir: "app",
		}},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := f.e.MaterializeConversationWorkspace(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if borrowSOP {
		// Give the same checkout the run state a conversation borrows after an SOP.
		run, _ = f.s.RunState(run.ID)
		run.Kind, run.Status = "", "succeeded"
		run.WorkflowSnapshot = domain.WorkflowDefinition{ID: "delivery-sop", SandboxProfileID: "local"}
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	root, reason := f.e.LocalRunWorkspace(run)
	if reason != "" {
		t.Fatal(reason)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "app.txt"), []byte("Forgejo delivery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: &deps{repo: f.s, engine: f.e}}
	session := &AgentToolSession{workspaceRunID: run.ID, ProjectID: "platform", UserID: f.user.ID, User: f.user, Mode: AgentToolAssistant}
	type submission struct {
		IssueID string
		Results []workspaceMRResult
	}
	submit := func() submission {
		t.Helper()
		result, err := gateway.workspaceSubmitMR(ctx, session, map[string]any{"title": "Forgejo delivery"})
		var got submission
		if err != nil || json.Unmarshal([]byte(result), &got) != nil || len(got.Results) != 1 || strings.Contains(result, secret) {
			t.Fatalf("workspace submission = %s, %v", result, err)
		}
		return got
	}
	callsBeforeSubmit := calls.Load()
	first := submit()
	pushed := first.Results[0]
	if !pushed.Pushed || !pushed.PushOnly || pushed.Commit == "" || pushed.MRURL != "" || calls.Load() != callsBeforeSubmit {
		t.Fatalf("SSH submission should only push the work branch: %+v", pushed)
	}
	defaultBranch := "main"
	if borrowSOP {
		defaultBranch = "next" // A catalog edit must not retarget the existing workspace.
	}
	bindForgejo := func(clone, web string) {
		t.Helper()
		res := cvBRequest(t, f, http.MethodPut, "/api/repositories", map[string]string{
			"id": repository.ID, "projectId": "platform", "provider": "forgejo", "repoUrl": clone, "webUrl": web,
			"defaultBranch": defaultBranch,
		}, true)
		if res.Code != http.StatusOK {
			t.Fatalf("Forgejo binding: HTTP %d %s", res.Code, res.Body)
		}
	}
	if borrowSOP {
		bindForgejo(strings.Replace(remote, "/team/app.git", "/team/other.git", 1), api.URL+"/team/other")
		if other := submit().Results[0]; !other.PushOnly || other.MRURL != "" || calls.Load() != callsBeforeSubmit {
			t.Fatalf("a changed SSH remote must not enable Forgejo delivery for the old checkout: %+v", other)
		}
	}
	bindForgejo(remote, webURL)
	got := submit()
	if delivered := got.Results[0]; !created.Load() || !delivered.Pushed || delivered.PushOnly ||
		delivered.MRURL != webURL+"/pulls/4" || delivered.Commit != pushed.Commit || delivered.TargetBranch != "main" {
		t.Fatalf("retry did not open the Forgejo PR from the already-pushed commit against the original base: %+v", delivered)
	}
	if saved, _ := f.s.RunState(run.ID); saved.Repositories[0] != run.Repositories[0] {
		t.Fatal("Forgejo submission changed the frozen repository context")
	}
	if borrowSOP {
		return
	}
	if got.IssueID == "" || got.IssueID != first.IssueID {
		t.Fatalf("Forgejo retry did not keep the conversation requirement: %q, then %q", first.IssueID, got.IssueID)
	}
	pending, err := f.s.ListMRDeliveryStates()
	if err != nil || len(pending) != 1 || pending[0].ForgeUserID != f.user.ID {
		t.Fatalf("stored delivery = %+v, %v", pending, err)
	}
	merged.Store(true)
	svc := tracker.NewService()
	svc.ConfigureWithStore(f.s.SyncConfig(), f.s)
	svc.SyncMRDeliveryStatesOnce(f.s)
	issue, ok := f.s.Issue(got.IssueID)
	if !ok || issue.DeliveryMRState != "merged" || issue.Status != "done" {
		t.Fatalf("merged delivery did not update the issue: %+v", issue)
	}
}

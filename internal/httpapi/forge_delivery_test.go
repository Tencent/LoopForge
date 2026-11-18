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
	branchHead := func() string {
		output, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/"+branch).Output()
		if err != nil {
			t.Error("Forgejo request arrived before Git had pushed its branch")
		}
		return strings.TrimSpace(string(output))
	}
	pullResponse := func() map[string]any {
		state := "open"
		var at *time.Time
		if merged.Load() {
			state = "closed"
			now := time.Now().UTC()
			at = &now
		}
		return map[string]any{
			"number": 4, "state": state, "merged": merged.Load(), "merged_at": at,
			"head": map[string]any{"label": branch, "ref": "refs/pull/4/head", "repo_id": 27, "sha": branchHead()},
			"base": map[string]any{"label": "main", "repo_id": 27},
		}
	}
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
		case "GET " + repository + "/branches/" + branch:
			_ = json.NewEncoder(w).Encode(map[string]any{"name": branch, "commit": map[string]string{"id": branchHead()}})
		case "GET " + repository + "/pulls":
			if created.Load() {
				_ = json.NewEncoder(w).Encode([]any{pullResponse()})
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		case "POST " + repository + "/pulls", "GET " + repository + "/pulls/4", "PATCH " + repository + "/pulls/4":
			if r.Method == http.MethodPost {
				created.Store(true)
				w.WriteHeader(http.StatusCreated)
			}
			_ = json.NewEncoder(w).Encode(pullResponse())
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
		ID: "run-forgejo", Kind: domain.RunKindWorkspace, Status: "waiting", Generation: 1,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: f.user.ID},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if borrowSOP {
		// A completed SOP freezes its original SSH binding and branch choice.
		run.Kind, run.Status = "", "succeeded"
		run.WorkflowSnapshot = domain.WorkflowDefinition{ID: "delivery-sop", SandboxProfileID: "local"}
		run.ExecutionContext.IssueID = "forgejo-sop-issue"
		if err := f.s.PutIssue(domain.Issue{ID: run.ExecutionContext.IssueID, ProjectID: "platform", Title: "SOP delivery", Status: "in_progress"}); err != nil {
			t.Fatal(err)
		}
		run.Repositories = []domain.RepositoryContext{{
			RepositoryID: repository.ID, Name: repository.Name, Provider: repository.Provider,
			CloneURL: repository.CloneURL, WebURL: repository.WebURL, GongfengProjectID: repository.GongfengProjectID,
			BaseBranch: "main", DefaultBranch: "main", WorkBranch: branch, CreateNewWorkBranch: true, WorkspaceDir: "app",
		}}
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	root, reason := f.e.LocalRunWorkspace(run)
	if reason != "" {
		t.Fatal(reason)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sshCtx := gitwork.WithSSHRemote(gitwork.WithSSHKeys(ctx, map[string]string{remote: privateKey}), remote)
	if _, err := gitwork.C(sshCtx, root, "clone", remote, "app"); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "app")
	sshTestGit(t, checkout, "switch", "-c", branch)
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("Forgejo delivery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pushed, err := gitwork.PushWorkBranch(sshCtx, checkout, branch, "Forgejo delivery")
	if err != nil || pushed == "" || pushed != branchHead() {
		t.Fatalf("native Git push = %q, %v", pushed, err)
	}
	gateway := &agentToolGateway{deps: &deps{repo: f.s, engine: f.e}}
	session := &AgentToolSession{workspaceRunID: run.ID, ChatSessionID: "chat-forgejo", ProjectID: "platform",
		UserID: f.user.ID, User: f.user, Mode: AgentToolAssistant, RuntimeMode: domain.AssistantModeWorkspace,
		ExecMode: assistantExecMode{AutoWrite: true}}
	type submission struct {
		IssueID, MRURL, Commit, TargetBranch string
		Existing, Updated                    bool
	}
	submit := func(expectError bool) submission {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"name": workspaceForgejoPRTool, "arguments": map[string]string{
			"repositoryId": repository.ID, "sourceBranch": branch, "commit": pushed, "title": "Forgejo delivery",
		}})
		result, err := gateway.callTool(ctx, session, args)
		failed := err != nil || result["isError"] == true
		if failed != expectError {
			t.Fatalf("Forgejo PR gateway = %v, %v", result, err)
		}
		var got submission
		if !failed {
			items := result["content"].([]map[string]any)
			text := items[0]["text"].(string)
			if strings.Contains(text, secret) || json.Unmarshal([]byte(text), &got) != nil {
				t.Fatal("unexpected Forgejo PR tool output")
			}
		}
		return got
	}
	callsBeforeSubmit := calls.Load()
	submit(true) // A generic SSH binding offers Git transport only.
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
		submit(true) // A replaced binding cannot use the old checkout.
	}
	bindForgejo(remote, webURL)
	session.ExecMode.AutoWrite = false
	submit(true) // A request that cannot get approval cannot call Forgejo.
	if calls.Load() != callsBeforeSubmit {
		t.Fatal("an unauthorized call reached the Forgejo API")
	}
	session.ExecMode.AutoWrite = true
	got := submit(false)
	if !created.Load() || got.MRURL != webURL+"/pulls/4" || got.Commit != pushed || got.TargetBranch != "main" || got.IssueID == "" {
		t.Fatalf("Forgejo PR did not use the already-pushed commit against the original base: %+v", got)
	}
	ref, _ := domain.ParseForgeRepositoryURL(webURL)
	if err := f.s.EnsureForgeDeliveryState(ref, 4, got.IssueID, f.user.ID+1); err != nil {
		t.Fatal(err)
	}
	if retried := submit(false); !retried.Existing || !retried.Updated || retried.MRURL != got.MRURL || retried.IssueID != got.IssueID {
		t.Fatalf("retry did not reuse the Forgejo PR and requirement: %+v", retried)
	}
	if borrowSOP {
		if saved, _ := f.s.RunState(run.ID); saved.Repositories[0] != run.Repositories[0] || got.IssueID != run.ExecutionContext.IssueID {
			t.Fatal("Forgejo submission changed the frozen repository or issue of the SOP")
		}
		run.Status = "running"
		run.Nodes["mr"] = &domain.NodeRun{NodeID: "mr", Status: "running", Attempt: 1}
		bound, _ := f.s.Repository(repository.ID)
		run.Repositories[0].Provider = bound.Provider
		run.Repositories[0].GongfengProjectID = bound.GongfengProjectID
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		token, err := forge.SignRunGrant(forge.RunGrant{RunID: run.ID, NodeID: "mr", UserID: f.user.ID, Generation: run.Generation, Attempt: 1}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		callNode := func(bearer string, expected int) {
			t.Helper()
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
				"name": "forgejo_pr_submit", "arguments": map[string]string{"repositoryId": repository.ID, "commit": pushed, "title": "SOP MR"},
			}})
			req := httptest.NewRequest(http.MethodPost, forge.RunMCPPath, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+bearer)
			recorder := httptest.NewRecorder()
			f.h.ServeHTTP(recorder, req)
			if recorder.Code != expected || (expected == http.StatusOK && (!strings.Contains(recorder.Body.String(), got.MRURL) || strings.Contains(recorder.Body.String(), `"isError":true`))) {
				t.Fatalf("Forgejo node HTTP %d: %s", recorder.Code, recorder.Body)
			}
		}
		before := calls.Load()
		callNode(token+"x", http.StatusUnauthorized)
		if calls.Load() != before {
			t.Fatal("invalid node capability reached Forgejo")
		}
		callNode(token, http.StatusOK)
		run.ExecutionContext.ResponsibleUserID++
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		callNode(token, http.StatusUnauthorized)
		run.ExecutionContext.ResponsibleUserID--
		run.Nodes["mr"].Status = "succeeded"
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		callNode(token, http.StatusUnauthorized)
		return
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

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestWorkspaceGitLabProjectsPreflightsRequester(t *testing.T) {
	rc := domain.RepositoryContext{RepositoryID: "gl", Name: "app", Provider: "gitlab", CloneURL: "https://gitlab.example.test/team/app.git", GongfengProjectID: "gitlab:https://gitlab.example.test/team/app", WorkBranch: "feature"}
	requester := domain.User{GitLabBaseURL: "https://gitlab.example.test", GitLabToken: "requester-inert"}
	projects, needsGit, err := workspaceGitLabProjects([]domain.RepositoryContext{rc}, requester)
	if err != nil || needsGit || projects["gl\x00feature"] != rc.GongfengProjectID {
		t.Fatalf("GitLab-only submission: %v %v %v", projects, needsGit, err)
	}
	_, needsGit, err = workspaceGitLabProjects([]domain.RepositoryContext{rc, {Provider: "github"}}, requester)
	if err != nil || !needsGit {
		t.Fatalf("mixed credentials: %v %v", needsGit, err)
	}
	for _, tc := range []struct {
		name       string
		user       domain.User
		repository domain.RepositoryContext
	}{
		{"missing requester PAT", domain.User{GitLabBaseURL: requester.GitLabBaseURL, GitToken: "generic-inert"}, rc},
		{"wrong instance", domain.User{GitLabBaseURL: "https://other.example.test", GitLabToken: requester.GitLabToken}, rc},
		{"credential URL", requester, func() domain.RepositoryContext {
			r := rc
			r.CloneURL = "https://inert:secret@gitlab.example.test/team/app.git"
			return r
		}()},
		{"wrong project", requester, func() domain.RepositoryContext { r := rc; r.GongfengProjectID += "-other"; return r }()},
		{"wrong provider", requester, func() domain.RepositoryContext { r := rc; r.Provider = "github"; return r }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := workspaceGitLabProjects([]domain.RepositoryContext{tc.repository}, tc.user); err == nil {
				t.Fatal("invalid credentials/destination accepted")
			}
		})
	}
	rc.ReadOnly = true
	if _, needsGit, err := workspaceGitLabProjects([]domain.RepositoryContext{rc}, domain.User{}); err != nil || needsGit {
		t.Fatalf("read-only credentials required: %v %v", needsGit, err)
	}
}

func TestWorkspaceSubmitGitLabMRUsesRequesterWithoutGitHubToken(t *testing.T) {
	for _, mode := range []string{"create", "reuse", "denied", "direct"} {
		t.Run(mode, func(t *testing.T) {
			const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if got := r.Header.Get("PRIVATE-TOKEN"); got != "requester-gitlab-inert" {
					t.Error("API did not receive requester credential")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				mr := map[string]any{"iid": 7, "state": "opened", "source_project_id": 42, "target_project_id": 42, "source_branch": "feature", "target_branch": "main", "sha": head, "web_url": "https://untrusted.invalid/ignore"}
				switch {
				case r.URL.Path == "/api/v4/projects/team/app":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "path_with_namespace": "team/app"})
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/merge_requests"):
					if mode == "reuse" {
						_ = json.NewEncoder(w).Encode([]any{mr})
					} else {
						_, _ = w.Write([]byte("[]"))
					}
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/merge_requests"), r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/merge_requests/7"):
					if mode == "denied" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte("requester-gitlab-inert owner-gitlab-inert"))
						return
					}
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["title"] != "fix GitLab" || !strings.Contains(payload["description"], "requester") {
						t.Error("MR metadata did not match requester submission")
					}
					_ = json.NewEncoder(w).Encode(mr)
				default:
					t.Errorf("unexpected GitLab API action: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			for _, user := range []domain.User{
				{LoginName: "owner", GitLabBaseURL: upstream.URL, GitLabToken: "owner-gitlab-inert"},
				{LoginName: "requester", GitLabBaseURL: upstream.URL, GitLabToken: "requester-gitlab-inert"},
			} {
				if err := st.PutUser(user); err != nil {
					t.Fatal(err)
				}
			}
			owner, _ := st.UserByLoginName("owner")
			requester, _ := st.UserByLoginName("requester")
			if requester.GitToken != "" {
				t.Fatal("test must not have a GitHub credential")
			}
			project := domain.Project{ID: "gitlab-conversation-project", Name: "GitLab", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: requester.ID, Role: "editor"}}}
			if err := st.PutProject(project); err != nil {
				t.Fatal(err)
			}
			runner := domain.Runner{ID: "gitlab-conversation-runner", Name: "test runner", OwnerUserID: requester.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionGitLabCredentials}
			if err := st.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			rc := domain.RepositoryContext{RepositoryID: "gl", Name: "app", Provider: "gitlab", CloneURL: upstream.URL + "/team/app.git", WebURL: upstream.URL + "/team/app", GongfengProjectID: "gitlab:" + upstream.URL + "/team/app", WorkBranch: "feature", BaseBranch: "main", WorkspaceDir: "app"}
			run := domain.Run{ID: "run-gitlab-conversation", Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID}, ExecutionContext: domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: owner.ID, ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID, RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared}, Repositories: []domain.RepositoryContext{rc}, Nodes: map[string]*domain.NodeRun{}}
			if err := st.PutRun(run); err != nil {
				t.Fatal(err)
			}
			e := engine.New(st, runtime.NewManager(t.TempDir()))
			dispatches := 0
			e.SetRunnerDispatchNotify(func(string) {
				dispatches++
				task, claimed, err := st.ClaimRunnerTask(runner.ID, []string{"terminal"}, time.Now().UTC(), time.Minute)
				if err != nil || !claimed {
					t.Fatalf("claim push task: %v %v", claimed, err)
				}
				if task.Invocation == nil || len(task.Invocation.Repositories) != 1 || task.Invocation.Repositories[0].GitAuthBaseURL != upstream.URL || task.Invocation.Repositories[0].GitToken != requester.GitLabToken {
					t.Fatal("requester's instance and token did not survive queue/claim")
				}
				task.Status = domain.RunnerTaskSuccess
				task.Result = fmt.Sprintf(`[{"repositoryId":"gl","name":"app","workBranch":"feature","head":"%s"}]`, head)
				if mode == "direct" {
					task.Result = fmt.Sprintf(`[{"repositoryId":"gl","name":"app","workBranch":"feature","head":"%s","sharedBranch":"preview","sharedHead":"bbbb"}]`, head)
				}
				task.UpdatedAt = time.Now().UTC()
				raw, err := json.Marshal(task)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.DB.Table("runner_tasks").Where("id = ?", task.ID).Updates(map[string]any{"status": task.Status, "payload": raw, "updated_at": task.UpdatedAt}).Error; err != nil {
					t.Fatal(err)
				}
			})
			h := &agentToolGateway{deps: &deps{repo: st, engine: e}}
			session := &AgentToolSession{Mode: AgentToolAssistant, RunID: run.ID, ProjectID: project.ID, UserID: requester.ID, User: requester}
			args := map[string]any{"title": "fix GitLab", "description": "regression tests"}
			if mode == "direct" {
				args["mode"] = "direct"
				args["targetBranch"] = "preview"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := h.workspaceSubmitMR(ctx, session, args)
			if err != nil {
				t.Fatal(err)
			}
			if dispatches != 1 {
				t.Fatalf("push dispatches=%d", dispatches)
			}
			if strings.Contains(out, "requester-gitlab-inert") || strings.Contains(out, "owner-gitlab-inert") {
				t.Fatal("credential leaked in output")
			}
			var result struct {
				Results []workspaceMRResult `json:"results"`
				IssueID string              `json:"issueId"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Results) != 1 {
				t.Fatalf("invalid result: %s %v", out, err)
			}
			got := result.Results[0]
			if !got.Pushed {
				t.Fatal("push result missing")
			}
			if mode == "direct" {
				if calls != 0 || !got.Direct || got.MRURL != "" || got.Commit != "bbbb" {
					t.Fatalf("direct push created MR: %+v calls=%d", got, calls)
				}
				return
			}
			if mode == "denied" {
				if got.MRURL != "" || !strings.Contains(got.Error, "403") || got.CreateURL != upstream.URL+"/team/app/-/merge_requests/new" {
					t.Fatalf("API failure not actionable: %+v", got)
				}
				return
			}
			if got.MRURL != upstream.URL+"/team/app/-/merge_requests/7" || got.MRIID != 7 || got.Error != "" || got.Existing != (mode == "reuse") || got.Updated != (mode == "reuse") {
				t.Fatalf("MR result: %+v", got)
			}
			issue, exists := st.Issue(result.IssueID)
			if !exists || len(issue.DeliveryMRList()) != 1 || issue.DeliveryMRList()[0].Project != rc.GongfengProjectID {
				t.Fatalf("GitLab delivery not linked to requirement: %+v", issue.DeliveryMRList())
			}
		})
	}
}

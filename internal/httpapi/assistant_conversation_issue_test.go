package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func conversationIssueFixture(t *testing.T) (*agentToolGateway, *AgentToolSession, *repo.Store) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("alice")
	if err := st.PutProject(domain.Project{ID: "p", Name: "P", Key: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAssistantSession(user.ID, domain.AssistantSession{ID: "chat-1", ProjectID: "p", Title: "修登录页"}); err != nil {
		t.Fatal(err)
	}
	ws := domain.Run{ID: "ws-1", Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID, Status: "waiting", Input: "对话改代码：修登录页文案",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID}, ExecutionContext: domain.TelemetryContext{ProjectID: "p", ResponsibleUserID: user.ID},
		Repositories: []domain.RepositoryContext{{RepositoryID: "repo-web", Name: "web"}}, Nodes: map[string]*domain.NodeRun{}}
	sop := domain.Run{ID: "sop-1", WorkflowID: "wf", Status: "succeeded", WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf"}, ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Nodes: map[string]*domain.NodeRun{}}
	for _, run := range []domain.Run{ws, sop} {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: user.ID, User: user, ChatSessionID: "chat-1", ChatOwnerID: user.ID}
	return &agentToolGateway{deps: &deps{repo: st}}, s, st
}

// A conversation that changes code identifies its requirement: it lands in
// the requirement list, is linked to the conversation and the workspace, and
// the MRs it submits become the requirement's delivery MRs.
func TestConversationRequirementCreatedAndLinked(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	run, _ := st.RunState("ws-1")
	if issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{}, false); err != nil || created || issue.ID != "" {
		t.Fatalf("without a requirement nothing may be created: %+v %v %v", issue, created, err)
	}
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "修复登录页文案", Description: "按钮文案错了"}, false)
	if err != nil || !created || issue.ID == "" {
		t.Fatalf("create = %+v %v %v", issue, created, err)
	}
	if issue.ProjectID != "p" || issue.Kind != "requirement" || issue.Source != "local" || issue.Status != "in_progress" || issue.AutomationState != "ignored" || issue.AssigneeUserID != s.UserID || issue.ConversationSessionID != "chat-1" || issue.RepositoryID != "repo-web" || issue.Number == 0 {
		t.Fatalf("created requirement = %+v", issue)
	}
	if len(issue.Operations) != 1 || !strings.Contains(issue.Operations[0].Detail, "修登录页") || issue.Operations[0].Actor != "爱丽丝" {
		t.Fatalf("operations = %+v", issue.Operations)
	}
	if got, _ := st.RunState("ws-1"); got.ExecutionContext.IssueID != issue.ID {
		t.Fatalf("workspace not linked: %q", got.ExecutionContext.IssueID)
	}
	if conv, _ := st.AssistantSession(s.UserID, "chat-1"); conv.IssueID != issue.ID {
		t.Fatalf("conversation not linked: %q", conv.IssueID)
	}
	// A linked workspace keeps its requirement.
	again, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "另一个"}, false)
	if err != nil || created || again.ID != issue.ID {
		t.Fatalf("relink = %+v %v %v", again, created, err)
	}

	repos := []domain.Repository{{ID: "repo-web", Name: "web", ProjectID: "p", CloneURL: "https://git.example.com/team/web.git"}}
	results := []deliveredMR{
		{URL: "https://git.example.com/team/web/merge_requests/7", TargetBranch: "master"},
		{URL: "not a merge request"},
	}
	if err := h.recordConversationMRs(issue.ID, results, repos); err != nil {
		t.Fatal(err)
	}
	if err := h.recordConversationMRs(issue.ID, results, repos); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.Issue(issue.ID)
	mrs := stored.DeliveryMRList()
	if len(mrs) != 1 || mrs[0].IID != 7 || mrs[0].Project != "team/web" || mrs[0].RepositoryID != "repo-web" || stored.Status != "in_review" {
		t.Fatalf("delivery MRs = %+v status=%s", mrs, stored.Status)
	}
	states := st.MRDeliveryStatesMap([]domain.MRDeliveryKey{{Project: "team/web", IID: 7}})
	if states[domain.MRDeliveryKey{Project: "team/web", IID: 7}].IssueID != issue.ID {
		t.Fatalf("MR not registered for polling: %+v", states)
	}
	// Once the MR merges, polling completes the requirement like an SOP delivery.
	if err := st.UpdateMRDeliveryState("team/web", 7, "merged", "master", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Issue(issue.ID); done.Status != "done" || done.DeliveryState != "merged" {
		t.Fatalf("merged MR must deliver the requirement: status=%s delivery=%s", done.Status, done.DeliveryState)
	}
	if err := h.recordConversationMRs("", results, repos); err != nil {
		t.Fatal(err)
	}
}

// GitHub pull requests opened from a conversation workspace are delivery MRs
// too: polling tracks them under the github: project key.
func TestConversationRequirementRecordsGitHubPullRequest(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	run, _ := st.RunState("ws-1")
	issue, _, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "修复登录页文案"}, false)
	if err != nil {
		t.Fatal(err)
	}
	results := []deliveredMR{{URL: "https://github.com/acme/web/pull/9", TargetBranch: "main"}}
	repos := []domain.Repository{{ID: "repo-web", Name: "web", ProjectID: "p", CloneURL: "git@github.com:acme/web.git"}}
	if err := h.recordConversationMRs(issue.ID, results, repos); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.Issue(issue.ID)
	mrs := stored.DeliveryMRList()
	if len(mrs) != 1 || mrs[0].Project != "github:acme/web" || mrs[0].IID != 9 || mrs[0].URL != "https://github.com/acme/web/pull/9" || mrs[0].RepositoryID != "repo-web" || stored.Status != "in_review" {
		t.Fatalf("delivery PRs = %+v status=%s", mrs, stored.Status)
	}
	key := domain.MRDeliveryKey{Project: "github:acme/web", IID: 9}
	if st.MRDeliveryStatesMap([]domain.MRDeliveryKey{key})[key].IssueID != issue.ID {
		t.Fatal("pull request not registered for polling")
	}
	if err := st.UpdateMRDeliveryState("github:acme/web", 9, "merged", "main", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Issue(issue.ID); done.Status != "done" || done.DeliveryState != "merged" {
		t.Fatalf("merged pull request must deliver the requirement: status=%s delivery=%s", done.Status, done.DeliveryState)
	}
}

// Submitting from a workspace without a requirement reuses the
// conversation's own open requirement first, else creates one from the MR.
func TestConversationRequirementPrefersSessionIssue(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	if err := st.PutIssue(domain.Issue{ID: "iss-open", ProjectID: "p", Title: "已有需求", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkAssistantSessionIssue(s.UserID, "chat-1", "iss-open"); err != nil {
		t.Fatal(err)
	}
	run, _ := st.RunState("ws-1")
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "MR 标题"}, true)
	if err != nil || created || issue.ID != "iss-open" {
		t.Fatalf("session requirement must be reused: %+v %v %v", issue, created, err)
	}
	if got, _ := st.RunState("ws-1"); got.ExecutionContext.IssueID != "iss-open" {
		t.Fatalf("workspace = %q", got.ExecutionContext.IssueID)
	}

	// A finished session requirement is not reopened by new work.
	h, s, st = conversationIssueFixture(t)
	if err := st.PutIssue(domain.Issue{ID: "iss-done", ProjectID: "p", Title: "旧需求", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkAssistantSessionIssue(s.UserID, "chat-1", "iss-done"); err != nil {
		t.Fatal(err)
	}
	run, _ = st.RunState("ws-1")
	issue, created, err = h.linkConversationRequirement(s, run, conversationRequirement{Title: "MR 标题"}, true)
	if err != nil || !created || issue.ID == "iss-done" || issue.Title != "MR 标题" {
		t.Fatalf("new work after a finished requirement = %+v %v %v", issue, created, err)
	}
	if conv, _ := st.AssistantSession(s.UserID, "chat-1"); conv.IssueID != "iss-done" {
		t.Fatalf("an already linked conversation keeps its link: %q", conv.IssueID)
	}

	// SOP runs are never relinked from a conversation.
	sop, _ := st.RunState("sop-1")
	if issue, created, err := h.linkConversationRequirement(s, sop, conversationRequirement{Title: "x"}, false); err != nil || created || issue.ID != "" {
		t.Fatalf("SOP run relinked: %+v %v %v", issue, created, err)
	}
	if got, _ := st.RunState("sop-1"); got.ExecutionContext.IssueID != "" {
		t.Fatalf("SOP run issue = %q", got.ExecutionContext.IssueID)
	}
	// Another project's workspace is out of scope.
	other := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "q", UserID: s.UserID, User: s.User, ChatSessionID: "chat-1", ChatOwnerID: s.UserID}
	if issue, created, err := h.linkConversationRequirement(other, run, conversationRequirement{Title: "x"}, false); err != nil || created || issue.ID != "" {
		t.Fatalf("cross-project link: %+v %v %v", issue, created, err)
	}
}

func TestConversationUsageAttributedToWorkspace(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	attributeConversationUsage(h.deps, nil, "glm-5", &domain.TokenUsage{TotalTokens: 1})
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	attributeConversationUsage(h.deps, s, "glm-5", &domain.TokenUsage{TotalTokens: 5})
	if run, _ := st.RunState("ws-1"); run.Usage.TotalTokens != 0 {
		t.Fatalf("a turn without a bound workspace must not attribute usage: %+v", run.Usage)
	}
	s.workspaceRunID = "ws-1"
	attributeConversationUsage(h.deps, s, "claude-sonnet-4", &domain.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120})
	attributeConversationUsage(h.deps, s, "claude-sonnet-4", &domain.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12})
	run, _ := st.RunState("ws-1")
	if run.Usage.TotalTokens != 132 || run.Usage.InputTokens != 110 || run.Usage.OutputTokens != 22 {
		t.Fatalf("workspace usage = %+v", run.Usage)
	}
	// A shared task conversation never attributes to a conversation workspace.
	task := &AgentToolSession{RunID: "sop-1", workspaceRunID: "ws-1"}
	attributeConversationUsage(h.deps, task, "claude-sonnet-4", &domain.TokenUsage{TotalTokens: 1000})
	if again, _ := st.RunState("ws-1"); again.Usage.TotalTokens != 132 {
		t.Fatalf("task conversation leaked usage: %+v", again.Usage)
	}
}

// A new conversation is stored only when its first turn ends, after the tool
// created the requirement: the end of the turn links it.
func TestConversationFirstTurnLinksRequirementAfterSave(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	s.ChatSessionID = "chat-new"
	s.workspaceRunID = "ws-1"
	run, _ := st.RunState("ws-1")
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "新会话里的需求"}, false)
	if err != nil || !created || issue.ConversationSessionID != "chat-new" {
		t.Fatalf("create = %+v %v %v", issue, created, err)
	}
	if _, ok := st.AssistantSession(s.UserID, "chat-new"); ok {
		t.Fatal("fixture: the conversation must not exist before its first turn is saved")
	}
	if err := st.PutAssistantSession(s.UserID, domain.AssistantSession{ID: "chat-new", ProjectID: "p", Title: "新会话", WorkspaceRunID: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	if conv, _ := st.AssistantSession(s.UserID, "chat-new"); conv.IssueID != issue.ID {
		t.Fatalf("conversation not linked after its first turn: %q", conv.IssueID)
	}
	// An already linked conversation keeps its requirement.
	if err := st.PutIssue(domain.Issue{ID: "iss-other", ProjectID: "p", Title: "另一个", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkConversationRunIssue("ws-1", "iss-other"); err != nil {
		t.Fatal(err)
	}
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	if conv, _ := st.AssistantSession(s.UserID, "chat-new"); conv.IssueID != issue.ID {
		t.Fatalf("conversation relinked: %q", conv.IssueID)
	}
	// Another project's turn never links.
	s.ProjectID = "q"
	s.ChatSessionID = "chat-1"
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	if conv, _ := st.AssistantSession(s.UserID, "chat-1"); conv.IssueID != "" {
		t.Fatalf("cross-project link: %q", conv.IssueID)
	}
}

// After a turn, the GitHub PRs the speaker opened from the conversation
// workspace are registered: authored by the speaker's token after the
// workspace was created, with a local branch at their head commit.
func TestTurnEndRegistersWorkspacePullRequests(t *testing.T) {
	gateway, s, st := conversationIssueFixture(t)
	en := engine.New(st, runtime.NewManager(t.TempDir()))
	gateway.deps.engine = en
	if err := st.DB.Table("runs").Where("id = ?", "ws-1").Update("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(en.RunWorkspace("ws-1"), "web")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", checkout, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "fix")
	git("remote", "add", "origin", "https://github.com/acme/web.git")
	head := git("rev-parse", "HEAD")

	soon, old := time.Now().Add(time.Minute).UTC().Format(time.RFC3339), time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"login":"alice-gh"}`)
		case "/repos/acme/web/pulls":
			fmt.Fprintf(w, `[
				{"number":9,"html_url":"https://github.com/acme/web/pull/9","title":"修复登录页文案","body":"按钮文案","state":"open","created_at":%[1]q,"user":{"login":"alice-gh"},"head":{"sha":%[2]q},"base":{"ref":"main"}},
				{"number":8,"html_url":"https://github.com/acme/web/pull/8","title":"别的对话","state":"open","created_at":%[1]q,"user":{"login":"alice-gh"},"head":{"sha":"0000000000000000000000000000000000000000"},"base":{"ref":"main"}},
				{"number":7,"html_url":"https://github.com/acme/web/pull/7","title":"别人的","state":"open","created_at":%[1]q,"user":{"login":"bob"},"head":{"sha":%[2]q},"base":{"ref":"main"}},
				{"number":5,"html_url":"https://github.com/acme/web/pull/5","title":"开区前","state":"open","created_at":%[3]q,"user":{"login":"alice-gh"},"head":{"sha":%[2]q},"base":{"ref":"main"}}
			]`, soon, head, old)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base, client := githubAPIBase, gitHostClient
	githubAPIBase, gitHostClient = srv.URL, func() *http.Client { return srv.Client() }
	t.Cleanup(func() { githubAPIBase, gitHostClient = base, client })
	if err := st.SaveGitHTTPCredential(s.UserID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitHub", Platform: domain.GitPlatformGitHub, Host: "github.com"},
		Token:             "gh-token",
	}); err != nil {
		t.Fatal(err)
	}

	h := &assistantHandlers{deps: gateway.deps}
	if note := h.registerTurnDeliveries(context.Background(), s); note != "" {
		t.Fatalf("a turn without a workspace registered %q", note)
	}
	s.workspaceRunID = "ws-1"
	if note := h.registerTurnDeliveries(context.Background(), s); note != "" {
		t.Fatalf("a turn that fetched no Git token registered %q", note)
	}
	s.rememberGitCredential("github.com", domain.GitPlatformGitHub, domain.GitTokenHTTPUser, "gh-token")
	note := h.registerTurnDeliveries(context.Background(), s)
	if !strings.Contains(note, "PR #9") || strings.Contains(note, "#8") || strings.Contains(note, "#7") || strings.Contains(note, "#5") || !strings.Contains(note, "新建需求") {
		t.Fatalf("note = %q", note)
	}
	stored, _ := st.RunState("ws-1")
	issue, _ := st.Issue(stored.ExecutionContext.IssueID)
	if mrs := issue.DeliveryMRList(); issue.Title != "修复登录页文案" || len(mrs) != 1 || mrs[0].URL != "https://github.com/acme/web/pull/9" || mrs[0].TargetBranch != "main" || issue.Status != "in_review" {
		t.Fatalf("issue = %+v delivery = %+v", issue, mrs)
	}
	if again := h.registerTurnDeliveries(context.Background(), s); again != "" {
		t.Fatalf("a registered PR was registered again: %q", again)
	}
	if states := st.MRDeliveryStatesMap([]domain.MRDeliveryKey{{Project: "github:acme/web", IID: 9}}); states[domain.MRDeliveryKey{Project: "github:acme/web", IID: 9}].RegisteredBy != s.UserID {
		t.Fatalf("registered_by = %+v", states)
	}
}

// GitLab MRs and GitHub Enterprise PRs on hosts the turn fetched a token for
// are registered under host-qualified keys; a host that was not asked is not.
func TestTurnEndRegistersMergeRequestsOnOtherHosts(t *testing.T) {
	gateway, s, st := conversationIssueFixture(t)
	en := engine.New(st, runtime.NewManager(t.TempDir()))
	gateway.deps.engine = en
	if err := st.DB.Table("runs").Where("id = ?", "ws-1").Update("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	var heads []string
	for _, c := range []struct{ dir, remote string }{
		{"app", "git@gitlab.example.org:group/sub/app.git"},
		{"svc", "https://ghe.example.com/team/svc.git"},
		{"other", "https://gitlab.unasked.example/x/y.git"},
	} {
		dir := filepath.Join(en.RunWorkspace("ws-1"), c.dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", c.dir}, {"remote", "add", "origin", c.remote}} {
			if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		heads = append(heads, strings.TrimSpace(string(out)))
	}
	soon := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("PRIVATE-TOKEN") == "gl-token" && r.URL.Path == "/gitlab/user":
			fmt.Fprint(w, `{"id":42}`)
		case r.Header.Get("PRIVATE-TOKEN") == "gl-token" && r.URL.EscapedPath() == "/gitlab/projects/group%2Fsub%2Fapp/merge_requests" && r.URL.Query().Get("author_id") == "42":
			fmt.Fprintf(w, `[{"iid":3,"web_url":"https://gitlab.example.org/group/sub/app/-/merge_requests/3","title":"GitLab 修复","state":"opened","created_at":%q,"sha":%q,"target_branch":"main"}]`, soon, heads[0])
		case r.Header.Get("Authorization") == "Bearer ghe-token" && r.URL.Path == "/ghe/user":
			fmt.Fprint(w, `{"login":"alice"}`)
		case r.Header.Get("Authorization") == "Bearer ghe-token" && r.URL.Path == "/ghe/repos/team/svc/pulls":
			fmt.Fprintf(w, `[{"number":4,"html_url":"https://ghe.example.com/team/svc/pull/4","title":"GHE 修复","state":"open","created_at":%q,"user":{"login":"alice"},"head":{"sha":%q},"base":{"ref":"main"}}]`, soon, heads[1])
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base, client := gitHostAPIBase, gitHostClient
	gitHostAPIBase = func(cred domain.GitHTTPCredential) string {
		if cred.Platform == domain.GitPlatformGitLab {
			return srv.URL + "/gitlab"
		}
		return srv.URL + "/ghe"
	}
	gitHostClient = func() *http.Client { return srv.Client() }
	t.Cleanup(func() { gitHostAPIBase, gitHostClient = base, client })
	for _, c := range []struct{ host, platform, token string }{
		{"gitlab.example.org", domain.GitPlatformGitLab, "gl-token"},
		{"ghe.example.com", domain.GitPlatformGitHub, "ghe-token"},
		{"gitlab.unasked.example", domain.GitPlatformGitLab, "unasked-token"},
	} {
		if err := st.SaveGitHTTPCredential(s.UserID, "", domain.GitHTTPCredentialSecret{
			GitHTTPCredential: domain.GitHTTPCredential{Name: c.host, Platform: c.platform, Host: c.host},
			Token:             c.token,
		}); err != nil {
			t.Fatal(err)
		}
	}
	s.workspaceRunID = "ws-1"
	s.rememberGitCredential("gitlab.example.org", domain.GitPlatformGitLab, domain.GitTokenHTTPUser, "gl-token")
	s.rememberGitCredential("ghe.example.com", domain.GitPlatformGitHub, domain.GitTokenHTTPUser, "ghe-token")

	h := &assistantHandlers{deps: gateway.deps}
	note := h.registerTurnDeliveries(context.Background(), s)
	if !strings.Contains(note, "MR !3") || !strings.Contains(note, "PR #4") {
		t.Fatalf("note = %q", note)
	}
	stored, _ := st.RunState("ws-1")
	issue, _ := st.Issue(stored.ExecutionContext.IssueID)
	got := map[string]int{}
	for _, mr := range issue.DeliveryMRList() {
		got[mr.Project] = mr.IID
	}
	if len(got) != 2 || got["gitlab:gitlab.example.org/group/sub/app"] != 3 || got["ghe:ghe.example.com/team/svc"] != 4 {
		t.Fatalf("delivery = %+v", issue.DeliveryMRList())
	}
	keys := []domain.MRDeliveryKey{{Project: "gitlab:gitlab.example.org/group/sub/app", IID: 3}, {Project: "ghe:ghe.example.com/team/svc", IID: 4}}
	for key, state := range st.MRDeliveryStatesMap(keys) {
		if state.RegisteredBy != s.UserID || state.IssueID != issue.ID {
			t.Fatalf("%v state = %+v", key, state)
		}
	}
}

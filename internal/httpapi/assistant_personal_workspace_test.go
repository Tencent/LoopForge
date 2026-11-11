package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"github.com/gin-gonic/gin"
)

func TestNoRepositoryWorkspaceReadWriteAndAttach(t *testing.T) {
	for _, projectID := range []string{"", "workbench"} {
		t.Run("project="+projectID, func(t *testing.T) {
			h, owner := workbenchFixture(t)
			chat := domain.AssistantSession{ID: "scratch-chat", ProjectID: projectID}
			if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
				t.Fatal(err)
			}
			en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
			h.deps.engine = en
			gateway := &agentToolGateway{deps: h.deps}
			gateway.api = New(h.repo, en, tracker.NewService(), "", "")
			s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: projectID, UserID: owner.ID, User: owner, ChatSessionID: chat.ID, ChatOwnerID: owner.ID}
			bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "write a report"})
			if err != nil || len(bound.Run.RepositoryContexts()) != 0 {
				t.Fatalf("allocate without repositories: %+v %v", bound, err)
			}
			// The regular sandbox creates this directory; materialize it here so
			// file operations can be exercised without a Docker daemon.
			if err := os.MkdirAll(en.RunWorkspace(bound.Run.ID), 0700); err != nil {
				t.Fatal(err)
			}
			call := func(name string, args map[string]any) (string, error) {
				return gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_"+name, args)
			}
			if raw, err := call("open", map[string]any{"goal": "write a report"}); err != nil || !strings.Contains(raw, "可写工作区") {
				t.Fatalf("open: %s %v", raw, err)
			}
			if _, err := call("write", map[string]any{"path": "report.txt", "content": "scratch content", "expectedSha256": "missing"}); err != nil {
				t.Fatal(err)
			}
			if raw, err := call("read", map[string]any{"path": "report.txt"}); err != nil || !strings.Contains(raw, "scratch content") {
				t.Fatalf("read: %s %v", raw, err)
			}
			if raw, err := call("attach", map[string]any{"path": "report.txt"}); err != nil || !strings.Contains(raw, "report.txt") {
				t.Fatalf("attach: %s %v", raw, err)
			}
			if _, err := call("submit_mr", map[string]any{}); err == nil || !strings.Contains(err.Error(), "没有检出仓库") {
				t.Fatalf("submit without repository: %v", err)
			}
		})
	}
}

func TestPersonalWorkspaceIsolationAndSwitching(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "other-person"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("other-person")
	runs := []domain.Run{
		{ID: "personal-first", Kind: domain.RunKindWorkspace, Status: "waiting", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}},
		{ID: "personal-second", Kind: domain.RunKindWorkspace, Status: "waiting", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}},
		{ID: "personal-other", Kind: domain.RunKindWorkspace, Status: "waiting", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: other.ID}},
	}
	for _, run := range runs {
		if err := h.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	chat := domain.AssistantSession{ID: "personal-chat", WorkspaceRunID: runs[0].ID, OwnWorkspaceRunIDs: []string{runs[0].ID, runs[1].ID}}
	if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
		t.Fatal(err)
	}
	for _, run := range runs[:2] {
		if err := h.repo.SetAssistantSessionWorkspaceRun(owner.ID, chat.ID, run.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "personal-other-chat"}); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: h.deps}
	session := &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: chat.ID, ChatOwnerID: owner.ID, workspaceRunID: runs[0].ID}
	if !gateway.mayUseWorkspace(session, runs[0]) || gateway.mayUseWorkspace(session, runs[2]) {
		t.Fatal("personal run ownership ignored")
	}
	sameUserOtherChat := &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: "personal-other-chat", ChatOwnerID: owner.ID, workspaceRunID: runs[0].ID}
	if gateway.mayUseWorkspace(sameUserOtherChat, runs[0]) {
		t.Fatal("another conversation may use a private workspace")
	}
	if _, err := gateway.callMainBotWorkspace(context.Background(), sameUserOtherChat, "workbench_workspace_read", map[string]any{"path": "secret.txt"}); err == nil {
		t.Fatal("cross-chat workspace read allowed")
	}
	if _, err := gateway.callMainBotBrowser(context.Background(), sameUserOtherChat, "workbench_browser_snapshot", nil); err == nil {
		t.Fatal("cross-chat browser allowed")
	}
	if _, err := gateway.bindConversationWorkspace(context.Background(), session, workspaceOpenChoice{WorkspaceRunID: runs[2].ID}); err == nil {
		t.Fatal("another user's workspace bound")
	}
	bound, err := gateway.bindConversationWorkspace(context.Background(), session, workspaceOpenChoice{WorkspaceRunID: runs[1].ID})
	if err != nil || bound.Run.ID != runs[1].ID {
		t.Fatalf("switch: %+v %v", bound, err)
	}
	saved, _ := h.repo.AssistantSession(owner.ID, chat.ID)
	resumed := &AgentToolSession{UserID: owner.ID}
	loadTurnWorkspace(h.deps, resumed, saved)
	if resumed.workspaceRun() != runs[1].ID {
		t.Fatal("workspace binding was not restored on the next turn")
	}
}

func TestPersonalWorkspacePeripheralEndpoints(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "other-user"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("other-user")
	run := domain.Run{ID: "private-run-peripheral", Kind: domain.RunKindWorkspace, Status: "waiting", Input: "sensitive personal task", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}, Artifacts: []domain.Artifact{{ID: "private-knowledge", RunID: "private-run-peripheral", Name: domain.KnowledgeArtifactBasename, Path: domain.KnowledgeArtifactBasename, Content: "private knowledge content", CreatedAt: time.Now()}}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	projectRun := domain.Run{ID: "project-run-peripheral", Status: "waiting", Input: "project task", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: owner.ID}}
	if err := h.repo.PutRun(projectRun); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	router := New(h.repo, en, tracker.NewService(), "", "")
	call := func(user domain.User, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/runs/" + run.ID, "/api/runs/" + run.ID + "/control", "/api/runs/" + run.ID + "/logs", "/api/runs/" + run.ID + "/workspace/tree", "/api/runs/" + run.ID + "/browser/stream", "/api/runs/" + run.ID + "/preview/download", "/api/runs/" + run.ID + "/artifacts/report.txt"} {
		if rec := call(other, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Fatalf("outsider GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := call(owner, http.MethodGet, "/api/runs/"+run.ID, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "sensitive personal task") {
		t.Fatalf("owner cannot read own run: %d %s", rec.Code, rec.Body.String())
	}
	if err := h.repo.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: "legacy-private-submission", RunID: run.ID, Title: "private title", Content: "private knowledge content"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []domain.KnowledgeSubmission{
		{ID: "missing-run-submission", RunID: "deleted-run", ArtifactID: "deleted-artifact", Title: "orphan private title", Content: "orphan private content"},
		{ID: "missing-run-id-submission", ArtifactID: "orphan-artifact", Title: "orphan private title", Content: "orphan private content"},
	} {
		if err := h.repo.PutKnowledgeSubmission(item); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []domain.User{owner, other} {
		for _, path := range []string{"/api/knowledge/candidates", "/api/knowledge/submissions"} {
			if rec := call(user, http.MethodGet, path, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "private-knowledge") || strings.Contains(rec.Body.String(), "private title") || strings.Contains(rec.Body.String(), "missing-run-submission") {
				t.Fatalf("private knowledge exposed at %s: %d %s", path, rec.Code, rec.Body.String())
			}
		}
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/knowledge/submissions", `{"runId":"private-run-peripheral","artifactId":"private-knowledge"}`},
			{http.MethodGet, "/api/knowledge/submissions/legacy-private-submission", ""},
			{http.MethodPut, "/api/knowledge/submissions/legacy-private-submission", `{}`},
			{http.MethodDelete, "/api/knowledge/submissions/legacy-private-submission", ""},
			{http.MethodPost, "/api/knowledge/submissions/legacy-private-submission/publish", ""},
			{http.MethodPost, "/api/knowledge/submissions/legacy-private-submission/reject", `{}`},
			{http.MethodGet, "/api/knowledge/submissions/missing-run-submission", ""},
			{http.MethodGet, "/api/knowledge/submissions/missing-run-id-submission", ""},
			{http.MethodDelete, "/api/knowledge/submissions/missing-run-submission", ""},
			{http.MethodPost, "/api/knowledge/submissions/missing-run-submission/publish", ""},
			{http.MethodPost, "/api/knowledge/submissions/missing-run-submission/reject", `{}`},
		} {
			if rec := call(user, tc.method, tc.path, tc.body); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "private knowledge content") {
				t.Fatalf("private knowledge via %s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		}
		for _, body := range []string{
			`{"artifactId":"private-knowledge"}`,
			`{"runId":"absent-run","artifactId":"private-knowledge"}`,
			`{"runId":"project-run-peripheral","artifactId":"private-knowledge"}`,
		} {
			if rec := call(user, http.MethodPost, "/api/knowledge/submissions", body); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "private knowledge content") {
				t.Fatalf("forged artifact provenance: %d %s", rec.Code, rec.Body.String())
			}
		}
	}
	for _, tc := range []struct {
		user domain.User
		want bool
	}{{owner, true}, {other, false}} {
		rec := call(tc.user, http.MethodGet, "/api/runs", "")
		if rec.Code != 200 || strings.Contains(rec.Body.String(), run.ID) != tc.want || strings.Contains(rec.Body.String(), projectRun.ID) == false {
			t.Fatalf("run listing for %d: %d %s", tc.user.ID, rec.Code, rec.Body.String())
		}
		for _, path := range []string{"/api/assistant/references?scope=all&type=run", "/api/assistant/references?type=run", "/api/assistant/references?projectId=workbench&type=run"} {
			rec = call(tc.user, http.MethodGet, path, "")
			if strings.Contains(rec.Body.String(), run.ID) {
				t.Fatalf("private run in reference list %s: %s", path, rec.Body.String())
			}
		}
		// Workspace IDs cannot be supplied as the task-scoped picker context.
		rec = call(tc.user, http.MethodGet, "/api/assistant/references?scope=all&runId="+run.ID, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("workspace accepted as task context: %d %s", rec.Code, rec.Body.String())
		}
		c, _ := workbenchContext(tc.user, http.MethodPost, "/", bytes.NewBuffer(nil))
		ref := domain.AssistantReference{Type: "run", ID: run.ID}
		if _, _, err := h.accessibleWorkbenchReference(c, tc.user.ID, ref, ""); err == nil {
			t.Fatal("personal workspace was accepted as a project run reference")
		}
	}
	// Public MCP invokes the same router internally and must preserve identity.
	for _, name := range []string{"runs_get", "runs_control_get"} {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"path": map[string]any{"id": run.ID}}}})
		rec := call(other, http.MethodPost, "/mcp", string(body))
		if strings.Contains(rec.Body.String(), "sensitive personal task") || !strings.Contains(rec.Body.String(), "无权访问个人工作区") {
			t.Fatalf("MCP %s exposed private run: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestPersonalWorkspaceHTTPRejectsOtherUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "outsider"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("outsider")
	run := domain.Run{ID: "personal-private", Kind: domain.RunKindWorkspace, Status: "waiting", Input: "private goal", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/runs/:id", "/api/runs/:id/workspace/tree", "/api/runs/:id/browser", "/api/runs/:id/preview/download"} {
		r := gin.New()
		r.Use(h.deps.runWriteGuard())
		r.Any(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		url := strings.Replace(path, ":id", run.ID, 1)
		for _, tc := range []struct {
			user   domain.User
			status int
		}{{owner, 204}, {other, 403}} {
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(tc.user)))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("%s user %d: got %d want %d", path, tc.user.ID, rec.Code, tc.status)
			}
		}
	}
}

func TestPersonalWorkspaceRequiresSharedSandboxAndNoRepositories(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutSandbox(domain.SandboxProfile{ID: "private-no-project", Name: "private", Driver: "docker", DockerfileID: repo.GenericBackendDockerfileID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.repo.ConversationSandbox("", "private-no-project"); err == nil {
		t.Fatal("non-shared projectless sandbox exposed")
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	if _, err := en.StartConversationWorkspace("test", []domain.RepositoryContext{{Name: "foreign"}}, domain.TelemetryContext{ResponsibleUserID: owner.ID}, repo.GenericDockerSandboxID); err == nil {
		t.Fatal("personal workspace accepted a project repository")
	}
	run, err := en.StartConversationWorkspace("scratch", nil, domain.TelemetryContext{ResponsibleUserID: owner.ID}, repo.GenericDockerSandboxID)
	if err != nil || !run.IsWorkspace() || run.ExecutionContext.ProjectID != "" || run.ExecutionContext.ResponsibleUserID != owner.ID {
		t.Fatalf("personal workspace allocation: %+v %v", run, err)
	}
}

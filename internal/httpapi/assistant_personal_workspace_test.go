package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestPersonalWorkspaceBindingAndReuse(t *testing.T) {
	h, owner := workbenchFixture(t)
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	chat := domain.AssistantSession{
		ID:              "personal-chat",
		ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "personal-runner"},
	}
	if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutSandbox(domain.SandboxProfile{ID: "private-sandbox", Name: "Private", Driver: domain.SandboxDriverDocker, DockerfileID: repo.GenericBackendDockerfileID}); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: h.deps}
	session := &AgentToolSession{Mode: AgentToolAssistant, ChatSessionID: chat.ID, ChatOwnerID: owner.ID, UserID: owner.ID, User: owner}
	catalog := gateway.buildCatalog(context.Background(), session)
	tool, ok := lookupAgentTool(catalog, "workbench_workspace_open")
	if !ok || !strings.Contains(tool.Description, repo.GenericDockerSandboxID) || strings.Contains(tool.Description, "private-sandbox") {
		t.Fatalf("personal workspace catalog: %+v", tool)
	}
	if _, err := gateway.bindConversationWorkspace(context.Background(), session, workspaceOpenChoice{SandboxID: "private-sandbox"}); err == nil {
		t.Fatal("personal chat accepted a non-shared sandbox")
	}
	first, err := gateway.bindConversationWorkspace(context.Background(), session, workspaceOpenChoice{})
	if err != nil || !first.Created || !first.Run.IsPersonalWorkspace() || first.Run.ExecutionContext.ResponsibleUserID != owner.ID ||
		first.Run.ExecutionContext.ExecutionRunnerID != chat.Execution().RunnerID || len(first.Run.RepositoryContexts()) != 0 {
		t.Fatalf("opening personal workspace: %+v, %v", first, err)
	}
	second, err := gateway.bindConversationWorkspace(context.Background(), session, workspaceOpenChoice{SandboxID: altConversationSandbox(t, h)})
	if err != nil || !second.Created || second.Previous != first.Run.ID {
		t.Fatalf("switching workspace: %+v, %v", second, err)
	}
	saved, _ := h.repo.AssistantSession(owner.ID, chat.ID)
	if saved.RunID != "" || saved.WorkspaceRunID != second.Run.ID || len(saved.OwnWorkspaceRunIDs) != 2 {
		t.Fatalf("unexpected persisted workspace binding: %+v", saved)
	}
	next := &AgentToolSession{Mode: AgentToolAssistant, ChatSessionID: chat.ID, ChatOwnerID: owner.ID, UserID: owner.ID, User: owner}
	loadTurnWorkspace(h.deps, next, saved)
	again, err := gateway.bindConversationWorkspace(context.Background(), next, workspaceOpenChoice{})
	if err != nil || !again.Reused || again.Run.ID != second.Run.ID {
		t.Fatalf("resuming saved workspace: %+v, %v", again, err)
	}
	back, err := gateway.bindConversationWorkspace(context.Background(), next, workspaceOpenChoice{WorkspaceRunID: first.Run.ID})
	if err != nil || back.Created || back.Run.ID != first.Run.ID {
		t.Fatalf("switching back to a workspace of the same conversation: %+v, %v", back, err)
	}
}

func TestPersonalWorkspaceOpenFailureRollsBackOwnership(t *testing.T) {
	for _, name := range []string{"first open", "switching environments"} {
		t.Run(name, func(t *testing.T) {
			h, owner := workbenchFixture(t)
			gateway := workspaceAccessGateway(t, h)
			const runnerID = "workspace-preparation-runner"
			if err := h.repo.PutRunner(domain.Runner{ID: runnerID, RunnerVersion: domain.RunnerVersionConversationCLI, Status: domain.RunnerStatusOnline}); err != nil {
				t.Fatal(err)
			}
			h.engine.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				if req.Operation == domain.RunnerWorkspaceOperationPrepare {
					return domain.RunnerWorkspaceResponse{Status: http.StatusOK}, nil
				}
				return domain.RunnerWorkspaceResponse{}, os.ErrNotExist
			}
			const chatID = "workspace-preparation-failure"
			chat := domain.AssistantSession{
				ID:              chatID,
				ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: runnerID},
			}
			if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
				t.Fatal(err)
			}
			session := &AgentToolSession{Mode: AgentToolAssistant, ChatSessionID: chat.ID, ChatOwnerID: owner.ID, UserID: owner.ID, User: owner}
			ctx := context.Background()
			var previous string
			args := map[string]any{}
			if name == "switching environments" {
				bound, err := gateway.bindConversationWorkspace(ctx, session, workspaceOpenChoice{})
				if err != nil {
					t.Fatal(err)
				}
				previous = bound.Run.ID
				args["sandboxProfileId"] = altConversationSandbox(t, h)
				session.workspaceTurn = newAssistantWorkspaceTurn(h.engine, nil)
				t.Cleanup(func() {
					if err := session.workspaceTurn.close(ctx); err != nil {
						t.Error(err)
					}
					waitWorkspaceCheckpointSettled(t, h.repo, previous)
				})
			}
			api := gateway.api
			var failedRunID string
			gateway.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				failedRunID = mainBotWorkspaceRun(r.Context())
				http.Error(w, "workspace tree unavailable", http.StatusServiceUnavailable)
			})
			if _, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_open", args); err == nil || !strings.Contains(err.Error(), "workspace tree unavailable") || failedRunID == "" {
				t.Fatalf("expected preparation to reach the failing tree request, got %v", err)
			}
			waitWorkspaceCheckpointSettled(t, h.repo, failedRunID)
			if run, ok := h.repo.RunState(failedRunID); !ok || run.Status != "cancelled" {
				t.Fatalf("failed workspace was not cancelled: found=%v status=%q", ok, run.Status)
			}
			saved, _ := h.repo.AssistantSession(owner.ID, chat.ID)
			var owned []string
			if previous == "" {
				req := httptest.NewRequest(http.MethodPut, "/api/assistant/sessions/"+chat.ID, strings.NewReader(`{"projectId":"workbench"}`))
				req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(owner)))
				rec := httptest.NewRecorder()
				api.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("first failed open blocked binding to a project: %d %s", rec.Code, rec.Body.String())
				}
			} else {
				owned = []string{previous}
			}
			if saved.WorkspaceRunID != previous || session.workspaceRun() != previous || !slices.Equal(saved.OwnWorkspaceRunIDs, owned) {
				t.Fatalf("rollback left workspace state run=%q stored=%q owned=%v, want run=%q owned=%v", session.workspaceRun(), saved.WorkspaceRunID, saved.OwnWorkspaceRunIDs, previous, owned)
			}
			if previous != "" {
				gateway.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mainBotWorkspaceRun(r.Context()) != previous {
						t.Error("retry did not use the restored workspace")
					}
					_, _ = w.Write([]byte(`{"entries":[]}`))
				})
				if _, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_open", nil); err != nil {
					t.Fatalf("could not reuse the restored workspace in the same turn: %v", err)
				}
			}
		})
	}
}

func TestPersonalWorkspaceAccess(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "workspace-admin"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := h.repo.UserByLoginName("workspace-admin")
	if _, err := h.repo.GrantPlatformAdmin(admin.LoginName); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "private-workspace", Kind: domain.RunKindWorkspace, Status: "waiting",
		ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID},
		Artifacts: []domain.Artifact{{
			ID: "private-note", RunID: "private-workspace", Name: domain.KnowledgeArtifactBasename,
			Path: domain.KnowledgeArtifactBasename, Content: "private content", CreatedAt: time.Now().UTC(),
		}},
	}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []domain.AssistantSession{{ID: "private-chat", WorkspaceRunID: run.ID}, {ID: "owner-other-chat"}} {
		if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutAssistantSession(admin.ID, domain.AssistantSession{ID: "admin-chat"}); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	handler := New(h.repo, en, tracker.NewService(), "", "")
	call := func(user domain.User, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, endpoint := range []string{"", "/logs", "/workspace/tree", "/browser/stream", "/preview/download", "/artifacts/private-note"} {
		if res := call(admin, http.MethodGet, "/api/runs/"+run.ID+endpoint, ""); res.Code != http.StatusForbidden {
			t.Errorf("GET %s as a different user: %d %s", endpoint, res.Code, res.Body.String())
		}
	}
	if res := call(admin, http.MethodGet, "/devpreview/"+run.ID+"/file/index.html", ""); res.Code != http.StatusForbidden {
		t.Errorf("private dev preview: %d %s", res.Code, res.Body.String())
	}
	if res := call(owner, http.MethodGet, "/api/runs/"+run.ID, ""); res.Code != http.StatusOK {
		t.Fatalf("owner cannot load own run: %d %s", res.Code, res.Body.String())
	}
	if res := call(admin, http.MethodGet, "/api/runs", ""); res.Code != http.StatusOK || strings.Contains(res.Body.String(), run.ID) {
		t.Errorf("private workspace in another user's run list: %d %s", res.Code, res.Body.String())
	}
	if res := call(admin, http.MethodGet, "/api/assistant/references?runId="+run.ID, ""); res.Code != http.StatusForbidden {
		t.Errorf("private workspace accepted as a task context: %d %s", res.Code, res.Body.String())
	}
	if res := call(owner, http.MethodPut, "/api/assistant/sessions/private-chat", `{"projectId":"workbench"}`); res.Code != http.StatusConflict {
		t.Errorf("personal workspace conversation promoted to a project: %d %s", res.Code, res.Body.String())
	}
	if res := call(admin, http.MethodGet, "/api/knowledge/candidates", ""); res.Code != http.StatusOK || strings.Contains(res.Body.String(), "private-note") {
		t.Errorf("private workspace artifact in shared candidates: %d %s", res.Code, res.Body.String())
	}
	if res := call(owner, http.MethodPost, "/api/knowledge/submissions", `{"runId":"private-workspace","artifactId":"private-note"}`); res.Code != http.StatusForbidden {
		t.Errorf("private workspace artifact submitted to shared knowledge: %d %s", res.Code, res.Body.String())
	}
	for _, tc := range []struct {
		name, chat string
		actor      domain.User
		chatOwner  int64
	}{
		{"different user", "admin-chat", admin, admin.ID},
		{"different conversation", "owner-other-chat", owner, owner.ID},
		{"foreign conversation", "private-chat", admin, owner.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &AgentToolSession{Mode: AgentToolAssistant, UserID: tc.actor.ID, User: tc.actor, ChatSessionID: tc.chat, ChatOwnerID: tc.chatOwner, ExpiresAt: time.Now().Add(time.Minute)}
			if err := agentToolSessions.create(session); err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			const args = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workbench_workspace_open","arguments":{"workspaceRunId":"private-workspace"}}}`
			res := call(tc.actor, http.MethodPost, "/internal/agent-tools/"+session.Token+"/mcp", args)
			var rpc struct {
				Error  *agentToolRPCError `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if json.Unmarshal(res.Body.Bytes(), &rpc) != nil || res.Code != http.StatusOK || session.workspaceRun() != "" {
				t.Fatalf("foreign workspace was not rejected: %d %s", res.Code, res.Body.String())
			}
			if tc.chatOwner != tc.actor.ID {
				if rpc.Error == nil || rpc.Error.Code != -32602 {
					t.Fatalf("tool was exposed to a non-owner: %s", res.Body.String())
				}
			} else if !rpc.Result.IsError {
				t.Fatalf("tool allowed a workspace from another conversation: %s", res.Body.String())
			}
		})
	}
}

func TestPersonalWorkspaceDockerWritesAndPublishes(t *testing.T) {
	hostConversationWorkspace(t)
	h, owner := workbenchFixture(t)
	configureConversationSandbox(t, h)
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.engine = en
	handler := New(h.repo, en, tracker.NewService(), "", "")
	const chatID = "personal-docker-chat"
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: chatID}); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = handler
	turn := newAssistantWorkspaceTurn(en, nil)
	session := &AgentToolSession{Mode: AgentToolAssistant, ChatSessionID: chatID, ChatOwnerID: owner.ID, UserID: owner.ID, User: owner, workspaceTurn: turn, ExpiresAt: time.Now().Add(5 * time.Minute)}
	ctx := context.Background()
	t.Cleanup(func() {
		if err := turn.close(ctx); err != nil {
			t.Error(err)
		}
		if err := h.deleteConversation(owner.ID, chatID); err != nil {
			t.Error(err)
		}
	})
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_open", nil)
	if err != nil || session.requestedMode() != domain.AssistantModeWorkspace || !strings.Contains(opened, `"handoff":true`) {
		t.Fatalf("opening a personal Docker workspace: %s, %v", opened, err)
	}
	root := t.TempDir()
	files, err := h.switchConversationProcessMode(session, root, session.requestedMode())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Cleanup()
	sandbox, release, err := en.AcquireConversationCLI(ctx, session.workspaceRun(), root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := sandbox.Exec(ctx, runtime.CommandSpec{
		WorkingDir: filepath.Join(root, "ws"),
		Argv:       []string{"sh", "-ec", "test ! -e .git; printf '<h1>Personal workspace</h1>' > report.html"},
	}, nil)
	release()
	if err != nil {
		t.Fatalf("writing without Git in the workspace: %s, %v", out, err)
	}
	result, err := gateway.publishArtifact(ctx, session, json.RawMessage(`{"path":"report.html"}`))
	if err != nil || result["isError"] == true {
		t.Fatalf("publishing workspace file: %+v, %v", result, err)
	}
	attachments := saveAgentReplyFiles(owner.ID, session.takeReplyFiles(), "")
	if len(attachments) != 1 || attachments[0].Name != "report.html" {
		t.Fatalf("published attachments: %+v", attachments)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/assistant/attachments/"+attachments[0].ID+"?sessionId="+chatID, nil)
	request = request.WithContext(context.WithValue(request.Context(), requestUserContextKey{}, domainUserToAuth(owner)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "<h1>Personal workspace</h1>" {
		t.Fatalf("downloading the produced file: %d %s", response.Code, response.Body.String())
	}
}

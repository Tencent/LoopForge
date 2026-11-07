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
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func cvBDirectHandlerRequest(t *testing.T, f cvBHTTPFixture, method, path string, body any,
	handler gin.HandlerFunc, params map[string]string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if authenticated {
		req = authedRequest(t, req, f.user)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	for key, value := range params {
		c.Params = append(c.Params, gin.Param{Key: key, Value: value})
	}
	handler(c)
	return rec
}

func TestCatalogAssetLifecycle(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	request := func(method, path string, body any, want int) {
		t.Helper()
		rec := cvBRequest(t, f, method, path, body, true)
		if rec.Code != want {
			t.Errorf("%s %s = %d want %d: %s", method, path, rec.Code, want, rec.Body.String())
		}
	}

	request(http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "repoUrl": "https://git.example.com/cvb/coverage.git",
	}, http.StatusOK)
	repositories := f.s.Repositories()
	var repositoryID string
	for _, item := range repositories {
		if item.GongfengProjectID == "cvb/coverage" {
			repositoryID = item.ID
		}
	}
	if repositoryID == "" {
		t.Fatal("auto-created repository not found")
	}
	project, _ := f.s.Project("platform")
	project.DefaultRepositoryID = repositoryID
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	request(http.MethodDelete, "/api/repositories?id="+repositoryID, nil, http.StatusOK)

	rule := domain.RuleDefinition{ID: "cvb-r2-share-rule", Name: "Share rule", ProjectID: "platform"}
	skill := domain.SkillDefinition{ID: "cvb-r2-share-skill", Name: "Share skill", ProjectID: "platform"}
	if err := f.s.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSkill(skill); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "cvb-r2-shared-agent", Name: "Shared agent", ProjectID: "platform", ExecutorID: "codebuddy",
		Shared: true, Rules: []string{rule.ID}, Skills: []string{skill.ID},
	}, http.StatusOK)
	if stored, _ := f.s.Rule(rule.ID); !stored.Shared {
		t.Fatal("agent rule was not shared")
	}
	if stored, _ := f.s.Skill(skill.ID); !stored.Shared {
		t.Fatal("agent skill was not shared")
	}

	request(http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "cvb-r2-delete-agent", Name: "Delete agent", ProjectID: "platform", ExecutorID: "codebuddy",
	}, http.StatusOK)
	request(http.MethodPost, "/api/agents/cvb-r2-delete-agent/clone",
		map[string]any{"id": "cvb-r2-cloned-agent", "name": "Clone", "projectId": "platform"}, http.StatusCreated)
	request(http.MethodDelete, "/api/agents/cvb-r2-delete-agent", nil, http.StatusNoContent)

	unused := domain.RuleDefinition{ID: "cvb-r2-unused-rule", Name: "Unused", ProjectID: "platform"}
	if err := f.s.PutRule(unused); err != nil {
		t.Fatal(err)
	}
	request(http.MethodDelete, "/api/rules?id="+unused.ID, nil, http.StatusNoContent)
	request(http.MethodPut, "/api/skills", domain.SkillDefinition{
		ID: skill.ID, Name: skill.Name, ProjectID: skill.ProjectID, Shared: false,
	}, http.StatusConflict)

	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-global-sandbox", Name: "Global sandbox", ProjectID: "platform", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{ID: "cvb-r2-global-flow", Name: "Global", ProjectID: "platform", SandboxProfileID: "cvb-r2-global-sandbox"}
	if err := f.s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	global := domain.RuleDefinition{Global: true, ProjectID: "platform",
		WorkflowIDs: []string{"", workflow.ID, workflow.ID}}
	if err := normalizeGlobalRule(f.s, &global); err != nil || len(global.WorkflowIDs) != 1 {
		t.Fatalf("normalized global rule: %+v %v", global, err)
	}
	if err := normalizeGlobalRule(f.s, &domain.RuleDefinition{Global: true, ProjectID: "platform",
		WorkflowIDs: []string{"missing"}}); err == nil {
		t.Fatal("missing global workflow accepted")
	}
	notGlobal := domain.RuleDefinition{WorkflowIDs: []string{"x"}}
	if err := normalizeGlobalRule(f.s, &notGlobal); err != nil || notGlobal.WorkflowIDs != nil {
		t.Fatalf("non-global normalization: %+v %v", notGlobal, err)
	}
	if err := normalizeGlobalRule(f.s, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogSandboxDependencyAndScopeHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &catalogHandlers{deps: &deps{repo: f.s}}
	dockerfile := domain.DockerfileDefinition{ID: "cvb-r2-docker", Name: "Docker", ProjectID: "platform", Content: "FROM alpine\n"}
	environment := domain.EnvironmentDefinition{ID: "cvb-r2-env", Name: "Environment", ProjectID: "platform"}
	if err := f.s.PutDockerfile(dockerfile); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutEnvironment(environment); err != nil {
		t.Fatal(err)
	}
	if err := h.shareSandboxDependencies(domain.SandboxProfile{ID: "box", ProjectID: "platform", Shared: true,
		DockerfileID: dockerfile.ID, EnvironmentID: environment.ID}); err != nil {
		t.Fatal(err)
	}
	if item, _ := f.s.Dockerfile(dockerfile.ID); !item.Shared {
		t.Fatal("dockerfile not shared")
	}
	if item, _ := f.s.Environment(environment.ID); !item.Shared {
		t.Fatal("environment not shared")
	}
	if err := h.shareSandboxDependencies(domain.SandboxProfile{DockerfileID: "missing"}); err == nil {
		t.Fatal("missing dockerfile accepted")
	}
	if err := h.shareSandboxDependencies(domain.SandboxProfile{EnvironmentID: "missing"}); err == nil {
		t.Fatal("missing environment accepted")
	}
	if !workflowHasNotifications(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{
		Notifications: []domain.NotificationBinding{{Scope: "node"}},
	}}}) || workflowHasNotifications(domain.WorkflowDefinition{}) {
		t.Fatal("notification workflow detection")
	}
	filtered := filterAgentsForProject([]domain.AgentDefinition{
		{ID: "mine", ProjectID: "platform"}, {ID: "shared", ProjectID: "other", Shared: true},
		{ID: "private", ProjectID: "other"}, {ID: "unowned"},
	}, "platform")
	if len(filtered) != 2 {
		t.Fatalf("filtered agents: %#v", filtered)
	}
}

func TestRunEvalBusyApplyAndCloneHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-eval-sandbox", Name: "Eval sandbox", ProjectID: "platform", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "cvb-r2-eval-branches", WorkflowID: "cvb-r2-eval-flow", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, WorkflowSnapshot: domain.WorkflowDefinition{
			ID: "cvb-r2-eval-flow", Name: "Eval flow", Generic: true, SandboxProfileID: "cvb-r2-eval-sandbox", Nodes: []domain.WorkflowNode{{
				ID: "node", Type: "agent", AgentID: "cvb-r2-eval-agent",
			}},
		}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	active := domain.RunEvalRecord{ID: "cvb-r2-active-eval", RunID: run.ID, Status: domain.RunEvalRunning}
	if err := f.s.PutRunEval(active); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}, evalJobs: newRunEvalHub()}
	h.evalJobs.jobs[run.ID] = &runEvalJob{runID: run.ID, done: make(chan struct{}), subs: map[chan runEvalJobEvent]struct{}{}}
	rec := cvBDirectHandlerRequest(t, f, http.MethodPost, "/runs/"+run.ID+"/eval", nil, h.startRunEval,
		map[string]string{"id": run.ID}, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy eval = %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := h.ensureProjectWorkflow(domain.Run{}, "platform", "missing"); err == nil {
		t.Fatal("missing workflow accepted")
	}
	clonedWorkflow, err := h.ensureProjectWorkflow(run, "platform", run.WorkflowID)
	if err != nil || !strings.Contains(clonedWorkflow.ID, "cvb-r2-eval-flow") || clonedWorkflow.Generic {
		t.Fatalf("cloned workflow: %+v %v", clonedWorkflow, err)
	}
	again, err := h.ensureProjectWorkflow(run, "platform", run.WorkflowID)
	if err != nil || again.ID != clonedWorkflow.ID {
		t.Fatalf("existing workflow clone: %+v %v", again, err)
	}
	if _, err := h.ensureProjectAgent(run, "platform", "missing"); err == nil {
		t.Fatal("missing agent accepted")
	}
	agent := domain.AgentDefinition{ID: "cvb-r2-eval-agent", Name: "Agent", ProjectID: "other", Shared: true, ExecutorID: "codebuddy"}
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	clonedAgent, err := h.ensureProjectAgent(run, "platform", agent.ID)
	if err != nil || clonedAgent.Shared || clonedAgent.ProjectID != "platform" {
		t.Fatalf("cloned agent: %+v %v", clonedAgent, err)
	}
	if _, err := h.applyOneRecommendation(run, "platform", domain.RunEvalRecommendation{Target: "other", Action: "unknown"}); err == nil {
		t.Fatal("unsupported recommendation accepted")
	}
}

func TestWorkspacePathEdgeCases(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		wantDir bool
	}{
		{"/absolute", false}, {"bad\x00path", false}, {"../outside", false}, {".env", false},
		{"link", false}, {"file.txt", true}, {"dir", false},
	} {
		if _, _, err := resolveWorkspacePath(root, tc.path, tc.wantDir); err == nil {
			t.Errorf("path %q unexpectedly accepted", tc.path)
		}
	}
	for _, path := range []string{".agentflow/private-home/x", ".agentflow/runtime/x", ".env.local", ".ssh/key", "id_rsa"} {
		if !workspacePathSensitive(path) {
			t.Errorf("sensitive path accepted: %s", path)
		}
	}
	if workspacePathSensitive(".env.example") {
		t.Fatal("environment example treated as secret")
	}
	if _, ok := safeGitObjectID("abcdef0"); !ok {
		t.Fatal("safe object rejected")
	}
	for _, id := range []string{"short", "abcdefg", strings.Repeat("a", 65)} {
		if _, ok := safeGitObjectID(id); ok {
			t.Errorf("unsafe object accepted: %s", id)
		}
	}
	for _, path := range []string{"/absolute", "../outside", ".git/config", "bad\x00path"} {
		if _, err := workspaceLogicalPath(path); err == nil {
			t.Errorf("unsafe logical path accepted: %q", path)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := searchWorkspace(cancelled, root, "text"); err == nil {
		t.Fatal("cancelled workspace search succeeded")
	}
}

func TestNotificationPreviewVariants(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, body := range []any{
		map[string]any{"projectId": "platform", "binding": map[string]any{"scope": "run", "events": []string{"succeeded"}, "template": "{{status}}"}, "sourceType": "run"},
		map[string]any{"projectId": "platform", "binding": map[string]any{"scope": "node", "events": []string{"succeeded"}, "template": "{{status}}"}, "sourceType": "schedule_job"},
		map[string]any{"projectId": "platform", "binding": map[string]any{"scope": "node", "events": []string{"succeeded"}, "template": "{{status}}"}, "sourceType": "assistant_task"},
	} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/notification-preview", body, true)
		if rec.Code != http.StatusOK {
			t.Errorf("notification preview = %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestCatalogEnvironmentConflictMatrix(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	other := domain.Project{ID: "cvb-r2-assets-other", Name: "Other"}
	if err := f.s.PutProject(other); err != nil {
		t.Fatal(err)
	}
	docker := domain.DockerfileDefinition{ID: "cvb-r2-assets-docker", Name: "Docker", ProjectID: "platform", Shared: true, Content: "FROM alpine\n"}
	env := domain.EnvironmentDefinition{ID: "cvb-r2-assets-env", Name: "Env", ProjectID: "platform", Shared: true}
	box := domain.SandboxProfile{ID: "cvb-r2-assets-box", Name: "Box", ProjectID: "platform", Shared: true, Driver: "docker",
		DockerfileID: docker.ID, EnvironmentID: env.ID}
	if err := f.s.PutDockerfile(docker); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutEnvironment(env); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(box); err != nil {
		t.Fatal(err)
	}
	flow := domain.WorkflowDefinition{ID: "cvb-r2-assets-flow", Name: "Flow", ProjectID: "platform",
		SandboxProfileID: box.ID, Shared: true}
	if err := f.s.PutWorkflow(flow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/dockerfiles", domain.DockerfileDefinition{ID: docker.ID, Name: "Moved", ProjectID: other.ID}},
		{"/api/dockerfiles", domain.DockerfileDefinition{ID: docker.ID, Name: docker.Name, ProjectID: "platform", Shared: false}},
		{"/api/environments", domain.EnvironmentDefinition{ID: env.ID, Name: "Moved", ProjectID: other.ID}},
		{"/api/environments", domain.EnvironmentDefinition{ID: env.ID, Name: env.Name, ProjectID: "platform", Shared: false}},
		{"/api/sandboxes", domain.SandboxProfile{ID: box.ID, Name: "Moved", ProjectID: other.ID}},
		{"/api/sandboxes", domain.SandboxProfile{ID: box.ID, Name: box.Name, ProjectID: "platform", Shared: false}},
	} {
		rec := cvBRequest(t, f, http.MethodPut, tc.path, tc.body, true)
		if rec.Code != http.StatusConflict && !(tc.path == "/api/environments" && rec.Code == http.StatusOK) {
			t.Errorf("PUT %s = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestNotificationManagementErrorBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	create := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
		"projectId": "platform", "name": "Bot", "botId": "cvb-r2-errors", "secret": "secret", "enabled": true,
	}, true)
	var bot domain.NotificationBot
	if create.Code != http.StatusOK || json.Unmarshal(create.Body.Bytes(), &bot) != nil {
		t.Fatalf("create bot = %d: %s", create.Code, create.Body.String())
	}
	nh := &notificationHandlers{deps: &deps{repo: f.s, engine: f.e}}
	if rec := cvBDirectHandlerRequest(t, f, http.MethodPatch, "/bots", nil, nh.bots, nil, true); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("bots unsupported method = %d", rec.Code)
	}
	if rec := cvBDirectHandlerRequest(t, f, http.MethodPatch, "/bots/"+bot.ID, nil, nh.bot,
		map[string]string{"id": bot.ID}, true); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("bot unsupported method = %d", rec.Code)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPut, "/api/notification-bots/" + bot.ID,
			map[string]any{"projectId": "platform", "name": "Bot", "botId": "changed", "enabled": true}, http.StatusBadRequest},
		{http.MethodPost, "/api/notification-bots/" + bot.ID + "/test", nil, http.StatusBadRequest},
		{http.MethodPost, "/api/notification-deliveries/missing/retry?projectId=platform", nil, http.StatusNotFound},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	h := &notificationHandlers{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h.providerError(c, context.Canceled)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("plain provider error = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	h.providerError(c, &notification.ProviderError{Code: 404, Message: "missing"})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "404") {
		t.Fatalf("coded provider error = %d: %s", rec.Code, rec.Body.String())
	}

	rec = cvBRequest(t, f, http.MethodPut, "/api/notification-bots/"+bot.ID, map[string]any{
		"projectId": "platform", "name": "Bot updated", "botId": "cvb-r2-errors-new",
		"secret": "new-secret", "enabled": true,
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace bot credentials = %d: %s", rec.Code, rec.Body.String())
	}
	ownerless := domain.NotificationBot{ID: "cvb-r2-ownerless", ProjectID: "platform", Name: "Ownerless",
		BotID: "cvb-r2-ownerless", Enabled: true}
	if err := f.s.SaveNotificationBot(ownerless, "secret"); err != nil {
		t.Fatal(err)
	}
	rec = cvBRequest(t, f, http.MethodPut, "/api/notification-bots/"+ownerless.ID, map[string]any{
		"projectId": "platform", "name": ownerless.Name, "botId": ownerless.BotID, "enabled": true,
		"interactionEnabled": true,
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ownerless interaction = %d: %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{
		"/api/notification-deliveries?projectId=missing",
		"/api/notification-deliveries/missing/retry?projectId=missing",
	} {
		method := http.MethodGet
		if strings.Contains(path, "/retry") {
			method = http.MethodPost
		}
		if rec = cvBRequest(t, f, method, path, nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("missing project notifications %s = %d", path, rec.Code)
		}
	}
	firstInteractive := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
		"projectId": "platform", "name": "Interactive one", "botId": "cvb-r2-duplicate-interactive",
		"secret": "secret", "enabled": true, "interactionEnabled": true,
	}, true)
	if firstInteractive.Code != http.StatusOK {
		t.Fatalf("first interactive bot = %d: %s", firstInteractive.Code, firstInteractive.Body.String())
	}
	secondInteractive := cvBRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{
		"projectId": "platform", "name": "Interactive two", "botId": "cvb-r2-duplicate-interactive",
		"secret": "secret", "enabled": true, "interactionEnabled": true,
	}, true)
	if secondInteractive.Code != http.StatusConflict {
		t.Fatalf("duplicate interactive bot = %d: %s", secondInteractive.Code, secondInteractive.Body.String())
	}
}

func TestRoomDecisionValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-r2-decision-room", ProjectID: "platform", Name: "Decisions"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	check := func(path string, body any, want int) {
		rec := cvBRequest(t, f, http.MethodPost, path, body, true)
		if rec.Code != want {
			t.Errorf("POST %s = %d want %d: %s", path, rec.Code, want, rec.Body.String())
		}
	}
	check("/api/rooms/missing/decisions", map[string]any{}, http.StatusNotFound)
	check("/api/rooms/"+room.ID+"/decisions", map[string]any{}, http.StatusBadRequest)
	check("/api/rooms/"+room.ID+"/decisions",
		map[string]any{"runId": "not-dispatched", "nodeId": "n", "action": "approve"}, http.StatusForbidden)

	missingRun := "cvb-r2-decision-missing-run"
	if err := f.s.PutMessage(domain.ChatMessage{ID: "dispatch-missing", RoomID: room.ID, Kind: domain.ChatKindDispatch, RunID: missingRun}); err != nil {
		t.Fatal(err)
	}
	check("/api/rooms/"+room.ID+"/decisions",
		map[string]any{"runId": missingRun, "nodeId": "n", "action": "approve"}, http.StatusNotFound)

	notWaiting := domain.Run{ID: "cvb-r2-decision-done", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{
		Nodes: []domain.WorkflowNode{{ID: "n", Name: "Node"}},
	}, Nodes: map[string]*domain.NodeRun{"n": {Status: "succeeded"}}}
	if err := f.s.PutRun(notWaiting); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "dispatch-done", RoomID: room.ID, Kind: domain.ChatKindDispatch, RunID: notWaiting.ID}); err != nil {
		t.Fatal(err)
	}
	check("/api/rooms/"+room.ID+"/decisions",
		map[string]any{"runId": notWaiting.ID, "nodeId": "n", "action": "approve"}, http.StatusConflict)

	noReject := domain.Run{ID: "cvb-r2-decision-no-reject", Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{
		Nodes: []domain.WorkflowNode{{ID: "n", Name: "Node"}},
	}, Nodes: map[string]*domain.NodeRun{"n": {Status: "waiting_approval"}}}
	if err := f.s.PutRun(noReject); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "dispatch-no-reject", RoomID: room.ID, Kind: domain.ChatKindDispatch, RunID: noReject.ID}); err != nil {
		t.Fatal(err)
	}
	check("/api/rooms/"+room.ID+"/decisions",
		map[string]any{"runId": noReject.ID, "nodeId": "n", "action": "reject"}, http.StatusBadRequest)

	rejectable := domain.Run{ID: "cvb-r2-decision-reject", Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work"}, {ID: "approve", Name: "Approve", Type: "approval", RejectTo: "work"}},
	}, Nodes: map[string]*domain.NodeRun{"work": {Status: "succeeded"}, "approve": {Status: "waiting_approval"}}}
	if err := f.s.PutRun(rejectable); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "dispatch-reject", RoomID: room.ID,
		Kind: domain.ChatKindDispatch, RunID: rejectable.ID}); err != nil {
		t.Fatal(err)
	}
	check("/api/rooms/"+room.ID+"/decisions",
		map[string]any{"runId": rejectable.ID, "nodeId": "approve", "action": "reject", "reason": "redo"}, http.StatusOK)
}

func TestCatalogMCPAndGenericHelperBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &catalogHandlers{deps: &deps{repo: f.s}}
	definitions := []domain.MCPServerDefinition{
		{ID: "cvb-r2-mcp-same", Name: "Same", Scope: "project", ProjectID: "platform", Transport: "http", URL: "https://example.test/same"},
		{ID: "cvb-r2-mcp-other", Name: "Other", Scope: "project", ProjectID: "other", MarketID: "market-x", Transport: "http", URL: "https://example.test/other"},
		{ID: "cvb-r2-mcp-existing", Name: "Existing", Scope: "project", ProjectID: "platform", MarketID: "market-x", Transport: "http", URL: "https://example.test/existing"},
		{ID: "cvb-r2-mcp-personal", Name: "Personal", Scope: "personal", OwnerUserID: f.user.ID, Transport: "http", URL: "https://example.test/personal"},
	}
	for _, definition := range definitions {
		if err := f.s.PutMCPServer(definition); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.cloneAgentMCPServers([]string{
		"missing", definitions[0].ID, definitions[1].ID, definitions[3].ID,
	}, "platform", f.user.ID)
	if err != nil || len(got) != 3 {
		t.Fatalf("cloned MCPs: %#v %v", got, err)
	}
	if _, err := h.cloneAgentMCPServers([]string{definitions[3].ID}, "platform", 0); err == nil {
		t.Fatal("anonymous personal MCP clone accepted")
	}

	existing := domain.AgentDefinition{ID: "cvb-r2-upgrade-agent", ProjectID: "platform"}
	h.upgradeGenericTemplateAgent(&existing, domain.AgentDefinition{MCPServers: []string{"missing"}}, "platform", time.Now())
	existing.MCPServers = []string{"custom"}
	h.upgradeGenericTemplateAgent(&existing, domain.AgentDefinition{MCPServers: []string{definitions[0].ID}}, "platform", time.Now())
	if existing.MCPServers[0] != "custom" {
		t.Fatal("locally edited MCP binding changed")
	}

	if got := h.pickAvailableID([]string{"a", "b"}, func(id string) bool { return id == "a" }); got != "b" {
		t.Fatalf("available id = %s", got)
	}
	attempts := 0
	gotID := h.pickAvailableID([]string{"a", "b"}, func(string) bool {
		attempts++
		return attempts < 4
	})
	if gotID == "" || attempts < 4 {
		t.Fatalf("random fallback id = %q attempts=%d", gotID, attempts)
	}
	if got := h.genericTemplateSandbox("missing", "platform"); got == "" {
		t.Fatal("fallback sandbox not selected")
	}
	if _, err := h.adoptGenericWorkflow(domain.WorkflowDefinition{}, "id", "name", "", "missing"); err == nil {
		t.Fatal("adoption into missing project accepted")
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/asset?id=x", nil)
	h.deleteCatalogAsset(c, "规则", "missing",
		func(string) (bool, bool) { return false, true },
		func(string) []string { return nil }, "角色",
		func(string) error { return context.Canceled })
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete error status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDispatchWorkflowSelectionErrors(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	d := &deps{repo: f.s, engine: f.e}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: "missing"}); err == nil {
		t.Fatal("missing issue dispatched")
	}
	base := domain.Issue{ProjectID: "platform", Title: "Dispatch", Status: "todo",
		AssigneeUserID: f.user.ID, Source: "cvb-unknown", Kind: "cvb-unknown"}
	noWorkflow := base
	noWorkflow.ID = "cvb-r2-dispatch-no-flow"
	if err := f.s.PutIssue(noWorkflow); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: noWorkflow.ID}); err == nil {
		t.Fatal("issue without workflow dispatched")
	}

	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-dispatch-box", Name: "Box", ProjectID: "platform", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	fallbackID := repo.ProjectIssueWorkflowID("platform")
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: fallbackID, Name: "Fallback", ProjectID: "platform",
		SandboxProfileID: "cvb-r2-dispatch-box"}); err != nil {
		t.Fatal(err)
	}
	fallback := base
	fallback.ID = "cvb-r2-dispatch-fallback"
	fallback.RepositoryIDs = []string{"missing-repository"}
	if err := f.s.PutIssue(fallback); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: fallback.ID}); err == nil {
		t.Fatal("invalid repository dispatched")
	}

	rerun := base
	rerun.ID = "cvb-r2-dispatch-rerun"
	rerun.WorkflowID = fallbackID
	if err := f.s.PutIssue(rerun); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: rerun.ID, Rerun: true}); err == nil {
		t.Fatal("never-run issue rerun")
	}

	project := domain.Project{ID: "cvb-r2-dispatch-project", Name: "Dispatch project", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{ID: "cvb-r2-dispatch-flow", Name: "Flow", ProjectID: project.ID,
		SandboxProfileID: "cvb-r2-dispatch-box"}
	if err := f.s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	makeIssue := func(id string) domain.Issue {
		issue := domain.Issue{ID: id, ProjectID: project.ID, Title: id, Status: "todo",
			Source: "unknown", Kind: "unknown", RepositoryIDs: []string{"missing-repository"}}
		if err := f.s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	bound := makeIssue("cvb-r2-dispatch-binding")
	bound.SOPBindings = []domain.IssueSOPBinding{{WorkflowID: workflow.ID}}
	if err := f.s.PutIssue(bound); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: bound.ID}); err == nil {
		t.Fatal("binding with invalid repository dispatched")
	}
	project.WorkflowID = workflow.ID
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	projectFallback := makeIssue("cvb-r2-dispatch-project-flow")
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: projectFallback.ID}); err == nil {
		t.Fatal("project workflow with invalid repository dispatched")
	}
}

func TestRoomDecisionApproveSuccess(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-r2-approve-room", ProjectID: "platform", Name: "Approval"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "cvb-r2-approve-run", Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{
		ID: "flow", Name: "Flow", Nodes: []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}},
	}, Nodes: map[string]*domain.NodeRun{"approve": {Status: "waiting_approval", Attempt: 1}}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMessage(domain.ChatMessage{ID: "cvb-r2-approve-dispatch", RoomID: room.ID,
		Kind: domain.ChatKindDispatch, RunID: run.ID, IssueID: "issue", IssueTitle: "Issue"}); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/rooms/"+room.ID+"/decisions",
		map[string]any{"runId": run.ID, "nodeId": "approve", "action": "approve", "reason": "looks good"}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogRemainingReachableBranches(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	f := cvBNewHTTPFixture(t)
	if err := repo.SeedTestGenericTemplates(f.s); err != nil {
		t.Fatal(err)
	}
	// Parsing from a non-primary cloneUrl exercises fallback field population.
	rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "cloneUrl": "git@git.example.com:cvb/fallback.git",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback repository = %d: %s", rec.Code, rec.Body.String())
	}

	agent := domain.AgentDefinition{ID: "cvb-r2-existing-agent", Name: "Existing", ProjectID: "platform", ExecutorID: "codebuddy"}
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	agent.ProjectID = ""
	agent.Name = "Existing updated"
	rec = cvBRequest(t, f, http.MethodPut, "/api/agents", agent, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent inherited project = %d: %s", rec.Code, rec.Body.String())
	}
	if rec = cvBRequest(t, f, http.MethodGet, "/api/agents/"+agent.ID, nil, true); rec.Code != http.StatusOK {
		t.Fatalf("agent detail = %d", rec.Code)
	}
	if err := f.s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Admin"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := f.s.UserByLoginName("example-admin")
	f.user = admin

	genericTemplate, _ := f.s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	missingSandbox := genericTemplate.Workflow
	missingSandbox.SandboxProfileID = ""
	if rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", missingSandbox, true); rec.Code != http.StatusBadRequest {
		t.Errorf("generic missing sandbox = %d: %s", rec.Code, rec.Body.String())
	}
	missingSandbox.SandboxProfileID = "does-not-exist"
	if rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", missingSandbox, true); rec.Code != http.StatusBadRequest {
		t.Errorf("generic unknown sandbox = %d: %s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		body any
		want int
	}{
		{map[string]any{"id": "missing-role", "name": "Missing", "executorId": "unsupported"}, http.StatusBadRequest},
		{map[string]any{"id": "missing-role", "name": "Missing"}, http.StatusNotFound},
	} {
		rec = cvBRequest(t, f, http.MethodPut,
			"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("generic agent update = %d want %d: %s", rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec = cvBRequest(t, f, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("generic agents = %d: %s", rec.Code, rec.Body.String())
	}
	template, _ := f.s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	if len(template.Agents) > 0 {
		update := template.Agents[0]
		update.Name += " updated"
		rec = cvBRequest(t, f, http.MethodPut,
			"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", update, true)
		if rec.Code != http.StatusOK {
			t.Errorf("valid generic agent update = %d: %s", rec.Code, rec.Body.String())
		}
		update.Effort = "definitely-invalid"
		rec = cvBRequest(t, f, http.MethodPut,
			"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", update, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("invalid generic effort = %d: %s", rec.Code, rec.Body.String())
		}
	}
	rec = cvBRequest(t, f, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/publish", nil, true)
	if rec.Code != http.StatusOK {
		t.Errorf("generic publish = %d: %s", rec.Code, rec.Body.String())
	}

	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-notify-box", Name: "Notify", ProjectID: "platform", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	notifyBot := domain.NotificationBot{ID: "cvb-r2-catalog-bot", ProjectID: "platform", Name: "Catalog bot",
		BotID: "cvb-r2-catalog-bot", OwnerID: admin.ID, Enabled: true, Target: &domain.BotChat{ID: "chat", Name: "Chat"}}
	if err := f.s.SaveNotificationBot(notifyBot, "secret"); err != nil {
		t.Fatal(err)
	}
	notificationWorkflow := domain.WorkflowDefinition{ID: "cvb-r2-notify-workflow", Name: "Notify", ProjectID: "platform",
		SandboxProfileID: "cvb-r2-notify-box", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: agent.ID,
			Notifications: []domain.NotificationBinding{{BotID: notifyBot.ID, Events: []string{"succeeded"}}}}}}
	if rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", notificationWorkflow, true); rec.Code != http.StatusOK {
		t.Fatalf("notification workflow create = %d: %s", rec.Code, rec.Body.String())
	}
	storedNotification, _ := f.s.Workflow(notificationWorkflow.ID)
	storedNotification.Nodes[0].Notifications = nil
	if rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", storedNotification, true); rec.Code != http.StatusOK {
		t.Errorf("notification workflow update = %d: %s", rec.Code, rec.Body.String())
	}

	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "cvb-r2-conflict-flow", Name: "Conflict", ProjectID: "platform",
		SandboxProfileID: "cvb-r2-notify-box"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := f.s.Workflow("cvb-r2-conflict-flow")
	current := stale
	current.Name = "Current"
	if err := f.s.PutWorkflow(current); err != nil {
		t.Fatal(err)
	}
	stale.Name = "Stale"
	if rec = cvBRequest(t, f, http.MethodPut, "/api/workflows", stale, true); rec.Code != http.StatusConflict {
		t.Errorf("workflow revision conflict = %d: %s", rec.Code, rec.Body.String())
	}

	h := &catalogHandlers{deps: &deps{repo: f.s}}
	direct := cvBDirectHandlerRequest(t, f, http.MethodPatch, "/repositories", nil, h.repositories, nil, true)
	if direct.Code != http.StatusOK {
		t.Fatalf("unsupported repository method = %d", direct.Code)
	}
	if h.sharedAgentReferences("rule", "definitely-missing") || h.sharedAgentReferences("skill", "definitely-missing") {
		t.Fatal("missing asset reported as referenced")
	}
}

func TestClosedRepositoryErrorResponses(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	f.s.Close()
	d := &deps{repo: f.s, engine: f.e}
	catalog := &catalogHandlers{deps: d}
	runs := &runsHandlers{deps: d}
	schedules := &scheduleHandlers{deps: d}
	runners := &runnersHandlers{deps: d, runnerWaiters: newRunnerWaiterRegistry()}
	tokens := &tokenHandlers{deps: d}

	cases := []struct {
		method, path string
		body         any
		handler      gin.HandlerFunc
		params       map[string]string
		want         int
	}{
		{http.MethodDelete, "/repositories?id=any", nil, catalog.repositories, nil, http.StatusBadRequest},
		{http.MethodGet, "/analytics", nil, runs.analytics, nil, http.StatusServiceUnavailable},
		{http.MethodGet, "/runs", nil, runs.runs, nil, http.StatusBadRequest},
		{http.MethodDelete, "/schedule/id", nil, schedules.scheduleJob, map[string]string{"id": "id"}, http.StatusBadRequest},
		{http.MethodPost, "/runner-tasks", map[string]any{"provider": "mock", "prompt": "prompt"},
			runners.runnerTasksCollection, nil, http.StatusInternalServerError},
		{http.MethodPost, "/runner-tasks/id/cancel", nil, runners.runnerTaskCancel,
			map[string]string{"id": "id"}, http.StatusInternalServerError},
		{http.MethodPost, "/tokens", map[string]any{"name": "closed", "expiresInDays": 1},
			tokens.authTokens, nil, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := cvBDirectHandlerRequest(t, f, tc.method, tc.path, tc.body, tc.handler, tc.params, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	server := &Server{deps: d}
	rec := cvBDirectHandlerRequest(t, f, http.MethodGet, "/bootstrap", nil, server.bootstrap, nil, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("closed bootstrap = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleRepositoryAndActiveRunHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project := domain.Project{ID: "cvb-r2-schedule-project", Name: "Schedule"}
	repository := domain.Repository{ID: "cvb-r2-schedule-repo", ProjectID: project.ID, Name: "Repo",
		DefaultBranch: "main", IntegrateBranch: "develop"}
	project.DefaultRepositoryID = repository.ID
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	h := &scheduleHandlers{deps: &deps{repo: f.s, engine: f.e}}
	if got, ok := h.issueRepository(domain.Issue{ProjectID: project.ID}); !ok || got.ID != repository.ID {
		t.Fatalf("default issue repository: %+v %v", got, ok)
	}
	if _, ok := h.issueRepository(domain.Issue{ProjectID: project.ID, RepositoryID: "missing"}); ok {
		t.Fatal("missing issue repository resolved")
	}
	ctx := scheduledRepositoryContext(repository)
	if ctx.BaseBranch != "develop" || ctx.DefaultBranch != "main" {
		t.Fatalf("scheduled context: %+v", ctx)
	}
	if alignWorkflowUsesPlatformAgent(domain.WorkflowDefinition{}) {
		t.Fatal("empty workflow uses platform agent")
	}
	if !alignWorkflowUsesPlatformAgent(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{
		ID: "resolve", Type: "agent", AgentID: engine.BaselineAlignAgentID,
	}}}) {
		t.Fatal("platform align agent not detected")
	}
	active := domain.Run{ID: "cvb-r2-align-active", WorkflowID: engine.BaselineAlignWorkflowID, Status: "running",
		Repository: domain.RepositoryContext{RepositoryID: repository.ID}, Nodes: map[string]*domain.NodeRun{}}
	unrelated := domain.Run{ID: "cvb-r2-align-unrelated", WorkflowID: "other", Status: "running", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(unrelated); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRun(active); err != nil {
		t.Fatal(err)
	}
	if id, ok := h.activeAlignRunID(repository.ID); !ok || id != active.ID {
		t.Fatalf("active align run = %q %v", id, ok)
	}
	h.cancelStaleAlignRuns("")
}

func TestCatalogAnonymousPersonalMCPClone(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project := domain.Project{ID: "cvb-r2-clone-target", Name: "Clone target"}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	server := domain.MCPServerDefinition{ID: "cvb-r2-personal-clone", Name: "Personal", Scope: "personal",
		OwnerUserID: 999, Transport: "http", URL: "https://example.test/mcp"}
	if err := f.s.PutMCPServer(server); err != nil {
		t.Fatal(err)
	}
	agent := domain.AgentDefinition{ID: "cvb-r2-personal-agent", Name: "Personal agent", ProjectID: "platform",
		ExecutorID: "codebuddy", MCPServers: []string{server.ID}}
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	h := &catalogHandlers{deps: &deps{repo: f.s}}
	router := gin.New()
	router.POST("/api/agents/:id/clone", h.agent)
	rec := httptest.NewRecorder()
	body := `{"id":"cvb-r2-personal-copy","name":"Copy","projectId":"cvb-r2-clone-target"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+agent.ID+"/clone", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous personal clone = %d: %s", rec.Code, rec.Body.String())
	}
}

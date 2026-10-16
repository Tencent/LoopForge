package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestGroupDispatchRequiresProjectMembership(t *testing.T) {
	base, u := workbenchFixture(t)
	e := engine.New(base.repo, runtime.NewManagerWithLimit(t.TempDir(), 2))
	e.ConfigureExecution("test", "test", time.Minute, false)
	h := &runsHandlers{deps: &deps{repo: base.repo, engine: e}}
	if err := base.repo.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	if err := base.repo.PutRun(domain.Run{ID: "group", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev", "review"}}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	body := `{"agentId":"dev","prompt":"实现功能","requestId":"request-one"}`
	stranger := u
	stranger.ID += 100
	c, rec := workbenchContext(stranger, "POST", "/api/runs/group/group/tasks", bytes.NewBufferString(body))
	c.Params = gin.Params{{Key: "id", Value: "group"}}
	h.groupControl(c)
	if rec.Code != 403 {
		t.Fatalf("non-member status %d %s", rec.Code, rec.Body)
	}
	c, rec = workbenchContext(u, "POST", "/api/runs/group/group/tasks", bytes.NewBufferString(body))
	c.Params = gin.Params{{Key: "id", Value: "group"}}
	h.groupControl(c)
	if rec.Code != 201 {
		t.Fatalf("member status %d %s", rec.Code, rec.Body)
	}
}
func TestGroupReferencesEnableRealDelegationWithoutImpersonation(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "workbench", Instructions: "实现并验证"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutRun(domain.Run{ID: "group", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev", "review"}}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	req := assistantRequest{ProjectID: "workbench", RunID: "group", ApprovalMode: "auto", Messages: []assistantMessage{{Role: "user", Content: "实现", References: []domain.AssistantReference{{Type: "agent", ID: "dev"}}}}}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if !req.groupMode || req.consultation || !strings.Contains(req.contextText, "runs_group_dispatch") {
		t.Fatalf("group not delegated: %+v", req)
	}
	if !assistantPlatformToolAllowed(workbenchExecMode("auto", req.consultation), "runs_group_dispatch") {
		t.Fatal("full access cannot dispatch")
	}
}

func TestMCPGroupInviteRequiresAndForwardsLogin(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "group-owner", CodeBuddyAPIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("group-owner")
	if err := st.PutProject(domain.Project{ID: "workbench", Name: "Workbench", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: "review", Name: "评审", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(domain.Run{
		ID: "group-invite", Status: "waiting",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"},
		WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev"}},
		Nodes:            map[string]*domain.NodeRun{},
	}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManagerWithLimit(t.TempDir(), 2))
	e.ConfigureExecution("test", "test", time.Minute, false)
	handler := New(st, e, nil, "", "")
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"runs_group_invite","arguments":{"path":{"id":"group-invite"},"body":{"agentId":"review"}}}}`
	anon := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	anon.Header.Set("Content-Type", "application/json")
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != 200 {
		t.Fatalf("anon mcp status=%d %s", anonRec.Code, anonRec.Body.String())
	}
	if !strings.Contains(anonRec.Body.String(), "请先登录") {
		t.Fatalf("anon invite must fail login gate: %s", anonRec.Body.String())
	}
	token, err := auth.NewClient(auth.Config{}).SignUser(&auth.User{ID: u.ID, LoginName: u.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	authed := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	authed.Header.Set("Content-Type", "application/json")
	authed.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	authedRec := httptest.NewRecorder()
	handler.ServeHTTP(authedRec, authed)
	if authedRec.Code != 200 {
		t.Fatalf("authed mcp status=%d %s", authedRec.Code, authedRec.Body.String())
	}
	var rpc struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(authedRec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil || rpc.Result.IsError {
		t.Fatalf("authed invite failed: %s", authedRec.Body.String())
	}
	run, ok := st.RunState("group-invite")
	if !ok || !run.WorkflowSnapshot.HasGroupMember("review") {
		t.Fatalf("review not invited: %#v", run.WorkflowSnapshot.GroupMembers)
	}
}

func TestGroupSOPExplicitCompletionNeedsNoWorkspace(t *testing.T) {
	base, u := workbenchFixture(t)
	e := engine.New(base.repo, runtime.NewManagerWithLimit(t.TempDir(), 2))
	h := &runsHandlers{deps: &deps{repo: base.repo, engine: e}}
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}}}
	r := domain.Run{ID: "complete-no-workspace", Status: "waiting", WorkspaceCleanedAt: time.Now(), ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", SOPCheckpoint: &domain.SOPCheckpoint{Workflow: wf, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "pending"}}}}, Nodes: map[string]*domain.NodeRun{}}
	if err := base.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"complete":true,"nodeId":"a"}`, `{"complete":true}`} {
		c, rec := workbenchContext(u, "POST", "/api/runs/complete-no-workspace/group/sop", bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "id", Value: r.ID}}
		h.groupControl(c)
		want := 200
		if strings.Contains(body, "nodeId") {
			want = 400
		}
		if rec.Code != want {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	got, _ := base.repo.RunState(r.ID)
	if got.Status != "succeeded" || got.WorkspaceCleanedAt.IsZero() || got.Nodes["a"].Status != "skipped" {
		t.Fatalf("unexpected completed run: %+v", got)
	}
}

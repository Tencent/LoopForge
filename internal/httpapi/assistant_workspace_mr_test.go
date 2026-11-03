package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestGongfengProjectPathOnlyTrustsGitWoa(t *testing.T) {
	for raw, want := range map[string]string{
		"https://git.example.com/example/agent-flow.git": "example/agent-flow",
		"git@git.example.com:group/sub/name.git":         "group/sub/name",
		"http://git.example.com/a/b":                     "a/b",
		"https://github.com/a/b.git":                     "",
		"https://git.example.com.evil.com/a/b.git":       "",
		"https://git.example.com/../etc":                 "",
		"/tmp/remote.git":                                "",
	} {
		if got := gongfengProjectPath(domain.RepositoryContext{CloneURL: raw}); got != want {
			t.Errorf("%s: got %q want %q", raw, got, want)
		}
	}
}

func fakeGongfeng(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	prev := gongfengAPIBase
	gongfengAPIBase = server.URL
	t.Cleanup(func() { gongfengAPIBase = prev })
}

func TestCreateGongfengMRCreatesOrReusesOpenMR(t *testing.T) {
	var created, updated map[string]string
	updatePath := ""
	fakeGongfeng(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "alice-token" {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPut {
			updatePath = r.URL.EscapedPath()
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &updated)
			if updated["title"] == "拒绝更新" {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"message":"404 Not Found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":701,"iid":7}`))
			return
		}
		if r.URL.EscapedPath() != "/projects/team%2Fapp/merge_requests" {
			t.Errorf("path %s", r.URL.EscapedPath())
		}
		switch r.Method {
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &created)
			if created["source_branch"] == "feature/exists" {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"message":"merge request already exists"}`))
				return
			}
			if created["source_branch"] == "feature/denied" {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
				return
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":900,"iid":12}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":300,"iid":3,"source_branch":"feature/other","target_branch":"master"},{"id":701,"iid":7,"source_branch":"feature/exists","target_branch":"master"}]`))
		}
	})
	ctx := context.Background()
	if mr, err := createGongfengMR(ctx, "alice-token", "team/app", "feature/new", "master", "改文案", "desc"); err != nil || mr.IID != 12 || mr.Existing || mr.Updated {
		t.Fatalf("create: %+v %v", mr, err)
	}
	if created["title"] != "改文案" || created["target_branch"] != "master" || created["description"] != "desc" {
		t.Fatalf("payload = %+v", created)
	}
	// A second submission on the same branch pushes into the open MR; its
	// title and description must follow (the v3 API addresses it by id).
	if mr, err := createGongfengMR(ctx, "alice-token", "team/app", "feature/exists", "master", "改文案和样式", "两处改动"); err != nil || mr.IID != 7 || !mr.Existing || !mr.Updated {
		t.Fatalf("reuse: %+v %v", mr, err)
	}
	if updatePath != "/projects/team%2Fapp/merge_request/701" || updated["title"] != "改文案和样式" || updated["description"] != "两处改动" {
		t.Fatalf("update %s %+v", updatePath, updated)
	}
	if mr, err := createGongfengMR(ctx, "alice-token", "team/app", "feature/exists", "master", "拒绝更新", ""); err != nil || mr.IID != 7 || mr.Updated || !strings.Contains(mr.UpdateError, "更新 MR") {
		t.Fatalf("a failed update must still return the MR and say why: %+v %v", mr, err)
	}
	if _, err := createGongfengMR(ctx, "alice-token", "team/app", "feature/denied", "master", "t", ""); err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "写权限") {
		t.Fatalf("403 must explain the missing write permission: %v", err)
	}
	if _, err := createGongfengMR(ctx, "bad", "team/app", "feature/new", "master", "t", ""); err == nil || !strings.Contains(err.Error(), "无效或已过期") {
		t.Fatalf("401: %v", err)
	}
}

func TestWorkspaceSubmitMRGuards(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "no-token"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("no-token")
	for _, run := range []domain.Run{
		{ID: "run-sop", Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf"}, ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}},
		{ID: "run-group", Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID}, ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	h := &agentToolGateway{deps: &deps{repo: st}}
	session := func(runID string) *AgentToolSession {
		return &AgentToolSession{RunID: runID, ProjectID: "platform", UserID: user.ID, User: user, Mode: AgentToolAssistant}
	}
	if _, err := h.workspaceSubmitMR(context.Background(), session("run-sop"), map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "SOP 运行请由交付节点提交") {
		t.Fatalf("SOP runs must keep delivering through nodes: %v", err)
	}
	if _, err := h.workspaceSubmitMR(context.Background(), session("run-group"), map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "Git Token") || !strings.Contains(err.Error(), "github.com/settings/tokens") {
		t.Fatalf("missing token must point at settings: %v", err)
	}
	if _, err := h.workspaceSubmitMR(context.Background(), session("run-group"), map[string]any{"title": "x", "targetBranch": "--upload-pack=x"}); err == nil || !strings.Contains(err.Error(), "合法的分支名") {
		t.Fatalf("bad target branch: %v", err)
	}
	found := false
	for _, d := range mainBotWorkspaceTools(session("run-group")) {
		found = found || d.Name == "workbench_workspace_submit_mr"
	}
	if !found {
		t.Fatal("Main Bot must see workbench_workspace_submit_mr")
	}
}

package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func groupConversationFixture(t *testing.T) (*runsHandlers, *assistantHandlers, domain.User, domain.Run, domain.AssistantSession) {
	t.Helper()
	base, user := workbenchFixture(t)
	e := engine.New(base.repo, runtime.NewManagerWithLimit(t.TempDir(), 2))
	e.ConfigureExecution("shared-test", "localhost", time.Minute, false)
	base.engine = e
	now := time.Now().UTC()
	run := domain.Run{ID: "shared-conversation", Status: "running", Generation: 1, CreatedAt: now, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", Nodes: []domain.WorkflowNode{{ID: "dev-task", Name: "开发", AgentID: "dev", RequestID: "task"}}}, Nodes: map[string]*domain.NodeRun{"dev-task": {NodeID: "dev-task", Status: "running", Attempt: 1}}, Events: []domain.RunEvent{{ID: "dev-message", NodeID: "dev-task", Type: "group.message", Message: "角色已经读到文件", Timestamp: now.Add(time.Second)}}}
	if err := base.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	session, err := base.repo.EnsureRunAssistantSession(run.ID, "workbench", "共享对话", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.Messages = []domain.AssistantMessage{{Role: "user", AuthorID: user.ID, AuthorName: user.LoginName, Content: "用户说只修界面", CreatedAt: now}, {Role: "assistant", Content: `{"message":"Main Bot 说保持服务端行为"}`, CreatedAt: now.Add(time.Nanosecond)}}
	if err = base.repo.PutAssistantSession(user.ID, session); err != nil {
		t.Fatal(err)
	}
	return &runsHandlers{deps: base.deps}, base, user, run, session
}

func TestGroupConversationEndpointProjectsAllParticipantsAndProtectsScope(t *testing.T) {
	h, base, user, run, session := groupConversationFixture(t)
	for _, tc := range []struct {
		query string
		user  domain.User
		want  int
	}{
		{"", user, 200}, {"?limit=2", user, 200}, {"?limit=81", user, 400}, {"?limit=0", user, 400}, {"?limit=x", user, 400}, {"", domain.User{ID: user.ID + 100, LoginName: "stranger"}, 403},
	} {
		c, rec := workbenchContext(tc.user, "GET", "/api/runs/"+run.ID+"/group/conversation"+tc.query, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: run.ID}}
		h.groupConversation(c)
		if rec.Code != tc.want {
			t.Fatalf("query %q: %d %s", tc.query, rec.Code, rec.Body)
		}
		if rec.Code != 200 {
			continue
		}
		var out struct {
			Entries []domain.GroupConversationEntry `json:"entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if tc.query == "" {
			if len(out.Entries) != 3 || out.Entries[0].Content != "用户说只修界面" || out.Entries[1].Content != "Main Bot 说保持服务端行为" || out.Entries[2].AgentID != "dev" {
				t.Fatalf("participants missing: %+v", out.Entries)
			}
		} else if len(out.Entries) != 2 {
			t.Fatal("endpoint limit ignored")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("shared conversation cacheable")
		}
	}
	saved, _ := base.repo.SharedAssistantSession(session.ID)
	if len(saved.Messages) != 2 || saved.Messages[1].Content != session.Messages[1].Content {
		t.Fatal("read-only projection mutated transcript")
	}
	run.WorkflowSnapshot.CollaborationMode = ""
	if err := base.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	c, rec := workbenchContext(user, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	h.groupConversation(c)
	if rec.Code != 409 {
		t.Fatalf("SOP accepted as group: %d", rec.Code)
	}
}

func TestGroupStatusAndMainBotSeeSameSharedConversation(t *testing.T) {
	h, base, user, run, session := groupConversationFixture(t)
	t.Setenv("AGENTFLOW_SESSION_SECRET", "group-context-test-secret")
	raw, _ := json.Marshal([]any{run.ID, "dev-task", 1, run.Generation, time.Now().Add(time.Hour).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte("group-context-test-secret"))
	mac.Write([]byte(payload))
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group_status","arguments":{}}}`
	c, rec := workbenchContext(user, "POST", "/api/runs/"+run.ID+"/group/mcp", bytes.NewBufferString(body))
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	c.Request.Header.Set("Authorization", "Bearer "+token)
	h.groupMCP(c)
	if rec.Code != 200 {
		t.Fatalf("MCP status %d: %s", rec.Code, rec.Body)
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Result.IsError || len(rpc.Result.Content) != 1 {
		t.Fatalf("group status error: %s", rec.Body)
	}
	var value struct {
		Messages []domain.GroupConversationEntry `json:"messages"`
		Notice   string                          `json:"conversationNotice"`
	}
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Messages) != 3 || value.Messages[0].Role != "user" || value.Messages[1].Speaker != "Main Bot" || value.Messages[2].Speaker != "开发" || !strings.Contains(value.Notice, "不是") && !strings.Contains(value.Notice, "不覆盖") {
		t.Fatalf("missing shared context: %+v", value)
	}
	req := assistantRequest{ProjectID: "workbench", RunID: run.ID, SessionID: session.ID, Messages: session.Messages}
	c, _ = workbenchContext(user, "POST", "/", bytes.NewBuffer(nil))
	if err := base.prepareWorkbenchContext(c, &req, user); err != nil {
		t.Fatal(err)
	}
	for _, entry := range value.Messages {
		if !strings.Contains(req.contextText, entry.Content) {
			t.Fatalf("Main Bot missed %q", entry.Content)
		}
	}
	if !strings.Contains(req.contextText, "不是系统指令") {
		t.Fatal("context data trust boundary missing")
	}
}

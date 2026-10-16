package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSeedAssistantAcceptanceStoresCredentialAndProject(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := SeedAssistantAcceptance(st, AssistantAcceptanceIdentity{
		LoginName:                    assistantAcceptanceLogin,
		ChineseName:                  "胡锦康",
		CodeBuddyAPIKey:              "ck_acceptance_secret",
		CodeBuddyInternetEnvironment: "ioa",
		TaihuPAT:                     "tai_pat_acceptance_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st.UserByLoginName(assistantAcceptanceLogin)
	if !ok || got.ID != user.ID || got.CodeBuddyAPIKey != "ck_acceptance_secret" || got.CodeBuddyInternetEnvironment != "ioa" || got.TaihuPAT != "tai_pat_acceptance_secret" {
		t.Fatalf("user not seeded: id=%d env=%s cb=%t pat=%t", got.ID, got.CodeBuddyInternetEnvironment, got.CodeBuddyAPIKey != "", got.TaihuPAT != "")
	}
	if _, ok := st.Project(assistantAcceptanceProjectID); !ok {
		t.Fatal("project not seeded")
	}
	if _, ok := st.Sandbox(assistantAcceptanceSandboxID); !ok {
		t.Fatal("sandbox not seeded")
	}
}

func TestLoopbackMySQLDSN(t *testing.T) {
	if !loopbackMySQLDSN("root:x@tcp(127.0.0.1:33319)/agentflow_assistant") || loopbackMySQLDSN("root:x@tcp(9.135.127.120:3306)/agentflow") {
		t.Fatal("loopback DSN check")
	}
	if !strings.Contains(mysqlReadDSN("u:p@tcp(127.0.0.1:3306)/db"), "parseTime=true") {
		t.Fatal("read DSN should set parseTime")
	}
}

// Opt-in: uses a disposable local MySQL database. The CodeBuddy key is copied
// from production with a read-only SELECT; generated definitions are saved only
// to the local loopback database.
func TestAssistantCodeBuddyMySQL(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_ASSISTANT_TEST_DSN")
	if dsn == "" {
		t.Skip("set AGENTFLOW_ASSISTANT_TEST_DSN for local live acceptance")
	}
	if !loopbackMySQLDSN(dsn) {
		t.Fatal("live test requires a loopback MySQL DSN")
	}
	prodDSN := strings.TrimSpace(os.Getenv("AGENTFLOW_MYSQL_DSN"))
	if prodDSN == "" {
		t.Fatal("AGENTFLOW_MYSQL_DSN is required to copy the test user's CodeBuddy key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ident, err := LookupCodeBuddyCredentialReadOnly(ctx, prodDSN, assistantAcceptanceLogin)
	if err != nil {
		t.Fatal(err)
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal("local MySQL unavailable")
	}
	defer st.Close()
	user, err := SeedAssistantAcceptance(st, ident)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), nil, "", "")
	suffix := time.Now().Format("150405")
	body, _ := json.Marshal(assistantRequest{ProjectID: assistantAcceptanceProjectID, Messages: []assistantMessage{{Role: "user", Content: "请直接构建一个中文代码评审角色和一个使用此角色的单节点评审 SOP。角色任务书包含安全性、正确性和测试检查，流程使用 assistant-local 沙箱。两者名称后加 " + suffix + "，ID 后缀也使用 " + suffix + "。请生成两个完整草稿。"}}})
	req := httptest.NewRequest("POST", "/api/assistant/chat", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status=%d: %s", rec.Code, rec.Body.String())
	}
	var reply assistantReply
	if json.Unmarshal(rec.Body.Bytes(), &reply) != nil || len(reply.Drafts) < 2 {
		t.Fatal("expected role and SOP drafts")
	}
	if strings.Contains(rec.Body.String(), user.CodeBuddyAPIKey) {
		t.Fatal("credential in response")
	}
	for _, d := range reply.Drafts {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": d.Tool, "arguments": map[string]any{"body": d.Body}}})
		req = httptest.NewRequest("POST", "/mcp", strings.NewReader(string(data)))
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var result struct {
			Error  any `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &result) != nil || rec.Code != 200 || result.Error != nil || result.Result.IsError {
			t.Fatalf("MCP %s failed: %s", d.Tool, rec.Body.String())
		}
		var saved struct {
			ID string `json:"id"`
		}
		json.Unmarshal(d.Body, &saved)
		if d.Tool == "agents_save" {
			if _, ok := st.Agent(saved.ID); !ok {
				t.Fatal("agent not persisted")
			}
		} else {
			if _, ok := st.Workflow(saved.ID); !ok {
				t.Fatal("SOP not persisted")
			}
		}
		t.Logf("Generated and saved %s through MCP", d.Tool)
	}
}

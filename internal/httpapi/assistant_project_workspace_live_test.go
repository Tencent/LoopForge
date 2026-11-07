package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
)

// Explicit opt-in: real model calls, a disposable loopback MySQL database and
// Docker workspaces containing only a temporary local Git repository.
func TestProjectConversationWorkspaceLive(t *testing.T) {
	key := os.Getenv("AGENTFLOW_SHARED_CHAT_LIVE_KEY")
	if key == "" {
		t.Skip("explicit CodeBuddy test credential required")
	}
	hostConversationWorkspace(t)
	if !strings.Contains(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"), "/agentflow_chat_test") {
		t.Fatal("live test requires the disposable local chat database")
	}
	h, owner, peer, task := sharedNativeMySQLFixture(t, []string{key, key})
	project, _ := h.repo.Project(task.ProjectID)
	project.Members = []domain.ProjectMember{{UserID: peer.ID, Role: "viewer"}}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	dockerfile := domain.DockerfileDefinition{ID: project.ID + "-df", Name: "Shared chat test", ProjectID: project.ID, Content: "FROM alpine:3.20\nRUN apk add --no-cache bash git coreutils && adduser -D -u 1000 user_00\nWORKDIR /workspace\n"}
	if err := h.repo.PutDockerfile(dockerfile); err != nil {
		t.Fatal(err)
	}
	sandbox := domain.SandboxProfile{ID: project.ID + "-sandbox", Name: "Shared chat Docker", ProjectID: project.ID, Driver: "docker", DockerfileID: dockerfile.ID}
	if err := h.repo.PutSandbox(sandbox); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutRepository(domain.Repository{ID: project.ID + "-repo", Name: "demo", ProjectID: project.ID, LocalPath: gitRepo(t), DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	chat := domain.AssistantSession{ID: project.ID + "-chat", ProjectID: project.ID, Title: "Shared workspace acceptance", SandboxProfileID: sandbox.ID}
	if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h.engine = en
	t.Cleanup(func() {
		for _, run := range h.repo.Runs() {
			if run.ExecutionContext.ProjectID == project.ID && run.IsWorkspace() {
				_ = en.Cancel(run.ID)
				if err := en.DeleteRun(run.ID); err != nil {
					t.Error("local workspace cleanup failed", err)
				}
			}
		}
	})
	api := New(h.repo, en, nil, "", "")
	var mu sync.Mutex
	calls := map[string]int{}
	succeeded := map[string]int{}
	catalog := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string                `json:"method"`
			Params struct{ Name string } `json:"params"`
		}
		if strings.HasPrefix(r.URL.Path, "/internal/agent-tools/") && r.Body != nil {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read failed", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			_ = json.Unmarshal(data, &rpc)
		}
		if rpc.Method == "" {
			api.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, r)
		var response struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool                    `json:"isError"`
				Content []struct{ Text string } `json:"content"`
				Tools   []struct{ Name string } `json:"tools"`
			} `json:"result"`
		}
		if rec.Code == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &response) == nil {
			mu.Lock()
			if rpc.Method == "tools/list" {
				for _, tool := range response.Result.Tools {
					catalog[stripAgentToolServerPrefix(tool.Name)] = true
				}
			}
			if rpc.Method == "tools/call" {
				name := stripAgentToolServerPrefix(rpc.Params.Name)
				calls[name]++
				ok := len(response.Error) == 0 && !response.Result.IsError
				if name == "workbench_workspace_command" {
					ok = false
					for _, content := range response.Result.Content {
						var result struct {
							ExitCode int
							Output   string
						}
						if json.Unmarshal([]byte(content.Text), &result) == nil && result.ExitCode == 0 && strings.Contains(result.Output, "WORKSPACE_OK") {
							ok = true
						}
					}
				}
				if ok {
					succeeded[name]++
				}
			}
			mu.Unlock()
		}
		for name, values := range rec.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	t.Cleanup(func() { config.SetCurrent(previous) })

	runTurn := func(actor domain.User, prompt, marker string) domain.AssistantSession {
		t.Helper()
		stored, _ := h.repo.AssistantSession(owner.ID, chat.ID)
		messages := append(stored.Messages, assistantMessage{Role: "user", Content: prompt})
		body, _ := json.Marshal(assistantRequest{SessionID: chat.ID, ProjectID: project.ID, Model: assistantChatDefaultModel, ApprovalMode: AssistantApprovalAuto, Messages: messages})
		c, rec := workbenchContext(actor, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
		h.chat(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("live turn failed: HTTP %d %s", rec.Code, redactAssistantSecrets(rec.Body.String(), actor))
		}
		stored, _ = h.repo.AssistantSession(owner.ID, chat.ID)
		if len(stored.Messages) < 2 || !strings.Contains(stored.Messages[len(stored.Messages)-1].Content, marker) {
			t.Fatal("live model did not complete the requested tool operations")
		}
		mu.Lock()
		var missing []string
		for _, tool := range []string{"workbench_workspace_read", "workbench_workspace_write", "workbench_workspace_command"} {
			if succeeded[tool] == 0 {
				missing = append(missing, tool)
			}
		}
		mu.Unlock()
		if len(missing) != 0 {
			t.Fatalf("live tool operations did not succeed: %v", missing)
		}
		return stored
	}
	first := runTurn(owner, "这是本机隔离回归测试。只操作项目中 demo 仓库，不提交、不推送、不调用外部业务工具。依次实际调用 workbench_workspace_open（goal=共享工作区验收），workbench_workspace_read 读取 demo/README.md，workbench_workspace_write 用读取的 sha256 把内容替换为 OWNER_WORKSPACE_OK 加换行，workbench_workspace_command 运行 cat demo/README.md。核对实际命令输出后，最后仅回复 LIVE_OWNER_WORKSPACE_OK。", "LIVE_OWNER_WORKSPACE_OK")
	if first.WorkspaceRunID == "" {
		t.Fatal("creator did not bind a workspace")
	}
	mu.Lock()
	calls = map[string]int{}
	succeeded = map[string]int{}
	catalog = map[string]bool{}
	mu.Unlock()
	second := runTurn(peer, "现在换了项目的另一名成员继续同一个共享会话。只使用本会话已绑定的工作区，不要新建或切换，不提交、不推送、不调用外部业务工具。依次实际调用 workbench_workspace_read 读取 demo/README.md 并确认 OWNER_WORKSPACE_OK；workbench_workspace_write 用读取的 sha256 把内容替换为 PEER_WORKSPACE_OK 加换行；workbench_workspace_command 运行 cat demo/README.md 并核对输出。另通过工具搜索确认 workbench_browser_navigate 存在即可，无需启动浏览器。完成后仅回复 LIVE_PEER_WORKSPACE_OK。", "LIVE_PEER_WORKSPACE_OK")
	if second.WorkspaceRunID != first.WorkspaceRunID || second.UserID != owner.ID || second.Messages[len(second.Messages)-2].AuthorID != peer.ID {
		t.Fatal("participant lost the creator's workspace, shared storage or attribution")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, tool := range []string{"workbench_workspace_read", "workbench_workspace_write", "workbench_workspace_command"} {
		if calls[tool] == 0 {
			t.Errorf("participant did not actually call %s", tool)
		}
	}
	if calls["workbench_workspace_open"] != 0 {
		t.Fatal("participant replaced the shared workspace instead of continuing it")
	}
	if !catalog["workbench_browser_navigate"] {
		t.Fatal("participant lost the browser tool from the live MCP catalog")
	}
	workspace, _ := h.repo.RunState(second.WorkspaceRunID)
	path, reason := en.LocalRunWorkspace(workspace)
	data, err := os.ReadFile(filepath.Join(path, "demo", "README.md"))
	if reason != "" || err != nil || strings.TrimSpace(string(data)) != "PEER_WORKSPACE_OK" {
		t.Fatalf("shared Docker workspace does not contain the participant's edit: reason=%s error=%v", reason, err)
	}
	t.Log("real CodeBuddy owner and viewer turns completed read, write and command tools in the same Docker workspace; shared state persisted in local MySQL")
}

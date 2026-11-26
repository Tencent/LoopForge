package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
)

// Opt-in acceptance uses an explicitly supplied key and a disposable Docker
// MySQL. The counter is a local read-only MCP; no production tools are installed.
func TestAssistantUnlimitedTurnsLive(t *testing.T) {
	path := os.Getenv("AGENTFLOW_ASSISTANT_LIVE_KEY_FILE")
	if path == "" {
		t.Skip("explicit private CodeBuddy credential file required")
	}
	var credential struct {
		Key     string `json:"key"`
		Network string `json:"network"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &credential) != nil || credential.Key == "" || credential.Network != "ioa" {
		t.Fatal("private iOA test credential unavailable")
	}
	h, actor, _, session := sharedNativeMySQLFixture(t, []string{credential.Key, credential.Key})
	api := New(h.repo, engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), nil, "", "")
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	t.Cleanup(func() { config.SetCurrent(previous) })
	h.api = api

	var mu sync.Mutex
	count, token := 0, rand.Text()
	initialToken := token
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments struct {
					Token string `json:"token"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(request.Method, "notifications/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "live-counter", "version": "1"}}
		case "tools/list":
			result["tools"] = []map[string]any{{"name": "next_step", "description": "只读测试计数器。使用上次结果的 nextToken 串行调用，直到 done=true。", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"token": map[string]string{"type": "string"}}, "required": []string{"token"}}}}
		case "tools/call":
			mu.Lock()
			if request.Params.Arguments.Token != token || count >= 32 {
				result = map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": "use the last nextToken; stop when done=true"}}}
			} else {
				count++
				token = rand.Text()
				body, _ := json.Marshal(map[string]any{"step": count, "nextToken": token, "done": count == 32})
				result["content"] = []map[string]string{{"type": "text", "text": string(body)}}
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(counter.Close)
	if err := h.repo.PutMCPServer(domain.MCPServerDefinition{ID: "live-counter", Name: "本地只读计数器", ProjectID: session.ProjectID, Scope: "project", Transport: "http", URL: counter.URL}); err != nil {
		t.Fatal(err)
	}

	run := func(message string, retry bool) domain.AssistantSession {
		t.Helper()
		saved, _ := h.repo.AssistantSession(actor.ID, session.ID)
		messages := saved.Messages
		if !retry {
			messages = append(messages, assistantMessage{Role: "user", Content: message})
		}
		body, _ := json.Marshal(assistantRequest{SessionID: session.ID, ProjectID: session.ProjectID, RunID: session.RunID, Model: "deepseek-v4.1-flash", Messages: messages})
		c, w := workbenchContext(actor, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
		h.startGeneration(c)
		if w.Code != http.StatusAccepted {
			t.Fatalf("live generation start: HTTP %d", w.Code)
		}
		deadline := time.Now().Add(6 * time.Minute)
		for time.Now().Before(deadline) {
			job, ok := h.repo.AssistantGeneration(actor.ID, session.ID)
			if ok && job.Status != "running" {
				if h.generate != nil {
					if job.Status != "failed" || !strings.Contains(job.Error, "未返回完整回答") {
						t.Fatalf("injected failure was not persisted: %s", job.Status)
					}
				} else if job.Status != "completed" {
					t.Fatalf("live generation failed: status=%s error=%s", job.Status, job.Error)
				}
				saved, _ = h.repo.AssistantSession(actor.ID, session.ID)
				return saved
			}
			time.Sleep(250 * time.Millisecond)
		}
		job, _ := h.repo.AssistantGeneration(actor.ID, session.ID)
		_ = h.repo.CancelAssistantGeneration(actor.ID, session.ID, job.ID)
		t.Fatal("live generation timed out")
		return saved
	}

	session = run("只用自然语言回复 LIVE_REPLY_OK，不调用工具。", false)
	if !strings.Contains(session.Messages[len(session.Messages)-1].Content, "LIVE_REPLY_OK") {
		t.Fatal("live natural language reply missing")
	}
	t.Log("iOA natural language reply persisted in local MySQL")
	session = run("这是隔离的只读工具测试。请通过项目 MCP 的 next_step 工具完成计数验证。起始 token="+initialToken+"。每次只调用一次 next_step，等它返回后使用新的 nextToken 继续，直到 done=true（共32步）；token只能从工具结果获取，不能并行、不能用代码循环替代工具调用。不要读其他资源。完成后仅回复 LIVE_32_TURNS_OK。", false)
	mu.Lock()
	completedSteps := count
	mu.Unlock()
	if completedSteps != 32 || !strings.Contains(session.Messages[len(session.Messages)-1].Content, "LIVE_32_TURNS_OK") {
		t.Fatalf("live sequential tool test incomplete: %d/32 steps", completedSteps)
	}
	t.Log("32 sequential live MCP calls and final reply completed beyond the old 30-turn cap")

	h.generate = func(context.Context, string, domain.User) (string, error) { return "", errAssistantMissingResult }
	session = run("只回复 LIVE_RETRY_OK，不调用工具。", false)
	failedLength := len(session.Messages)
	h.generate = nil
	session = run("", true)
	if len(session.Messages) != failedLength+1 || !strings.Contains(session.Messages[len(session.Messages)-1].Content, "LIVE_RETRY_OK") {
		t.Fatal("live retry lost the answer or duplicated the user turn")
	}
	t.Log("persisted failure retried with the real iOA model without duplicating the user message")
}

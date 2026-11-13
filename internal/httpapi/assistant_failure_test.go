package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestAssistantCLIReportsTerminalFailuresWithoutResult(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"max turns in assistant text", `{"type":"assistant","message":{"content":[{"type":"text","text":"Max turns (30) exceeded"}]}}`, "执行轮数上限"},
		{"max turns result", `{"type":"result","is_error":true,"result":"Max turns (30) exceeded"}`, "执行轮数上限"},
		{"auth event", `{"type":"error","error":"Unauthorized: test-private-key"}`, "凭据无效或已过期"},
		{"structured auth event", `{"type":"error","error":{"message":"Invalid API key: test-private-key"}}`, "凭据无效或已过期"},
		{"auth result", `{"type":"result","is_error":true,"result":"Unauthorized: test-private-key"}`, "凭据无效或已过期"},
		{"missing result", `{"type":"assistant","message":{"content":[{"type":"text","text":"正在检查代码"}]}}`, "未返回完整回答"},
		{"other event", `{"type":"error","error":"upstream unavailable: test-private-key"}`, "模型服务暂时不可用"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "codebuddy")
			script := "#!/bin/sh\ncat <<'RESULT'\n" + tc.output + "\nRESULT\n"
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
			raw, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Prompt: "检查实现", SessionRoot: dir}, domain.User{ID: 1, CodeBuddyAPIKey: "test-private-key"}, nil)
			if err == nil || raw != "" {
				t.Fatalf("failed CLI was accepted as a reply: raw=%q err=%v", raw, err)
			}
			status, message := assistantGenerationFailure(context.Background(), err)
			if status != http.StatusBadGateway || !strings.Contains(message, tc.want) || strings.Contains(message, "test-private-key") {
				t.Fatalf("status=%d message=%q, want %q", status, message, tc.want)
			}
		})
	}
}

func TestAssistantGenerationFailurePreservesCancellationAndDeadline(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		_, err := decodeAssistantCLIResult("", false, cause, []string{"process stopped"})
		if !errors.Is(err, cause) {
			t.Fatalf("lost cancellation cause: %v", err)
		}
		status, message := assistantGenerationFailure(context.Background(), err)
		if status != http.StatusGatewayTimeout || !strings.Contains(message, "取消或超时") {
			t.Fatalf("status=%d message=%q", status, message)
		}
	}
}

func TestPrivateAssistantReportsExecutionFailureWithoutReplayingTools(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		cause      error
	}{
		{"max turns", "执行轮数上限", runtime.ErrMaxTurns},
		{"missing result", "未返回完整回答", errAssistantMissingResult},
		{"credentials", "凭据无效或已过期", errors.New("HTTP 401: test-only")},
		{"process", "连接失败或连接中断", errors.New("network interrupted: test-only")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, actor := workbenchFixture(t)
			calls := 0
			h.generate = func(context.Context, string, domain.User) (string, error) {
				calls++
				return "", tc.cause
			}
			c, w := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBufferString(`{"sessionId":"private-failure","projectId":"workbench","messages":[{"role":"user","content":"核查实现"}]}`))
			h.chat(c)
			if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), tc.want) || strings.Contains(w.Body.String(), "test-only") || calls != 1 {
				t.Fatalf("execution failure was misreported or replayed: status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestAssistantClosedToolSessionDoesNotRetrySubmission(t *testing.T) {
	for _, processError := range []bool{true, false} {
		t.Run(fmt.Sprintf("process_error=%t", processError), func(t *testing.T) {
			h, actor := workbenchFixture(t)
			calls := 0
			h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
				calls++
				if calls > 1 {
					return "", context.Canceled
				}
				agentToolSessions.mu.Lock()
				var session *AgentToolSession
				for _, candidate := range agentToolSessions.sessions {
					if candidate.ChatSessionID == "closed-tool-session" && candidate.UserID == actor.ID {
						session = candidate
						break
					}
				}
				agentToolSessions.mu.Unlock()
				if session == nil {
					t.Fatal("turn did not create its tool session")
				}
				session.Close()
				if ctx.Err() == nil {
					t.Fatal("closing the gateway did not cancel the model")
				}
				if processError {
					return "", ctx.Err()
				}
				return "", nil
			}
			c, w := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBufferString(`{"sessionId":"closed-tool-session","projectId":"workbench","messages":[{"role":"user","content":"检查代码"}]}`))
			h.chat(c)
			if calls != 1 || w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), "取消或超时") {
				t.Fatalf("cancelled execution retried or lost its cause: calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
			}
		})
	}
}

func TestFailedSharedGenerationSnapshotKeepsCauseAndRetryHistory(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		cause      error
	}{
		{"max turns", "执行轮数上限", runtime.ErrMaxTurns},
		{"missing terminal result", "未返回完整回答", errAssistantMissingResult},
		{"credentials", "凭据无效或已过期", errors.New("HTTP 401: test-only")},
		{"process", "连接失败或连接中断", errors.New("exit status 1; stderr=network interrupted test-only")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, actor, _, _, session := sharedWorkbenchFixture(t)
			h.generate = func(context.Context, string, domain.User) (string, error) { return "", tc.cause }
			messages := append(session.Messages, assistantMessage{Role: "user", Content: "核查实现是否符合预期"})
			start := func(messages []assistantMessage) {
				t.Helper()
				body, _ := json.Marshal(assistantRequest{SessionID: session.ID, ProjectID: session.ProjectID, RunID: session.RunID, Messages: messages})
				c, w := workbenchContext(actor, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
				h.startGeneration(c)
				if w.Code != http.StatusAccepted {
					t.Fatalf("start: %d %s", w.Code, w.Body.String())
				}
			}
			wait := func(status string) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					job, ok := h.repo.AssistantGeneration(actor.ID, session.ID)
					if ok && job.Status == status {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("generation did not reach " + status)
			}
			start(messages)
			wait("failed")
			c, w := workbenchContext(actor, "GET", "/generation", bytes.NewBuffer(nil))
			c.Params = gin.Params{{Key: "id", Value: session.ID}}
			h.generation(c)
			var snapshot domain.AssistantGeneration
			if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || snapshot.Status != "failed" || !strings.Contains(snapshot.Error, tc.want) || strings.Contains(snapshot.Error, "test-only") || len(snapshot.Reply) != 0 {
				t.Fatalf("wrong persisted failure: %s", w.Body.String())
			}
			saved, _ := h.repo.AssistantSession(actor.ID, session.ID)
			if user := saved.Messages[len(saved.Messages)-2]; user.Role != "user" || user.AuthorID != actor.ID {
				t.Fatalf("retry history lost its accepted user: %+v", user)
			}
			failed := saved.Messages[len(saved.Messages)-1]
			if failed.GenerationID != snapshot.ID || failed.Failure == nil {
				t.Fatalf("failure has no durable diagnostic reference: %+v", failed)
			}
			h.generate = func(context.Context, string, domain.User) (string, error) { return "核查完成", nil }
			start(append(saved.Messages, assistantMessage{Role: "user", Content: "请重试"}))
			wait("completed")
			saved, _ = h.repo.AssistantSession(actor.ID, session.ID)
			if len(saved.Messages) != len(messages)+3 || saved.Messages[len(saved.Messages)-1].Role != "assistant" {
				t.Fatalf("retry duplicated the user turn or lost the answer: %+v", saved.Messages)
			}
			if report, ok := h.repo.AssistantDiagnostics(actor.ID, session.ID, snapshot.ID); !ok || report.Status != "failed" || report.Failure == nil {
				t.Fatalf("retry overwrote diagnostics: %+v", report)
			}
		})
	}
}

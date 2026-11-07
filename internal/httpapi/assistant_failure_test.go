package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		{"other event", `{"type":"error","error":"upstream unavailable: test-private-key"}`, "生成失败，请稍后重试"},
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
		{"process", "生成失败，请稍后重试", errors.New("network interrupted: test-only")},
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

func TestFailedSharedGenerationSnapshotKeepsCauseAndRetryHistory(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		cause      error
	}{
		{"max turns", "执行轮数上限", runtime.ErrMaxTurns},
		{"missing terminal result", "未返回完整回答", errAssistantMissingResult},
		{"credentials", "凭据无效或已过期", errors.New("HTTP 401: test-only")},
		{"process", "生成失败，请稍后重试", errors.New("exit status 1; stderr=network interrupted test-only")},
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
			if last := saved.Messages[len(saved.Messages)-1]; last.Role != "user" || last.AuthorID != actor.ID {
				t.Fatalf("retry history lost its accepted user: %+v", last)
			}
			h.generate = func(context.Context, string, domain.User) (string, error) { return "核查完成", nil }
			start(saved.Messages)
			wait("completed")
			saved, _ = h.repo.AssistantSession(actor.ID, session.ID)
			if len(saved.Messages) != len(messages)+1 || saved.Messages[len(saved.Messages)-1].Role != "assistant" {
				t.Fatalf("retry duplicated the user turn or lost the answer: %+v", saved.Messages)
			}
		})
	}
}

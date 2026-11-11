package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAgentToolSessionFollowsTurnWithoutDeadline(t *testing.T) {
	for _, mode := range []AgentToolMode{AgentToolAssistant, AgentToolScheduled, AgentToolClarify, AgentToolReport} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := make(chan struct{})
			session, files, err := startAgentToolSession(ctx, domain.User{ID: 1}, "project", mode, assistantExecMode{}, "", nil, func() { cancel(); close(stopped) }, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			defer files.Cleanup()
			// Advance the janitor's clock beyond the old five-minute TTL.
			agentToolSessions.purgeExpired(time.Now().Add(24 * time.Hour))
			if _, ok := agentToolSessions.get(session.Token); !ok || ctx.Err() != nil {
				t.Fatal("a live turn without a deadline lost its tools")
			}
			cancel()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not clean up the gateway")
			}
			if _, ok := agentToolSessions.get(session.Token); ok {
				t.Fatal("a cancelled turn retained access to its tools")
			}
			if session.finished() {
				t.Fatal("cancellation was mistaken for a submitted reply")
			}
		})
	}
}

func TestAgentToolStandaloneSessionStillExpires(t *testing.T) {
	registry := newAgentToolSessionRegistry()
	session := &AgentToolSession{}
	if err := registry.create(session); err != nil {
		t.Fatal(err)
	}
	if session.ExpiresAt.IsZero() {
		t.Fatal("a binding with no owning context needs a bounded lifetime")
	}
	registry.purgeExpired(time.Now().Add(10 * time.Minute))
	if _, ok := registry.get(session.Token); ok || session.finished() {
		t.Fatal("standalone binding outlived its fallback TTL or claimed a reply")
	}
}

func TestAssistantAndScheduledTaskKeepToolsPastFiveMinutes(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		name := "ordinary"
		if scheduled {
			name = "scheduled"
		}
		t.Run(name, func(t *testing.T) {
			h, actor := workbenchFixture(t)
			calls := 0
			h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
				calls++
				agentToolSessions.mu.Lock()
				var session *AgentToolSession
				for _, candidate := range agentToolSessions.sessions {
					if candidate.ctx == ctx {
						session = candidate
						break
					}
				}
				agentToolSessions.mu.Unlock()
				if session == nil {
					t.Fatal("turn has no gateway bound to its execution context")
				}
				agentToolSessions.purgeExpired(time.Now().Add(24 * time.Hour))
				if _, ok := agentToolSessions.get(session.Token); !ok || ctx.Err() != nil {
					t.Fatal("long-running generation lost its tools")
				}
				_, err := (&agentToolGateway{}).callTool(context.Background(), session, json.RawMessage(`{"name":"workbench_tool_schema","arguments":{"name":"issues_list"}}`))
				if err != nil {
					t.Fatalf("long-running generation could not call a tool: %v", err)
				}
				return "检查完成", nil
			}
			if scheduled {
				task := assistantTask{Name: "代码扫描", Prompt: "检查代码", ProjectID: "workbench"}
				if err := h.executeAssistantTask(context.Background(), actor, &task); err != nil || task.LastStatus != "ok" {
					t.Fatalf("scheduled generation: status=%s error=%v", task.LastStatus, err)
				}
			} else {
				c, w := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBufferString(`{"sessionId":"long-turn","projectId":"workbench","messages":[{"role":"user","content":"检查代码"}]}`))
				h.chat(c)
				if w.Code != 200 {
					t.Fatalf("ordinary generation: status=%d body=%s", w.Code, w.Body.String())
				}
			}
			if calls != 1 {
				t.Fatalf("generation unexpectedly retried %d times", calls)
			}
		})
	}
}

func TestAgentToolSessionHonorsTurnDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	session, files, err := startAgentToolSession(ctx, domain.User{ID: 1}, "project", AgentToolAssistant, assistantExecMode{}, "", nil, cancel, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer files.Cleanup()
	if !session.ExpiresAt.Equal(deadline) {
		t.Fatalf("gateway deadline differs from the turn: got %v want %v", session.ExpiresAt, deadline)
	}
	agentToolSessions.purgeExpired(time.Now().Add(10 * time.Minute))
	if _, ok := agentToolSessions.get(session.Token); !ok {
		t.Fatal("gateway expired before the turn deadline")
	}
	agentToolSessions.purgeExpired(deadline)
	if _, ok := agentToolSessions.get(session.Token); ok || ctx.Err() == nil || session.finished() {
		t.Fatal("deadline did not revoke access and cancel execution without submitting a reply")
	}
}

func TestAgentToolSessionRejectsCancelledStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session, files, err := startAgentToolSession(ctx, domain.User{}, "", AgentToolAssistant, assistantExecMode{}, "", nil, cancel, t.TempDir())
	if session != nil {
		defer session.Close()
	}
	defer files.Cleanup()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled turn created a gateway: %v", err)
	}
}

func TestAgentToolSessionClosedWithoutFinalRejectsCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &AgentToolSession{Mode: AgentToolAssistant, ExpiresAt: time.Now().Add(-time.Second), CancelModel: cancel}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	agentToolSessions.purgeExpired(time.Now())
	if ctx.Err() == nil || session.finished() {
		t.Fatal("expired session must cancel execution without claiming a final reply")
	}
	_, err := (&agentToolGateway{}).callTool(context.Background(), session, json.RawMessage(`{"name":"workbench_tool_schema","arguments":{"name":"issues_list"}}`))
	if !errors.Is(err, errAgentToolInvalidSession) {
		t.Fatalf("closed session accepted a tool or claimed a terminal reply: %v", err)
	}
	if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant}); err == nil {
		t.Fatal("expired session accepted a late submission")
	}
}

func TestAgentToolSessionFinalSurvivesCancellationAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, files, err := startAgentToolSession(ctx, domain.User{}, "", AgentToolAssistant, assistantExecMode{}, "", nil, cancel, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer files.Cleanup()
	reply := assistantReply{Message: "完成", Drafts: []assistantDraft{}}
	if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &reply}); err != nil {
		t.Fatal(err)
	}
	session.Close()
	if ctx.Err() == nil || !session.finished() {
		t.Fatal("successful terminal submission was lost during cleanup")
	}
	result, ok := session.takeFinal()
	if !ok || result.Assistant == nil || result.Assistant.Message != "完成" {
		t.Fatalf("cleanup lost the committed result: %+v", result)
	}
}

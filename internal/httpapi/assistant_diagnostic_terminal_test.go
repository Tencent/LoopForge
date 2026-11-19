package httpapi

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestAssistantDiagnosticTerminalCommitDistinguishesCancellation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		commit    bool
		err       error
		wantState string
	}{
		{"terminal cleanup", true, context.Canceled, "completed"},
		{"external cancellation", false, context.Canceled, "cancelled"},
		{"deadline", false, context.DeadlineExceeded, "failed"},
		{"unrelated failure after commit", true, errors.New("provider failed"), "failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := domain.AssistantGeneration{ID: tt.name, Status: "running", StartedAt: time.Now().UTC()}
			d := newAssistantDiagnosticsCollector(g)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), assistantDiagnosticsKey{}, d))
			defer cancel()
			attempt := d.begin("codebuddy", "test-model", false, domain.User{})
			session := &AgentToolSession{ctx: ctx, Final: make(chan AgentTerminalResult, 1), CancelModel: cancel}
			attempt.terminalSession = session
			if tt.commit {
				if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "Done"}}); err != nil {
					t.Fatal(err)
				}
				if ctx.Err() != context.Canceled {
					t.Fatal("terminal commit did not cancel the model")
				}
			} else {
				cancel()
			}
			attempt.finish(tt.err)
			report := d.snapshot(g)
			got := report.Attempts[0]
			if got.Status != tt.wantState || got.FinishedAt == nil {
				t.Fatalf("attempt outcome = %+v, want %s", got, tt.wantState)
			}
			if tt.wantState == "completed" && got.Error != "" {
				t.Fatalf("normal terminal cleanup recorded an error: %s", got.Error)
			}
			if tt.wantState != "completed" && got.Error == "" {
				t.Fatal("lost real failure or cancellation evidence")
			}
			last := report.Timeline[len(report.Timeline)-1]
			if last.Kind != "cli."+tt.wantState {
				t.Fatalf("timeline outcome = %+v", last)
			}
		})
	}
}

func TestAssistantDiagnosticTerminalReplyPersistsCompletedGeneration(t *testing.T) {
	h, user := workbenchFixture(t)
	const sessionID = "terminal-completed-diagnostic"
	root := t.TempDir()
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		session := assistantToolSessionForChat(sessionID)
		if session == nil {
			return "", errors.New("missing assistant tool session")
		}
		raw, _, err := runAssistantCLI(ctx, assistantCLIRequest{
			Prompt: prompt, SessionRoot: root, Native: true, RemoteBinary: true, TerminalSession: session,
			Execute: func(ctx context.Context, _ string, _ runtime.CommandSpec, _ runtime.RunCredentials, _ runtime.EventSink) (string, error) {
				session := assistantToolSessionForChat(sessionID)
				if session == nil {
					return "", errors.New("missing assistant tool session")
				}
				if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "已完成需求分析"}}); err != nil {
					return "", err
				}
				return "", ctx.Err()
			},
		}, user, nil)
		return raw, err
	}
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"`+sessionID+`","messages":[{"role":"user","content":"分析需求"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	g := waitGeneration(t, h, user.ID, sessionID, func(g domain.AssistantGeneration) bool { return g.Status != "running" })
	report, ok := h.repo.AssistantDiagnostics(user.ID, sessionID, g.ID)
	if !ok || g.Status != "completed" || report.Status != "completed" || report.Error != "" || len(report.Attempts) != 1 {
		t.Fatalf("terminal reply did not persist a completed diagnostic: generation=%+v report=%+v", g, report)
	}
	if report.Attempts[0].Status != "completed" || report.Attempts[0].Error != "" {
		t.Fatalf("successful terminal reply recorded a cancelled attempt: %+v", report.Attempts[0])
	}
}

func TestAssistantDiagnosticTerminalCommitOnlyCompletesActiveAttempt(t *testing.T) {
	g := domain.AssistantGeneration{ID: "terminal-attempts", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), assistantDiagnosticsKey{}, d))
	defer cancel()
	previous := d.begin("codebuddy", "test-model", false, domain.User{})
	previous.finish(context.Canceled)
	active := d.begin("codebuddy", "test-model", true, domain.User{})
	review := d.begin("codebuddy", "review-model", false, domain.User{})
	session := &AgentToolSession{ctx: ctx, Final: make(chan AgentTerminalResult, 1), CancelModel: cancel}
	active.terminalSession = session
	if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "Done"}}); err != nil {
		t.Fatal(err)
	}
	active.finish(context.Canceled)
	review.finish(context.Canceled)
	later := d.begin("codebuddy", "test-model", true, domain.User{})
	later.finish(context.Canceled)
	report := d.snapshot(g)
	for i, want := range []string{"cancelled", "completed", "cancelled", "cancelled"} {
		if report.Attempts[i].Status != want {
			t.Fatalf("attempt %d status = %s, want %s", i, report.Attempts[i].Status, want)
		}
	}
}

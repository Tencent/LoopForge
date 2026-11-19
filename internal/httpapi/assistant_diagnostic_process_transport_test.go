package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestAssistantDiagnosticProcessEvidenceSurvivesRunnerJSON(t *testing.T) {
	for _, transport := range []string{"direct", "runner JSON", "runner JSON numbers"} {
		t.Run(transport, func(t *testing.T) {
			g := domain.AssistantGeneration{ID: transport, Status: "running", StartedAt: time.Now().UTC()}
			d := newAssistantDiagnosticsCollector(g)
			a := d.begin("codebuddy", "test-model", false, domain.User{})
			started := time.Now().UTC()
			events := []domain.TelemetryEvent{
				{Name: "process.started", Timestamp: started, Message: "private-command-argument", Attributes: map[string]any{"pid": 1234}},
				{Name: "process.stdout", Message: "output"},
				{Name: "process.finished", Attributes: map[string]any{"exitCode": 7, "signal": "terminated", "userCpuMs": int64(12), "systemCpuMs": int64(3)}},
			}
			for _, event := range events {
				if transport != "direct" {
					raw, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					var decoded domain.TelemetryEvent
					decoder := json.NewDecoder(bytes.NewReader(raw))
					if transport == "runner JSON numbers" {
						decoder.UseNumber()
					}
					if err := decoder.Decode(&decoded); err != nil {
						t.Fatal(err)
					}
					event = decoded
				}
				a.process(event)
				if event.Name == "process.started" {
					p := d.snapshot(g).Attempts[0].Process
					if p.State != "running" || p.PID != 1234 || p.StartedAt == nil || !p.StartedAt.Equal(started) {
						t.Fatalf("lost started evidence: %+v", p)
					}
				}
			}
			report := d.snapshot(g)
			attempt := report.Attempts[0]
			p := attempt.Process
			if p.State != "exited" || p.StdoutLines != 1 || p.FinishedAt == nil || p.Signal != "terminated" || p.UserCPUMs != 12 || p.SystemCPUMs != 3 || attempt.ExitCode == nil || *attempt.ExitCode != 7 {
				t.Fatalf("lost finished evidence: %+v process=%+v", attempt, p)
			}
			raw, _ := json.Marshal(report)
			if bytes.Contains(raw, []byte("private-command-argument")) {
				t.Fatal("diagnostics exposed command arguments")
			}
		})
	}
}

func TestConversationExecutorKeepsExitEvidenceAfterCancellation(t *testing.T) {
	h, user := workbenchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &AgentToolSession{ctx: ctx, ChatOwnerID: user.ID, ChatSessionID: "process-cancel-evidence", User: user}
	var events []domain.TelemetryEvent
	_, err := h.conversationExecutor(session, t.TempDir())(ctx, "", runtime.CommandSpec{Argv: []string{"sh", "-c", "printf 'ready\\n'; sleep 30"}}, runtime.RunCredentials{}, func(event domain.TelemetryEvent) {
		events = append(events, event)
		if event.Name == "process.stdout" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation outcome: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Name != "process.finished" {
		t.Fatalf("stopped process lost its final evidence: %+v", events)
	}
	if _, ok := events[len(events)-1].Attributes["exitCode"]; !ok {
		t.Fatal("stopped process lost its exit code")
	}
}

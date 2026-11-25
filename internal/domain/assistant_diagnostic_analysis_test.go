package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAnalyzeAssistantDiagnosticsIdempotencyAndBoundaries(t *testing.T) {
	started := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	checkpoint := started.Add(12 * time.Second)
	report := AssistantDiagnostics{
		Version:          2,
		GenerationID:     "gen-hang-01",
		SessionID:        "sess-01",
		Status:           "interrupted",
		StartedAt:        started,
		UpdatedAt:        checkpoint.Add(time.Minute),
		LastCheckpointAt: &checkpoint,
		Environment: &AssistantDiagnosticEnvironment{
			PlatformVersion: "v0.9.0",
			InstanceID:      "api-node-a",
		},
		Termination: &AssistantDiagnosticTermination{
			At:      checkpoint.Add(time.Minute),
			Code:    "CHECKPOINT_LOST",
			Source:  "platform",
			Message: "超过 45 秒未收到运行实例心跳，生成已中断",
		},
		Attempts: []AssistantCLIAttempt{{
			ID:     "gen-hang-01/1",
			Engine: "codex",
			Status: "running",
			Process: &AssistantDiagnosticProcess{
				PID:         4321,
				State:       "running",
				StdoutLines: 0,
				StderrLines: 0,
			},
		}},
		Tools: []AssistantDiagnosticTool{{
			ID:        "gateway/1",
			AttemptID: "gen-hang-01/1",
			Source:    "gateway",
			Name:      "tapd__stories_list",
			Status:    "running",
			StartedAt: started.Add(2 * time.Second),
		}},
	}

	AnalyzeAssistantDiagnostics(&report)
	firstEvidenceCount := len(report.Assessment.Evidence)
	if report.Tools[0].Status != "unfinished" {
		t.Fatalf("expected running tool to become unfinished, got %q", report.Tools[0].Status)
	}
	if report.Attempts[0].Process.State != "unknown_after_interruption" {
		t.Fatalf("expected running process to become unknown_after_interruption, got %q", report.Attempts[0].Process.State)
	}

	// Running analysis again on an already-analyzed snapshot must preserve identical evidence.
	AnalyzeAssistantDiagnostics(&report)
	if len(report.Assessment.Evidence) != firstEvidenceCount {
		t.Fatalf("non-idempotent evidence count: got %d, want %d (%v)", len(report.Assessment.Evidence), firstEvidenceCount, report.Assessment.Evidence)
	}

	joined := strings.Join(report.Assessment.Evidence, "\n") + "\n" + strings.Join(report.Assessment.NextSteps, "\n")
	for _, want := range []string{"tapd__stories_list", "CHECKPOINT_LOST", "2026-11-13T08:00:12Z", "PID 4321", "api-node-a"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in assessment:\n%s", want, joined)
		}
	}
}

func TestAnalyzeAssistantDiagnosticsUserCancelWithEarlyError(t *testing.T) {
	now := time.Now().UTC()
	report := AssistantDiagnostics{
		Version:      2,
		GenerationID: "gen-cancel-401",
		SessionID:    "sess-02",
		Status:       "cancelled",
		StartedAt:    now,
		UpdatedAt:    now,
		Termination: &AssistantDiagnosticTermination{
			At:      now,
			Code:    "USER_CANCELLED",
			Source:  "platform",
			Message: "用户主动停止了本轮生成",
		},
		Attempts: []AssistantCLIAttempt{{
			ID:         "gen-cancel-401/1",
			Engine:     "codex",
			CLIVersion: "0.163.0",
			Status:     "cancelled",
			FirstError: &AssistantDiagnosticEvent{
				At:         now,
				Kind:       "cli.stderr.sampling_error",
				StatusCode: 401,
				RequestID:  "req-early-401",
				Message:    "unexpected status 401 Unauthorized",
			},
		}},
	}
	AnalyzeAssistantDiagnostics(&report)
	if report.Assessment.Outcome != "cancelled_by_user" {
		t.Fatalf("unexpected outcome: %+v", report.Assessment)
	}
	joined := strings.Join(report.Assessment.Evidence, "\n") + "\n" + strings.Join(report.Assessment.NextSteps, "\n")
	if !strings.Contains(joined, "firstError") || !strings.Contains(joined, "HTTP 401") || !strings.Contains(joined, "requestId") {
		t.Fatalf("missing early error context on user cancel:\n%s", joined)
	}
}

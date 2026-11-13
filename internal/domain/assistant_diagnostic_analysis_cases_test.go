package domain

import (
	"strings"
	"testing"
	"time"
)

func diagnosticJoined(a *AssistantDiagnosticAssessment) string {
	return strings.Join(a.Evidence, "\n") + "\n" + strings.Join(a.Unknowns, "\n") + "\n" + strings.Join(a.NextSteps, "\n")
}

func TestAnalyzeAssistantDiagnosticsSkipsNilAndLegacyReports(t *testing.T) {
	AnalyzeAssistantDiagnostics(nil)
	legacy := AssistantDiagnostics{Version: 1, Status: "failed", Failure: &AssistantFailure{Code: "NETWORK_ERROR"}}
	AnalyzeAssistantDiagnostics(&legacy)
	if legacy.Assessment != nil || legacy.Coverage != nil {
		t.Fatalf("v1 report must stay untouched: %+v", legacy)
	}
}

func TestAnalyzeAssistantDiagnosticsFailureClassification(t *testing.T) {
	cases := []struct {
		code, outcome, unknown string
	}{
		{"AUTHENTICATION_FAILED", "failure_observed", "上游拒绝或服务异常已被观察到"},
		{"RATE_LIMITED", "failure_observed", "上游拒绝或服务异常已被观察到"},
		{"UPSTREAM_UNAVAILABLE", "failure_observed", "上游拒绝或服务异常已被观察到"},
		{"NETWORK_ERROR", "failure_observed", "连接失败的具体网络节点"},
		{"CLI_EXIT_NONZERO", "undetermined", "不足以单独判定 CLI 内部根因"},
		{"MISSING_RESULT", "undetermined", "不足以单独判定 CLI 内部根因"},
		{"GENERATION_FAILED", "undetermined", "不足以单独判定 CLI 内部根因"},
		{"CLI_NOT_INSTALLED", "failure_observed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			report := AssistantDiagnostics{
				Version:      2,
				GenerationID: "gen-" + tc.code,
				Status:       "failed",
				Failure:      &AssistantFailure{Code: tc.code, Phase: "cli", Message: "失败：" + tc.code},
				Environment:  &AssistantDiagnosticEnvironment{InstanceID: "api-1"},
				Attempts:     []AssistantCLIAttempt{{ID: "a/1", CLIVersion: "1.0.0", Status: "failed"}},
			}
			AnalyzeAssistantDiagnostics(&report)
			a := report.Assessment
			if a.Outcome != tc.outcome || a.Summary != "失败："+tc.code {
				t.Fatalf("outcome/summary = %q/%q, want %q", a.Outcome, a.Summary, tc.outcome)
			}
			joined := diagnosticJoined(a)
			if !strings.Contains(joined, "失败阶段：cli；分类："+tc.code) {
				t.Fatalf("missing failure evidence:\n%s", joined)
			}
			if tc.unknown != "" && !strings.Contains(joined, tc.unknown) {
				t.Fatalf("missing unknown %q:\n%s", tc.unknown, joined)
			}
			if tc.unknown == "" && len(a.Unknowns) != 0 {
				t.Fatalf("unexpected unknowns for %s: %v", tc.code, a.Unknowns)
			}
			if !strings.Contains(joined, "diagnostic=gen-"+tc.code+" 与 instance=api-1") {
				t.Fatalf("failed run must point to server logs:\n%s", joined)
			}
		})
	}
}

func TestAnalyzeAssistantDiagnosticsFailedWithoutFailureDetail(t *testing.T) {
	report := AssistantDiagnostics{Version: 2, Status: "failed"}
	AnalyzeAssistantDiagnostics(&report)
	if report.Assessment.Outcome != "undetermined" || report.Assessment.Summary != "现有证据不足以确认失败原因" {
		t.Fatalf("failed without detail must stay undetermined: %+v", report.Assessment)
	}
}

func TestAnalyzeAssistantDiagnosticsTerminationOutcomes(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for code, outcome := range map[string]string{
		"USER_STEER":      "redirected_by_user",
		"SERVER_SHUTDOWN": "termination_recorded",
		"CHECKPOINT_LOST": "termination_recorded",
	} {
		report := AssistantDiagnostics{
			Version:     2,
			Status:      "interrupted",
			Termination: &AssistantDiagnosticTermination{At: now, Code: code, Message: "结束：" + code},
			Attempts:    []AssistantCLIAttempt{{ID: "t/1", CLIVersion: "1", FirstError: &AssistantDiagnosticEvent{Kind: "cli.stderr"}}},
		}
		AnalyzeAssistantDiagnostics(&report)
		a := report.Assessment
		if a.Outcome != outcome || a.Summary != "结束："+code {
			t.Fatalf("%s: outcome %q summary %q", code, a.Outcome, a.Summary)
		}
		joined := diagnosticJoined(a)
		if !strings.Contains(joined, "平台记录的结束原因："+code) {
			t.Fatalf("%s: missing termination evidence:\n%s", code, joined)
		}
		if strings.Contains(joined, "用户停止前") {
			t.Fatalf("%s: early-error hint is only for USER_CANCELLED:\n%s", code, joined)
		}
		// Without lastCheckpointAt the heartbeat line must not be fabricated.
		if strings.Contains(joined, "最后一次运行实例心跳时间") {
			t.Fatalf("%s: heartbeat time without lastCheckpointAt:\n%s", code, joined)
		}
		hasLostUnknown := strings.Contains(joined, "只确认平台未继续保存执行状态")
		if hasLostUnknown != (code == "CHECKPOINT_LOST") {
			t.Fatalf("%s: CHECKPOINT_LOST unknown presence = %v", code, hasLostUnknown)
		}
	}
}

func TestAnalyzeAssistantDiagnosticsStatusWithoutTermination(t *testing.T) {
	completed := AssistantDiagnostics{Version: 2, Status: "completed", Environment: &AssistantDiagnosticEnvironment{InstanceID: "api-1"},
		Tools: []AssistantDiagnosticTool{{ID: "x", Name: "read", Source: "cli", Status: "failed", RequestID: "req-1"}}}
	AnalyzeAssistantDiagnostics(&completed)
	if completed.Assessment.Outcome != "completed" {
		t.Fatalf("completed outcome = %+v", completed.Assessment)
	}
	joined := diagnosticJoined(completed.Assessment)
	if !strings.Contains(joined, "记录到 1 次工具失败（首个：read（来源：cli））") || !strings.Contains(joined, "requestId") {
		t.Fatalf("recovered tool failure must stay visible:\n%s", joined)
	}
	if strings.Contains(joined, "instance=api-1") {
		t.Fatalf("completed run must not ask to search server logs:\n%s", joined)
	}

	running := AssistantDiagnostics{Version: 2, Status: "running",
		Attempts: []AssistantCLIAttempt{{ID: "r/1", CLIVersion: "1", Process: &AssistantDiagnosticProcess{PID: 77, State: "running"}}}}
	AnalyzeAssistantDiagnostics(&running)
	a := running.Assessment
	joined = diagnosticJoined(a)
	if a.Outcome != "pending" || running.Attempts[0].Process.State != "running" {
		t.Fatalf("live snapshot must stay pending/running: %+v %+v", a, running.Attempts[0].Process)
	}
	if !strings.Contains(joined, "未观察到正在执行的工具") || !strings.Contains(joined, "PID 77") {
		t.Fatalf("idle running snapshot evidence missing:\n%s", joined)
	}

	busy := AssistantDiagnostics{Version: 2, Status: "running",
		Tools: []AssistantDiagnosticTool{{ID: "g/1", Name: "tapd", Source: "gateway", Status: "running"}}}
	AnalyzeAssistantDiagnostics(&busy)
	if busy.Tools[0].Status != "running" || strings.Contains(diagnosticJoined(busy.Assessment), "未观察到正在执行的工具") {
		t.Fatalf("running tool must stay running and suppress idle unknown: %+v", busy.Assessment)
	}

	for _, status := range []string{"interrupted", "cancelled"} {
		report := AssistantDiagnostics{Version: 2, Status: status}
		AnalyzeAssistantDiagnostics(&report)
		joined := diagnosticJoined(report.Assessment)
		if report.Assessment.Outcome != "undetermined" || !strings.Contains(joined, "不能认定为用户主动停止") || !strings.Contains(joined, "优先检查 firstError") {
			t.Fatalf("%s without termination: %+v", status, report.Assessment)
		}
	}
}

func TestAnalyzeAssistantDiagnosticsProcessSignalsAndIdentifiers(t *testing.T) {
	exit := 137
	report := AssistantDiagnostics{
		Version:   2,
		Status:    "failed",
		Truncated: true,
		Attempts: []AssistantCLIAttempt{
			{ID: "p/1", Status: "failed", ExitCode: &exit, Process: &AssistantDiagnosticProcess{State: "exited", Signal: "killed", StdoutLines: 3}},
			{ID: "p/2", Status: "failed", Process: &AssistantDiagnosticProcess{State: "start_failed"},
				Events: []AssistantDiagnosticEvent{{Kind: "cli.error", TraceID: "trace-9"}}},
		},
	}
	AnalyzeAssistantDiagnostics(&report)
	a := report.Assessment
	joined := diagnosticJoined(a)
	for _, want := range []string{
		"调用 p/1 的进程退出码为 137，协议结果为 failed",
		"调用 p/1 收到进程信号 killed",
		"进程信号不能单独证明 OOM",
		"调用 p/2 的 CLI 进程启动失败",
		"traceId",
		"部分证据已按上限截断",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q:\n%s", want, joined)
		}
	}
	// Both attempts lack a CLI version, but the unknown is reported once.
	if n := strings.Count(strings.Join(a.Unknowns, "\n"), "CLI 未提供版本"); n != 1 {
		t.Fatalf("missing-version unknown repeated %d times: %v", n, a.Unknowns)
	}
	if strings.Contains(joined, "requestId") {
		t.Fatalf("no requestId was recorded:\n%s", joined)
	}
}

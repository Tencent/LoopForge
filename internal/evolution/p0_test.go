package evolution

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func int64Ptr(v int64) *int64 { return &v }
func intPtr(v int) *int       { return &v }
func boolPtr(v bool) *bool    { return &v }

func TestBuildToolTraceSummary(t *testing.T) {
	base := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	events := []domain.TelemetryEvent{
		// Deliberately newest-first to verify chronological normalization.
		{
			ID: "e6", Category: "tool", NodeID: "code", Sequence: 6, Timestamp: base.Add(9 * time.Second),
			Phase: "finish", ToolCallID: "c4",
			Tool:       &domain.ToolObservation{Name: "bash", CallID: "c4", Success: boolPtr(false), DurationMs: 4200},
			Error:      &domain.ErrorObservation{Message: "exit status 1: missing mock"},
			Attributes: map[string]any{"command": "go test ./...", "outputBytes": 16384},
		},
		{
			ID: "e5", Category: "tool", NodeID: "code", Sequence: 5, Timestamp: base.Add(5 * time.Second),
			Phase: "start", ToolCallID: "c4",
			Tool:       &domain.ToolObservation{Name: "bash", CallID: "c4"},
			Attributes: map[string]any{"command": "go test ./..."},
		},
		{
			ID: "e4", Category: "tool", NodeID: "code", Sequence: 4, Timestamp: base.Add(4 * time.Second),
			Phase: "finish", ToolCallID: "c3",
			Tool:       &domain.ToolObservation{Name: "read_file", CallID: "c3", Success: boolPtr(true), DurationMs: 120},
			Attributes: map[string]any{"path": "internal/repo/evolution.go"},
		},
		{
			ID: "e3", Category: "tool", NodeID: "code", Sequence: 3, Timestamp: base.Add(3 * time.Second),
			Phase: "finish", ToolCallID: "c2",
			Tool:       &domain.ToolObservation{Name: "read_file", CallID: "c2", Success: boolPtr(true), DurationMs: 100},
			Attributes: map[string]any{"path": "internal/repo/evolution.go"},
		},
		{
			ID: "e2", Category: "tool", NodeID: "code", Sequence: 2, Timestamp: base.Add(2 * time.Second),
			Phase: "finish", ToolCallID: "c1",
			Tool:       &domain.ToolObservation{Name: "read_file", CallID: "c1", Success: boolPtr(true), DurationMs: 90},
			Attributes: map[string]any{"path": "internal/repo/evolution.go"},
		},
		// Other node event should be excluded when filtering to "code".
		{
			ID: "e1", Category: "tool", NodeID: "other", Sequence: 1, Timestamp: base.Add(1 * time.Second),
			Phase: "finish", ToolCallID: "c0",
			Tool:       &domain.ToolObservation{Name: "grep", CallID: "c0", Success: boolPtr(true)},
			Attributes: map[string]any{"pattern": "ignored"},
		},
	}

	trace := BuildToolTraceSummary(events, "code")
	if trace == nil {
		t.Fatal("expected non-nil tool trace")
	}
	if trace.TotalCalls != 4 || trace.FailedCalls != 1 || trace.DuplicateCalls != 2 {
		t.Fatalf("unexpected counts: %+v", trace)
	}
	if len(trace.TopTools) < 2 || trace.TopTools[0].Tool != "read_file" || trace.TopTools[0].Calls != 3 || trace.TopTools[0].Duplicates != 2 {
		t.Fatalf("unexpected top tools: %+v", trace.TopTools)
	}
	if len(trace.Duplicates) != 1 || !strings.Contains(trace.Duplicates[0], "read_file(internal/repo/evolution.go) ×3") {
		t.Fatalf("unexpected duplicates: %+v", trace.Duplicates)
	}
	if len(trace.Failures) != 1 || !strings.Contains(trace.Failures[0], "bash(go test ./...)") || !strings.Contains(trace.Failures[0], "missing mock") {
		t.Fatalf("unexpected failures: %+v", trace.Failures)
	}
	if len(trace.HeavyCalls) != 1 || !strings.Contains(trace.HeavyCalls[0], "4.2s") || !strings.Contains(trace.HeavyCalls[0], "16.0KB") {
		t.Fatalf("unexpected heavy calls: %+v", trace.HeavyCalls)
	}
	if len(trace.Steps) != 4 || !strings.HasPrefix(trace.Steps[0], "1. read_file(") || !strings.HasPrefix(trace.Steps[3], "4. bash(") {
		t.Fatalf("unexpected chronological steps: %+v", trace.Steps)
	}
}

func TestBuildToolTraceSummaryTruncatesLongSteps(t *testing.T) {
	base := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	events := make([]domain.TelemetryEvent, 0, 22)
	for i := 1; i <= 22; i++ {
		events = append(events, domain.TelemetryEvent{
			ID: "e", Category: "tool", NodeID: "code", Sequence: int64(i), Timestamp: base.Add(time.Duration(i) * time.Second),
			Tool: &domain.ToolObservation{Name: "read_file", Success: boolPtr(true)},
		})
	}
	trace := BuildToolTraceSummary(events, "code")
	if trace == nil || len(trace.Steps) != maxTraceSteps {
		t.Fatalf("expected %d steps, got %+v", maxTraceSteps, trace)
	}
	if !strings.Contains(trace.Steps[10], "省略中间 7 步") {
		t.Fatalf("expected omission marker at index 10, got %q", trace.Steps[10])
	}
}

func TestBuildEvidenceWithToolsAndRefinePrompt(t *testing.T) {
	run := domain.Run{
		ID: "run-1", WorkflowID: "wf-1", Status: "succeeded",
		Usage: domain.TokenUsage{TotalTokens: 120000}, ToolCalls: 8,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded"}},
	}
	finding := domain.EvoFinding{
		ID: "f-1", AssetKind: domain.EvoAssetAgent, AssetID: "ag-1", AssetName: "开发",
		Title:   "减少 Token 消耗",
		Summary: "平均 Token 120000",
		Signals: []domain.EvoSignal{{RunID: "run-1", NodeID: "code", Kind: SignalSOPPreference, Detail: "高消耗"}},
	}
	tools := []domain.TelemetryEvent{
		{Category: "tool", NodeID: "code", Sequence: 1, Tool: &domain.ToolObservation{Name: "grep", Success: boolPtr(true)}, Attributes: map[string]any{"pattern": "TODO"}},
		{Category: "tool", NodeID: "code", Sequence: 2, Tool: &domain.ToolObservation{Name: "grep", Success: boolPtr(true)}, Attributes: map[string]any{"pattern": "TODO"}},
	}

	ev := BuildEvidenceWithTools(run, finding, tools)
	if ev.ToolTrace == nil || ev.ToolTrace.DuplicateCalls != 1 {
		t.Fatalf("expected tool trace on evidence, got %+v", ev)
	}
	sys, prompt := BuildImproverPrompt(domain.EvoAssetAgent, "开发", "原始正文", finding, []Evidence{ev}, []string{"[避免重复失败] 粗暴截断"})
	if !strings.Contains(sys, "toolTrace") || !strings.Contains(prompt, "grep(TODO) ×2") || !strings.Contains(prompt, "避免重复失败") {
		t.Fatalf("improver prompt missing toolTrace or negative experience:\nsys=%s\nprompt=%s", sys, prompt)
	}

	refSys, refPrompt := BuildRefinePrompt(
		domain.EvoAssetAgent, "开发", "原始正文", "第一轮草稿", finding,
		[]string{domain.EvoPrefTokens}, "未通过（P1 省 Token 未达标）",
		[]ReplayCritique{{SourceRunID: "run-1", Outcome: domain.EvoOutcomeSame, SourceTokens: 120000, ReplayTokens: 118000, ToolTrace: ev.ToolTrace}},
		"隐藏留出/回归集共 1 条：修好 0，回归 0。",
		[]string{"[有效经验] 合并读取"},
	)
	if !strings.Contains(refSys, "失败回放反思修正") || !strings.Contains(refPrompt, "第一轮草稿") || !strings.Contains(refPrompt, "隐藏留出/回归集汇总") || !strings.Contains(refPrompt, "grep(TODO) ×2") {
		t.Fatalf("refine prompt missing expected reflection context:\nsys=%s\nprompt=%s", refSys, refPrompt)
	}
}

func TestSummarizeWithPreferencesGoalGate(t *testing.T) {
	makeReplay := func(group, outcome string, srcScore, repScore int, srcTok, repTok int64, srcTools, repTools int, srcMs, repMs int64) domain.EvoReplay {
		return domain.EvoReplay{
			Group: group, Outcome: outcome, SourceScore: srcScore, Score: repScore,
			SourceMeasurements: &domain.EvoReplayMeasurements{Tokens: int64Ptr(srcTok), ToolCalls: intPtr(srcTools), ExecutionMs: int64Ptr(srcMs)},
			ReplayMeasurements: &domain.EvoReplayMeasurements{Tokens: int64Ptr(repTok), ToolCalls: intPtr(repTools), ExecutionMs: int64Ptr(repMs)},
		}
	}

	// P1=tokens, source already clean (fixable==0): 20% token drop passes!
	passed := SummarizeWithPreferences([]domain.EvoReplay{
		makeReplay(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame, 2, 2, 100000, 80000, 10, 8, 60000, 50000),
		makeReplay(domain.EvoGroupHoldout, domain.EvoOutcomeSame, 2, 2, 100000, 80000, 10, 8, 60000, 50000),
	}, []string{domain.EvoPrefTokens})
	if !passed.Passed || !passed.GoalMet || passed.GoalMetric != domain.EvoPrefTokens || passed.GoalDeltaPct != -20.0 {
		t.Fatalf("expected P1 tokens pass, got %+v", passed)
	}

	// P1=tokens, source already clean, no regression, but tokens only dropped 2% -> MUST FAIL!
	flat := SummarizeWithPreferences([]domain.EvoReplay{
		makeReplay(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame, 2, 2, 100000, 98000, 10, 10, 60000, 59000),
	}, []string{domain.EvoPrefTokens})
	if flat.Passed || flat.GoalMet || !strings.Contains(flat.Verdict, "未达标") {
		t.Fatalf("expected flat P1 tokens replay to fail, got %+v", flat)
	}

	// P1=tools: 10 -> 8 (-20%) passes; 10 -> 10 fails.
	toolsOK := SummarizeWithPreferences([]domain.EvoReplay{
		makeReplay(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame, 2, 2, 100000, 99000, 10, 8, 60000, 60000),
	}, []string{domain.EvoPrefTools})
	if !toolsOK.Passed || !toolsOK.GoalMet || toolsOK.GoalMetric != domain.EvoPrefTools {
		t.Fatalf("expected P1 tools pass, got %+v", toolsOK)
	}
	toolsFlat := SummarizeWithPreferences([]domain.EvoReplay{
		makeReplay(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame, 2, 2, 100000, 80000, 10, 10, 60000, 50000),
	}, []string{domain.EvoPrefTools})
	if toolsFlat.Passed || toolsFlat.GoalMet {
		t.Fatalf("expected flat P1 tools replay to fail even if tokens dropped, got %+v", toolsFlat)
	}

	// P1=duration: 60s -> 50s (-16.7%) passes; 60s -> 65s fails.
	durOK := SummarizeWithPreferences([]domain.EvoReplay{
		makeReplay(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame, 2, 2, 100000, 100000, 10, 10, 60000, 50000),
	}, []string{domain.EvoPrefDuration})
	if !durOK.Passed || !durOK.GoalMet {
		t.Fatalf("expected P1 duration pass, got %+v", durOK)
	}

	// P1=clean or nil preserves feedback pass behavior when nothing regressed.
	cleanOnly := SummarizeWithPreferences([]domain.EvoReplay{
		makeReplay(domain.EvoGroupDiagnosis, domain.EvoOutcomeSame, 2, 2, 100000, 100000, 10, 10, 60000, 60000),
	}, []string{domain.EvoPrefClean})
	if !cleanOnly.Passed || cleanOnly.GoalMetric != "" {
		t.Fatalf("expected clean-only preference to keep legacy pass rule, got %+v", cleanOnly)
	}
}

func TestJudgeCanaryWithPreferencesGoalGate(t *testing.T) {
	base := domain.EvoCanary{
		MinRuns: 5,
		Control: domain.EvoArmStats{Runs: 5, Clean: 5, Rate: 1, AvgTokens: 100000, AvgTools: 10, AvgDuration: 60},
		Canary:  domain.EvoArmStats{Runs: 5, Clean: 5, Rate: 1, AvgTokens: 82000, AvgTools: 8, AvgDuration: 48},
	}
	status, verdict := JudgeCanaryWithPreferences(base, []string{domain.EvoPrefTokens})
	if status != domain.EvoCanaryPassed || !strings.Contains(verdict, "已达成 P1 目标") {
		t.Fatalf("expected P1 tokens canary pass, got status=%s verdict=%q", status, verdict)
	}

	// Clean rate holds (5/5 vs 5/5), but candidate uses MORE tokens -> MUST FAIL!
	worseTok := base
	worseTok.Canary.AvgTokens = 106000
	status, verdict = JudgeCanaryWithPreferences(worseTok, []string{domain.EvoPrefTokens})
	if status != domain.EvoCanaryFailed || !strings.Contains(verdict, "未达成 P1 目标") {
		t.Fatalf("expected higher-token canary to fail P1 tokens gate, got status=%s verdict=%q", status, verdict)
	}

	// Candidate lowers tokens massively, but clean rate collapses -> MUST FAIL correctness guardrail!
	worseClean := base
	worseClean.Canary = domain.EvoArmStats{Runs: 5, Clean: 1, Failed: 4, Rate: 0.2, AvgTokens: 20000}
	status, _ = JudgeCanaryWithPreferences(worseClean, []string{domain.EvoPrefTokens})
	if status != domain.EvoCanaryFailed {
		t.Fatalf("expected degraded clean rate to fail regardless of token savings, got %s", status)
	}

	// Still collecting runs shows live P1 delta in verdict.
	running := base
	running.Canary.Runs = 3
	running.Canary.Clean = 3
	status, verdict = JudgeCanaryWithPreferences(running, []string{domain.EvoPrefTokens})
	if status != domain.EvoCanaryRunning || !strings.Contains(verdict, "当前 P1 省 Token") {
		t.Fatalf("expected running canary verdict with live P1 progress, got status=%s verdict=%q", status, verdict)
	}
}

package evolution

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestScoreRunProcessDeterministicAndRelativeBaseline(t *testing.T) {
	now := time.Now().UTC()
	mkRun := func(id, status string, tok int64, tools int, durSec int64, rework int, telemetry []domain.TelemetryEvent) domain.Run {
		return domain.Run{
			ID:        id,
			Status:    status,
			CreatedAt: now,
			Usage:     domain.TokenUsage{TotalTokens: tok},
			ToolCalls: tools,
			Timing:    &domain.RunTiming{ExecutionMs: durSec * 1000},
			Nodes: map[string]*domain.NodeRun{
				"triage": {Status: status, ReworkCount: rework, Attempt: 1 + rework},
			},
			WorkflowSnapshot: domain.WorkflowDefinition{
				ID: "wf-1",
				Nodes: []domain.WorkflowNode{
					{ID: "triage", Name: "分流节点"},
				},
			},
			Telemetry: telemetry,
		}
	}

	// Build frozen baseline from 3 initial clean runs.
	baseRuns := []domain.Run{
		mkRun("b1", "succeeded", 200_000, 10, 60, 0, nil),
		mkRun("b2", "succeeded", 190_000, 9, 55, 0, nil),
		mkRun("b3", "succeeded", 210_000, 11, 65, 0, nil),
	}
	baseRuns[0].CreatedAt = now.Add(-3 * time.Hour)
	baseRuns[1].CreatedAt = now.Add(-2 * time.Hour)
	baseRuns[2].CreatedAt = now.Add(-1 * time.Hour)

	baseline := ComputeSOPScoreBaseline(baseRuns, 5)
	if !baseline.BaselineReady || baseline.MedianTokens != 200_000 || baseline.MedianTools != 10 || baseline.MedianDurationSec != 60 {
		t.Fatalf("unexpected frozen baseline: %+v", baseline)
	}

	// Exact determinism: same run scored twice returns identical score & deductions.
	improved := mkRun("r-imp", "succeeded", 130_000, 6, 36, 0, nil)
	s1 := ScoreRunProcess(improved, baseline)
	s2 := ScoreRunProcess(improved, baseline)
	if s1.ScorerVersion != domain.EvoScorerVersion || s1.Score != s2.Score || len(s1.Deductions) != len(s2.Deductions) {
		t.Fatalf("expected deterministic score, got s1=%+v s2=%+v", s1, s2)
	}
	if s1.Score <= baseline.BaselineScore+8 {
		t.Fatalf("expected improved run to score well above baseline %d, got %d (%+v)", baseline.BaselineScore, s1.Score, s1.Deductions)
	}

	// Duplicate tool calls & rework produce traceable event-level deductions.
	dupEvents := []domain.TelemetryEvent{
		{ID: "e1", Sequence: 1, Category: "tool", ToolCallID: "c1", Tool: &domain.ToolObservation{Name: "read_file", CallID: "c1"}, Attributes: map[string]any{"path": "a.go"}},
		{ID: "e2", Sequence: 2, Category: "tool", ToolCallID: "c2", Tool: &domain.ToolObservation{Name: "read_file", CallID: "c2"}, Attributes: map[string]any{"path": "a.go"}},
		{ID: "e3", Sequence: 3, Category: "tool", ToolCallID: "c3", Tool: &domain.ToolObservation{Name: "read_file", CallID: "c3"}, Attributes: map[string]any{"path": "a.go"}},
		{ID: "e4", Sequence: 4, Category: "tool", ToolCallID: "c4", Tool: &domain.ToolObservation{Name: "read_file", CallID: "c4"}, Attributes: map[string]any{"path": "a.go"}},
	}
	noisy := mkRun("r-noisy", "succeeded", 240_000, 14, 80, 1, dupEvents)
	sn := ScoreRunProcess(noisy, baseline)
	if sn.Score > maxReworkedProcessScore {
		t.Fatalf("expected reworked run <= %d, got %d", maxReworkedProcessScore, sn.Score)
	}
	var sawRework, sawDup bool
	for _, d := range sn.Deductions {
		if d.Dimension == "control" && d.Delta < 0 && strings.Contains(d.Reason, "返工") {
			sawRework = true
		}
		if d.Dimension == "tools" && d.Delta < 0 && strings.Contains(d.Reason, "重复") {
			sawDup = true
		}
	}
	if !sawRework || !sawDup {
		t.Fatalf("expected rework and duplicate tool deductions, got %+v", sn.Deductions)
	}

	// Failed run monotonicity invariant.
	failed := mkRun("r-fail", "failed", 1_000, 1, 2, 0, nil)
	sf := ScoreRunProcess(failed, baseline)
	if sf.Score > maxFailedProcessScore || sf.Score >= sn.Score {
		t.Fatalf("failed run must stay <= %d and below reworked run, got fail=%d reworked=%d", maxFailedProcessScore, sf.Score, sn.Score)
	}

	// SummarizeSOPRunsWithScorer uses process_v1 and never gets overwritten by AI evals.
	sum := SummarizeSOPRunsWithScorer([]domain.Run{improved}, baseline, nil)
	if sum.ScoreSource != "process_v1" || sum.ScorerVersion != domain.EvoScorerVersion || sum.Score == nil || *sum.Score != s1.Score {
		t.Fatalf("unexpected static summary: %+v", sum)
	}
}

func TestAssertionsEvaluateBacktestAndParse(t *testing.T) {
	goodRun := domain.Run{
		ID:     "run-1001",
		Status: "succeeded",
		Artifacts: []domain.Artifact{
			{Name: "triage.json", Path: "triage.json", Attempt: 1, Content: `{"ticketId":"TOKEN-01","route":"logistics","reason":"track parcel"}`},
		},
		Nodes: map[string]*domain.NodeRun{
			"n1": {Status: "succeeded", Output: "完成分流", GateResult: &domain.GateResult{Decision: "pass"}},
		},
	}
	badRouteRun := domain.Run{
		ID:     "run-1002",
		Status: "succeeded",
		Artifacts: []domain.Artifact{
			{Name: "triage.json", Path: "triage.json", Attempt: 1, Content: `{"ticketId":"TOKEN-01","route":"refund","reason":"wrong"}`},
		},
	}

	// One-sentence Chinese parsing.
	parsed1 := ParseAssertionFromSentence("必须生成 triage.json 且包含 ticketId、route、reason", []domain.Run{goodRun})
	if len(parsed1) == 0 || parsed1[0].Kind != domain.EvoAssertJSONValid || len(parsed1[0].RequiredFields) < 2 {
		t.Fatalf("expected json_valid with required fields, got %+v", parsed1)
	}
	parsed2 := ParseAssertionFromSentence("route 只能是 logistics / refund / human", []domain.Run{goodRun})
	if len(parsed2) == 0 || parsed2[0].Kind != domain.EvoAssertJSONField || len(parsed2[0].AllowedValues) != 3 {
		t.Fatalf("expected json_field enum assertion, got %+v", parsed2)
	}
	parsed3 := ParseAssertionFromSentence("run-1001 的 route 必须等于 logistics", []domain.Run{goodRun})
	if len(parsed3) == 0 || parsed3[0].SampleRunID != "run-1001" || parsed3[0].ExpectedValue != "logistics" {
		t.Fatalf("expected sample-scoped json_field assertion, got %+v", parsed3)
	}

	// Evaluate all assertions on goodRun vs badRouteRun.
	all := append(append([]domain.EvoSOPAssertion{}, parsed1...), parsed2...)
	all = append(all, parsed3...)
	repGood := EvaluateRunAssertions(goodRun, all)
	if !repGood.Configured || !repGood.AllPassed || repGood.Passed != repGood.Total {
		t.Fatalf("expected goodRun to pass all assertions, got %+v", repGood)
	}
	// Sample-scoped assertion for run-1001 must NOT falsely fail run-1002 unless replaying run-1001.
	repLiveOther := EvaluateRunAssertions(badRouteRun, all)
	if !repLiveOther.AllPassed {
		t.Fatalf("sample-scoped rule should skip unrelated live run, got %+v", repLiveOther)
	}
	repReplay1001 := EvaluateRunAssertionsForSource(badRouteRun, "run-1001", all)
	if repReplay1001.AllPassed {
		t.Fatalf("replaying run-1001 with wrong route must fail golden assertion, got %+v", repReplay1001)
	}

	// Backtest catches a misconfigured rule immediately.
	badRule, err := ValidateSOPAssertion(domain.EvoSOPAssertion{Kind: domain.EvoAssertArtifactExists, Enabled: true, Path: "report.pdf"})
	if err != nil {
		t.Fatalf("validate badRule: %v", err)
	}
	btBad := BacktestAssertion(badRule, []domain.Run{goodRun})
	if btBad.AllPassed || btBad.PassedRuns != 0 || btBad.TestedRuns != 1 {
		t.Fatalf("expected backtest 0/1 fail, got %+v", btBad)
	}

	// Conservative suggestion extracts intersection across clean runs.
	sugg := SuggestAssertionsFromRuns([]domain.Run{goodRun, badRouteRun})
	if len(sugg) == 0 || sugg[0].Kind != domain.EvoAssertJSONValid || sugg[0].Path != "triage.json" {
		t.Fatalf("expected suggested triage.json assertion, got %+v", sugg)
	}
}

func TestSemanticJudgeSwappedConsistencyAndReplayHardGate(t *testing.T) {
	rawR1 := `{"winner":"B","checklist":[{"criterion":"准确","aPass":true,"bPass":true}]}`
	rawR2 := `{"winner":"A","checklist":[{"criterion":"准确","aPass":true,"bPass":true}]}`
	w1, i1, ok1 := ParseSemanticJudgeResponse(rawR1, false)
	w2, i2, ok2 := ParseSemanticJudgeResponse(rawR2, true)
	if !ok1 || !ok2 || w1 != "new" || w2 != "new" {
		t.Fatalf("expected both swapped rounds to map to 'new', got w1=%q w2=%q", w1, w2)
	}
	consistent := CombineSemanticJudgeRounds(w1, w2, i1, i2, "gpt-5")
	if !consistent.Consistent || consistent.Winner != "new" {
		t.Fatalf("expected consistent new winner, got %+v", consistent)
	}

	// Position bias: model always picks A regardless of swap -> inconsistent.
	wb1, _, _ := ParseSemanticJudgeResponse(`{"winner":"A"}`, false)
	wb2, _, _ := ParseSemanticJudgeResponse(`{"winner":"A"}`, true)
	biased := CombineSemanticJudgeRounds(wb1, wb2, nil, nil, "gpt-5")
	if biased.Consistent || biased.Winner != "inconsistent" {
		t.Fatalf("expected position-biased judge to be marked inconsistent, got %+v", biased)
	}

	// Layer 2 assertion failure hard-blocks replay summary even when tokens dropped 30%.
	tokBefore := int64(200_000)
	tokAfter := int64(140_000)
	replays := []domain.EvoReplay{
		{
			Group:              domain.EvoGroupDiagnosis,
			SourceScore:        2,
			Score:              0,
			Outcome:            domain.EvoOutcomeRegressed,
			SourceMeasurements: &domain.EvoReplayMeasurements{Tokens: &tokBefore},
			ReplayMeasurements: &domain.EvoReplayMeasurements{Tokens: &tokAfter},
			AssertionReport:    &domain.EvoRunAssertionReport{Configured: true, Total: 2, Passed: 1, AllPassed: false},
		},
	}
	sum := SummarizeWithPreferences(replays, []string{domain.EvoPrefTokens})
	if sum.Passed || sum.AssertionsFailed != 1 || !strings.Contains(sum.Verdict, "L2 结果验收拦截") {
		t.Fatalf("expected L2 assertion failure to hard-block summary, got %+v", sum)
	}
}

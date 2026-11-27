package evolution

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestNormalizeSOPPreferencesAndBatchSize(t *testing.T) {
	got := NormalizeSOPPreferences([]string{"tools", "tokens", "tools", "invalid", "clean"})
	if len(got) != 3 || got[0] != "tools" || got[1] != "tokens" || got[2] != "clean" {
		t.Fatalf("unexpected prefs: %+v", got)
	}
	if def := NormalizeSOPPreferences(nil); len(def) != 1 || def[0] != domain.EvoPrefTokens {
		t.Fatalf("expected default tokens pref, got %+v", def)
	}
	if NormalizeSOPBatchSize(0) != 10 || NormalizeSOPBatchSize(-5) != 10 || NormalizeSOPBatchSize(200) != 100 || NormalizeSOPBatchSize(15) != 15 {
		t.Fatalf("batch size normalization mismatch")
	}
}

func TestSummarizeSOPRunsAndVersions(t *testing.T) {
	now := time.Now().UTC()
	wf := domain.WorkflowDefinition{ID: "wf-1", Name: "Bug 修复", Version: 2}
	runs := []domain.Run{
		{
			ID: "r1", WorkflowID: "wf-1", Status: "succeeded", CreatedAt: now.Add(-4 * time.Hour),
			WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-1", Version: 1},
			Usage:            domain.TokenUsage{TotalTokens: 100000},
			ToolCalls:        20,
			Timing:           &domain.RunTiming{ExecutionMs: 120000},
			Nodes:            map[string]*domain.NodeRun{"n1": {Status: "succeeded", ReworkCount: 1}},
		},
		{
			ID: "r2", WorkflowID: "wf-1", Status: "succeeded", CreatedAt: now.Add(-2 * time.Hour),
			WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-1", Version: 2},
			Usage:            domain.TokenUsage{TotalTokens: 42000},
			ToolCalls:        10,
			Timing:           &domain.RunTiming{ExecutionMs: 60000},
			Nodes:            map[string]*domain.NodeRun{"n1": {Status: "succeeded"}},
		},
		{
			ID: "r3", WorkflowID: "wf-1", Status: "failed", CreatedAt: now.Add(-1 * time.Hour),
			WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-1", Version: 2},
			Usage:            domain.TokenUsage{TotalTokens: 58000},
			ToolCalls:        14,
			Timing:           &domain.RunTiming{ExecutionMs: 90000},
			Nodes:            map[string]*domain.NodeRun{"n1": {Status: "failed"}},
		},
	}

	evals := map[string]domain.RunEvalRecord{
		"r2": {RunID: "r2", Status: domain.RunEvalSucceeded, Score: 92},
	}
	versions := BuildSOPVersions(wf, runs, evals, 10)
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
	if versions[0].Version != "v1" || versions[0].Current {
		t.Fatalf("unexpected v1 point: %+v", versions[0])
	}
	if versions[0].Metrics.ScoreSource != "heuristic" || versions[0].Metrics.Score == nil {
		t.Fatalf("expected heuristic score for v1, got %+v", versions[0].Metrics)
	}
	if versions[1].Version != "v2" || !versions[1].Current {
		t.Fatalf("unexpected v2 point: %+v", versions[1])
	}
	if versions[1].Metrics.Runs != 2 || versions[1].Metrics.AvgTokens != 50000 || versions[1].Metrics.AvgTools != 12 || versions[1].Metrics.CleanCount != 1 {
		t.Fatalf("unexpected v2 metrics: %+v", versions[1].Metrics)
	}
	if versions[1].Metrics.ScoreSource != "eval" || versions[1].Metrics.Score == nil || *versions[1].Metrics.Score != 92 {
		t.Fatalf("expected eval score 92 for v2, got %+v", versions[1].Metrics)
	}
}

func TestDetermineSOPStageAndChangeFiles(t *testing.T) {
	now := time.Now().UTC()
	pPending := domain.EvoProposal{ID: "p1", Status: domain.EvoProposalPending, UpdatedAt: now}
	pCanary := domain.EvoProposal{ID: "p2", Status: domain.EvoProposalCanary, UpdatedAt: now.Add(-time.Minute)}
	pReleased := domain.EvoProposal{ID: "p3", Status: domain.EvoProposalReleased, UpdatedAt: now.Add(-2 * time.Minute)}

	if st, active, _ := DetermineSOPStage([]domain.EvoProposal{pPending, pCanary}); st != domain.EvoSOPStageGray || active == nil || active.ID != "p2" {
		t.Fatalf("expected gray stage with p2, got %s %+v", st, active)
	}
	if st, active, _ := DetermineSOPStage([]domain.EvoProposal{pPending, pReleased}); st != domain.EvoSOPStageOptimize || active == nil || active.ID != "p1" {
		t.Fatalf("expected optimize stage with p1, got %s %+v", st, active)
	}
	if st, active, last := DetermineSOPStage([]domain.EvoProposal{pReleased}); st != domain.EvoSOPStageMonitor || active != nil || last == nil || last.ID != "p3" {
		t.Fatalf("expected monitor stage with last p3, got %s %+v %+v", st, active, last)
	}

	diffProp := &domain.EvoProposal{
		AssetKind:        domain.EvoAssetAgent,
		AssetID:          "coder",
		AssetName:        "Coder",
		BaseContent:      "line 1\nold rule\nline 3",
		CandidateContent: "line 1\nnew rule\nline 3",
	}
	files := BuildSOPChangeFiles(diffProp)
	if len(files) != 1 || files[0].Path != "roles/coder.md" {
		t.Fatalf("unexpected change files: %+v", files)
	}
	var hasDel, hasAdd bool
	for _, l := range files[0].Lines {
		if l[0] == "del" && l[1] == "old rule" {
			hasDel = true
		}
		if l[0] == "add" && l[1] == "new rule" {
			hasAdd = true
		}
	}
	if !hasDel || !hasAdd {
		t.Fatalf("expected del/add lines in diff, got %+v", files[0].Lines)
	}
}

func TestSOPPreferencePriorityGuidanceAndSampleRanking(t *testing.T) {
	now := time.Now().UTC()
	prefs := []string{domain.EvoPrefTools, domain.EvoPrefDuration, domain.EvoPrefTokens}
	if got := SOPPreferencePriorityText(prefs); got != "P1 减少工具调用次数 > P2 缩短执行耗时 > P3 减少 Token 消耗" {
		t.Fatalf("unexpected priority text: %q", got)
	}
	guidance := SOPPreferencePromptGuidance(prefs)
	if !strings.Contains(guidance, "P1 减少工具调用次数") || !strings.Contains(guidance, "冲突") {
		t.Fatalf("expected tiered conflict guidance, got: %q", guidance)
	}

	runs := []domain.Run{
		{ID: "r-low-tools-high-tok", Status: "succeeded", CreatedAt: now, ToolCalls: 8, Usage: domain.TokenUsage{TotalTokens: 900000}, Timing: &domain.RunTiming{ExecutionMs: 90000}},
		{ID: "r-tie-tools-fast", Status: "succeeded", CreatedAt: now, ToolCalls: 20, Usage: domain.TokenUsage{TotalTokens: 500000}, Timing: &domain.RunTiming{ExecutionMs: 30000}},
		{ID: "r-tie-tools-slow", Status: "succeeded", CreatedAt: now, ToolCalls: 20, Usage: domain.TokenUsage{TotalTokens: 100000}, Timing: &domain.RunTiming{ExecutionMs: 120000}},
	}
	finding := BuildSOPPreferenceFinding(
		"p1",
		domain.WorkflowDefinition{ID: "wf-1", Name: "测试 SOP"},
		domain.EvoAssetAgent, "a1", "角色 A", "n1",
		domain.EvoSOPPolicy{Preferences: prefs},
		runs,
		SummarizeSOPRuns(runs, nil),
	)
	if len(finding.SampleRunIDs) != 3 {
		t.Fatalf("expected 3 samples, got %+v", finding.SampleRunIDs)
	}
	// P1=tools puts both 20-tool runs ahead of 8-tool run; P2=duration puts slow ahead of fast.
	if finding.SampleRunIDs[0] != "r-tie-tools-slow" || finding.SampleRunIDs[1] != "r-tie-tools-fast" || finding.SampleRunIDs[2] != "r-low-tools-high-tok" {
		t.Fatalf("expected P1>P2 sample ordering, got %+v", finding.SampleRunIDs)
	}
	if !strings.Contains(finding.Title, "P1 减少工具调用次数 > P2 缩短执行耗时") {
		t.Fatalf("expected priority chain in finding title, got %q", finding.Title)
	}
}

func TestSplitSOPVersionsByAssetHistoryHandlesMultipleEditsRollbackAndReadOnly(t *testing.T) {
	now := time.Now().UTC()
	wf := domain.WorkflowDefinition{ID: "wf-1", Name: "Test SOP", Version: 2}
	asset := domain.EvoSOPAssetRef{Kind: domain.EvoAssetAgent, ID: "coder", Name: "开发工程师", CurrentVersion: 4}
	v1Hash := domain.ContentHash("prompt v1")
	v2Hash := domain.ContentHash("prompt v2")
	v3Hash := domain.ContentHash("prompt v3")
	versionsDesc := []domain.AssetVersion{
		{AssetKind: domain.EvoAssetAgent, AssetID: "coder", Version: 4, Source: domain.EvoVersionRollback, Hash: v1Hash, CreatedAt: now.Add(-1 * time.Hour)},
		{AssetKind: domain.EvoAssetAgent, AssetID: "coder", Version: 3, Source: domain.EvoVersionManual, Hash: v3Hash, CreatedAt: now.Add(-2 * time.Hour)},
		{AssetKind: domain.EvoAssetAgent, AssetID: "coder", Version: 2, Source: domain.EvoVersionChatbot, Hash: v2Hash, CreatedAt: now.Add(-3 * time.Hour)},
		{AssetKind: domain.EvoAssetAgent, AssetID: "coder", Version: 1, Source: domain.EvoVersionBaseline, Hash: v1Hash, CreatedAt: now.Add(-5 * time.Hour)},
	}
	runs := []domain.Run{
		{ID: "r-v4", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", CreatedAt: now.Add(-30 * time.Minute), Usage: domain.TokenUsage{TotalTokens: 30000}},
		{ID: "r-v3", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", CreatedAt: now.Add(-90 * time.Minute), Usage: domain.TokenUsage{TotalTokens: 50000}},
		// Note: no runs on v2 (double-edit before running).
		{ID: "r-v1", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", CreatedAt: now.Add(-4 * time.Hour), Usage: domain.TokenUsage{TotalTokens: 90000}},
	}
	pinsByRun := map[string]int{
		"r-v1": 1,
		"r-v3": 3,
		"r-v4": 4,
	}
	base := BuildSOPVersions(wf, runs, nil, 10)
	got := SplitSOPVersionsByAssetHistory(wf, base, runs, nil, asset, versionsDesc, pinsByRun, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 buckets (v1, v3, v4 — skipping unrun v2), got %d: %+v", len(got), got)
	}
	// v1 has same hash as live v4 (after rollback) -> must NOT offer redundant rollback.
	if got[0].RollbackVersion != 0 {
		t.Fatalf("expected v1 matching live hash to omit rollback, got %+v", got[0])
	}
	// v3 differs from live v4 -> must offer exact rollback target 3 (not v2).
	if got[1].RollbackVersion != 3 || got[1].Metrics.AvgTokens != 50000 {
		t.Fatalf("expected v3 row with rollbackVersion=3 and 50000 tokens, got %+v", got[1])
	}
	// v4 is current -> must be marked Current and omit rollback.
	if !got[2].Current || got[2].RollbackVersion != 0 || got[2].Source != domain.EvoVersionRollback {
		t.Fatalf("expected current v4 rollback row, got %+v", got[2])
	}

	// Read-only asset must never expose RollbackVersion on any row.
	roAsset := asset
	roAsset.ReadOnly = true
	gotRO := SplitSOPVersionsByAssetHistory(wf, base, runs, nil, roAsset, versionsDesc, pinsByRun, 10)
	for _, row := range gotRO {
		if row.RollbackVersion != 0 {
			t.Fatalf("readOnly asset must not set RollbackVersion, got %+v", row)
		}
	}
}

func TestComputeRunHeuristicScoreContinuousDistinction(t *testing.T) {
	mk := func(status string, tokens int64, tools int, durSec int64, rework int) domain.Run {
		return domain.Run{
			ID:        "r",
			Status:    status,
			Usage:     domain.TokenUsage{TotalTokens: tokens},
			ToolCalls: tools,
			Timing:    &domain.RunTiming{ExecutionMs: durSec * 1000},
			Nodes:     map[string]*domain.NodeRun{"n1": {Status: status, ReworkCount: rework, Attempt: 1 + rework}},
		}
	}

	heavy := ComputeRunHeuristicScore(mk("succeeded", 312_000, 9, 120, 0))
	midHigh := ComputeRunHeuristicScore(mk("succeeded", 160_000, 4, 70, 0))
	midLow := ComputeRunHeuristicScore(mk("succeeded", 140_000, 3, 45, 0))
	lean := ComputeRunHeuristicScore(mk("succeeded", 35_000, 2, 15, 0))
	reworkedHeavy := ComputeRunHeuristicScore(mk("succeeded", 400_000, 30, 300, 2))
	reworked := ComputeRunHeuristicScore(mk("succeeded", 80_000, 6, 40, 1))
	failedFast := ComputeRunHeuristicScore(mk("failed", 5_000, 1, 10, 0))
	failedZero := ComputeRunHeuristicScore(mk("failed", 0, 0, 0, 0))

	// P2-10: Early/zero-usage failures must NEVER outscore any succeeded (even heavy reworked) run.
	if failedFast > 20 || failedZero > 20 {
		t.Fatalf("expected failed runs to stay <=20, got failedFast=%d failedZero=%d", failedFast, failedZero)
	}
	if !(failedFast < reworkedHeavy && failedZero < reworkedHeavy && reworkedHeavy < reworked && reworked < heavy && heavy < midHigh && midHigh < midLow && midLow < lean && lean <= 100) {
		t.Fatalf("expected strictly ordered continuous scores, got failedZero=%d failedFast=%d reworkedHeavy=%d reworked=%d heavy=%d midHigh=%d midLow=%d lean=%d",
			failedZero, failedFast, reworkedHeavy, reworked, heavy, midHigh, midLow, lean)
	}
}

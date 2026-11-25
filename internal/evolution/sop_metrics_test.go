package evolution

import (
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

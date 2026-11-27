package httpapi

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
	"agentflow/internal/repo"
)

func TestEvolutionSOPDetectionPublishesAndDeduplicates(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "before-detection", "succeeded", "succeeded", "")
	endpoint := "/api/evolution/sop-detection?projectId=evo-p"
	enable := requestJSONAuthed[domain.EvoSOPDetectionSetting](t, env.router, env.user, http.MethodPut, endpoint,
		domain.EvoSOPDetectionSetting{Enabled: true, Model: "codebuddy:claude-sonnet-5"}, http.StatusOK)
	if !enable.Enabled || enable.Model != "codebuddy:claude-sonnet-5" {
		t.Fatalf("unexpected setting: %+v", enable)
	}
	project, _ := env.store.Project("evo-p")
	enabledAt := project.SOPDetection.EnabledAt
	project.Description = "an unrelated project update"
	project.SOPDetection = nil
	if err := env.store.PutProject(project); err != nil {
		t.Fatal(err)
	}
	project, _ = env.store.Project("evo-p")
	if project.SOPDetection == nil || !project.SOPDetection.EnabledAt.Equal(enabledAt) {
		t.Fatalf("project updates must preserve RSI detection: %+v", project.SOPDetection)
	}
	run := env.seedRun(t, "detect-this", "succeeded", "succeeded", "")
	replay := env.seedRun(t, "exclude-replay", "succeeded", "succeeded", "")
	replay.ExecutionContext.Purpose = domain.RunPurposeEvolutionReplay
	if err := env.store.PutRun(replay); err != nil {
		t.Fatal(err)
	}

	var analyzed []string
	evaluate := func(_ context.Context, projectID string, cfg domain.SOPDetectionConfig, target domain.Run) (domain.RunEvalResult, error) {
		analyzed = append(analyzed, target.ID)
		if projectID != "evo-p" || cfg.ConfiguredBy != env.user.ID || cfg.Model != enable.Model {
			t.Fatalf("unexpected ownership or model: %q %+v", projectID, cfg)
		}
		return domain.RunEvalResult{Findings: []domain.RunEvalFinding{{
			NodeID: "code", Dimension: "工具调用失败", Message: "工具调用失败 · read_file 失败 2 次",
		}}}, nil
	}
	env.h.sweepSOPDetections(context.Background(), evaluate)
	env.h.sweepSOPDetections(context.Background(), evaluate)
	if !slices.Equal(analyzed, []string{run.ID}) {
		t.Fatalf("processed runs = %v", analyzed)
	}
	record, ok := env.store.RunEval(repo.SOPDetectionEvalID(run.ID))
	if !ok || record.Status != domain.RunEvalSucceeded || !record.SOPDetection {
		t.Fatalf("detection was not persisted: %+v", record)
	}
	view := requestJSON[domain.EvoOverview](t, env.router, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if !view.SOPDetection.Enabled || len(view.Findings) != 1 || len(view.Findings[0].Signals) != 1 {
		t.Fatalf("detection did not reach RSI workbench: %+v", view)
	}
	finding := view.Findings[0]
	if finding.Signals[0].Kind != evolution.SignalSOPDetection || !evolution.FeedbackSample(run, finding) {
		t.Fatalf("successful runs need to remain eligible for replay: %+v", finding)
	}
	requestJSONAuthed[domain.EvoSOPDetectionSetting](t, env.router, env.user, http.MethodPut, endpoint,
		domain.EvoSOPDetectionSetting{Enabled: false}, http.StatusOK)
	env.seedRun(t, "while-disabled", "succeeded", "succeeded", "")
	env.h.sweepSOPDetections(context.Background(), evaluate)
	if len(analyzed) != 1 {
		t.Fatalf("disabled detection still started work: %v", analyzed)
	}
}

func TestEvolutionReplayMeasurementsExcludeReusedNodes(t *testing.T) {
	now := time.Now().UTC()
	run := domain.Run{
		Status: "succeeded", CreatedAt: now.Add(-10 * time.Second), UpdatedAt: now,
		Usage: domain.TokenUsage{TotalTokens: 600}, ToolCalls: 4,
		Nodes: map[string]*domain.NodeRun{
			"reused":  {Status: "succeeded", ToolCalls: 3, Usage: domain.TokenUsage{TotalTokens: 500}, StartedAt: now.Add(-10 * time.Second), FinishedAt: now.Add(-2 * time.Second)},
			"changed": {Status: "succeeded", ToolCalls: 1, Usage: domain.TokenUsage{TotalTokens: 100}, StartedAt: now.Add(-2 * time.Second), FinishedAt: now},
		},
	}
	got := evoReplayMeasurements(run, []string{"reused"})
	if got.Tokens == nil || *got.Tokens != 100 || got.ToolCalls == nil || *got.ToolCalls != 1 || got.ExecutionMs == nil || *got.ExecutionMs != 2000 {
		t.Fatalf("reused scope was included: %+v", got)
	}
	missing := evoReplayMeasurements(domain.Run{}, nil)
	if missing.Tokens != nil || missing.ToolCalls != nil || missing.ExecutionMs != nil {
		t.Fatalf("missing resource readings must be absent: %+v", missing)
	}
}

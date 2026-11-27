package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
)

func TestEvolutionP0ToolTraceAndReflectRefineLoop(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	// Seed 2 clean-succeeded high-token runs so the SOP optimization starts
	// with fixable==0 and uses run-diag (diagnosis) + run-hold (holdout/regression).
	okTrue := true
	okFalse := false
	diagTelemetry := []domain.TelemetryEvent{
		{ID: "t1", RunID: "p0-src-diag", NodeID: "code", Sequence: 1, Timestamp: now.Add(-9 * time.Minute), Category: "tool", Name: "read_file", Phase: "finish",
			Tool: &domain.ToolObservation{Name: "read_file", CallID: "c1", Success: &okTrue, DurationMs: 120}, Attributes: map[string]any{"path": "order.json"}},
		{ID: "t2", RunID: "p0-src-diag", NodeID: "code", Sequence: 2, Timestamp: now.Add(-8 * time.Minute), Category: "tool", Name: "read_file", Phase: "finish",
			Tool: &domain.ToolObservation{Name: "read_file", CallID: "c2", Success: &okTrue, DurationMs: 110}, Attributes: map[string]any{"path": "order.json"}},
		{ID: "t3", RunID: "p0-src-diag", NodeID: "code", Sequence: 3, Timestamp: now.Add(-7 * time.Minute), Category: "tool", Name: "bash", Phase: "finish",
			Tool:  &domain.ToolObservation{Name: "bash", CallID: "c3", Success: &okFalse, DurationMs: 4500},
			Error: &domain.ErrorObservation{Message: "command not found"}, Attributes: map[string]any{"command": "jq . order.json"}},
	}
	seedHighTokenRun := func(id string, at time.Time, tokens int64, tools int, telemetry []domain.TelemetryEvent) {
		run := domain.Run{
			ID: id, WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
			Input:            "分析订单日志并生成结论",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			Usage:            domain.TokenUsage{TotalTokens: tokens},
			ToolCalls:        tools,
			Timing:           &domain.RunTiming{ExecutionMs: 60000},
			Telemetry:        telemetry,
			Nodes: map[string]*domain.NodeRun{
				"code": {
					NodeID: "code", Status: "succeeded", Attempt: 1,
					Usage: domain.TokenUsage{TotalTokens: tokens}, ToolCalls: tools,
					StartedAt: at.Add(-60 * time.Second), FinishedAt: at,
				},
			},
			CreatedAt: at, UpdatedAt: at,
		}
		if err := env.store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	seedHighTokenRun("p0-src-diag", now.Add(-10*time.Minute), 120000, 12, diagTelemetry)
	seedHighTokenRun("p0-src-hold", now.Add(-20*time.Minute), 110000, 10, nil)

	// Seed a prior failed proposal so negative experience is fed into prompts.
	if err := env.store.PutEvoProposal(domain.EvoProposal{
		ID: "evp-prior-fail", ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID,
		AssetName: env.agent.Name, Title: "盲目删减上下文", Rationale: "去掉所有摘要说明",
		BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt),
		Status: domain.EvoProposalFailed, Error: "未通过：P1 省 Token 未达标",
	}); err != nil {
		t.Fatal(err)
	}

	// Configure SOP policy with P1 = tokens.
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		ProjectID: "evo-p", WorkflowID: env.wf.ID, Enabled: true,
		Preferences: []string{domain.EvoPrefTokens}, BatchSize: 5, UpdatedBy: env.user.ID,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	calls := 0
	var firstPrompt, secondSystem, secondPrompt string
	draft1 := env.agent.Prompt + "\n第一轮尝试：尽量精简回复。"
	draft2 := env.agent.Prompt + "\n第二轮修正：单次读取 order.json 后缓存关键字段，禁止重复 read_file 与无效 jq 命令。"

	setEvoImproveForTest(func(_ context.Context, _ domain.EvoProposal, system, prompt string, _ domain.User) (string, error) {
		mu.Lock()
		calls++
		n := calls
		if n == 1 {
			firstPrompt = prompt
		} else {
			secondSystem = system
			secondPrompt = prompt
		}
		mu.Unlock()
		if n == 1 {
			raw, _ := json.Marshal(map[string]any{"title": "第一轮尝试省 Token", "rationale": "精简输出", "content": draft1})
			return string(raw), nil
		}
		raw, _ := json.Marshal(map[string]any{"title": "反思修正重复读与无效命令", "rationale": "针对工具轨迹消除重复读取", "content": draft2})
		return string(raw), nil
	})
	t.Cleanup(func() { setEvoImproveForTest(nil) })

	// Trigger SOP optimization.
	started := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/sops/"+env.wf.ID+"/optimize", map[string]any{"projectId": "evo-p"}, http.StatusAccepted)
	pid := started.ID
	if pid == "" {
		t.Fatalf("expected non-empty proposal ID, got %+v", started)
	}

	// Wait for round 1 to reach validating or pending (mock executor may complete fast with 0 tokens).
	p1 := env.waitProposal(t, pid, domain.EvoProposalValidating, domain.EvoProposalPending)
	if p1.WorkflowID != env.wf.ID || len(p1.Preferences) != 1 || p1.Preferences[0] != domain.EvoPrefTokens {
		t.Fatalf("expected proposal to carry workflowId & P1 tokens preference, got %+v", p1)
	}

	mu.Lock()
	gotFirstPrompt := firstPrompt
	mu.Unlock()
	if !strings.Contains(gotFirstPrompt, "read_file(order.json) ×2") || !strings.Contains(gotFirstPrompt, "command not found") {
		t.Fatalf("round 1 prompt missing tool-trace hotspots: %s", gotFirstPrompt)
	}
	if !strings.Contains(gotFirstPrompt, "避免重复失败") || !strings.Contains(gotFirstPrompt, "盲目删减上下文") {
		t.Fatalf("round 1 prompt missing negative experience: %s", gotFirstPrompt)
	}

	// Now deterministically test the P1 goal failure -> automatic reflection round 2
	// -> P1 goal pass transition on a clean validating proposal with measured replays.
	tok120k := int64(120000)
	tok110k := int64(110000)
	tools12 := 12
	tools10 := 10
	ms60k := int64(60000)

	// Reject p1 so asset is free for deterministic reflect-refine proposal.
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+pid+"/reject", nil, http.StatusOK)

	refID := "evp-p0-reflect"
	refProp := domain.EvoProposal{
		ID: refID, ProjectID: "evo-p", FindingID: p1.FindingID,
		AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, AssetName: env.agent.Name,
		WorkflowID: env.wf.ID, Preferences: []string{domain.EvoPrefTokens},
		Title: "第一轮尝试省 Token", Rationale: "精简输出",
		BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt),
		CandidateContent: draft1, Status: domain.EvoProposalValidating,
		ExpectedReplays: 2, CreatedBy: env.user.ID,
	}
	if err := env.store.PutEvoProposal(refProp); err != nil {
		t.Fatal(err)
	}

	seedFinishedReplay := func(replayID, runID, group, srcRunID string, srcTok, repTok int64, srcTools, repTools int) {
		rRun := domain.Run{
			ID: runID, WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p", Purpose: domain.RunPurposeEvolutionReplay, EvolutionReplayID: replayID},
			Usage:            domain.TokenUsage{TotalTokens: repTok}, ToolCalls: repTools,
			Timing:           &domain.RunTiming{ExecutionMs: 55000},
			Telemetry: []domain.TelemetryEvent{
				{ID: "rt-" + runID + "-1", RunID: runID, NodeID: "code", Sequence: 1, Timestamp: now, Category: "tool", Name: "read_file", Phase: "finish",
					Tool: &domain.ToolObservation{Name: "read_file", CallID: "rc1", Success: &okTrue}, Attributes: map[string]any{"path": "order.json"}},
				{ID: "rt-" + runID + "-2", RunID: runID, NodeID: "code", Sequence: 2, Timestamp: now.Add(time.Second), Category: "tool", Name: "read_file", Phase: "finish",
					Tool: &domain.ToolObservation{Name: "read_file", CallID: "rc2", Success: &okTrue}, Attributes: map[string]any{"path": "order.json"}},
			},
			Nodes: map[string]*domain.NodeRun{
				"code": {NodeID: "code", Status: "succeeded", Attempt: 1, Usage: domain.TokenUsage{TotalTokens: repTok}, ToolCalls: repTools},
			},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := env.store.PutRun(rRun); err != nil {
			t.Fatal(err)
		}
		if err := env.store.PutEvoReplay(domain.EvoReplay{
			ID: replayID, ProposalID: refID, ProjectID: "evo-p", Group: group,
			SourceRunID: srcRunID, SourceStatus: "succeeded", SourceScore: 2,
			RunID: runID, Status: "running", StartedAt: now.Add(-time.Minute),
			SourceMeasurements: &domain.EvoReplayMeasurements{Tokens: &srcTok, ToolCalls: &srcTools, ExecutionMs: &ms60k},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Round 1 replays only reduce tokens by ~1.7% (120k -> 118k, 110k -> 108k) -> MUST FAIL P1 goal gate
	// and automatically launch reflection round 2!
	seedFinishedReplay("evr-r1-diag", "run-r1-diag", domain.EvoGroupDiagnosis, "p0-src-diag", tok120k, 118000, tools12, 11)
	seedFinishedReplay("evr-r1-hold", "run-r1-hold", domain.EvoGroupHoldout, "p0-src-hold", tok110k, 108000, tools10, 10)

	env.h.sweepProposal(domain.EvoProposal{ID: refID}, time.Now().UTC())

	// Wait until round 2 has completed drafting and entered validating (or settled).
	deadline := time.Now().Add(5 * time.Second)
	var p2 domain.EvoProposal
	for time.Now().Before(deadline) {
		if cur, ok := env.store.EvoProposal(refID); ok && cur.RefineRound == 1 && cur.CandidateContent == draft2 && cur.Status != domain.EvoProposalDrafting {
			p2 = cur
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if p2.ID == "" {
		cur, _ := env.store.EvoProposal(refID)
		t.Fatalf("expected automatic reflect-and-refine round 2, got status=%s round=%d err=%q", cur.Status, cur.RefineRound, cur.Error)
	}
	if len(p2.RefineHistory) != 1 || !strings.Contains(p2.RefineHistory[0].Verdict, "未达标") {
		t.Fatalf("expected round 1 archived in RefineHistory, got %+v", p2.RefineHistory)
	}

	mu.Lock()
	gotSecondSys, gotSecondPrompt, totalCalls := secondSystem, secondPrompt, calls
	mu.Unlock()
	if totalCalls != 2 || !strings.Contains(gotSecondSys, "失败回放反思修正") || !strings.Contains(gotSecondPrompt, draft1) || !strings.Contains(gotSecondPrompt, "未达标") || !strings.Contains(gotSecondPrompt, "read_file(order.json) ×2") {
		t.Fatalf("unexpected reflection prompt: calls=%d sys=%q prompt=%q", totalCalls, gotSecondSys, gotSecondPrompt)
	}
	// Holdout run ID must NEVER be leaked into the reflection prompt.
	if strings.Contains(gotSecondPrompt, "p0-src-hold") {
		t.Fatalf("reflection prompt leaked holdout run ID: %s", gotSecondPrompt)
	}

	// Verify round 2 replays were freshly replaced (no stale r1 replay IDs).
	replays2 := env.store.EvoReplays(refID)
	if len(replays2) != 2 {
		t.Fatalf("expected 2 fresh round 2 replays, got %d", len(replays2))
	}
	for _, r := range replays2 {
		if r.ID == "evr-r1-diag" || r.ID == "evr-r1-hold" {
			t.Fatalf("stale round 1 replay %s was not cleared before round 2", r.ID)
		}
	}

	// Verify SummarizeWithPreferences settles round 2 replays with 82k tokens (-29%) as passed.
	tok82k := int64(82000)
	tools5 := 5
	for i := range replays2 {
		replays2[i].Outcome = domain.EvoOutcomeSame
		replays2[i].ReplayMeasurements = &domain.EvoReplayMeasurements{Tokens: &tok82k, ToolCalls: &tools5, ExecutionMs: &ms60k}
	}
	sum2 := evolution.SummarizeWithPreferences(replays2, p2.Preferences)
	if !sum2.Passed || !sum2.GoalMet || sum2.GoalMetric != domain.EvoPrefTokens || sum2.GoalDeltaPct >= -20 {
		t.Fatalf("expected round 2 P1 token goal to pass strongly, got %+v", sum2)
	}

	// Wait for background mock replay goroutines to finish before TempDir cleanup.
	waitDone := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitDone) {
		allDone := true
		for _, r := range replays2 {
			if r.RunID == "" {
				continue
			}
			if run, ok := env.store.RunState(r.RunID); !ok || !domain.TerminalRunStatus(run.Status) {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEvolutionP0CanaryGoalGate(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	candBody := env.agent.Prompt + "\n合并工具查询减少 Token。"
	p := domain.EvoProposal{
		ID: "evp-p0-canary", ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID,
		AssetName: env.agent.Name, WorkflowID: env.wf.ID, Preferences: []string{domain.EvoPrefTokens},
		Title: "省 Token 灰度", BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt),
		CandidateContent: candBody, Status: domain.EvoProposalCanary,
		Canary: &domain.EvoCanary{Percent: 50, MinRuns: 3, Status: domain.EvoCanaryRunning, StartedAt: now.Add(-1 * time.Hour)},
	}
	if err := env.store.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}

	seedArm := func(prefix, hash string, count int, tokens int64) {
		for i := 0; i < count; i++ {
			id := prefix + "-" + string(rune('a'+i))
			run := domain.Run{
				ID: id, WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
				ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
				Usage:            domain.TokenUsage{TotalTokens: tokens}, ToolCalls: 8,
				Timing:           &domain.RunTiming{ExecutionMs: 40000},
				Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1}},
				CreatedAt:        now.Add(-10 * time.Minute), UpdatedAt: now.Add(-9 * time.Minute),
			}
			if err := env.store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if err := env.store.PutRunAssetPins([]domain.RunAssetPin{{
				RunID: id, AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID,
				Hash: hash, CreatedAt: now.Add(-10 * time.Minute),
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Both arms 100% clean, but candidate uses MORE tokens (110k vs 100k) -> fails P1 tokens gate!
	seedArm("ctrl", p.BaseHash, 3, 100000)
	seedArm("cand", domain.ContentHash(candBody), 3, 110000)

	stored, ok := env.store.EvoProposal(p.ID)
	if !ok {
		t.Fatal("missing stored canary proposal")
	}
	env.h.sweepCanary(stored, now)
	got, ok := env.store.EvoProposal(p.ID)
	if !ok {
		t.Fatal("missing updated canary proposal")
	}
	if got.Status != domain.EvoProposalFailed || got.Canary.Status != domain.EvoCanaryFailed || !strings.Contains(got.Canary.Verdict, "未达成 P1 目标") {
		t.Fatalf("expected canary to fail when tokens increased despite 100%% clean rate: status=%s canary=%+v", got.Status, got.Canary)
	}
	if got.Canary.Control.AvgTokens != 100000 || got.Canary.Canary.AvgTokens != 110000 {
		t.Fatalf("expected arm AvgTokens populated, got ctrl=%+v cand=%+v", got.Canary.Control, got.Canary.Canary)
	}

	// Now verify passing case with 20% lower tokens (80k vs 100k).
	cPass := domain.EvoCanary{
		MinRuns: 3,
		Control: domain.EvoArmStats{Runs: 3, Clean: 3, Rate: 1, AvgTokens: 100000},
		Canary:  domain.EvoArmStats{Runs: 3, Clean: 3, Rate: 1, AvgTokens: 80000},
	}
	if st, _ := evolution.JudgeCanaryWithPreferences(cPass, []string{domain.EvoPrefTokens}); st != domain.EvoCanaryPassed {
		t.Fatalf("expected lower-token canary to pass, got %s", st)
	}
}

func TestEvolutionP0BudgetPrecheckSleepAndWake(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	// 1. Validate budget API rejects monthly < daily and persists valid limits.
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPut, "/api/evolution/budget?projectId=evo-p",
		map[string]any{"dailyTokenLimit": 200000, "monthlyTokenLimit": 100000}, http.StatusBadRequest)

	savedBudget := requestJSONAuthed[domain.EvoBudgetStatus](t, env.router, env.user, http.MethodPut, "/api/evolution/budget?projectId=evo-p",
		map[string]any{"dailyTokenLimit": 100000, "monthlyTokenLimit": 500000}, http.StatusOK)
	if savedBudget.DailyTokenLimit != 100000 || savedBudget.MonthlyTokenLimit != 500000 || savedBudget.Exceeded {
		t.Fatalf("unexpected saved budget: %+v", savedBudget)
	}

	// Seed a replay run today consuming 120k tokens -> exceeds 100k daily limit!
	replayRun := domain.Run{
		ID: "ev-replay-spend", WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p", Purpose: domain.RunPurposeEvolutionReplay},
		Usage:            domain.TokenUsage{TotalTokens: 120000},
		Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded"}},
		CreatedAt:        now.Add(-5 * time.Minute), UpdatedAt: now.Add(-4 * time.Minute),
	}
	if err := env.store.PutRun(replayRun); err != nil {
		t.Fatal(err)
	}

	// Overview reports budget exceeded.
	ov := requestJSONAuthed[domain.EvoOverview](t, env.router, env.user, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if !ov.Budget.Exceeded || !ov.Budget.DailyExceeded || ov.Budget.DailyUsedTokens < 120000 {
		t.Fatalf("expected overview to report daily budget exceeded: %+v", ov.Budget)
	}

	// Configure enabled SOP policy with 3 completed runs (batchSize=3).
	pol := domain.EvoSOPPolicy{
		ProjectID: "evo-p", WorkflowID: env.wf.ID, Enabled: true,
		Preferences: []string{domain.EvoPrefTokens}, BatchSize: 3, UpdatedBy: env.user.ID,
	}
	if err := env.store.PutEvoSOPPolicy(pol); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := env.store.PutRun(domain.Run{
			ID: "budget-biz-" + string(rune('a'+i)), WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			Usage:            domain.TokenUsage{TotalTokens: 50000},
			Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded"}},
			CreatedAt:        now.Add(-10 * time.Minute), UpdatedAt: now.Add(-9 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Auto-batch pre-check must skip triggering while daily budget is exceeded,
	// and SOP detail must expose autoBlockReason explaining why!
	env.h.sweepSOPBatches(now)
	if props := env.store.EvoProposals("evo-p", 20); len(props) != 0 {
		t.Fatalf("budget-exceeded auto-batch must not create proposals, got %d", len(props))
	}
	detailBlocked := requestJSONAuthed[domain.EvoSOPDetail](t, env.router, env.user, http.MethodGet, "/api/evolution/sops/"+env.wf.ID+"?projectId=evo-p", nil, http.StatusOK)
	if !strings.Contains(detailBlocked.AutoBlockReason, "预算已用尽") {
		t.Fatalf("expected autoBlockReason to explain budget block, got %q", detailBlocked.AutoBlockReason)
	}

	// 2. Test 3 consecutive no-gain outcomes put SOP into sleeping state, then wake endpoint reopens it.
	for i := 0; i < 3; i++ {
		env.h.recordSOPProposalOutcome(domain.EvoProposal{
			ID: "p-nogain-" + string(rune('a'+i)), ProjectID: "evo-p", WorkflowID: env.wf.ID,
		}, false, "回放未达标", now)
	}
	slept := env.store.EvoSOPPolicy("evo-p", env.wf.ID)
	if slept.ConsecutiveNoGain != 3 || slept.SleepStage != 1 || !evolution.IsSOPPolicySleeping(slept, now) {
		t.Fatalf("expected SOP sleeping after 3 no-gains, got %+v", slept)
	}

	// SOP summary must report stage == "sleeping".
	sops := requestJSONAuthed[[]domain.EvoSOPSummary](t, env.router, env.user, http.MethodGet, "/api/evolution/sops?projectId=evo-p", nil, http.StatusOK)
	if len(sops) != 1 || sops[0].Stage != domain.EvoSOPStageSleeping || sops[0].ConsecutiveNoGain != 3 {
		t.Fatalf("expected sleeping stage in SOP list, got %+v", sops)
	}

	// Increase budget so only sleep is tested; auto-batch must still skip while sleeping.
	requestJSONAuthed[domain.EvoBudgetStatus](t, env.router, env.user, http.MethodPut, "/api/evolution/budget?projectId=evo-p",
		map[string]any{"dailyTokenLimit": 1000000, "monthlyTokenLimit": 5000000}, http.StatusOK)
	env.h.sweepSOPBatches(now)
	if props := env.store.EvoProposals("evo-p", 20); len(props) != 0 {
		t.Fatalf("sleeping SOP must not auto-trigger, got %d proposals", len(props))
	}

	// User wakes the sleeping SOP via POST /wake.
	woken := requestJSONAuthed[domain.EvoSOPPolicy](t, env.router, env.user, http.MethodPost, "/api/evolution/sops/"+env.wf.ID+"/wake",
		map[string]any{"projectId": "evo-p"}, http.StatusOK)
	if woken.ConsecutiveNoGain != 0 || woken.SleepStage != 0 || !woken.SleepingUntil.IsZero() || !woken.Enabled {
		t.Fatalf("expected wake endpoint to reset sleep state, got %+v", woken)
	}
}

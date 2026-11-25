package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestEvolutionSOPWorkbenchEndpoints(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	wfV1 := env.wf
	wfV1.Version = 1
	wfV2 := env.wf
	wfV2.Version = 2
	if err := env.store.PutWorkflow(wfV2); err != nil {
		t.Fatal(err)
	}

	runs := []domain.Run{
		{
			ID: "sop-r1", WorkflowID: wfV2.ID, WorkflowSnapshot: wfV1, Status: "failed",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			CreatedAt:        now.Add(-4 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour),
			Usage: domain.TokenUsage{TotalTokens: 96000}, ToolCalls: 24,
			Timing: &domain.RunTiming{ExecutionMs: 140000},
			Nodes:  map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "failed", Attempt: 2, ReworkCount: 1}},
		},
		{
			ID: "sop-r2", WorkflowID: wfV2.ID, WorkflowSnapshot: wfV2, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			CreatedAt:        now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
			Usage: domain.TokenUsage{TotalTokens: 48000}, ToolCalls: 10,
			Timing: &domain.RunTiming{ExecutionMs: 70000},
			Nodes:  map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1}},
		},
		{
			ID: "sop-r3", WorkflowID: wfV2.ID, WorkflowSnapshot: wfV2, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			CreatedAt:        now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
			Usage: domain.TokenUsage{TotalTokens: 42000}, ToolCalls: 8,
			Timing: &domain.RunTiming{ExecutionMs: 60000},
			Nodes:  map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1}},
		},
	}
	for _, r := range runs {
		if err := env.store.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}

	// Update per-SOP policy (multi-select preferences + adjustable batch size + toggle).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/evolution/sops/"+wfV2.ID+"/policy",
		strings.NewReader(`{"projectId":"evo-p","enabled":true,"preferences":["tokens","tools","clean"],"batchSize":15}`))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT policy status=%d body=%s", rec.Code, rec.Body.String())
	}
	var savedPolicy domain.EvoSOPPolicy
	if err := json.Unmarshal(rec.Body.Bytes(), &savedPolicy); err != nil {
		t.Fatal(err)
	}
	if !savedPolicy.Enabled || savedPolicy.BatchSize != 15 || len(savedPolicy.Preferences) != 3 {
		t.Fatalf("unexpected policy: %+v", savedPolicy)
	}

	candBody := env.agent.Prompt + "\n先查看目录摘要再定位文件，减少重复 grep。"
	p := domain.EvoProposal{
		ID: "evp-sop-canary", ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID,
		AssetName: env.agent.Name, Title: "精简检索路径减少 Token 与工具调用",
		BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt), BaseVersion: 2,
		CandidateContent: candBody, Status: domain.EvoProposalCanary,
		Canary: &domain.EvoCanary{
			Percent: 50, MinRuns: 5, Status: domain.EvoCanaryRunning,
			StartedAt: now.Add(-3 * time.Hour),
		},
	}
	if err := env.store.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutRunAssetPins([]domain.RunAssetPin{
		{RunID: "sop-r2", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 2, Hash: p.BaseHash, CreatedAt: now.Add(-2 * time.Hour)},
		{RunID: "sop-r3", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 3, Hash: domain.ContentHash(candBody), CreatedAt: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify overview includes SOPs.
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("overview status=%d", rec.Code)
	}
	var ov domain.EvoOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.SOPs) != 1 {
		t.Fatalf("expected 1 SOP in overview, got %d", len(ov.SOPs))
	}
	if ov.SOPs[0].Stage != domain.EvoSOPStageGray || ov.SOPs[0].BatchSize != 15 {
		t.Fatalf("unexpected overview SOP summary: %+v", ov.SOPs[0])
	}

	// Verify SOP detail endpoint returns versions, grayRounds, and changeFiles.
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evolution/sops/"+wfV2.ID+"?projectId=evo-p", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sop detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail domain.EvoSOPDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Stage != domain.EvoSOPStageGray || detail.CurrentVersion != "v2" {
		t.Fatalf("unexpected detail stage/version: %+v", detail)
	}
	if len(detail.Versions) != 2 {
		t.Fatalf("expected 2 version points, got %d", len(detail.Versions))
	}
	if len(detail.GrayRounds) != 1 || detail.GrayRounds[0].Control.Runs != 1 || detail.GrayRounds[0].Candidate.Runs != 1 {
		t.Fatalf("unexpected gray rounds: %+v", detail.GrayRounds)
	}
	if len(detail.ChangeFiles) != 1 || len(detail.ChangeFiles[0].Lines) == 0 {
		t.Fatalf("expected changeFiles diff, got %+v", detail.ChangeFiles)
	}
}

func TestEvolutionSOPTracksChatbotAndManualAssetEdits(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	wf := env.wf
	wf.Version = 2
	if err := env.store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}

	// Run before Chatbot edit.
	beforeRun := domain.Run{
		ID: "chat-before", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed",
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
		CreatedAt:        now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
		Usage: domain.TokenUsage{TotalTokens: 90000}, ToolCalls: 22,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "failed", Attempt: 2, ReworkCount: 1}},
	}
	if err := env.store.PutRun(beforeRun); err != nil {
		t.Fatal(err)
	}

	// Simulate Chatbot editing the agent prompt via catalog HTTP endpoint with assistantEditSourceContextKey.
	cat := &catalogHandlers{deps: env.h.deps}
	env.router.PUT("/api/agents", cat.agents)
	updatedAgent := env.agent
	updatedAgent.ExecutorID = "codebuddy"
	updatedAgent.Prompt = env.agent.Prompt + "\n先检查测试失败栈，避免重复全局搜索。"
	raw, _ := json.Marshal(updatedAgent)
	req := httptest.NewRequest(http.MethodPut, "/api/agents", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), assistantEditSourceContextKey{}, domain.EvoVersionChatbot))
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/agents status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify asset_versions immediately recorded baseline v1 + chatbot v2.
	vers := env.store.AssetVersions(domain.EvoAssetAgent, env.agent.ID)
	if len(vers) != 2 || vers[0].Source != domain.EvoVersionChatbot || vers[1].Source != domain.EvoVersionBaseline {
		t.Fatalf("expected baseline+chatbot versions, got %+v", vers)
	}

	// Run after Chatbot edit.
	afterTime := vers[0].CreatedAt.Add(time.Second)
	afterRun := domain.Run{
		ID: "chat-after", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
		CreatedAt:        afterTime, UpdatedAt: afterTime,
		Usage: domain.TokenUsage{TotalTokens: 35000}, ToolCalls: 7,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1}},
	}
	if err := env.store.PutRun(afterRun); err != nil {
		t.Fatal(err)
	}

	// SOP detail exposes RecentEdits diff and splits version trend into v2 (pre-edit) vs v2 · Chatbot (post-edit).
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evolution/sops/"+wf.ID+"?projectId=evo-p", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sop detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail domain.EvoSOPDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.RecentEdits) != 1 || detail.RecentEdits[0].Source != domain.EvoVersionChatbot || len(detail.RecentEdits[0].Diff.Lines) == 0 {
		t.Fatalf("expected chatbot recent edit with diff, got %+v", detail.RecentEdits)
	}
	if len(detail.Versions) != 2 || detail.Versions[1].Source != domain.EvoVersionChatbot || !detail.Versions[1].Current {
		t.Fatalf("expected split version trend with chatbot revision point, got %+v", detail.Versions)
	}
	if detail.Versions[0].Metrics.AvgTokens != 90000 || detail.Versions[1].Metrics.AvgTokens != 35000 {
		t.Fatalf("expected pre/post chatbot metrics split, got v0=%+v v1=%+v", detail.Versions[0].Metrics, detail.Versions[1].Metrics)
	}
}

func TestEvolutionSOPAutoBatchOptimizationTriggersAtBatchSize(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	promptSeen := make(chan string, 1)
	setEvoImproveForTest(func(_ context.Context, _ domain.EvoProposal, _, prompt string, _ domain.User) (string, error) {
		select {
		case promptSeen <- prompt:
		default:
		}
		return `{"title":"精简重复核对降低 Token","rationale":"去除基线中的两次重复核对，直接输出三字段 JSON","content":"原始正文：请完成开发任务并自测。\n直接输出三字段 JSON，不做重复核对。"}`, nil
	})
	defer setEvoImproveForTest(nil)

	// Configure SOP with batchSize=5 and tokens preference (matching acceptance case).
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		WorkflowID:  env.wf.ID,
		ProjectID:   "evo-p",
		Enabled:     true,
		Preferences: []string{domain.EvoPrefTokens},
		BatchSize:   5,
		UpdatedBy:   env.user.ID,
		UpdatedAt:   now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// 5 high-token runs (4 clean, 1 reworked).
	for i := 0; i < 5; i++ {
		rework := 0
		if i == 0 {
			rework = 1
		}
		r := domain.Run{
			ID:               "batch-r-" + string(rune('1'+i)),
			WorkflowID:       env.wf.ID,
			WorkflowSnapshot: env.wf,
			Status:           "succeeded",
			Input:            `{"ticketId":"TOKEN-01","message":"我想看看包裹送到哪了"}`,
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			CreatedAt:        now.Add(time.Duration(i-10) * time.Minute),
			UpdatedAt:        now.Add(time.Duration(i-10) * time.Minute),
			Usage:            domain.TokenUsage{TotalTokens: 438500},
			ToolCalls:        12,
			Timing:           &domain.RunTiming{ExecutionMs: 191000},
			Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1 + rework, ReworkCount: rework, Usage: domain.TokenUsage{TotalTokens: 438500}, ToolCalls: 12}},
		}
		if err := env.store.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}

	// Sweep should automatically trigger optimization once 5/5 runs are collected.
	env.h.sweepSOPBatches(now)

	props := env.store.EvoProposals("evo-p", 20)
	if len(props) != 1 {
		t.Fatalf("expected 1 auto-triggered proposal after 5 runs, got %d", len(props))
	}

	select {
	case prompt := <-promptSeen:
		if !strings.Contains(prompt, "减少 Token 消耗") || !strings.Contains(prompt, "438500") {
			t.Fatalf("expected improver prompt to include token preference and batch token stats, got: %s", prompt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for auto-triggered improver invocation")
	}

	// Second sweep must NOT create a duplicate proposal.
	env.h.sweepSOPBatches(now.Add(time.Minute))
	if again := env.store.EvoProposals("evo-p", 20); len(again) != 1 {
		t.Fatalf("expected no duplicate auto-trigger, got %d proposals", len(again))
	}
}

func TestEvolutionSOPDetectionRunsForEnabledPolicyWithoutGlobalSwitch(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	// Do NOT enable legacy project-level SOPDetection; only enable per-SOP policy with standalone model.
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		WorkflowID:  env.wf.ID,
		ProjectID:   "evo-p",
		Enabled:     true,
		Preferences: []string{domain.EvoPrefTokens},
		BatchSize:   5,
		Model:       "codex:gpt-5",
		UpdatedBy:   env.user.ID,
		UpdatedAt:   now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	run := env.seedRun(t, "sop-policy-detect", "succeeded", "succeeded", "")

	var seenModel string
	var seenRuns []string
	evaluate := func(_ context.Context, projectID string, cfg domain.SOPDetectionConfig, target domain.Run) (domain.RunEvalResult, error) {
		if projectID != "evo-p" {
			t.Fatalf("unexpected projectID %q", projectID)
		}
		seenModel = cfg.Model
		seenRuns = append(seenRuns, target.ID)
		return domain.RunEvalResult{Passed: true, Score: 92}, nil
	}
	env.h.sweepSOPDetections(context.Background(), evaluate)
	env.h.sweepSOPDetections(context.Background(), evaluate)

	if len(seenRuns) != 1 || seenRuns[0] != run.ID {
		t.Fatalf("expected run %s to be scanned once, got %v", run.ID, seenRuns)
	}
	if seenModel != "codex:gpt-5" {
		t.Fatalf("expected SOP policy model codex:gpt-5, got %q", seenModel)
	}
}

func TestEvolutionSOPAutoBatchTriggersWithoutSavedPolicyAndWithSharedKey(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	// Remove personal CodeBuddy key from user and configure platform shared CodeBuddy key instead.
	u := env.user
	if _, err := env.store.UpdateUserCredentials(u.ID, repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: true}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{
		APIKey:              "ck_shared_test_key",
		InternetEnvironment: "internal",
		Models:              []string{"claude-sonnet-5"},
	}, u.ID); err != nil {
		t.Fatal(err)
	}

	// Do NOT save an evo_sop_policies row: SOP defaults to Enabled=true, BatchSize=10.
	// Verify failed proposal error is surfaced in SOP summary lastRunNote so UI does not stay silent.
	if err := env.store.PutEvoProposal(domain.EvoProposal{
		ID: "evp-old-failed", ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID,
		AssetName: env.agent.Name, Title: "旧失败提案", Status: domain.EvoProposalFailed, Error: "测试失败原因",
		CreatedAt: now.Add(-30 * time.Minute), UpdatedAt: now.Add(-25 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		r := domain.Run{
			ID:               "default-batch-r-" + string(rune('a'+i)),
			WorkflowID:       env.wf.ID,
			WorkflowSnapshot: env.wf,
			Status:           "succeeded",
			Input:            `{"ticketId":"T-1"}`,
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p", ResponsibleUserID: u.ID},
			CreatedAt:        now.Add(time.Duration(i-15) * time.Minute),
			UpdatedAt:        now.Add(time.Duration(i-15) * time.Minute),
			Usage:            domain.TokenUsage{TotalTokens: 320000},
			ToolCalls:        14,
			Timing:           &domain.RunTiming{ExecutionMs: 90000},
			Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1, Usage: domain.TokenUsage{TotalTokens: 320000}, ToolCalls: 14}},
		}
		if err := env.store.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}

	// Verify resolveSOPActorUser picks up run user + shared key cleanly even without evoImproveOverride.
	policy := env.store.EvoSOPPolicy("evo-p", env.wf.ID)
	actor, model, ok := env.h.resolveSOPActorUser(policy, env.h.loadWindowRuns("evo-p", 14, 50), 0)
	if !ok || actor.CodeBuddyAPIKey != "ck_shared_test_key" || model != "claude-sonnet-5" {
		t.Fatalf("expected shared key resolution, got ok=%v key=%q model=%q", ok, actor.CodeBuddyAPIKey, model)
	}

	setEvoImproveForTest(func(_ context.Context, _ domain.EvoProposal, _, _ string, _ domain.User) (string, error) {
		return `{"title":"精简冗余输出","rationale":"降低 Token 消耗","content":"原始正文：请完成开发任务并自测。\n直接输出精简结果。"}`, nil
	})
	defer setEvoImproveForTest(nil)

	// Immediately during failed-proposal cooldown it should not loop; after cooldown it must recover and trigger.
	env.h.sweepSOPBatches(now)
	if props := env.store.EvoProposals("evo-p", 20); len(props) != 1 {
		t.Fatalf("expected cooldown to avoid immediate retry loop, got %d", len(props))
	}
	env.h.sweepSOPBatches(now.Add(15 * time.Minute))
	props := env.store.EvoProposals("evo-p", 20)
	if len(props) != 2 {
		t.Fatalf("expected default-enabled SOP to create a new proposal alongside old failed one, got %d (%+v)", len(props), props)
	}
}

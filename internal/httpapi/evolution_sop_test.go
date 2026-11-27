package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	if len(detail.Assets) != 1 || detail.Assets[0].ID != env.agent.ID || detail.Assets[0].CurrentVersion != 2 || detail.Assets[0].VersionCount != 2 {
		t.Fatalf("expected SOP detail assets with version counts for rollback, got %+v", detail.Assets)
	}
	if detail.Versions[0].RollbackVersion != 1 || detail.Versions[0].AssetID != env.agent.ID {
		t.Fatalf("expected pre-edit row to expose direct rollback target v1, got %+v", detail.Versions[0])
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
		return "", errors.New("test-stop-after-prompt")
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

func TestEvolutionSOPAutoBatchOptInSharedKeyAndViewerIsolation(t *testing.T) {
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

	// Add a viewer with a personal API key to ensure automated SOP jobs NEVER borrow a viewer's key.
	if err := env.store.PutUser(domain.User{StaffID: 3102, LoginName: "evo-viewer", ChineseName: "只读成员"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := env.store.UserByLoginName("evo-viewer")
	if _, err := env.store.UpdateUserCredentials(viewer.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "ck_viewer_secret"}); err != nil {
		t.Fatal(err)
	}
	proj, _ := env.store.Project("evo-p")
	proj.OwnerID = u.ID
	proj.Members = []domain.ProjectMember{
		{UserID: u.ID, Role: "owner"},
		{UserID: viewer.ID, Role: "viewer"},
	}
	if err := env.store.PutProject(proj); err != nil {
		t.Fatal(err)
	}

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
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p", ResponsibleUserID: viewer.ID},
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

	// P1-2: Unconfigured SOP defaults to Enabled=false and must NOT auto-trigger.
	if def := env.store.EvoSOPPolicy("evo-p", env.wf.ID); def.Enabled {
		t.Fatalf("expected DefaultEvoSOPPolicy.Enabled=false, got true")
	}
	env.h.sweepSOPBatches(now.Add(20 * time.Minute))
	if props := env.store.EvoProposals("evo-p", 20); len(props) != 1 {
		t.Fatalf("unconfigured SOP must not auto-trigger, got %d proposals", len(props))
	}

	// P1-3: Viewer policy UpdatedBy must be rejected and never borrow viewer's personal key.
	viewerPolicy := domain.EvoSOPPolicy{WorkflowID: env.wf.ID, ProjectID: "evo-p", Enabled: true, BatchSize: 10, UpdatedBy: viewer.ID}
	if _, _, ok := env.h.resolveSOPActorUser(viewerPolicy, env.wf); ok {
		t.Fatal("resolveSOPActorUser must reject project viewer")
	}

	// Explicitly enable policy as writable owner u.
	ownerPolicy := domain.EvoSOPPolicy{
		WorkflowID: env.wf.ID, ProjectID: "evo-p", Enabled: true,
		Preferences: []string{domain.EvoPrefTokens}, BatchSize: 10,
		UpdatedBy: u.ID, UpdatedAt: now.Add(-time.Hour),
	}
	if err := env.store.PutEvoSOPPolicy(ownerPolicy); err != nil {
		t.Fatal(err)
	}

	actor, model, ok := env.h.resolveSOPActorUser(ownerPolicy, env.wf)
	if !ok || actor.CodeBuddyAPIKey != "ck_shared_test_key" || model != "claude-sonnet-5" {
		t.Fatalf("expected shared key resolution for policy owner, got ok=%v key=%q model=%q", ok, actor.CodeBuddyAPIKey, model)
	}

	// P1-3: GET /api/evolution/overview must be strictly read-only and never create proposals.
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("overview status=%d", rec.Code)
	}
	if props := env.store.EvoProposals("evo-p", 20); len(props) != 1 {
		t.Fatalf("GET overview must not have write side effects, got %d proposals", len(props))
	}

	done := make(chan struct{}, 1)
	setEvoImproveForTest(func(_ context.Context, _ domain.EvoProposal, _, _ string, _ domain.User) (string, error) {
		select {
		case done <- struct{}{}:
		default:
		}
		return "", errors.New("test-stop-after-trigger")
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
		t.Fatalf("expected enabled SOP to create a new proposal alongside old failed one, got %d (%+v)", len(props), props)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for background improver job to finish")
	}
}

func TestEvolutionSOPPolicyIsolationAndValidation(t *testing.T) {
	env := newEvoTestEnv(t)
	if err := env.store.PutProject(domain.Project{ID: "evo-other", Name: "其他项目"}); err != nil {
		t.Fatal(err)
	}
	// Save a policy for env.wf in evo-p.
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		WorkflowID: env.wf.ID, ProjectID: "evo-p", Enabled: true, BatchSize: 12, Preferences: []string{domain.EvoPrefTokens}, UpdatedBy: env.user.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// P1-1: Attempting to update evo-p's workflow policy under another project must fail 404 and not overwrite evo-p.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/evolution/sops/"+env.wf.ID+"/policy",
		strings.NewReader(`{"projectId":"evo-other","enabled":false,"batchSize":30}`))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-project policy PUT, got %d body=%s", rec.Code, rec.Body.String())
	}
	after := env.store.EvoSOPPolicy("evo-p", env.wf.ID)
	if !after.Enabled || after.BatchSize != 12 {
		t.Fatalf("project A policy was overwritten: %+v", after)
	}

	// Non-existent workflow returns 404.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/evolution/sops/nonexistent-wf/policy",
		strings.NewReader(`{"projectId":"evo-p","enabled":true,"batchSize":10}`))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing workflow, got %d", rec.Code)
	}

	// Invalid model format returns 400 instead of silently saving "default".
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/evolution/sops/"+env.wf.ID+"/policy",
		strings.NewReader(`{"projectId":"evo-p","enabled":true,"batchSize":10,"model":"bad model with spaces!"}`))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid model, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvolutionSOPSharedAgentIsolation(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	// Create a second SOP sharing the exact same agent.
	wfB := domain.WorkflowDefinition{
		ID: "evo-wf-b", Name: "另一条共享角色 SOP", ProjectID: "evo-p", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "code", Name: "开发", Type: "agent", AgentID: env.agent.ID, ExecutorID: "mock", MaxAttempts: 1}},
	}
	if err := env.store.PutWorkflow(wfB); err != nil {
		t.Fatal(err)
	}
	// Only env.wf has a run & finding & canary proposal; wfB has its own run with no proposal/canary participation.
	if err := env.store.PutRun(domain.Run{
		ID: "run-wf-a", WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"}, CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutRun(domain.Run{
		ID: "run-wf-b", WorkflowID: wfB.ID, WorkflowSnapshot: wfB, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"}, CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	findingA := domain.EvoFinding{
		ID: "evf-wf-a", ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID,
		AssetName: env.agent.Name, Status: domain.EvoFindingProposed, ProposalID: "evp-wf-a",
		WorkflowIDs: []string{env.wf.ID}, SampleRunIDs: []string{"run-wf-a"},
		CreatedAt: now.Add(-9 * time.Minute), UpdatedAt: now.Add(-9 * time.Minute),
	}
	if err := env.store.PutEvoFinding(findingA); err != nil {
		t.Fatal(err)
	}
	candBody := env.agent.Prompt + "\n仅针对 A 的修改"
	propA := domain.EvoProposal{
		ID: "evp-wf-a", ProjectID: "evo-p", FindingID: findingA.ID,
		AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, AssetName: env.agent.Name,
		Title: "SOP A 专属优化", BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt),
		CandidateContent: candBody, Status: domain.EvoProposalCanary,
		Canary: &domain.EvoCanary{Percent: 50, MinRuns: 5, Status: domain.EvoCanaryRunning, StartedAt: now.Add(-20 * time.Minute)},
	}
	if err := env.store.PutEvoProposal(propA); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutRunAssetPins([]domain.RunAssetPin{
		{RunID: "run-wf-a", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 2, Hash: domain.ContentHash(candBody), CreatedAt: now.Add(-10 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}

	// wfB must remain in monitor stage with 0 gray rounds (no leak from wfA).
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/evolution/sops/"+wfB.ID+"?projectId=evo-p", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("wfB detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detailB domain.EvoSOPDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detailB); err != nil {
		t.Fatal(err)
	}
	if detailB.Stage != domain.EvoSOPStageMonitor || len(detailB.GrayRounds) != 0 || detailB.ActiveProposal != nil {
		t.Fatalf("shared-agent SOP B must not inherit SOP A canary/stage: stage=%s gray=%d active=%+v", detailB.Stage, len(detailB.GrayRounds), detailB.ActiveProposal)
	}
}

func TestEvolutionSOPDetectionSkipsDisabledWorkflowsHeadOfLine(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	disabledWF := domain.WorkflowDefinition{
		ID: "wf-disabled", Name: "未启用检测 SOP", ProjectID: "evo-p", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "code", Name: "开发", Type: "agent", AgentID: env.agent.ID, ExecutorID: "mock", MaxAttempts: 1}},
	}
	if err := env.store.PutWorkflow(disabledWF); err != nil {
		t.Fatal(err)
	}
	// Enable ONLY env.wf.
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		WorkflowID: env.wf.ID, ProjectID: "evo-p", Enabled: true, BatchSize: 5, UpdatedBy: env.user.ID, UpdatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Seed 10 older runs for the disabled SOP so they would fill a naive top-N query.
	for i := 0; i < 10; i++ {
		if err := env.store.PutRun(domain.Run{
			ID: "disabled-run-" + string(rune('a'+i)), WorkflowID: disabledWF.ID, WorkflowSnapshot: disabledWF,
			Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			CreatedAt: now.Add(time.Duration(-30+i) * time.Minute), UpdatedAt: now.Add(time.Duration(-30+i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	enabledRun := env.seedRun(t, "enabled-run-1", "succeeded", "succeeded", "")

	var seen []string
	env.h.sweepSOPDetections(context.Background(), func(_ context.Context, _ string, _ domain.SOPDetectionConfig, target domain.Run) (domain.RunEvalResult, error) {
		seen = append(seen, target.ID)
		return domain.RunEvalResult{Passed: true, Score: 90}, nil
	})
	if len(seen) != 1 || seen[0] != enabledRun.ID {
		t.Fatalf("expected only enabled SOP run %s to be scanned without head-of-line blocking, got %v", enabledRun.ID, seen)
	}
}

func TestEvolutionProjectLevelSOPDetectionSkipsExplicitlyDisabledSOP(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	disabledWF := domain.WorkflowDefinition{
		ID: "wf-proj-disabled", Name: "显式关闭 SOP", ProjectID: "evo-p", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "code", Name: "开发", Type: "agent", AgentID: env.agent.ID, ExecutorID: "mock", MaxAttempts: 1}},
	}
	if err := env.store.PutWorkflow(disabledWF); err != nil {
		t.Fatal(err)
	}
	// Enable legacy project-level SOP detection.
	if err := env.store.PutSOPDetection("evo-p", domain.SOPDetectionConfig{
		Enabled:      true,
		Model:        "codebuddy:claude-sonnet-5",
		EnabledAt:    now.Add(-time.Hour),
		ConfiguredBy: env.user.ID,
	}); err != nil {
		t.Fatal(err)
	}
	// Explicitly disable disabledWF in evo_sop_policies, enable env.wf.
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		WorkflowID: disabledWF.ID, ProjectID: "evo-p", Enabled: false, BatchSize: 5, UpdatedBy: env.user.ID, UpdatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutEvoSOPPolicy(domain.EvoSOPPolicy{
		WorkflowID: env.wf.ID, ProjectID: "evo-p", Enabled: true, BatchSize: 5, UpdatedBy: env.user.ID, UpdatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Seed 4 older runs for disabledWF (more than project-level limit=2) before enabledRun.
	for i := 0; i < 4; i++ {
		if err := env.store.PutRun(domain.Run{
			ID: "proj-disabled-" + string(rune('a'+i)), WorkflowID: disabledWF.ID, WorkflowSnapshot: disabledWF,
			Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"},
			CreatedAt: now.Add(time.Duration(-20+i) * time.Minute), UpdatedAt: now.Add(time.Duration(-20+i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	enabledRun := env.seedRun(t, "proj-enabled-1", "succeeded", "succeeded", "")

	var seen []string
	env.h.sweepSOPDetections(context.Background(), func(_ context.Context, _ string, _ domain.SOPDetectionConfig, target domain.Run) (domain.RunEvalResult, error) {
		seen = append(seen, target.ID)
		return domain.RunEvalResult{Passed: true, Score: 92}, nil
	})
	if len(seen) != 1 || seen[0] != enabledRun.ID {
		t.Fatalf("P2-7: project-level detection must skip disabled SOPs without head-of-line blocking, got %v", seen)
	}
}

func TestEvolutionSOPRollbackHashFallbackPreservesPreRollbackRuns(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()

	wf := env.wf
	wf.Version = 2
	if err := env.store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	v1Prompt := env.agent.Prompt
	v2Prompt := env.agent.Prompt + "\n修改稿 v2"
	v1Hash := domain.ContentHash(v1Prompt)
	v2Hash := domain.ContentHash(v2Prompt)

	// Record v1 (baseline), v2 (manual), v3 (rollback to v1 -> shares v1Hash!).
	if _, err := env.store.AppendAssetVersion(domain.AssetVersion{
		AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 1,
		Content: v1Prompt, Hash: v1Hash, Source: domain.EvoVersionBaseline, CreatedAt: now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.AppendAssetVersion(domain.AssetVersion{
		AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 2,
		Content: v2Prompt, Hash: v2Hash, Source: domain.EvoVersionManual, CreatedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.AppendAssetVersion(domain.AssetVersion{
		AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 3,
		Content: v1Prompt, Hash: v1Hash, Source: domain.EvoVersionRollback, Note: "回滚到 v1", CreatedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// Two runs on v1 BEFORE rollback, pinned with Version=0 and Hash=v1Hash (legacy pin format).
	for i, id := range []string{"pre-rb-1", "pre-rb-2"} {
		at := now.Add(time.Duration(-150+i*10) * time.Minute)
		if err := env.store.PutRun(domain.Run{
			ID: id, WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p"}, CreatedAt: at, UpdatedAt: at,
			Usage: domain.TokenUsage{TotalTokens: 60000}, ToolCalls: 6,
			Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", Attempt: 1}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := env.store.PutRunAssetPins([]domain.RunAssetPin{{
			RunID: id, AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Version: 0, Hash: v1Hash, CreatedAt: at,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	// Zero runs after v3 rollback -> v1 must have 2 runs and current v3 rollback row must have 0 runs.
	detail, ok := env.h.buildSOPDetail("evo-p", wf.ID, 14)
	if !ok {
		t.Fatal("expected SOP detail")
	}
	if len(detail.Versions) != 2 {
		t.Fatalf("expected 2 version points (v1 with 2 runs + current v3 rollback with 0 runs), got %d: %+v", len(detail.Versions), detail.Versions)
	}
	if detail.Versions[0].Metrics.Runs != 2 || detail.Versions[1].Metrics.Runs != 0 || !detail.Versions[1].Current || detail.Versions[1].Source != domain.EvoVersionRollback {
		t.Fatalf("P2-9: pre-rollback runs must stay on v1 and not bleed into v3 rollback row: v0=%+v v1=%+v", detail.Versions[0], detail.Versions[1])
	}
}

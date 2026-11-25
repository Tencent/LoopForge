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

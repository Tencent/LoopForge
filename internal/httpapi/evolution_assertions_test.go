package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
)

func TestSOPAssertionsGenerateSaveAndAutoReleaseGuard(t *testing.T) {
	env := newEvoTestEnv(t)
	now := time.Now().UTC()
	const projectID = "evo-p"

	doJSON := func(method, target string, payload any) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(body))
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	// Seed 3 clean historical runs for wf with triage.json artifact + an AI RunEvalRecord
	// that MUST NOT overwrite Layer 1 static score.
	for i := 0; i < 3; i++ {
		runID := "run-assert-" + string(rune('1'+i))
		run := domain.Run{
			ID:               runID,
			WorkflowID:       env.wf.ID,
			Status:           "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: projectID},
			CreatedAt:        now.Add(time.Duration(i-5) * time.Hour),
			UpdatedAt:        now.Add(time.Duration(i-5)*time.Hour + time.Minute),
			Usage:            domain.TokenUsage{TotalTokens: 180_000},
			ToolCalls:        8,
			Timing:           &domain.RunTiming{ExecutionMs: 50_000},
			WorkflowSnapshot: env.wf,
			Nodes: map[string]*domain.NodeRun{
				"code": {NodeID: "code", Status: "succeeded", Attempt: 1, Output: `{"ticketId":"T-1","route":"logistics","reason":"ok"}`},
			},
			Artifacts: []domain.Artifact{
				{ID: "art-" + runID, RunID: runID, NodeID: "code", Attempt: 1, Name: "triage.json", Path: "triage.json", Content: `{"ticketId":"T-1","route":"logistics","reason":"ok"}`},
			},
		}
		if err := env.store.PutRun(run); err != nil {
			t.Fatalf("put run: %v", err)
		}
		_ = env.store.PutRunEval(domain.RunEvalRecord{
			ID:     "ev-" + runID,
			RunID:  runID,
			Status: domain.RunEvalSucceeded,
			Score:  33, // drastically different AI score must NOT overwrite Layer 1 static score
		})
	}

	// 1. SOP detail starts with 0 assertions, autoReleaseAllowed=false, and Layer 1 static score (not AI 33).
	w := doJSON(http.MethodGet, "/api/evolution/sops/"+env.wf.ID+"?projectId="+projectID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get sop detail: %d %s", w.Code, w.Body.String())
	}
	var detail domain.EvoSOPDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.AssertionCount != 0 || detail.AutoReleaseAllowed {
		t.Fatalf("expected 0 assertions and autoReleaseAllowed=false, got count=%d allowed=%v", detail.AssertionCount, detail.AutoReleaseAllowed)
	}
	if detail.LatestMetrics.ScoreSource != "process_v1" || detail.LatestMetrics.ScorerVersion != domain.EvoScorerVersion {
		t.Fatalf("expected process_v1 static scorer, got %+v", detail.LatestMetrics)
	}
	if detail.LatestMetrics.Score == nil || *detail.LatestMetrics.Score == 33 {
		t.Fatalf("AI RunEval score 33 must not overwrite Layer 1 static score, got %+v", detail.LatestMetrics.Score)
	}

	// Proposal belonging to SOP with 0 assertions must be blocked from L3 auto release.
	sopProp := domain.EvoProposal{ID: "evp-sop-guard", ProjectID: projectID, WorkflowID: env.wf.ID}
	if allowed, reason := env.h.proposalAllowsAutoRelease(sopProp); allowed || reason == "" {
		t.Fatalf("expected auto release blocked when 0 assertions configured, got allowed=%v reason=%q", allowed, reason)
	}

	// 2. Generate assertion from one natural-language sentence + instant historical backtest.
	w = doJSON(http.MethodPost, "/api/evolution/sops/"+env.wf.ID+"/assertions/generate", map[string]any{
		"projectId": projectID,
		"prompt":    "必须生成 triage.json 且包含 ticketId、route，route 只能是 logistics / refund / human",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("generate assertions: %d %s", w.Code, w.Body.String())
	}
	var genResp struct {
		Source     string                        `json:"source"`
		Candidates []domain.EvoAssertionBacktest `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &genResp); err != nil || len(genResp.Candidates) == 0 {
		t.Fatalf("decode generate resp: err=%v body=%s", err, w.Body.String())
	}
	for _, cand := range genResp.Candidates {
		if cand.TestedRuns != 3 || cand.PassedRuns != 3 || !cand.AllPassed {
			t.Fatalf("expected 3/3 backtest pass, got %+v", cand)
		}
	}

	// 3. Save generated assertions -> unlocks autoReleaseAllowed=true.
	toSave := make([]domain.EvoSOPAssertion, 0, len(genResp.Candidates))
	for _, c := range genResp.Candidates {
		toSave = append(toSave, c.Assertion)
	}
	w = doJSON(http.MethodPut, "/api/evolution/sops/"+env.wf.ID+"/assertions", map[string]any{
		"projectId":  projectID,
		"assertions": toSave,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("put assertions: %d %s", w.Code, w.Body.String())
	}
	if allowed, reason := env.h.proposalAllowsAutoRelease(sopProp); !allowed {
		t.Fatalf("expected auto release unlocked after saving assertions, got reason=%q", reason)
	}

	// 4. Suggest endpoint returns high-confidence intersection assertions.
	w = doJSON(http.MethodPost, "/api/evolution/sops/"+env.wf.ID+"/assertions/suggest", map[string]any{
		"projectId": projectID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("suggest assertions: %d %s", w.Code, w.Body.String())
	}

	// 5. Reset baseline endpoint persists frozen baseline.
	w = doJSON(http.MethodPost, "/api/evolution/sops/"+env.wf.ID+"/baseline/reset", map[string]any{
		"projectId": projectID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("reset baseline: %d %s", w.Code, w.Body.String())
	}
	pol := env.store.EvoSOPPolicy(projectID, env.wf.ID)
	if pol.ScoreBaseline == nil || !pol.ScoreBaseline.BaselineReady || pol.ScoreBaseline.ScorerVersion != domain.EvoScorerVersion {
		t.Fatalf("expected persisted frozen baseline, got %+v", pol.ScoreBaseline)
	}
	if len(evolution.ActiveSOPAssertions(pol.Assertions)) == 0 {
		t.Fatalf("expected assertions preserved after baseline reset, got %+v", pol.Assertions)
	}
}

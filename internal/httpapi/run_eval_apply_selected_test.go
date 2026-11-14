package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func newApplySelectedHandler(t *testing.T) (*runsHandlers, *repo.Store) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &runsHandlers{deps: &deps{repo: store}}, store
}

func TestApplySelectedRecommendationsAppliesAndRecordsFailures(t *testing.T) {
	h, store := newApplySelectedHandler(t)
	if err := store.PutAgent(domain.AgentDefinition{ID: "fixer", Name: "修复者", ProjectID: "prj", Prompt: "修 bug"}); err != nil {
		t.Fatal(err)
	}
	recs := []domain.RunEvalRecommendation{
		{ID: "ok", Target: "agent", Action: "append_instructions", TargetID: "fixer", Text: "先跑测试", Status: domain.RunEvalRecPending, ApplyError: "上次失败"},
		{ID: "bad", Target: "agent", Action: "append_instructions", TargetID: "ghost", Text: "x", Status: domain.RunEvalRecPending},
	}
	if !h.applySelectedRecommendations(domain.Run{}, "prj", recs, map[string]bool{"ok": true, "bad": true}) {
		t.Fatal("expected change")
	}

	ok := recs[0]
	if ok.Status != domain.RunEvalRecApplied || ok.AppliedAt == nil || ok.AppliedAt.IsZero() {
		t.Fatalf("applied recommendation must record status and time: %#v", ok)
	}
	if ok.ApplyError != "" {
		t.Fatalf("a successful apply must clear the previous error, got %q", ok.ApplyError)
	}
	if ok.AppliedTargetID != "fixer" {
		t.Fatalf("applied target = %q", ok.AppliedTargetID)
	}
	if agent, _ := store.Agent("fixer"); !strings.Contains(agent.Prompt, "先跑测试") {
		t.Fatalf("agent prompt not patched: %q", agent.Prompt)
	}

	bad := recs[1]
	if !strings.Contains(bad.ApplyError, `"ghost"`) {
		t.Fatalf("failed apply must record the error, got %q", bad.ApplyError)
	}
	if bad.Status != domain.RunEvalRecPending || bad.AppliedAt != nil {
		t.Fatalf("failed recommendation must stay pending: %#v", bad)
	}
}

func TestApplySelectedRecommendationsReportsFailureAsChange(t *testing.T) {
	h, _ := newApplySelectedHandler(t)
	recs := []domain.RunEvalRecommendation{{ID: "bad", Target: "agent", Action: "append_instructions", TargetID: "ghost", Text: "x"}}
	if !h.applySelectedRecommendations(domain.Run{}, "prj", recs, map[string]bool{"bad": true}) {
		t.Fatal("a recorded apply error must count as a change so it is persisted")
	}
	if recs[0].ApplyError == "" {
		t.Fatal("apply error missing")
	}
}

func TestApplyRunEvalPersistsAndClonesIntoRunProject(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "shared-fixer", Name: "共享修复者", ProjectID: "other-project", Prompt: "修 bug"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "apply-selected-run", Status: "succeeded", Nodes: map[string]*domain.NodeRun{},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	eval := domain.RunEvalRecord{ID: "apply-selected-eval", RunID: run.ID, Status: domain.RunEvalSucceeded,
		Result: domain.RunEvalResult{Recommendations: []domain.RunEvalRecommendation{
			{ID: "rec-1", Target: "agent", Action: "append_instructions", TargetID: "shared-fixer", Text: "先跑测试", Status: domain.RunEvalRecPending},
		}}}
	if err := f.s.PutRunEval(eval); err != nil {
		t.Fatal(err)
	}

	rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval/apply",
		map[string]any{"evalId": eval.ID, "recommendationIds": []string{" rec-1 "}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d: %s", rec.Code, rec.Body.String())
	}

	stored, ok := f.s.RunEval(eval.ID)
	if !ok || len(stored.Result.Recommendations) != 1 {
		t.Fatalf("stored eval = %#v", stored)
	}
	got := stored.Result.Recommendations[0]
	if got.Status != domain.RunEvalRecApplied || got.AppliedAt == nil {
		t.Fatalf("applied recommendation was not persisted: %#v", got)
	}
	if got.AppliedTargetID != "shared-fixer-platform" {
		t.Fatalf("recommendation must be applied to a copy in the run's project, got %q", got.AppliedTargetID)
	}
	clone, ok := f.s.Agent("shared-fixer-platform")
	if !ok || clone.ProjectID != "platform" || !strings.Contains(clone.Prompt, "先跑测试") {
		t.Fatalf("project copy = %#v (found %v)", clone, ok)
	}
	if original, _ := f.s.Agent("shared-fixer"); strings.Contains(original.Prompt, "先跑测试") {
		t.Fatalf("original agent of another project must stay untouched: %q", original.Prompt)
	}
}

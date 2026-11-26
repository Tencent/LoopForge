package httpapi

import (
	"net/http"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestEvolutionReleaseEvaluationUsesComparableRunRecords(t *testing.T) {
	env := newEvoTestEnv(t)
	source := env.seedRun(t, "eval-source", "failed", "failed", "缺少验收")
	env.seedValidatingProposal(t, "evp-run-eval")
	replayID := "eval-replay"
	env.seedReplay(t, "evp-run-eval", source.ID, domain.Run{
		ID: replayID, WorkflowID: env.wf.ID, WorkflowSnapshot: env.wf, Status: "succeeded",
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded"}},
	})
	replay, _ := env.store.EvoReplay("evr-" + replayID)
	replay.SourceStatus, replay.SourceScore = source.Status, 0
	if err := env.store.PutEvoReplay(replay); err != nil {
		t.Fatal(err)
	}
	env.h.sweepProposal(domain.EvoProposal{ID: "evp-run-eval"}, time.Now().UTC())
	read := func() *evoReleaseEvaluation {
		return requestJSONAuthed[evoProposalDetailResponse](t, env.router, env.user, http.MethodGet, "/api/evolution/proposals/evp-run-eval", nil, http.StatusOK).ReleaseEvaluation
	}
	if got := read(); got == nil || got.SourceRunID != source.ID || got.ReplayRunID != replayID || got.Scores != nil {
		t.Fatalf("unscored pair = %+v", got)
	}
	policy := domain.DefaultRunEvalPolicy()
	policy.Threshold = 75
	putEval := func(id, runID, model string, score int) {
		t.Helper()
		dimensions := map[string]int{}
		for _, dim := range policy.Dimensions {
			dimensions[dim.ID] = score
		}
		now := time.Now().UTC()
		if err := env.store.PutRunEval(domain.RunEvalRecord{
			ID: id, RunID: runID, Status: domain.RunEvalSucceeded, Model: model,
			Policy: policy, Score: score, Passed: score >= policy.Threshold,
			Result: domain.RunEvalResult{Dimensions: dimensions}, CreatedAt: now, FinishedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	putEval("eval-1", source.ID, "same-model", 64)
	putEval("eval-2", replayID, "same-model", 90)
	putEval("sop-check-1", replayID, "detector-model", 52)
	got := read()
	if got == nil || got.Scores == nil || got.Scores.Before != 64 || got.Scores.After != 90 ||
		got.Scores.Threshold != 75 || len(got.Scores.Dimensions) != len(policy.Dimensions) {
		t.Fatalf("matched evaluation = %+v", got)
	}
	putEval("eval-3", replayID, "different-model", 91)
	if got := read(); got == nil || got.Scores != nil {
		t.Fatalf("incompatible model yielded scores: %+v", got)
	}
	putEval("eval-4", replayID, "same-model", 91)
	source.UpdatedAt = time.Now().UTC().Add(time.Second)
	if err := env.store.PutRun(source); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != nil {
		t.Fatalf("changed source returned %v", got)
	}
}

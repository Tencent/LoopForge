package httpapi

import (
	"reflect"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
)

type evoReleaseEvalDimension struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
	Before int    `json:"before"`
	After  int    `json:"after"`
}

type evoReleaseEvalScores struct {
	Before     int                       `json:"before"`
	After      int                       `json:"after"`
	Threshold  int                       `json:"threshold"`
	Model      string                    `json:"model"`
	Dimensions []evoReleaseEvalDimension `json:"dimensions"`
}

type evoReleaseEvaluation struct {
	ReplayID    string                `json:"replayId"`
	SourceRunID string                `json:"sourceRunId"`
	ReplayRunID string                `json:"replayRunId"`
	Scores      *evoReleaseEvalScores `json:"scores,omitempty"`
}

type evoProposalDetailResponse struct {
	domain.EvoProposal
	ReleaseEvaluation *evoReleaseEvaluation `json:"releaseEvaluation,omitempty"`
}

// Use one paired replay so the score change describes the same input and
// does not combine unrelated models or evaluation policies.
func (h *evolutionHandlers) releaseEvaluation(p domain.EvoProposal) *evoReleaseEvaluation {
	var first *evoReleaseEvaluation
	for _, r := range p.Replays {
		if r.FinishedAt.IsZero() || r.Outcome == "" || r.Outcome == domain.EvoOutcomeBlocked || r.SourceRunID == "" || r.RunID == "" {
			continue
		}
		source, hasSource := h.repo.RunState(r.SourceRunID)
		replay, hasReplay := h.repo.RunState(r.RunID)
		if !hasSource || !hasReplay || h.runProject(source) != p.ProjectID || h.runProject(replay) != p.ProjectID ||
			!replay.ExecutionContext.IsEvolutionReplay() || replay.ExecutionContext.EvolutionReplayID != r.ID ||
			!domain.TerminalRunStatus(source.Status) || !domain.TerminalRunStatus(replay.Status) ||
			evolution.RunScore(source) != r.SourceScore || evolution.RunScore(replay) != r.Score ||
			!evoRunSettledBy(source, r.StartedAt) || !evoRunSettledBy(replay, r.FinishedAt) {
			continue
		}
		pair := &evoReleaseEvaluation{ReplayID: r.ID, SourceRunID: source.ID, ReplayRunID: replay.ID}
		if first == nil {
			first = pair
		}
		before, beforeOK := h.repo.LatestRunEval(source.ID)
		after, afterOK := h.repo.LatestRunEval(replay.ID)
		if !beforeOK || !afterOK || !evoRunSettledBy(source, before.CreatedAt) || !evoRunSettledBy(replay, after.CreatedAt) {
			continue
		}
		if pair.Scores = evoComparableRunEvalScores(before, after); pair.Scores != nil {
			return pair
		}
	}
	return first
}

func evoComparableRunEvalScores(before, after domain.RunEvalRecord) *evoReleaseEvalScores {
	if before.Status != domain.RunEvalSucceeded || after.Status != domain.RunEvalSucceeded ||
		before.Model == "" || before.Model != after.Model || before.Policy.IsZero() || after.Policy.IsZero() {
		return nil
	}
	policy := domain.EffectiveRunEvalPolicy(before.Policy)
	if !reflect.DeepEqual(policy, domain.EffectiveRunEvalPolicy(after.Policy)) {
		return nil
	}
	for _, evaluation := range []domain.RunEvalRecord{before, after} {
		score, _, err := domain.ScoreRunEvalDimensions(policy, evaluation.Result.Dimensions)
		if err != nil || score != evaluation.Score {
			return nil
		}
	}
	out := &evoReleaseEvalScores{Before: before.Score, After: after.Score, Threshold: policy.Threshold, Model: before.Model}
	for _, dim := range policy.Dimensions {
		out.Dimensions = append(out.Dimensions, evoReleaseEvalDimension{
			Name: dim.Name, Weight: max(1, dim.Weight), Before: before.Result.Dimensions[dim.ID], After: after.Result.Dimensions[dim.ID],
		})
	}
	return out
}

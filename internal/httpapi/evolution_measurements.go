package httpapi

import (
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
)

func evoReplayMeasurements(run domain.Run, reused []string) *domain.EvoReplayMeasurements {
	metrics := &domain.EvoReplayMeasurements{}
	tokens, calls := run.Usage.TotalTokens, run.ToolCalls
	skip := make(map[string]bool, len(reused))
	for _, id := range reused {
		if skip[id] {
			continue
		}
		skip[id] = true
		if node := run.Nodes[id]; node != nil {
			tokens -= node.Usage.TotalTokens
			calls -= node.ToolCalls
		}
	}
	if tokens > 0 {
		metrics.Tokens = &tokens
	}
	if calls >= 0 && (run.TelemetrySequence > 0 || run.ToolCalls > 0) {
		metrics.ToolCalls = &calls
	}
	timingRun, events := run, run.Events
	if len(skip) > 0 {
		timingRun.Nodes = make(map[string]*domain.NodeRun, len(run.Nodes))
		for id, node := range run.Nodes {
			if !skip[id] {
				timingRun.Nodes[id] = node
			}
		}
		events = make([]domain.RunEvent, 0, len(run.Events))
		for _, event := range run.Events {
			if !skip[event.NodeID] {
				events = append(events, event)
			}
		}
	}
	at := run.UpdatedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	intervals, known := domain.RunExecutionIntervals(timingRun, events, at)
	if known {
		var elapsed time.Duration
		for _, interval := range intervals {
			elapsed += interval.FinishedAt.Sub(interval.StartedAt)
		}
		ms := elapsed.Milliseconds()
		metrics.ExecutionMs = &ms
	}
	return metrics
}

// Older proposals have scores but no captured resource readings. The source
// fallback requires a terminal record observed before the replay started.
func (h *evolutionHandlers) loadHistoricalReplayMeasurements(p *domain.EvoProposal) {
	for i := range p.Replays {
		r := &p.Replays[i]
		if r.Outcome == "" || r.Outcome == domain.EvoOutcomeBlocked || r.FinishedAt.IsZero() {
			continue
		}
		if r.SourceMeasurements == nil {
			if run, ok := h.repo.RunState(r.SourceRunID); ok && h.runProject(run) == p.ProjectID && evolution.RunScore(run) == r.SourceScore && evoRunSettledBy(run, r.StartedAt) {
				r.SourceMeasurements = evoReplayMeasurements(run, r.ReusedNodes)
			}
		}
		if r.ReplayMeasurements == nil {
			if run, ok := h.repo.RunState(r.RunID); ok && h.runProject(run) == p.ProjectID && run.ExecutionContext.IsEvolutionReplay() && evolution.RunScore(run) == r.Score && evoRunSettledBy(run, r.FinishedAt) {
				r.ReplayMeasurements = evoReplayMeasurements(run, r.ReusedNodes)
			}
		}
	}
}

func evoRunSettledBy(run domain.Run, at time.Time) bool {
	for i := len(run.Events) - 1; i >= 0; i-- {
		event := run.Events[i]
		if event.Type == "run."+run.Status && !event.Timestamp.IsZero() {
			return !at.IsZero() && !event.Timestamp.After(at)
		}
	}
	return !at.IsZero() && !run.UpdatedAt.IsZero() && !run.UpdatedAt.After(at)
}

package repo

import (
	"agentflow/internal/domain"
	"time"
)

type businessMergeLatency struct {
	mergedAt         time.Time
	completedAt      time.Time
	duration         int64
	missing          bool
	beforeCompletion bool
}

// Delivery artifacts bind a successful execution round to its own MRs. Later
// unrelated runs and an issue's editable UpdatedAt must not rewrite that cohort.
func (r *AnalyticsRepo) businessDeliveryMRs(runs []domain.Run) (map[string][]domain.DeliveryMR, error) {
	ids := make([]string, 0, len(runs))
	byID := map[string]domain.Run{}
	for _, run := range runs {
		if run.ExecutionContext.ParentRunID == "" {
			ids = append(ids, run.ID)
			byID[run.ID] = run
		}
	}
	for start := 0; start < len(ids); start += 500 {
		var rows []runArtifactRow
		if err := r.DB.Select("id", "run_id", "node_id", "name", "path", "content", "created_at").
			Where("run_id IN ? AND content LIKE ? AND name NOT LIKE ? AND name NOT LIKE ?", ids[start:min(start+500, len(ids))], "%merge_requests/%", "%patch%", "%diff%").
			Order("created_at, id").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			run := byID[row.RunID]
			run.Artifacts = append(run.Artifacts, domain.Artifact{ID: row.ID, NodeID: row.NodeID, Name: row.Name, Path: row.Path, Content: row.Content})
			byID[row.RunID] = run
		}
	}
	out := map[string][]domain.DeliveryMR{}
	for id, run := range byID {
		out[id] = domain.ResolveRunDeliveryMRs(run, nil)
	}
	return out, nil
}

// completionForMR chooses the latest observed success before merge. A flow
// which auto-merges before it finishes is classified separately, never clamped
// to zero. Later retries of the same Run cannot move an earlier merge cohort.
func completionForMR(events []domain.RunEvent, mergedAt time.Time) time.Time {
	var before, after time.Time
	for _, event := range events {
		if event.Type != "run.succeeded" || event.Timestamp.IsZero() {
			continue
		}
		at := event.Timestamp
		if !at.After(mergedAt) && at.After(before) {
			before = at
		}
		if at.After(mergedAt) && (after.IsZero() || at.Before(after)) {
			after = at
		}
	}
	if !before.IsZero() {
		return before
	}
	return after
}

// Each merged MR is one sample. Repositories in the same requirement may
// merge on different days, or while another MR is still open.
func businessCompletionToMerge(item businessIssueTiming, refs map[string][]domain.DeliveryMR, states map[domain.MRDeliveryKey]domain.MRDeliveryState, events map[string][]domain.RunEvent, filter domain.AnalyticsFilter) []businessMergeLatency {
	out := []businessMergeLatency{}
	for key, state := range states {
		if state.IssueID != item.issue.ID || state.MergedAt.IsZero() || !businessTimeInWindow(state.MergedAt, filter) {
			continue
		}
		var completed time.Time
		unknown := false
		for _, run := range item.runs {
			if run.ExecutionContext.ParentRunID != "" || run.CreatedAt.After(state.MergedAt) {
				continue
			}
			matched := false
			for _, mr := range refs[run.ID] {
				if mr.Project == key.Project && mr.IID == key.IID {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			at := completionForMR(events[run.ID], state.MergedAt)
			if at.IsZero() || at.Before(run.CreatedAt) {
				unknown = unknown || run.Status == "succeeded"
				continue
			}
			if at.After(completed) {
				completed = at
			}
		}
		latency := businessMergeLatency{mergedAt: state.MergedAt, completedAt: completed, missing: unknown || completed.IsZero()}
		if !latency.missing {
			latency.beforeCompletion = state.MergedAt.Before(completed)
			if !latency.beforeCompletion {
				latency.duration = state.MergedAt.Sub(completed).Milliseconds()
			}
		}
		out = append(out, latency)
	}
	return out
}

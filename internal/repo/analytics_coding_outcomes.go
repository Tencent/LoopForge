package repo

import (
	"strings"

	"agentflow/internal/domain"
)

type analyticsCodingState struct{ withMR, delivered bool }

func (r *AnalyticsRepo) analyticsCodingStates(ids map[string]bool, issues map[string]domain.Issue) (map[string]analyticsCodingState, error) {
	out := map[string]analyticsCodingState{}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		issue := issues[id]
		mrs := issue.DeliveryMRList()
		delivered := issue.DeliveryState == "merged" || (issue.DeliveryState == "" && domain.AggregateDeliveryMRState(mrs) == "merged")
		out[id] = analyticsCodingState{withMR: len(mrs) > 0 || delivered, delivered: delivered}
		keys = append(keys, id)
	}
	if len(keys) == 0 {
		return out, nil
	}
	repositories := map[string]domain.Repository{}
	for _, repository := range r.catalog.Repositories() {
		repositories[strings.ToLower(strings.TrimSpace(repository.GongfengProjectID))] = repository
	}
	for start := 0; start < len(keys); start += 500 {
		var rows []mrDeliveryStateRow
		if err := r.DB.Select("issue_id", "mr_project", "state", "target_branch").Where("issue_id IN ?", keys[start:min(start+500, len(keys))]).Find(&rows).Error; err != nil {
			return nil, err
		}
		byIssue := map[string][]domain.DeliveryMR{}
		for _, row := range rows {
			state := out[row.IssueID]
			state.withMR = true
			repository := repositories[strings.ToLower(strings.TrimSpace(row.MRProject))]
			classified := domain.ClassifyMRDeliveryState(row.State, row.TargetBranch, repository.DefaultBranch, repository.IntegrateBranch)
			byIssue[row.IssueID] = append(byIssue[row.IssueID], domain.DeliveryMR{State: classified})
			out[row.IssueID] = state
		}
		for id, mrs := range byIssue {
			// A reopened issue stays reopened, and one merged PR cannot stand
			// in for an incomplete multi-repository delivery.
			if issues[id].DeliveryState == "" && domain.AggregateDeliveryMRState(mrs) == "merged" {
				state := out[id]
				state.delivered = true
				out[id] = state
			}
		}
	}
	return out, nil
}

func addAnalyticsCodingOutcome(out *domain.AnalyticsCodingOutcome, state analyticsCodingState) {
	out.Issues++
	if state.withMR {
		out.WithMR++
	}
	if state.delivered {
		out.Delivered++
	}
}

func applyAnalyticsSOPDurations(out *domain.AnalyticsSOPPerformance, durations []int64) {
	out.MeasuredRuns = len(durations)
	var total int64
	for _, elapsed := range durations {
		total += elapsed
	}
	if len(durations) > 0 {
		out.AverageExecutionDurationMs = total / int64(len(durations))
		out.P95ExecutionDurationMs = percentile(durations, 95)
	}
}

package repo

import (
	"time"

	"agentflow/internal/domain"
)

// mrMergeAnalytics uses the MR clock independently of the Run creation window.
// The primary key (mr_project, mr_iid) counts each MR once, including multiple
// repositories for one requirement. Baseline promotion MRs have no work issue.
func (r *AnalyticsRepo) mrMergeAnalytics(filter domain.AnalyticsFilter, issues map[string]domain.Issue) ([]domain.AnalyticsMRMerge, int) {
	items := []domain.AnalyticsMRMerge{}
	if filter.ProjectIDs != nil && len(filter.ProjectIDs) == 0 {
		return items, 0
	}
	matchingRuns := map[string]bool{}
	if filter.WorkflowID != "" || filter.UserID != "" {
		runFilter := filter
		runFilter.From, runFilter.To = time.Time{}, time.Time{}
		runs, err := r.runs.RunSummariesFor(runFilter, "")
		if err != nil {
			return items, 0
		}
		for _, run := range runs {
			issueID := run.ExecutionContext.IssueID
			if analyticsRunMatchesUser(run, filter.UserID, issues[issueID]) {
				matchingRuns[issueID] = true
			}
		}
	}

	q := r.DB.Where("state = ? AND issue_id <> '' AND issue_id <> ?", "merged", domain.BaselinePromoteIssueID)
	if !filter.From.IsZero() {
		q = q.Where("merged_at IS NULL OR merged_at >= ?", filter.From.UTC())
	}
	if !filter.To.IsZero() {
		q = q.Where("merged_at IS NULL OR merged_at <= ?", filter.To.UTC())
	}
	var rows []mrDeliveryStateRow
	if err := q.Order("merged_at DESC, mr_project ASC, mr_iid ASC").Find(&rows).Error; err != nil {
		return items, 0
	}
	missingTime := 0
	for _, row := range rows {
		issue, ok := issues[row.IssueID]
		if !ok || !filter.MatchesProject(issue.ProjectID) ||
			!analyticsIssueMatchesSource(issue, filter.IssueSource) ||
			(filter.RequirementsOnly && !analyticsIsRequirementIssue(issue)) {
			continue
		}
		if filter.WorkflowID != "" || filter.UserID != "" {
			if !matchingRuns[issue.ID] && !(filter.WorkflowID == "" && analyticsRunMatchesUser(domain.Run{}, filter.UserID, issue)) {
				continue
			}
		}
		if row.MergedAt == nil || row.MergedAt.IsZero() {
			missingTime++
			continue
		}
		url := row.MRURL
		if url == "" {
			url = domain.GongfengMRURL(row.MRProject, row.MRIID)
		}
		items = append(items, domain.AnalyticsMRMerge{
			MRProject: row.MRProject, MRIID: row.MRIID, MRURL: url,
			TargetBranch: row.TargetBranch, IssueID: issue.ID, Title: issue.Title,
			MergedAt: row.MergedAt.UTC(),
		})
	}
	return items, missingTime
}

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
	runIssues, err := r.mrMergeRunIssues(filter, issues)
	if err != nil {
		return items, 0
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
		if !ok || !mrMergeIssueMatches(filter, issue, runIssues) {
			continue
		}
		if row.MergedAt == nil || row.MergedAt.IsZero() {
			missingTime++
			continue
		}
		items = append(items, newAnalyticsMRMerge(row, issue))
	}
	return items, missingTime
}

// mrMergeRunIssues returns the issue IDs whose Runs match the workflow/user
// filter. Runs are read without the time window because an MR may merge long
// after its Run was created. Without a workflow/user filter it returns an empty
// set and performs no query.
func (r *AnalyticsRepo) mrMergeRunIssues(filter domain.AnalyticsFilter, issues map[string]domain.Issue) (map[string]bool, error) {
	matchingRuns := map[string]bool{}
	if filter.WorkflowID == "" && filter.UserID == "" {
		return matchingRuns, nil
	}
	runFilter := filter
	runFilter.From, runFilter.To = time.Time{}, time.Time{}
	runs, err := r.runs.RunSummariesFor(runFilter, "")
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		issueID := run.ExecutionContext.IssueID
		if analyticsRunMatchesUser(run, filter.UserID, issues[issueID]) {
			matchingRuns[issueID] = true
		}
	}
	return matchingRuns, nil
}

// mrMergeIssueMatches reports whether a merged MR's issue passes the project,
// source, requirement-only and workflow/user filters. Under a user-only filter
// an issue without matching Runs still counts when the user is its assignee.
func mrMergeIssueMatches(filter domain.AnalyticsFilter, issue domain.Issue, runIssues map[string]bool) bool {
	if !filter.MatchesProject(issue.ProjectID) ||
		!analyticsIssueMatchesSource(issue, filter.IssueSource) ||
		(filter.RequirementsOnly && !analyticsIsRequirementIssue(issue)) {
		return false
	}
	if filter.WorkflowID != "" || filter.UserID != "" {
		if !runIssues[issue.ID] && !(filter.WorkflowID == "" && analyticsRunMatchesUser(domain.Run{}, filter.UserID, issue)) {
			return false
		}
	}
	return true
}

// newAnalyticsMRMerge builds the merge record for a row with a known merge
// time, falling back to the Gongfeng MR URL when none was stored.
func newAnalyticsMRMerge(row mrDeliveryStateRow, issue domain.Issue) domain.AnalyticsMRMerge {
	url := row.MRURL
	if url == "" {
		url = domain.GongfengMRURL(row.MRProject, row.MRIID)
	}
	return domain.AnalyticsMRMerge{
		MRProject: row.MRProject, MRIID: row.MRIID, MRURL: url,
		TargetBranch: row.TargetBranch, IssueID: issue.ID, Title: issue.Title,
		MergedAt: row.MergedAt.UTC(),
	}
}

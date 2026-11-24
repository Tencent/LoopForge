package repo

import (
	"agentflow/internal/domain"
)

// Personal/workflow/window header queries can omit another actor's child SOP.
// Read only descendant identities so root-cohort event counts stay complete,
// without widening the token/cost rows or loading child outputs and logs.
func (r *AnalyticsRepo) analyticsDashboardSignalRuns(runs []domain.Run, filter domain.AnalyticsFilter, issues map[string]domain.Issue) ([]domain.Run, error) {
	if filter.UserID == "" && filter.WorkflowID == "" && filter.From.IsZero() && filter.To.IsZero() {
		return runs, nil
	}
	known, visited := map[string]bool{}, map[string]bool{}
	var frontier []string
	for _, run := range runs {
		known[run.ID] = true
		if analyticsSOPCohortRun(run, filter, issues[run.ExecutionContext.IssueID]) {
			frontier = append(frontier, run.ID)
			visited[run.ID] = true
		}
	}
	out := append([]domain.Run(nil), runs...)
	snapshot := "json_object('collaborationMode', json_extract(snapshot, '$.collaborationMode')) AS snapshot"
	if r.isMySQL() {
		snapshot = "JSON_OBJECT('collaborationMode', JSON_UNQUOTE(JSON_EXTRACT(snapshot, '$.collaborationMode'))) AS snapshot"
	}
	for len(frontier) > 0 {
		var next []string
		for start := 0; start < len(frontier); start += 500 {
			var rows []runRow
			if err := r.DB.Select("id", "kind", "workflow_id", "execution_context", "created_at", snapshot).
				Where(r.sqlExecutionContextJSONText("parentRunId")+" IN ?", frontier[start:min(start+500, len(frontier))]).Find(&rows).Error; err != nil {
				return nil, err
			}
			for _, row := range rows {
				run := runFromRow(row)
				if visited[run.ID] || !filter.MatchesProject(domain.RunProjectID(run, issues[run.ExecutionContext.IssueID].ProjectID)) {
					continue
				}
				visited[run.ID] = true
				next = append(next, run.ID)
				if !known[run.ID] {
					out = append(out, run)
					known[run.ID] = true
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func analyticsSOPCohortRun(run domain.Run, filter domain.AnalyticsFilter, issue domain.Issue) bool {
	return !run.IsWorkspace() && !run.HistoricalGroup() && run.ExecutionContext.ParentRunID == "" &&
		(filter.WorkflowID == "" || run.WorkflowID == filter.WorkflowID) && filter.MatchesProject(domain.RunProjectID(run, issue.ProjectID)) &&
		analyticsRunInWindow(run, filter) && analyticsRunMatchesUser(run, filter.UserID, issue) && analyticsIssueMatchesSource(issue, filter.IssueSource) &&
		(!filter.RequirementsOnly || analyticsIsRequirementIssue(issue))
}

// Child decisions/tool outcomes belong to the selected root SOP even when a
// child has another responsible actor. Their token/cost rows remain separately
// scoped by the existing execution owner filter.
func analyticsDashboardSignalIDs(runs []domain.Run, filter domain.AnalyticsFilter, issues map[string]domain.Issue, ids []string) []string {
	byID, eligible, included := map[string]domain.Run{}, map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		included[id] = true
	}
	for _, run := range runs {
		byID[run.ID] = run
		eligible[run.ID] = analyticsSOPCohortRun(run, filter, issues[run.ExecutionContext.IssueID])
	}
	for _, run := range runs {
		if included[run.ID] || run.IsWorkspace() || run.HistoricalGroup() {
			continue
		}
		root, seen := run, map[string]bool{}
		for root.ExecutionContext.ParentRunID != "" && !seen[root.ID] {
			seen[root.ID] = true
			parent, ok := byID[root.ExecutionContext.ParentRunID]
			if !ok {
				break
			}
			root = parent
		}
		if eligible[root.ID] {
			ids = append(ids, run.ID)
			included[run.ID] = true
		}
	}
	return ids
}

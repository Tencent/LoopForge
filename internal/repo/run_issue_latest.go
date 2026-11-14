package repo

import "agentflow/internal/domain"

// LatestIssueSOPRun returns the newest top-level SOP run of an issue; role
// group records and conversation workspaces are not SOP runs.
func (r *RunsRepo) LatestIssueSOPRun(issueID string) (domain.Run, bool) {
	expr := r.sqlExecutionContextJSONText("issueId")
	ids := []string{}
	if err := r.DB.Model(&runRow{}).
		Where(expr+" = ? AND (kind IS NULL OR kind <> ?)", issueID, domain.RunKindWorkspace).
		Order("created_at DESC, id DESC").Limit(20).Pluck("id", &ids).Error; err != nil {
		return domain.Run{}, false
	}
	for _, id := range ids {
		run, ok := r.RunState(id)
		if ok && run.ExecutionContext.IssueID == issueID && run.ExecutionContext.ParentRunID == "" && !run.IsWorkspace() && !run.HistoricalGroup() {
			return run, true
		}
	}
	return domain.Run{}, false
}

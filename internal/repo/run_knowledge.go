package repo

import (
	"strings"

	"agentflow/internal/domain"
)

// KnowledgeArtifacts returns the newest run artifacts named knowledge.md
// (metadata only) with the owning run's project/issue/workflow. An empty
// projectID lists every project.
func (r *RunsRepo) KnowledgeArtifacts(projectID string, limit int) []domain.KnowledgeCandidate {
	out := make([]domain.KnowledgeCandidate, 0)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	base := domain.KnowledgeArtifactBasename
	var rows []runArtifactRow
	// Over-fetch so project filtering below still fills the page.
	fetch := limit
	if strings.TrimSpace(projectID) != "" {
		fetch = limit * 4
	}
	if err := r.DB.Select("id", "run_id", "node_id", "attempt", "name", "path", "media_type", "size_bytes", "sha256", "repository_dir", "created_at").
		Where("name = ? OR path = ? OR path LIKE ?", base, base, "%/"+base).
		Order("created_at DESC").Limit(fetch).Find(&rows).Error; err != nil || len(rows) == 0 {
		return out
	}
	runIDs := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if !seen[row.RunID] {
			seen[row.RunID] = true
			runIDs = append(runIDs, row.RunID)
		}
	}
	var runs []runRow
	if err := r.DB.Select("id", "kind", "workflow_id", "status", "execution_context").Where("id IN ?", runIDs).Find(&runs).Error; err != nil {
		return out
	}
	meta := make(map[string]domain.Run, len(runs))
	for _, row := range runs {
		run := runFromRow(row)
		if !run.IsPersonalWorkspace() {
			meta[run.ID] = run
		}
	}
	// Retries emit one artifact per attempt; only the latest per run/node/path
	// is a meaningful candidate.
	latest := map[string]bool{}
	for _, row := range rows {
		m, ok := meta[row.RunID]
		if !ok {
			continue
		}
		if projectID != "" && m.ExecutionContext.ProjectID != projectID {
			continue
		}
		key := row.RunID + "\x00" + row.NodeID + "\x00" + row.Path
		if latest[key] {
			continue
		}
		latest[key] = true
		out = append(out, domain.KnowledgeCandidate{
			Artifact:  artifactFromRow(row, false),
			ProjectID: m.ExecutionContext.ProjectID, IssueID: m.ExecutionContext.IssueID,
			WorkflowID: m.WorkflowID, RunStatus: m.Status,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

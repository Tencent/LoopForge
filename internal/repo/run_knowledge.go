package repo

import (
	"encoding/json"
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
	query := r.DB.Table("run_artifacts").
		Select("run_artifacts.id, run_id, node_id, attempt, name, path, media_type, size_bytes, sha256, repository_dir, run_artifacts.created_at, runs.workflow_id, runs.status, runs.execution_context").
		Joins("JOIN runs ON runs.id = run_artifacts.run_id").
		Where("name = ? OR path = ? OR path LIKE ?", base, base, "%/"+base).
		Order("run_artifacts.created_at DESC").Order("run_artifacts.id DESC")
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		query = query.Where(r.sqlExecutionContextJSONText("projectId")+" = ?", projectID)
	}
	type candidateRow struct {
		Artifact         runArtifactRow `gorm:"embedded"`
		WorkflowID       string
		Status           string
		ExecutionContext []byte
	}
	// Retries emit one artifact per attempt. Continue scanning bounded batches
	// until the requested number of distinct run/node/path candidates is found.
	// Project filtering must happen before the database limit.
	latest := map[string]bool{}
	batchSize := max(limit, 100)
	for offset := 0; ; offset += batchSize {
		var rows []candidateRow
		if err := query.Offset(offset).Limit(batchSize).Find(&rows).Error; err != nil {
			return out
		}
		for _, candidate := range rows {
			row := candidate.Artifact
			var ctx domain.TelemetryContext
			if json.Unmarshal(candidate.ExecutionContext, &ctx) != nil {
				continue
			}
			if projectID != "" && ctx.ProjectID != projectID {
				continue
			}
			key := row.RunID + "\x00" + row.NodeID + "\x00" + row.Path
			if latest[key] {
				continue
			}
			latest[key] = true
			out = append(out, domain.KnowledgeCandidate{
				Artifact:  artifactFromRow(row, false),
				ProjectID: ctx.ProjectID, IssueID: ctx.IssueID,
				WorkflowID: candidate.WorkflowID, RunStatus: candidate.Status,
			})
			if len(out) >= limit {
				return out
			}
		}
		if len(rows) < batchSize {
			break
		}
	}
	return out
}

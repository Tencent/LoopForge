package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
)

type insightAnalyticsExecution struct {
	execution domain.InsightExecution
	sourceID  string
}

// analyticsInsightRuns projects external facts only in memory so existing
// aggregation uses one arithmetic path. These values are never persisted as
// Runs or handed to the engine, delivery, leases, or recovery.
func (r *AnalyticsRepo) analyticsInsightRuns(filter domain.AnalyticsFilter) ([]domain.Run, map[string]insightAnalyticsExecution, error) {
	metadata := map[string]insightAnalyticsExecution{}
	if filter.Origin == "internal" || filter.RequirementsOnly || filter.IssueSource != "" {
		return nil, metadata, nil
	}
	rows, err := r.insights.insightExecutions(filter)
	if err != nil {
		return nil, nil, err
	}
	runs := []domain.Run{}
	for _, row := range rows {
		var v domain.InsightExecution
		if err := json.Unmarshal(row.Payload, &v); err != nil {
			return nil, nil, err
		}
		workflowID := "external:" + row.SourceID + ":" + v.Workflow.ID
		if filter.WorkflowID != "" && filter.WorkflowID != workflowID {
			continue
		}
		run := domain.Run{ID: "external:" + row.ID, WorkflowID: workflowID, WorkflowSnapshot: domain.WorkflowDefinition{ID: workflowID, Name: v.Workflow.Name}, Status: v.Status, CreatedAt: v.StartedAt, UpdatedAt: v.FinishedAt, ExecutionContext: domain.TelemetryContext{ProjectID: row.ProjectID, ResponsibleUserID: row.UserID}, Usage: domain.TokenUsage{CostStatus: "reported"}}
		for _, u := range v.Usage {
			priced := analyticsInsightPricedUsage(u)
			run.Usage.InputTokens += u.InputTokens
			run.Usage.OutputTokens += u.OutputTokens
			run.Usage.CacheReadTokens += u.CacheReadTokens
			run.Usage.CacheWriteTokens += u.CacheWriteTokens
			run.Usage.ReasoningTokens += u.ReasoningTokens
			run.Usage.CostUSD += priced.CostUSD
			if priced.CostStatus == "unknown" {
				run.Usage.CostStatus = "unknown"
			} else if priced.CostStatus == "estimated" && run.Usage.CostStatus != "unknown" {
				run.Usage.CostStatus = "estimated"
			}
		}
		run.Usage.TotalTokens = run.Usage.InputTokens + run.Usage.OutputTokens
		metadata[run.ID] = insightAnalyticsExecution{execution: v, sourceID: row.SourceID}
		runs = append(runs, run)
	}
	return runs, metadata, nil
}

func accumulateInsightModels(models map[string]*analyticsAccumulator, v domain.InsightExecution) {
	for _, u := range v.Usage {
		usage := analyticsInsightPricedUsage(u)
		// A workflow's terminal status does not establish individual model success.
		accumulateNode(models, u.Model, u.Model, &domain.NodeRun{Model: u.Model, Usage: usage}, 0, false)
		models[u.Model].item.QualityUnknownRuns++
	}
}

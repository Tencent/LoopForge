package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// HasWorkspaceSOPRuns reports whether another run relies on this workspace.
// Status filters let callers distinguish executing runs from saved history.
func (r *RunsRepo) HasWorkspaceSOPRuns(runID string, statuses ...string) (bool, error) {
	return workspaceSOPRunExists(r.workspaceSOPRuns(r.DB, runID, statuses))
}

func (r *RunsRepo) workspaceSOPRuns(tx *gorm.DB, runID string, statuses []string) *gorm.DB {
	query := tx.Model(&runRow{}).Where(r.sqlExecutionContextJSONText("workspaceRunId")+" = ?", runID)
	if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	}
	return query
}

func workspaceSOPRunExists(query *gorm.DB) (bool, error) {
	var run runRow
	err := query.Select("id").Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Lock the checkout owner while admitting a top-level SOP. This serializes
// dispatch and retry with other SOPs, foreground leases and workspace cleanup.
func (r *RunsRepo) checkConversationWorkspaceForSOP(tx *gorm.DB, run domain.Run, newRun bool) error {
	id := run.ExecutionContext.WorkspaceRunID
	row, err := r.takeRunForUpdate(tx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("对话工作区已删除")
	}
	if err != nil {
		return err
	}
	owner := runFromRow(row)
	if !owner.IsWorkspace() || owner.ExecutionContext.ProjectID == "" || owner.ExecutionContext.ProjectID != run.ExecutionContext.ProjectID {
		return fmt.Errorf("工作区不属于当前项目的对话")
	}
	if newRun && (domain.TerminalRunStatus(owner.Status) || owner.CancelRequested) {
		return fmt.Errorf("对话工作区已结束")
	}
	busy, err := workspaceSOPRunExists(r.workspaceSOPRuns(tx, id, []string{"running", "queued", "waiting"}).
		Where("id <> ? AND COALESCE(quota_parent_run_id, '') = ''", run.ID))
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("对话工作区已有未结束的 SOP：%w", ErrWorkspaceInUse)
	}
	return nil
}

// updateConversationWorkspaceRun rewrites only the named JSON columns of a
// conversation workspace run under its row lock, so a concurrent conversation
// turn never loses an increment. It does nothing for any other kind of run.
func (r *RunsRepo) updateConversationWorkspaceRun(runID string, mutate func(row *runRow) (map[string]any, bool)) (bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, nil
	}
	changed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row runRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "kind", "execution_context", "usage_json").Where("id = ?", runID).Take(&row).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if row.Kind != domain.RunKindWorkspace {
			return nil
		}
		values, ok := mutate(&row)
		if !ok {
			return nil
		}
		values["revision"] = gorm.Expr("revision + 1")
		values["updated_at"] = time.Now().UTC()
		changed = true
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(values).Error
	})
	return changed, err
}

// keepConversationRunFields stops a full-row write made from an older copy of
// a conversation workspace from undoing AddConversationRunUsage or
// LinkConversationRunIssue: conversation usage only grows and the linked
// requirement is never cleared.
func keepConversationRunFields(run *domain.Run, current runRow) {
	var stored domain.TokenUsage
	if len(current.UsageJSON) > 0 && json.Unmarshal(current.UsageJSON, &stored) == nil && (stored.TotalTokens > run.Usage.TotalTokens || stored.InputTokens > run.Usage.InputTokens || stored.OutputTokens > run.Usage.OutputTokens || stored.CacheReadTokens > run.Usage.CacheReadTokens || stored.CacheWriteTokens > run.Usage.CacheWriteTokens || stored.ReasoningTokens > run.Usage.ReasoningTokens || stored.CostUSD > run.Usage.CostUSD) {
		run.Usage = stored
	}
	if strings.TrimSpace(run.ExecutionContext.IssueID) != "" {
		return
	}
	var ctx domain.TelemetryContext
	if len(current.ExecutionContext) > 0 && json.Unmarshal(current.ExecutionContext, &ctx) == nil {
		run.ExecutionContext.IssueID = ctx.IssueID
	}
}

// AddConversationRunUsage adds the model usage of one conversation turn,
// priced with the turn's model, to the conversation workspace it worked in,
// so analytics attributes the conversation's tokens and cost to that
// workspace's project, user and requirement.
func (r *RunsRepo) AddConversationRunUsage(runID, model string, usage domain.TokenUsage) error {
	if usage.TotalTokens == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 && usage.ReasoningTokens == 0 && usage.CostUSD == 0 {
		return nil
	}
	usage = domain.PriceScheduleExecutionUsage(model, usage)
	_, err := r.updateConversationWorkspaceRun(runID, func(row *runRow) (map[string]any, bool) {
		var total domain.TokenUsage
		if len(row.UsageJSON) > 0 {
			_ = json.Unmarshal(row.UsageJSON, &total)
		}
		first := total.TotalTokens == 0 && total.InputTokens == 0 && total.OutputTokens == 0 && total.CacheReadTokens == 0 && total.CacheWriteTokens == 0 && total.ReasoningTokens == 0 && total.CostUSD == 0
		if first {
			total.CostPricingVersion = usage.CostPricingVersion
		} else if total.CostPricingVersion != usage.CostPricingVersion {
			total.CostPricingVersion = ""
		}
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.CacheReadTokens += usage.CacheReadTokens
		total.CacheWriteTokens += usage.CacheWriteTokens
		total.ReasoningTokens += usage.ReasoningTokens
		total.TotalTokens += usage.TotalTokens
		total.CostUSD += usage.CostUSD
		// The total is only as known as its least known turn.
		switch {
		case first || usage.CostStatus == "unknown" || total.CostStatus == "":
			total.CostStatus = usage.CostStatus
		case usage.CostStatus == "estimated" && total.CostStatus == "reported":
			total.CostStatus = "estimated"
		}
		return map[string]any{"usage_json": jsonBytes(total)}, true
	})
	return err
}

// LinkConversationRunIssue records the requirement a conversation workspace
// works on. A workspace already linked to another requirement keeps it; it
// reports whether the workspace now belongs to issueID.
func (r *RunsRepo) LinkConversationRunIssue(runID, issueID string) (bool, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return false, nil
	}
	linked := false
	_, err := r.updateConversationWorkspaceRun(runID, func(row *runRow) (map[string]any, bool) {
		var ctx domain.TelemetryContext
		if len(row.ExecutionContext) > 0 {
			_ = json.Unmarshal(row.ExecutionContext, &ctx)
		}
		current := strings.TrimSpace(ctx.IssueID)
		linked = current == "" || current == issueID
		if current != "" {
			return nil, false
		}
		ctx.IssueID = issueID
		return map[string]any{"execution_context": jsonBytes(ctx)}, true
	})
	return linked, err
}

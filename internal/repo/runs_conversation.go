package repo

import (
	"encoding/json"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
	if len(current.UsageJSON) > 0 && json.Unmarshal(current.UsageJSON, &stored) == nil && stored.TotalTokens > run.Usage.TotalTokens {
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
	if usage.TotalTokens == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CostUSD == 0 {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(model))
	usage = domain.AnalyticsPricedUsage(model, usage, strings.HasPrefix(lower, "gpt-"))
	_, err := r.updateConversationWorkspaceRun(runID, func(row *runRow) (map[string]any, bool) {
		var total domain.TokenUsage
		if len(row.UsageJSON) > 0 {
			_ = json.Unmarshal(row.UsageJSON, &total)
		}
		first := total.TotalTokens == 0 && total.InputTokens == 0 && total.OutputTokens == 0 && total.CostUSD == 0
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

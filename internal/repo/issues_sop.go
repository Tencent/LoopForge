package repo

import (
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// restoreSOPIssue is called with the issue mutex held and the run transaction.
// Reopened delivery metadata deliberately retains old MR links; MR reconciliation
// already ignores reopened issues until their next SOP delivery.
func (r *IssuesRepo) restoreSOPIssue(tx *gorm.DB, run domain.Run, complete bool) error {
	id := strings.TrimSpace(run.ExecutionContext.IssueID)
	if id == "" {
		return nil
	}
	if run.ExecutionContext.ParentRunID != "" {
		return fmt.Errorf("子 SOP 不能切换需求交付状态")
	}
	var row issueRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
		return err
	}
	base := *r.deps
	base.DB = tx
	tasks := &TasksRepo{deps: &base}
	scoped := &IssuesRepo{deps: &base, projects: &ProjectsRepo{deps: &base}, users: r.users, tasks: tasks}
	issue, ok := scoped.Issue(id)
	if !ok || issue.ProjectID != run.ExecutionContext.ProjectID {
		return fmt.Errorf("运行与需求项目不匹配")
	}
	if !complete && (issue.ManuallyClosed() || issue.TrackerClosed() || (issue.Status == "closed" && issue.CloseSource == "external")) {
		return fmt.Errorf("%s", issue.ExecutionBlockedReason())
	}
	taskStatus, action, detail := "running", "reopen", "自由协作切回 SOP，从所选节点重新开始，需求恢复进行中"
	if complete {
		taskStatus, action, detail = "completed", "complete", "自由协作切回 SOP，直接标记完成，未重新执行节点"
		if issue.Status != "closed" {
			issue.Status = "done"
		}
		issue.DeliveryState = "merged"
		issue.AutomationState = "ignored"
	} else {
		issue.Status = "in_progress"
		issue.DeliveryState = "reopened"
		issue.CloseSource = ""
		issue.AutomationState = "eligible"
	}
	issue.DeliveryUpdatedAt = time.Now().UTC()
	issue.AppendOperation(domain.IssueOperation{Action: action, Detail: detail + "（" + run.ID + "）"})
	for _, task := range tasks.Tasks() {
		if task.RunID == run.ID && task.IssueID == id {
			task.Status = taskStatus
			if err := tasks.PutTask(task); err != nil {
				return err
			}
		}
	}
	return scoped.putIssueLocked(issue)
}

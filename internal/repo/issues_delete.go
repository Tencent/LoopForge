package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
)

var ErrIssueBusy = errors.New("需求正在澄清或执行 SOP，请先停止后再删除")
var ErrIssueSucceeded = errors.New("需求已有成功运行记录，不允许删除")
var ErrIssueNotFound = errors.New("issue not found")

// IssueDeletionRuns checks every linked run before any destructive cleanup.
func (r *IssuesRepo) IssueDeletionRuns(id string) ([]string, error) {
	var ids []string
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		ids, err = r.issueDeletionRuns(tx, id)
		return err
	})
	return ids, err
}

func (r *IssuesRepo) issueDeletionRuns(tx *gorm.DB, id string) ([]string, error) {
	var row issueRow
	if err := r.locking(tx.Where("id = ?", id)).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIssueNotFound
		}
		return nil, err
	}
	var issue domain.Issue
	if err := jsonUnmarshal(row.Payload, &issue); err != nil {
		return nil, err
	}
	if issue.Status == "done" {
		return nil, ErrIssueSucceeded
	}
	if issue.Clarification.Status == "analyzing" {
		return nil, ErrIssueBusy
	}
	var tasks []taskRow
	if err := tx.Where("issue_id = ?", id).Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.Status == "completed" || task.Status == "succeeded" {
			return nil, ErrIssueSucceeded
		}
		if task.Status != "failed" && task.Status != "cancelled" {
			return nil, ErrIssueBusy
		}
	}
	expr := "json_extract(execution_context, '$.issueId')"
	if r.isMySQL() {
		expr = "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.issueId'))"
	}
	// Include legacy runs associated only through their Task's RunID.
	q := tx.Where(expr+" = ?", id).Or("id IN (?)", tx.Table("tasks").Select("run_id").Where("issue_id = ? AND run_id <> ''", id))
	var runs []runRow
	if err := q.Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.Status == "succeeded" {
			return nil, ErrIssueSucceeded
		}
	}
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		if run.Status != "failed" && run.Status != "cancelled" {
			return nil, ErrIssueBusy
		}
		ids = append(ids, run.ID)
	}
	return ids, nil
}

// DeleteIssue removes the issue, comments and failed/cancelled tasks atomically
// after the engine has cleaned every linked run and execution environment.
func (r *IssuesRepo) DeleteIssue(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		ids, err := r.issueDeletionRuns(tx, id)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			return fmt.Errorf("请先清理需求的历史运行记录")
		}
		if err := r.tasks.deleteIssueTasks(tx, id); err != nil {
			return err
		}
		if err := tx.Where("issue_id = ?", id).Delete(&issueCommentRow{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&issueRow{}).Error
	})
}

// IssueDeletionAllowed computes list affordances from complete execution history
// in two bulk queries, including runs omitted from the paginated UI run list.
func (r *IssuesRepo) IssueDeletionAllowed(issues []domain.Issue) (map[string]bool, error) {
	return r.issueActionAllowed(issues, false)
}

// IssueDeletionBlock explains a restriction using the complete execution history.
// RunID is only populated for an existing run, including legacy task-linked runs.
type IssueDeletionBlock struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	RunID  string `json:"runId,omitempty"`
	TaskID string `json:"taskId,omitempty"`
}

func (r *IssuesRepo) IssueDeletionBlocks(issues []domain.Issue) (map[string]IssueDeletionBlock, error) {
	return r.issueActionBlocks(issues, false)
}

// IssueClosureAllowed permits settled execution history, but no active work.
func (r *IssuesRepo) IssueClosureAllowed(issues []domain.Issue) (map[string]bool, error) {
	return r.issueActionAllowed(issues, true)
}

func (r *IssuesRepo) issueActionAllowed(issues []domain.Issue, allowSuccess bool) (map[string]bool, error) {
	blocks, err := r.issueActionBlocks(issues, allowSuccess)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, issue := range issues {
		_, blocked := blocks[issue.ID]
		allowed[issue.ID] = !blocked
	}
	return allowed, nil
}

func (r *IssuesRepo) issueActionBlocks(issues []domain.Issue, allowSuccess bool) (map[string]IssueDeletionBlock, error) {
	blocks := map[string]IssueDeletionBlock{}
	// Prefer actionable temporary blockers, then historical success, then status.
	priority := map[string]int{"completed": 1, "succeeded": 2, "active": 3, "clarifying": 4}
	block := func(id string, next IssueDeletionBlock) {
		prev := blocks[id]
		if priority[next.Code] > priority[prev.Code] || (next.Code == prev.Code && prev.RunID == "" && next.RunID != "") {
			blocks[id] = next
		}
	}
	ids := make([]string, 0, len(issues))
	issueIDs := map[string]bool{}
	for _, issue := range issues {
		ids = append(ids, issue.ID)
		issueIDs[issue.ID] = true
		if !allowSuccess && issue.Status == "done" {
			block(issue.ID, IssueDeletionBlock{Code: "completed", Reason: "需求已完成，当前不支持删除"})
		}
		if issue.Clarification.Status == "analyzing" {
			block(issue.ID, IssueDeletionBlock{Code: "clarifying", Reason: "正在澄清，请先停止澄清后再删除"})
		}
	}
	if len(ids) == 0 {
		return blocks, nil
	}
	var tasks []taskRow
	if err := r.DB.Select("id", "issue_id", "run_id", "status").Where("issue_id IN ?", ids).Find(&tasks).Error; err != nil {
		return nil, err
	}
	runIssues := map[string][]string{}
	runIDs := []string{}
	for _, task := range tasks {
		if task.Status != "failed" && task.Status != "cancelled" && !(allowSuccess && (task.Status == "completed" || task.Status == "succeeded")) {
			restriction := IssueDeletionBlock{Code: "active", Reason: "仍有未结束的任务，请先处理或停止后再删除", TaskID: task.ID}
			if task.Status == "completed" || task.Status == "succeeded" {
				restriction = IssueDeletionBlock{Code: "succeeded", Reason: "已有成功执行记录，当前不支持删除", TaskID: task.ID}
			}
			block(task.IssueID, restriction)
		}
		if task.RunID != "" {
			runIssues[task.RunID] = append(runIssues[task.RunID], task.IssueID)
			runIDs = append(runIDs, task.RunID)
		}
	}
	expr := "json_extract(execution_context, '$.issueId')"
	if r.isMySQL() {
		expr = "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.issueId'))"
	}
	q := r.DB.Select("id", "status", "execution_context").Where(expr+" IN ?", ids)
	if len(runIDs) > 0 {
		q = q.Or("id IN ?", runIDs)
	}
	var runs []runRow
	if err := q.Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.Status == "failed" || run.Status == "cancelled" || (allowSuccess && run.Status == "succeeded") {
			continue
		}
		restriction := IssueDeletionBlock{Code: "active", Reason: "仍有未结束的运行，请先处理或停止后再删除", RunID: run.ID}
		if run.Status == "succeeded" {
			restriction = IssueDeletionBlock{Code: "succeeded", Reason: "已有成功执行记录，当前不支持删除", RunID: run.ID}
		}
		var ctx domain.TelemetryContext
		if len(run.ExecutionContext) > 0 {
			if err := jsonUnmarshal(run.ExecutionContext, &ctx); err != nil {
				return nil, err
			}
		}
		if issueIDs[ctx.IssueID] {
			block(ctx.IssueID, restriction)
		}
		for _, id := range runIssues[run.ID] {
			block(id, restriction)
		}
	}
	return blocks, nil
}

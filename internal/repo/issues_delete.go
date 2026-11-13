package repo

import (
	"errors"
	"fmt"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

var ErrIssueBusy = errors.New("需求正在澄清或执行 SOP，请先停止后再删除")
var ErrIssueSucceeded = errors.New("需求已有成功运行记录，不允许删除")
var ErrIssueForceRequired = errors.New("需求已有执行记录或已完成，请确认强制删除")
var ErrIssueNotFound = errors.New("issue not found")

type IssueDeletionMode int

const (
	IssueDeleteNormal IssueDeletionMode = iota
	IssueDeleteForce
	// IssueDeleteUnsuccessful preserves the cleanup policy of internal callers.
	IssueDeleteUnsuccessful
)

// IssueDeletionRuns checks every linked run before any destructive cleanup.
func (r *IssuesRepo) IssueDeletionRuns(id string, mode IssueDeletionMode) ([]string, error) {
	var ids []string
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		ids, err = r.issueDeletionRuns(tx, id, mode)
		return err
	})
	return ids, err
}

func (r *IssuesRepo) issueDeletionRuns(tx *gorm.DB, id string, mode IssueDeletionMode) ([]string, error) {
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
	states, err := r.issueDeletionStates(tx, []domain.Issue{issue})
	if err != nil {
		return nil, err
	}
	state := states[issue.ID]
	if state.Block != nil {
		return nil, ErrIssueBusy
	}
	if mode == IssueDeleteNormal && state.RequiresForce() {
		return nil, ErrIssueForceRequired
	}
	if mode == IssueDeleteUnsuccessful && state.successful {
		return nil, ErrIssueSucceeded
	}
	return state.runIDs, nil
}

// DeleteIssue keeps the conservative policy used by sync and conversation cleanup.
func (r *IssuesRepo) DeleteIssue(id string) error {
	return r.DeleteIssueAfterRunCleanup(id, IssueDeleteUnsuccessful)
}

// DeleteIssueAfterRunCleanup rechecks the deletion conditions and removes the
// issue, comments and tasks atomically after the engine has cleaned all runs.
func (r *IssuesRepo) DeleteIssueAfterRunCleanup(id string, mode IssueDeletionMode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		ids, err := r.issueDeletionRuns(tx, id, mode)
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

// IssueDeletionBlock explains active work that prevents deleting a requirement.
// RunID is only populated for an existing run, including legacy task-linked runs.
type IssueDeletionBlock struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	RunID  string `json:"runId,omitempty"`
	TaskID string `json:"taskId,omitempty"`
}

type IssueDeletionState struct {
	Block      *IssueDeletionBlock
	hasHistory bool
	successful bool
	runIDs     []string
}

func (s *IssueDeletionState) RequiresForce() bool {
	return s.Block == nil && (s.hasHistory || s.successful)
}

// IssueDeletionStates checks complete execution history in two bulk queries.
func (r *IssuesRepo) IssueDeletionStates(issues []domain.Issue) (map[string]*IssueDeletionState, error) {
	return r.issueDeletionStates(r.DB, issues)
}

func (r *IssuesRepo) issueDeletionStates(db *gorm.DB, issues []domain.Issue) (map[string]*IssueDeletionState, error) {
	states := make(map[string]*IssueDeletionState, len(issues))
	if len(issues) == 0 {
		return states, nil
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
		state := &IssueDeletionState{successful: issue.Status == "done"}
		if issue.Clarification.Status == "analyzing" {
			state.Block = &IssueDeletionBlock{Code: "clarifying", Reason: "正在澄清，请先停止澄清后再删除"}
		}
		states[issue.ID] = state
	}
	var tasks []taskRow
	if err := db.Select("id", "issue_id", "run_id", "status").Where("issue_id IN ?", ids).Find(&tasks).Error; err != nil {
		return nil, err
	}
	// Legacy runs can be linked solely by their Task's RunID.
	runIssues := map[string]map[string]bool{}
	var runIDs []string
	for _, task := range tasks {
		state := states[task.IssueID]
		if state == nil {
			return nil, fmt.Errorf("执行任务 %s 的需求编号与记录不一致", task.ID)
		}
		state.addTask(task.ID, task.Status)
		if task.RunID != "" {
			if runIssues[task.RunID] == nil {
				runIssues[task.RunID] = map[string]bool{}
				runIDs = append(runIDs, task.RunID)
			}
			runIssues[task.RunID][task.IssueID] = true
		}
	}

	expr := r.sqlExecutionContextJSONText("issueId")
	q := db.Table("runs").Select("id, status, "+expr+" AS issue_id, "+
		"EXISTS (SELECT 1 FROM run_nodes WHERE run_id = runs.id AND status IN ('running', 'pending_input')) AS active_nodes").
		Where(expr+" IN ?", ids)
	if len(runIDs) > 0 {
		q = q.Or("id IN ?", runIDs)
	}
	var runs []issueDeletionRun
	if err := q.Find(&runs).Error; err != nil {
		return nil, err
	}
	for _, run := range runs {
		linked := runIssues[run.ID]
		for id := range linked {
			states[id].addRun(run)
		}
		if state := states[run.IssueID]; state != nil && !linked[run.IssueID] {
			state.addRun(run)
		}
	}
	return states, nil
}

// issueDeletionRun is a run linked to an issue, either through its execution
// context or through a legacy task RunID.
type issueDeletionRun struct {
	ID, Status, IssueID string
	ActiveNodes         bool
}

// addTask records one linked task: any task is history, a completed task
// counts as success, and an unfinished task blocks deletion.
func (s *IssueDeletionState) addTask(taskID, status string) {
	s.hasHistory = true
	switch status {
	case "completed", "succeeded":
		s.successful = true
	case "failed", "cancelled":
	default:
		if s.Block == nil {
			s.Block = &IssueDeletionBlock{Code: "active", Reason: "仍有未结束的任务，请先处理或停止后再删除", TaskID: taskID}
		}
	}
}

// addRun records one linked run and blocks deletion while it is still active.
func (s *IssueDeletionState) addRun(run issueDeletionRun) {
	s.hasHistory = true
	s.successful = s.successful || run.Status == "succeeded"
	s.runIDs = append(s.runIDs, run.ID)
	if domain.TerminalRunStatus(run.Status) && !run.ActiveNodes {
		return
	}
	// Prefer linking to the active run when the earlier reason was a task.
	if s.Block == nil || (s.Block.Code == "active" && s.Block.RunID == "") {
		s.Block = &IssueDeletionBlock{Code: "active", Reason: "仍有未结束的运行，请先处理或停止后再删除", RunID: run.ID}
	}
}

// IssueClosureAllowed permits settled execution history, but no active work.
func (r *IssuesRepo) IssueClosureAllowed(issues []domain.Issue) (map[string]bool, error) {
	states, err := r.IssueDeletionStates(issues)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(states))
	for id, state := range states {
		allowed[id] = state.Block == nil
	}
	return allowed, nil
}

package httpapi

import (
	"net/http"
	"strings"
	stdsync "sync"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// tasksHandlers 持有任务列表/详情与跨领域的任务状态同步节流状态。
// issues 领域经注入的指针触发 syncTasks。
type tasksHandlers struct {
	*deps
	syncTasksMu stdsync.Mutex
	syncTasksAt time.Time
}

func (h *tasksHandlers) tasks(c *gin.Context) {
	h.syncTasks()
	tasks := h.repo.Tasks()
	for i := range tasks {
		tasks[i] = publicTask(tasks[i])
	}
	c.JSON(200, tasks)
}

func (h *tasksHandlers) task(c *gin.Context) {
	h.syncTasks()
	task, ok := h.repo.Task(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, publicTask(task))
}

func publicTask(task domain.Task) domain.Task {
	task.Repository = domain.PublicRepositoryContext(task.Repository)
	return task
}

// settleIssueTasks moves one issue's Tasks onto runs that already finished.
// The periodic sync is throttled, so a close right after a run settles would
// otherwise still read the Task as running. Issue status is left to syncTasks.
func (h *tasksHandlers) settleIssueTasks(issueID string) {
	for _, task := range h.repo.Tasks() {
		if task.IssueID != issueID || strings.TrimSpace(task.RunID) == "" {
			continue
		}
		run, ok := h.repo.RunView(task.RunID)
		if !ok || run.HasActiveNode() {
			continue
		}
		status := taskStatusFromRun(run.Status)
		if (status != "completed" && status != "failed" && status != "cancelled") || status == task.Status {
			continue
		}
		_ = h.repo.UpdateTaskStatus(task, status)
	}
}

// syncTasks 由 issues/tasks 两个领域共同触发，2 秒内只跑一轮。
func (h *tasksHandlers) syncTasks() {
	h.syncTasksMu.Lock()
	defer h.syncTasksMu.Unlock()
	if time.Since(h.syncTasksAt) < 2*time.Second {
		return
	}
	h.syncTasksAt = time.Now()

	statuses := h.repo.RunStatuses()
	activeRuns := h.repo.RunsWithActiveNodes()
	tasks := h.repo.Tasks()
	for i, task := range tasks {
		runStatus, ok := statuses[task.RunID]
		if !ok {
			// A settled run may be deleted after cancellation while its task is
			// still marked running.  Leaving that dangling relation untouched
			// makes the issue look permanently active and the SOP pane wait for a
			// run that can never appear.  A non-empty missing RunID is terminal;
			// converge active task states so the issue can be dispatched again.
			if strings.TrimSpace(task.RunID) != "" && (task.Status == "queued" || task.Status == "dispatched" || task.Status == "running") {
				_ = h.repo.UpdateTaskStatus(task, "cancelled")
				tasks[i].Status = "cancelled"
			}
			continue
		}
		status := taskStatusFromRun(runStatus)
		if activeRuns[task.RunID] && (status == "failed" || status == "cancelled") {
			status = "running"
		}
		if status == "" || status == task.Status {
			continue
		}
		_ = h.repo.UpdateTaskStatus(task, status)
		tasks[i].Status = status
	}
	h.syncIssuesToLatestTask(latestTaskByIssue(tasks))
}

// latestTaskByIssue picks each issue's newest task: the later CreatedAt wins,
// and on a tie the larger ID wins. Tasks without an IssueID are skipped.
func latestTaskByIssue(tasks []domain.Task) map[string]domain.Task {
	latest := map[string]domain.Task{}
	for _, task := range tasks {
		if task.IssueID == "" {
			continue
		}
		prev, ok := latest[task.IssueID]
		if !ok || task.CreatedAt.After(prev.CreatedAt) || (task.CreatedAt.Equal(prev.CreatedAt) && task.ID > prev.ID) {
			latest[task.IssueID] = task
		}
	}
	return latest
}

// syncIssuesToLatestTask points every issue at its latest task and derives the
// issue status from it, patching only issues whose linkage or status changed.
func (h *tasksHandlers) syncIssuesToLatestTask(latest map[string]domain.Task) {
	for _, issue := range h.repo.Issues() {
		task, ok := latest[issue.ID]
		if !ok {
			continue
		}
		next := issue
		next.LatestTaskID = task.ID
		next.Status = issueStatusFromLatestTask(issue, task.Status)
		if next.LatestTaskID == issue.LatestTaskID && next.Status == issue.Status {
			continue
		}
		_, _ = h.repo.PatchIssue(issue.ID, func(current *domain.Issue) error {
			current.LatestTaskID = task.ID
			current.Status = issueStatusFromLatestTask(*current, task.Status)
			return nil
		})
	}
}

// rejectBlockedIssueRun 由 tasks/runs 两个领域共同调用（跨领域共享走包级函数）。
func rejectBlockedIssueRun(d *deps, c *gin.Context, runID string) bool {
	run, ok := d.repo.RunView(runID)
	if !ok {
		return false
	}
	issueID := strings.TrimSpace(run.ExecutionContext.IssueID)
	if issueID == "" {
		return false
	}
	issue, found := d.repo.Issue(issueID)
	if !found {
		return false
	}
	reason := issue.ExecutionBlockedReason()
	if reason == "" {
		return false
	}
	c.JSON(409, map[string]string{"error": reason})
	return true
}

func taskStatusFromRun(runStatus string) string {
	return map[string]string{"queued": "queued", "running": "running", "waiting": "running", "succeeded": "completed", "failed": "failed", "cancelled": "cancelled"}[runStatus]
}

func issueStatusFromLatestTask(issue domain.Issue, taskStatus string) string {
	if strings.EqualFold(issue.Status, "closed") {
		return "closed"
	}
	if issue.TrackerClosed() {
		return "closed"
	}
	if issue.DeliveryLocked() {
		return "done"
	}
	switch taskStatus {
	case "running":
		return "in_progress"
	case "completed":
		if issue.Status == "done" {
			return "done"
		}
		return "in_review"
	case "failed":
		return "blocked"
	case "cancelled":
		if issue.Status == "done" {
			return "done"
		}
		return "cancelled"
	default:
		return issue.Status
	}
}

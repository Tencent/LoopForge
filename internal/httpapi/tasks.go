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
	c.JSON(200, h.repo.Tasks())
}

func (h *tasksHandlers) task(c *gin.Context) {
	h.syncTasks()
	task, ok := h.repo.Task(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
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
				task.Status = "cancelled"
				_ = h.repo.PutTask(task)
				tasks[i] = task
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
		task.Status = status
		_ = h.repo.PutTask(task)
		tasks[i] = task
	}
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
		_ = h.repo.PutIssue(next)
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

func runHasActiveNode(run domain.Run) bool {
	for _, nr := range run.Nodes {
		if nr != nil && (nr.Status == "running" || nr.Status == "pending_input") {
			return true
		}
	}
	return false
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

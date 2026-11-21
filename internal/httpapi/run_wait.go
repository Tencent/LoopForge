package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

const runWaitMaxSeconds = 25

type runWaitResult struct {
	RunID     string `json:"runId"`
	ProjectID string `json:"projectId"`
	Status    string `json:"status"`
	Terminal  bool   `json:"terminal"`
	TimedOut  bool   `json:"timedOut"`
}

// waitRun waits on durable state so the result also works when a worker or
// another API replica finishes the run. Only the status column is polled.
func (h *runsHandlers) waitRun(c *gin.Context) {
	ctx := c.Request.Context()
	run, err := h.repo.RunHeader(c.Param("id"))
	if err != nil {
		respond(c, err)
		return
	}
	if run.ID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	projectID := domain.RunProjectID(run, "")
	if projectID == "" && run.ExecutionContext.IssueID != "" {
		if issue, found := h.repo.Issue(run.ExecutionContext.IssueID); found {
			projectID = strings.TrimSpace(issue.ProjectID)
		}
	}
	if projectID == "" {
		projectID, err = h.repo.RunWorkflowProjectID(run.ID)
		if err != nil {
			respond(c, err)
			return
		}
	}
	if _, ok := h.projectRunUser(c, projectID); !ok {
		return
	}
	if run.IsWorkspace() {
		c.JSON(http.StatusConflict, gin.H{"error": "only SOP runs can be waited on"})
		return
	}

	seconds, err := strconv.Atoi(c.DefaultQuery("timeoutSec", strconv.Itoa(runWaitMaxSeconds)))
	if err != nil || seconds < 0 || seconds > runWaitMaxSeconds {
		c.JSON(http.StatusBadRequest, gin.H{"error": "timeoutSec must be an integer between 0 and 25"})
		return
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		status, found, err := h.repo.ReadRunStatus(ctx, run.ID)
		if ctx.Err() != nil {
			c.Status(http.StatusRequestTimeout)
			return
		}
		if err != nil {
			respond(c, err)
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		remaining := time.Until(deadline)
		result := runWaitResult{RunID: run.ID, ProjectID: projectID, Status: status, Terminal: domain.TerminalRunStatus(status)}
		if result.Terminal || status == "waiting" || status == "paused" {
			c.JSON(http.StatusOK, result)
			return
		}
		if remaining <= 0 {
			result.TimedOut = true
			c.JSON(http.StatusOK, result)
			return
		}
		timer := time.NewTimer(min(2*time.Second, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			c.Status(http.StatusRequestTimeout)
			return
		case <-timer.C:
		}
	}
}

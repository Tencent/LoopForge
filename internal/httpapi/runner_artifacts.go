package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *runnersHandlers) artifactTransferTask(c *gin.Context, runnerID string) (domain.RunnerTask, bool) {
	task, ok := h.repo.RunnerTask(c.Param("id"))
	if !ok || task.LeaseOwner != runnerID || task.LeaseUntil == nil || !task.LeaseUntil.After(time.Now().UTC()) {
		c.JSON(http.StatusConflict, gin.H{"error": "task lease lost"})
		return domain.RunnerTask{}, false
	}
	if task.RunID == "" || task.Invocation == nil || !task.Invocation.ArtifactTransfer {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file transfer is not enabled for this task"})
		return domain.RunnerTask{}, false
	}
	return task, true
}

func (h *runnersHandlers) runnerTaskOutputFile(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	task, ok := h.artifactTransferTask(c, runner.ID)
	if !ok {
		return
	}
	path := c.Query("path")
	if !domain.SafeWorkspaceRelPath(path) || !slices.Contains(task.Invocation.OutputFiles, path) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is not an output of this task"})
		return
	}
	if c.Request.ContentLength > domain.MaxSOPArtifactBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": domain.ErrArtifactTooLarge.Error()})
		return
	}
	blob, err := h.repo.SaveArtifactBlob(c.Request.Context(), task.RunID, domain.RunnerOutputBlobID(task.ID, path), c.Request.Body)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrArtifactTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, blob)
}

func (h *runnersHandlers) runnerTaskInputFile(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	task, ok := h.artifactTransferTask(c, runner.ID)
	if !ok {
		return
	}
	path := c.Query("path")
	for _, input := range task.Invocation.Inputs {
		if input.Path != path || input.ArtifactID == "" {
			continue
		}
		if id, ok := strings.CutPrefix(input.ArtifactID, repo.IssueAttachmentInputPrefix); ok {
			h.runnerIssueAttachment(c, task.RunID, input.Path, id)
			return
		}
		art, body, err := h.repo.OpenArtifactContent(c.Request.Context(), task.RunID, input.ArtifactID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read task input artifact: " + err.Error()})
			return
		}
		defer body.Close()
		c.DataFromReader(http.StatusOK, art.Size, "application/octet-stream", body, nil)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "file is not an input of this task"})
}

func (h *runnersHandlers) runnerIssueAttachment(c *gin.Context, runID, path, id string) {
	run, err := h.repo.RunHeader(runID)
	if err != nil {
		respond(c, err)
		return
	}
	issue, ok := h.repo.Issue(run.ExecutionContext.IssueID)
	if !ok || domain.RunProjectID(run, issue.ProjectID) != issue.ProjectID {
		c.JSON(http.StatusNotFound, gin.H{"error": "file is not an input of this task"})
		return
	}
	file, err := h.repo.IssueAttachment(issue.ID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && file.InputPath() != path) {
		c.JSON(http.StatusNotFound, gin.H{"error": "file is not an input of this task"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "application/octet-stream", file.Data)
}

func (h *runnersHandlers) validateRunnerOutputTransfer(c *gin.Context, runnerID string, outputs *domain.RunnerTaskOutputs) bool {
	task, ok := h.artifactTransferTask(c, runnerID)
	if !ok {
		return false
	}
	for path := range outputs.FileErrors {
		if !slices.Contains(task.Invocation.OutputFiles, path) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid output file error path"})
			return false
		}
	}
	for path, claimed := range outputs.FileBlobs {
		id := domain.RunnerOutputBlobID(task.ID, path)
		if !domain.SafeWorkspaceRelPath(path) || !slices.Contains(task.Invocation.OutputFiles, path) || claimed.ID != id {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid uploaded output file reference"})
			return false
		}
		stored, err := h.repo.ArtifactBlob(c.Request.Context(), task.RunID, id)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "verify uploaded output file: " + err.Error()})
			return false
		}
		if err != nil || stored != claimed {
			c.JSON(http.StatusBadRequest, gin.H{"error": "uploaded output file does not match its stored reference"})
			return false
		}
	}
	return true
}

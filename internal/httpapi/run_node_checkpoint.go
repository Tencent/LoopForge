package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

func (h *runsHandlers) nodeCheckpointDiff(c *gin.Context, run domain.Run) {
	if _, ok := h.projectRunUser(c, run.ExecutionContext.ProjectID); !ok {
		return
	}
	if ownerID := engine.WorkspaceRunID(run); ownerID != run.ID {
		owner, found := h.repo.RunState(ownerID)
		if !found {
			c.JSON(http.StatusConflict, gin.H{"error": "工作区所属运行不存在"})
			return
		}
		if _, ok := h.projectRunUser(c, owner.ExecutionContext.ProjectID); !ok {
			return
		}
	}
	nodeID := strings.TrimSpace(c.Query("node"))
	attempt, err := strconv.Atoi(c.Query("attempt"))
	if nodeID == "" || err != nil || attempt < 1 || c.Query("path") != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node and a positive attempt are required"})
		return
	}
	node := run.Nodes[nodeID]
	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}
	var start *domain.NodeCheckpoint
	for i := range node.Checkpoints {
		if node.Checkpoints[i].Attempt == attempt {
			start = &node.Checkpoints[i]
			break
		}
	}
	if start == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "checkpoint not found"})
		return
	}
	var end *domain.NodeCheckpoint
	if start.End != nil {
		end = &domain.NodeCheckpoint{Repositories: start.End}
	} else if node.Attempt != attempt || node.Status != "running" && node.Status != "pending_input" {
		c.JSON(http.StatusConflict, gin.H{"error": "attempt end checkpoint is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if runnerID := h.engine.RunExecutionRunner(run); runnerID != "" || h.engine.WorkspaceRemoteHost(run) {
		var response domain.RunnerWorkspaceResponse
		var err error
		if runnerID != "" {
			if h.runnerControls == nil {
				c.JSON(http.StatusConflict, gin.H{"error": "runner unavailable"})
				return
			}
			response, err = h.runnerControls.requestWorkspace(ctx, runnerID, domain.RunnerWorkspaceRequest{
				RunID: run.ID, WorkspaceRunID: runnerWorkspaceRunID(run), WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout,
				Operation: domain.RunnerWorkspaceOperationNodeCheckpointDiff, NodeCheckpoint: start, NodeCheckpointEnd: end,
			})
		} else {
			response, err = h.engine.DiffNodeCheckpoints(ctx, run, *start, end)
		}
		if err != nil || response.Error != "" {
			message := response.Error
			if err != nil {
				message = err.Error()
			}
			c.JSON(http.StatusConflict, gin.H{"error": message})
			return
		}
		var payload struct {
			Diff      string   `json:"diff"`
			Truncated bool     `json:"truncated"`
			Files     []string `json:"files"`
			Live      bool     `json:"live,omitempty"`
		}
		if err := json.Unmarshal(response.Payload, &payload); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "invalid runner response"})
			return
		}
		if checkpointHasSensitivePath(payload.Files) {
			c.JSON(http.StatusForbidden, gin.H{"error": "checkpoint diff contains a protected path"})
			return
		}
		c.JSON(http.StatusOK, payload)
		return
	}
	root, reason := h.engine.LocalRunWorkspace(run)
	if reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}
	if end == nil {
		diff, truncated, files, err := runtime.DiffNodeWorkspaceCurrent(ctx, root, *start, workspaceFileLimit)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprint(err)})
		} else if checkpointHasSensitivePath(files) {
			c.JSON(http.StatusForbidden, gin.H{"error": "checkpoint diff contains a protected path"})
		} else {
			c.JSON(http.StatusOK, gin.H{"diff": diff, "truncated": truncated, "files": files, "live": true})
		}
		return
	}
	files, err := runtime.NodeWorkspaceCheckpointChangedFiles(ctx, root, *start, *end)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprint(err)})
		return
	}
	if checkpointHasSensitivePath(files) {
		c.JSON(http.StatusForbidden, gin.H{"error": "checkpoint diff contains a protected path"})
		return
	}
	diff, truncated, err := runtime.DiffNodeWorkspaceCheckpoints(ctx, root, *start, *end, workspaceFileLimit)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprint(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"diff": diff, "truncated": truncated, "files": files})
}

func checkpointHasSensitivePath(files []string) bool {
	for _, path := range files {
		if workspacePathSensitive(path) {
			return true
		}
	}
	return false
}

func (h *runsHandlers) workspaceRewind(c *gin.Context) {
	run, actor, ok := h.workspaceCheckpointActor(c)
	if !ok {
		return
	}
	var request struct {
		NodeID       string `json:"nodeId"`
		Attempt      int    `json:"attempt"`
		CheckpointID string `json:"checkpointId,omitempty"`
	}
	if !decode(c, &request) {
		return
	}
	request.NodeID = strings.TrimSpace(request.NodeID)
	if request.NodeID == "" || request.Attempt < 1 && request.CheckpointID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nodeId and attempt or checkpointId are required"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	if err := h.engine.RewindNodeWorkspace(ctx, run.ID, request.NodeID, request.Attempt, request.CheckpointID, actor); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

package httpapi

import (
	"net/http"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// syncHandlers 暴露外部同步（Gongfeng/TAPD）的配置与触发端点。
type syncHandlers struct {
	*deps
}

func (h *syncHandlers) syncConfig(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		c.JSON(200, h.repo.SyncConfig())
		return
	}
	if c.Request.Method == http.MethodPut || c.Request.Method == http.MethodPost {
		var cfg domain.SyncConfig
		if !decode(c, &cfg) {
			return
		}
		if cfg.AutoSyncIntervalSec <= 0 {
			cfg.AutoSyncIntervalSec = 300
		}
		current := h.repo.SyncConfig()
		cfg.LastSyncAt = current.LastSyncAt
		cfg.LastSyncStatus = current.LastSyncStatus
		cfg.LastSyncError = current.LastSyncError
		cfg.PlaybookSeedRev = current.PlaybookSeedRev
		// TAPD workspace/iteration live on projects; sync_config keeps platform MCP + auto-sync only.
		cfg.TAPDWorkspaceID = ""
		cfg.TAPDIterationID = ""
		h.configureSync(cfg)
		h.sync.StartAutoSync(h.repo)
		if err := h.repo.PutSyncConfig(cfg); err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, h.repo.SyncConfig())
		return
	}
}

func (h *syncHandlers) syncNow(c *gin.Context) {
	// Reload MCP auth tokens from ~/.codebuddy/mcp.json in case they refreshed
	cfg := h.repo.SyncConfig()
	h.configureSync(cfg)
	go func() {
		if err := h.sync.PullAll(h.repo); err != nil {
			_ = err // logged in sync service
		}
	}()
	c.JSON(200, map[string]any{"status": "started", "syncing": true})
}

func (h *syncHandlers) syncLogs(c *gin.Context) {
	c.JSON(200, h.sync.Logs())
}

func (h *syncHandlers) syncStatus(c *gin.Context) {
	c.JSON(200, map[string]any{"syncing": h.sync.IsSyncing(), "config": h.repo.SyncConfig(), "logs": h.sync.Logs()})
}

func (h *syncHandlers) syncCreate(c *gin.Context) {
	var req struct {
		IssueID string `json:"issueId"`
		Target  string `json:"target"` // gongfeng, tapd
	}
	if !decode(c, &req) {
		return
	}
	if req.IssueID == "" || req.Target == "" {
		c.JSON(400, map[string]string{"error": "issueId and target are required"})
		return
	}
	issue, ok := h.repo.Issue(req.IssueID)
	if !ok {
		c.JSON(404, map[string]string{"error": "issue not found"})
		return
	}
	if (req.Target == "tapd" || req.Target == "gongfeng") && strings.TrimSpace(issue.TapdID) != "" {
		c.JSON(http.StatusConflict, map[string]string{"error": "该平台需求已手动关联 TAPD ID；如需新建外部单据，请先清空统计关联 ID"})
		return
	}
	if req.Target == "gongfeng" && issue.RepositoryID == "" {
		if project, found := h.repo.Project(issue.ProjectID); found {
			issue.RepositoryID = project.DefaultRepositoryID
		}
	}
	var updated domain.Issue
	var err error
	// Reload auth tokens before pushing
	h.configureSync(h.repo.SyncConfig())
	switch req.Target {
	case "gongfeng":
		updated, err = h.sync.CreateGongfengIssue(h.repo, issue)
	case "tapd":
		updated, err = h.sync.CreateTAPDStory(h.repo, issue)
	default:
		c.JSON(400, map[string]string{"error": "target must be gongfeng or tapd"})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	stored, _ := h.repo.Issue(updated.ID)
	c.JSON(200, stored)
}

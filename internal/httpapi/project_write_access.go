package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// requireProjectWriter limits changes to a project's requirements, roles and
// SOPs to platform administrators, the project owner and non-viewer members.
// Projects without an owner are shared. Requests that passed authentication
// without a user identity (service tokens) are allowed.
func (d deps) requireProjectWriter(c *gin.Context, projectIDs ...string) bool {
	if !authRequired() {
		return true
	}
	user, ok := d.requestUser(c.Request, true)
	if !ok || d.isPlatformAdmin(c.Request) {
		return true
	}
	for _, id := range projectIDs {
		if !d.projectWriterAllowed(c, id, user.ID) {
			c.JSON(403, gin.H{"error": "只有项目成员、项目 Owner 或平台管理员可以修改该项目的内容"})
			return false
		}
	}
	return true
}

// Read access does not grant permission to execute agents in a run workspace.
// Keep the same predicate for HTTP writes and assistant execution preparation.
func (d deps) projectWriterAllowed(c *gin.Context, projectID string, userID int64) bool {
	if !authRequired() || projectID == "" || d.isPlatformAdmin(c.Request) {
		return true
	}
	p, found := d.repo.Project(projectID)
	if !found || p.OwnerID == 0 || p.OwnerID == userID {
		return true
	}
	for _, member := range p.Members {
		if member.UserID == userID && !strings.EqualFold(strings.TrimSpace(member.Role), "viewer") {
			return true
		}
	}
	return false
}

var guardedRunWrites = map[string]bool{
	"/api/runs/:id":                    true,
	"/api/runs/:id/approve":            true,
	"/api/runs/:id/reject":             true,
	"/api/runs/:id/retry":              true,
	"/api/runs/:id/decline-retry":      true,
	"/api/runs/:id/cancel":             true,
	"/api/runs/:id/pause":              true,
	"/api/runs/:id/resume":             true,
	"/api/runs/:id/refresh-sop":        true,
	"/api/runs/:id/pass":               true,
	"/api/runs/:id/eval":               true,
	"/api/runs/:id/eval/apply":         true,
	"/api/runs/:id/workspace/takeover": true,
	"/api/runs/:id/workspace/release":  true,
	"/api/runs/:id/workspace/restore":  true,
	"/api/runs/:id/workspace/file":     true,
	"/api/runs/:id/workspace/terminal": true,
	"/api/runs/:id/preview/backend":    true,
	"/api/runs/:id/preview":            true,
}

// runWriteGuard applies requireProjectWriter to run controls, workspace edits
// and terminals. Worker signals, group chat and preview viewing keep their own
// access rules.
func (d deps) runWriteGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || !guardedRunWrites[c.FullPath()] {
			c.Next()
			return
		}
		run, ok := d.repo.Run(c.Param("id"))
		if !ok {
			c.Next()
			return
		}
		issueProject := ""
		if issue, found := d.repo.Issue(run.ExecutionContext.IssueID); found {
			issueProject = issue.ProjectID
		}
		if !d.requireProjectWriter(c, domain.RunProjectID(run, issueProject)) {
			c.Abort()
			return
		}
		c.Next()
	}
}

func (d deps) requirePlatformAdmin(c *gin.Context) bool {
	if !authRequired() || d.isPlatformAdmin(c.Request) {
		return true
	}
	if _, ok := d.requestUser(c.Request, true); !ok {
		return true
	}
	c.JSON(403, gin.H{"error": "只有平台管理员可以修改平台级配置"})
	return false
}

// assetOwner returns the project of a stored asset and whether it is a
// platform-wide asset that only administrators may change.
func (d deps) assetOwner(kind, id string) (project string, global bool, found bool) {
	if id == "" {
		return "", false, false
	}
	switch kind {
	case "rules":
		v, ok := d.repo.Rule(id)
		return v.ProjectID, v.ProjectID == "", ok
	case "skills":
		v, ok := d.repo.Skill(id)
		return v.ProjectID, v.ProjectID == "", ok
	case "dockerfiles":
		v, ok := d.repo.Dockerfile(id)
		return v.ProjectID, v.ProjectID == "", ok
	case "environments":
		v, ok := d.repo.Environment(id)
		return v.ProjectID, v.ProjectID == "", ok
	case "sandboxes":
		v, ok := d.repo.Sandbox(id)
		return v.ProjectID, v.ProjectID == "", ok
	case "mcp-servers":
		v, ok := d.repo.MCPServer(id)
		return v.ProjectID, v.ProjectID == "" && v.Scope != "personal", ok
	case "schedule-jobs":
		v, ok := d.repo.ScheduleJob(id)
		return v.ProjectID, false, ok
	case "knowledge":
		v, ok := d.repo.KnowledgeSubmission(id)
		return v.ProjectID, false, ok
	}
	return "", false, false
}

// assetWriteGuard applies project membership to catalog assets, project MCP
// servers, schedule jobs and knowledge submissions, and limits platform-wide
// assets and sync settings to administrators.
func (d deps) assetWriteGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || !authRequired() {
			c.Next()
			return
		}
		path := c.FullPath()
		if !d.allowAssetWrite(c, path) {
			c.Abort()
			return
		}
		c.Next()
	}
}

func (d deps) allowAssetWrite(c *gin.Context, path string) bool {
	switch path {
	case "/api/sync/config", "/api/sync/now":
		return d.requirePlatformAdmin(c)
	case "/api/sync/create":
		var body struct {
			IssueID string `json:"issueId"`
		}
		peekJSON(c, &body)
		issue, _ := d.repo.Issue(strings.TrimSpace(body.IssueID))
		return d.requireProjectWriter(c, issue.ProjectID)
	case "/api/mcp-market/install", "/api/mcp-servers/custom":
		var body struct {
			Scope     string `json:"scope"`
			ProjectID string `json:"projectId"`
		}
		peekJSON(c, &body)
		if strings.TrimSpace(body.Scope) == "personal" {
			return true
		}
		if strings.TrimSpace(body.Scope) != "project" {
			return d.requirePlatformAdmin(c)
		}
		return d.requireProjectWriter(c, strings.TrimSpace(body.ProjectID))
	case "/api/knowledge/submissions/:id", "/api/knowledge/submissions/:id/publish", "/api/knowledge/submissions/:id/reject":
		project, _, _ := d.assetOwner("knowledge", c.Param("id"))
		return d.requireProjectWriter(c, project)
	case "/api/schedule-jobs/:id", "/api/schedule-jobs/:id/run":
		project, _, _ := d.assetOwner("schedule-jobs", c.Param("id"))
		return d.requireProjectWriter(c, project)
	case "/api/skills/:id/files", "/api/skills/:id/files/*path":
		return d.allowStoredAsset(c, "skills", c.Param("id"))
	case "/api/mcp-servers/:id/files", "/api/mcp-servers/:id/files/*path":
		return d.allowStoredAsset(c, "mcp-servers", c.Param("id"))
	}
	kind := strings.TrimPrefix(path, "/api/")
	switch kind {
	case "rules", "skills", "dockerfiles", "environments", "sandboxes", "mcp-servers", "schedule-jobs":
	default:
		return true
	}
	if c.Request.Method == http.MethodDelete {
		return d.allowStoredAsset(c, kind, strings.TrimSpace(c.Query("id")))
	}
	var body struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Scope     string `json:"scope"`
	}
	peekJSON(c, &body)
	id, project := strings.TrimSpace(body.ID), strings.TrimSpace(body.ProjectID)
	if !d.allowStoredAsset(c, kind, id) {
		return false
	}
	if project == "" && kind != "schedule-jobs" && !(kind == "mcp-servers" && strings.TrimSpace(body.Scope) == "personal") {
		if storedProject, _, found := d.assetOwner(kind, id); found && storedProject != "" {
			return true
		}
		return d.requirePlatformAdmin(c)
	}
	return d.requireProjectWriter(c, project)
}

func (d deps) allowStoredAsset(c *gin.Context, kind, id string) bool {
	project, global, found := d.assetOwner(kind, id)
	if !found {
		return true
	}
	if global {
		return d.requirePlatformAdmin(c)
	}
	return d.requireProjectWriter(c, project)
}

// peekJSON decodes the request body without consuming it for the handler.
func peekJSON(c *gin.Context, v any) {
	if c.Request.Body == nil {
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if err == nil {
		_ = json.Unmarshal(raw, v)
	}
}

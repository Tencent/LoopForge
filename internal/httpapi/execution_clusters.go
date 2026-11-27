package httpapi

import (
	"crypto/rand"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

type executionClusterHandlers struct {
	*deps
	clusters *repo.ExecutionClustersRepo
}

func (h *executionClusterHandlers) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	projectID := strings.TrimSpace(c.Query("projectId"))
	admin := h.isPlatformAdmin(c.Request)
	manage := admin && c.Query("manage") == "1"
	if projectID != "" {
		project, found := h.repo.Project(projectID)
		if !found || !workbenchProjectAllowsUser(project, user.ID, admin) {
			c.JSON(403, gin.H{"error": "无权访问项目"})
			return
		}
	}
	ctx := c.Request.Context()
	clusters, err := h.clusters.ExecutionClusters(ctx)
	if err != nil {
		respond(c, err)
		return
	}
	target, err := h.repo.ProjectExecutionTarget(ctx, projectID)
	if err != nil {
		respond(c, err)
		return
	}
	defaultID := target.ClusterID
	available := make([]gin.H, 0, len(clusters))
	defaultAvailable := false
	for _, cluster := range clusters {
		selectable := cluster.Enabled && cluster.AllowsProject(projectID)
		if cluster.ID == defaultID && selectable {
			defaultAvailable = true
		}
		if selectable {
			available = append(available, gin.H{"id": cluster.ID, "name": cluster.Name})
		}
	}
	if !defaultAvailable {
		defaultID = ""
	}
	result := gin.H{
		"available": available, "defaultClusterId": defaultID, "projectTarget": target, "canManage": admin,
		"minRunnerVersion": domain.RunnerVersionDefaultSelection,
		"canSetDefault":    projectID != "" && (admin || h.projectMemberWrites(projectID, user.ID)),
	}
	if manage {
		result["clusters"] = clusters
	}
	c.JSON(200, result)
}

type executionClusterInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	APIKey       *string  `json:"apiKey"`
	SecureAccess *bool    `json:"secureAccess"`
	Enabled      *bool    `json:"enabled"`
	ProjectIDs   []string `json:"projectIds"`
}

func readExecutionClusterInput(c *gin.Context) (executionClusterInput, domain.OpenSandboxConnection, bool) {
	var input executionClusterInput
	if !decodeLimit(c, &input, 32<<10) {
		return input, domain.OpenSandboxConnection{}, false
	}
	config := domain.OpenSandboxConnection{URL: input.URL}
	if input.APIKey != nil {
		config.APIKey = *input.APIKey
	}
	if input.SecureAccess != nil {
		config.SecureAccess = *input.SecureAccess
	}
	config, err := runtime.NormalizeOpenSandboxConnection(config)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
	}
	return input, config, err == nil
}

func (h *executionClusterHandlers) save(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.platformAdminWrite(c) {
		return
	}
	input, config, ok := readExecutionClusterInput(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		c.JSON(400, gin.H{"error": "请输入 1–100 字的集群名称"})
		return
	}
	if len(input.ProjectIDs) > 200 {
		c.JSON(400, gin.H{"error": "集群共享项目过多"})
		return
	}
	for i, id := range input.ProjectIDs {
		id = strings.TrimSpace(id)
		_, found := h.repo.Project(id)
		if id == "" || !found {
			c.JSON(400, gin.H{"error": "请选择有效的共享项目"})
			return
		}
		input.ProjectIDs[i] = id
	}
	slices.Sort(input.ProjectIDs)
	id := c.Param("id")
	create := id == ""
	if create {
		id = "cluster-" + rand.Text()
	}
	saved, err := h.clusters.SaveExecutionCluster(c.Request.Context(), domain.ExecutionCluster{
		ID: id, Name: name, Enabled: input.Enabled == nil || *input.Enabled,
		ProjectIDs: slices.Compact(input.ProjectIDs), OpenSandboxConnection: config,
	}, create, input.APIKey == nil, input.SecureAccess == nil)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, saved)
}

func (h *executionClusterHandlers) test(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.platformAdminWrite(c) {
		return
	}
	input, config, ok := readExecutionClusterInput(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if input.APIKey == nil && input.ID != "" {
		saved, err := h.clusters.AdminOpenSandboxClusterConnection(ctx, input.ID)
		if err != nil {
			respond(c, err)
			return
		}
		if saved.URL == config.URL {
			config.APIKey = saved.APIKey
		}
	}
	if err := runtime.ProbeOpenSandboxConnection(ctx, config); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *executionClusterHandlers) setDefault(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	projectID := strings.TrimSpace(c.Query("projectId"))
	project, found := h.repo.Project(projectID)
	if projectID == "" || !found || project.ArchivedAt != nil {
		c.JSON(404, gin.H{"error": "请选择有效的项目"})
		return
	}
	if !h.isPlatformAdmin(c.Request) && !h.projectMemberWrites(projectID, user.ID) {
		c.JSON(403, gin.H{"error": "无权更改该项目的默认集群"})
		return
	}
	if !requireWriteOrigin(c) {
		return
	}
	var input struct {
		ClusterID string `json:"clusterId"`
	}
	if !decodeLimit(c, &input, 4096) {
		return
	}
	ctx := c.Request.Context()
	if input.ClusterID != "" {
		if _, err := h.clusters.AllowedExecutionCluster(ctx, input.ClusterID, projectID, true); err != nil {
			respond(c, err)
			return
		}
	}
	target := domain.ExecutionTarget{Pool: domain.ExecutionTargetPersonal}
	if input.ClusterID != "" {
		target = domain.ExecutionTarget{Pool: domain.ExecutionTargetCluster, ClusterID: input.ClusterID}
	}
	if err := h.repo.SetProjectExecutionTarget(ctx, projectID, target); err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"defaultClusterId": input.ClusterID})
}

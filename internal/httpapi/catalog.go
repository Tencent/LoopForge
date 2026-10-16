package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type catalogHandlers struct {
	*deps
	notifications *notificationHandlers
}

// canManageProjectRepositories mirrors project settings: only the project
// owner or a platform administrator may change its repositories.
func (h *catalogHandlers) canManageProjectRepositories(c *gin.Context, projectID string) bool {
	if !authRequired() {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return false
	}
	if h.isPlatformAdmin(c.Request) {
		return true
	}
	if project, ok := h.repo.Project(projectID); ok && project.OwnerID == user.ID {
		return true
	}
	c.JSON(403, gin.H{"error": "只有项目 Owner 或平台管理员可以管理项目仓库"})
	return false
}

func (h *catalogHandlers) repositories(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		c.JSON(200, domain.PublicRepositories(h.repo.Repositories()))
		return
	}
	if c.Request.Method == http.MethodDelete {
		id := strings.TrimSpace(c.Query("id"))
		if id == "" {
			c.JSON(400, map[string]string{"error": "id is required"})
			return
		}
		if existing, ok := h.repo.Repository(id); ok && !h.canManageProjectRepositories(c, existing.ProjectID) {
			return
		}
		if err := h.repo.DeleteRepository(id); err != nil {
			respond(c, err)
			return
		}
		// 清除引用了该仓库的项目默认仓库设置
		for _, p := range h.repo.Projects() {
			if p.DefaultRepositoryID == id {
				p.DefaultRepositoryID = ""
				_ = h.repo.PutProject(p)
			}
		}
		c.JSON(200, map[string]string{"status": "deleted"})
		return
	}
	if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPut {
		return
	}
	var body struct {
		domain.Repository
		RepoURL string `json:"repoUrl"`
		URL     string `json:"url"`
	}
	if !decode(c, &body) {
		return
	}
	v := body.Repository
	if v.ID == "" {
		v.ID = fmt.Sprintf("repo-%d", time.Now().UnixNano())
	}
	v.ID = strings.TrimSpace(v.ID)
	v.ProjectID = strings.TrimSpace(v.ProjectID)
	v.Name = strings.TrimSpace(v.Name)
	v.GongfengProjectID = strings.TrimSpace(v.GongfengProjectID)
	v.DefaultBranch = strings.TrimSpace(v.DefaultBranch)
	v.WebURL = strings.TrimSpace(v.WebURL)
	v.CloneURL = strings.TrimSpace(v.CloneURL)
	v.IntegrateBranch = domain.NormalizeIntegrateBranch(v.IntegrateBranch)
	if existing, ok := h.repo.Repository(v.ID); ok && !h.canManageProjectRepositories(c, existing.ProjectID) {
		return
	}
	if !h.canManageProjectRepositories(c, v.ProjectID) {
		return
	}
	// localPath copies a directory from the executing host into the task
	// workspace, so only platform administrators may set it.
	if authRequired() && !h.isPlatformAdmin(c.Request) {
		v.LocalPath = ""
		if existing, ok := h.repo.Repository(v.ID); ok {
			v.LocalPath = existing.LocalPath
		}
	}
	// One pasted Gongfeng reference (web / SSH / clone / git clone / group/repo)
	// is enough. Prefer repoUrl so an SSH paste keeps an SSH clone URL.
	var parsed domain.GongfengRepoInfo
	var parsedOK bool
	for _, candidate := range []string{strings.TrimSpace(body.RepoURL), strings.TrimSpace(body.URL), v.CloneURL, v.WebURL, v.GongfengProjectID} {
		if info, ok := domain.ParseRepositoryURL(candidate); ok {
			parsed, parsedOK = info, true
			break
		}
	}
	// An explicit pasted URL is authoritative. Generated identity/web/clone
	// fields must not silently redirect a correct URL to another repository.
	primary := strings.TrimSpace(body.RepoURL)
	if primary == "" {
		primary = strings.TrimSpace(body.URL)
	}
	if primary != "" {
		info, ok := domain.ParseRepositoryURL(primary)
		if !ok {
			c.JSON(400, gin.H{"error": "仓库 URL 无法识别，请提供有效的 GitHub 仓库地址"})
			return
		}
		if body.RepoURL != "" && body.URL != "" {
			other, ok := domain.ParseRepositoryURL(body.URL)
			if !ok || other.Project != info.Project {
				c.JSON(400, gin.H{"error": "repoUrl 与 url 指向不同仓库"})
				return
			}
		}
		parsed, parsedOK = info, true
		v.GongfengProjectID = info.Project
		v.WebURL = info.WebURL
		v.CloneURL = info.CloneURL
	}
	// A link-shaped project id that fails to parse (github.com, malformed URL)
	// is rejected loudly: storing it as a project path would silently break
	// sync, branching and MR calls. Bare paths and numeric ids stay compatible.
	if v.GongfengProjectID != "" {
		if info, ok := domain.ParseRepositoryURL(v.GongfengProjectID); ok {
			v.GongfengProjectID = info.Project
		} else if !strings.HasPrefix(v.GongfengProjectID, "github:") && !domain.IsSafeGongfengProjectID(v.GongfengProjectID) {
			c.JSON(400, map[string]string{"error": "工蜂仓库链接无法识别：请粘贴 git.example.com 的仓库链接，或直接填写 group/repository 项目路径"})
			return
		}
	}
	if parsedOK {
		if v.GongfengProjectID == "" {
			v.GongfengProjectID = parsed.Project
		}
		if v.WebURL == "" {
			v.WebURL = parsed.WebURL
		}
		if v.CloneURL == "" {
			v.CloneURL = parsed.CloneURL
		}
		if v.Name == "" {
			v.Name = parsed.Name
		}
	}
	if v.GongfengProjectID != "" {
		if v.WebURL == "" {
			v.WebURL = "https://" + domain.GongfengRepoHost + "/" + v.GongfengProjectID
		}
		if v.CloneURL == "" {
			v.CloneURL = v.WebURL + ".git"
		}
		if v.Name == "" {
			v.Name = v.GongfengProjectID[strings.LastIndex(v.GongfengProjectID, "/")+1:]
		}
	}
	if v.ProjectID == "" || v.Name == "" || v.GongfengProjectID == "" {
		c.JSON(400, map[string]string{"error": "projectId、name、gongfengProjectId 必填；也可以只粘贴工蜂仓库链接自动识别"})
		return
	}
	if _, ok := h.repo.Project(v.ProjectID); !ok {
		c.JSON(400, map[string]string{"error": "project not found"})
		return
	}
	if strings.HasPrefix(v.GongfengProjectID, "github:") {
		v.Provider = "github"
		if v.DefaultBranch == "" {
			v.DefaultBranch = "main"
		}
	}
	if v.Provider == "" {
		v.Provider = "gongfeng"
	}
	if v.Provider != "gongfeng" && v.Provider != "github" {
		c.JSON(400, map[string]string{"error": "provider must be github or gongfeng"})
		return
	}
	if v.DefaultBranch == "" {
		v.DefaultBranch = "master"
	}
	if err := h.repo.PutRepository(v); err != nil {
		respond(c, err)
		return
	}
	stored, _ := h.repo.Repository(v.ID)
	c.JSON(200, domain.PublicRepository(stored))
}

func (h *catalogHandlers) repository(c *gin.Context) {
	repository, ok := h.repo.Repository(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "repository not found"})
		return
	}
	c.JSON(http.StatusOK, domain.PublicRepository(repository))
}

func (h *catalogHandlers) agents(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		agents := h.repo.Agents()
		if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
			agents = filterAgentsForProject(agents, projectID)
		}
		c.JSON(200, agents)
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.AgentDefinition
		if decode(c, &v) && v.ID != "" {
			v.ID, v.ProjectID = strings.TrimSpace(v.ID), strings.TrimSpace(v.ProjectID)
			if existing, ok := h.repo.Agent(v.ID); ok {
				if v.ProjectID == "" {
					v.ProjectID = strings.TrimSpace(existing.ProjectID)
				}
				if existing.ProjectID != "" && existing.ProjectID != v.ProjectID {
					c.JSON(http.StatusConflict, map[string]string{"error": "角色标识已被其他项目使用"})
					return
				}
			}
			if v.ProjectID == "" {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "角色需要 projectId"})
				return
			}
			if _, ok := h.repo.Project(v.ProjectID); !ok {
				c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
				return
			}
			if !h.requireProjectWriter(c, v.ProjectID) {
				return
			}
			if strings.TrimSpace(v.ExecutorID) == "" {
				v.ExecutorID = "codebuddy"
			}
			builtIn, ok := repo.BuiltInExecutor(v.ExecutorID)
			if !ok {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "CLI 仅支持 codebuddy、codex 或 claude"})
				return
			}
			if err := domain.ValidateAgentEffort(v.Effort, builtIn.Kind); err != nil {
				c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if err := domain.ValidateVerificationGates(v.Verification); err != nil {
				c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if v.Version == 0 {
				v.Version = 1
			}
			if v.Shared {
				if err := h.shareAgentAssets(v); err != nil {
					respond(c, err)
					return
				}
			}
			if err := h.repo.PutAgent(v); err != nil {
				respond(c, err)
				return
			}
			stored, _ := h.repo.Agent(v.ID)
			c.JSON(200, stored)
		}
	}
}

func filterAgentsForProject(agents []domain.AgentDefinition, projectID string) []domain.AgentDefinition {
	out := make([]domain.AgentDefinition, 0, len(agents))
	for _, agent := range agents {
		owner := strings.TrimSpace(agent.ProjectID)
		if owner == projectID || (owner != "" && agent.Shared) {
			out = append(out, agent)
		}
	}
	return out
}

func (h *catalogHandlers) validateWorkflowAgentScopes(workflow domain.WorkflowDefinition) error {
	projectID := strings.TrimSpace(workflow.ProjectID)
	if projectID == "" {
		return nil
	}
	for _, node := range workflow.Nodes {
		if node.Type != "agent" {
			continue
		}
		agentID := strings.TrimSpace(node.AgentID)
		if agentID == "" || domain.IsPreviewBuildAgent(agentID) {
			continue
		}
		agent, ok := h.repo.Agent(agentID)
		if !ok {
			return fmt.Errorf("角色 %q 不存在", agentID)
		}
		owner := strings.TrimSpace(agent.ProjectID)
		if owner == "" || (owner != projectID && !agent.Shared) {
			return fmt.Errorf("角色 %q 不属于当前项目且尚未共享", agent.Name)
		}
	}
	return nil
}
func (h *catalogHandlers) agent(c *gin.Context) {
	id := c.Param("id")
	current, ok := h.repo.Agent(id)
	if !ok {
		c.JSON(404, map[string]string{"error": "agent not found"})
		return
	}
	if c.Request.Method != http.MethodGet && c.FullPath() != "/api/agents/:id/clone" && !h.requireProjectWriter(c, current.ProjectID) {
		return
	}
	switch c.FullPath() {
	case "/api/agents/:id":
		if c.Request.Method == http.MethodDelete {
			v, _ := h.repo.Agent(id)
			if v.Shared {
				c.JSON(http.StatusConflict, map[string]string{"error": "共享状态的角色不能删除，请先取消共享"})
				return
			}
			if referencing := h.repo.WorkflowNamesUsingAgent(v); len(referencing) > 0 {
				c.JSON(http.StatusConflict, map[string]string{"error": fmt.Sprintf("角色仍被 %d 个流程引用（%s），请先解除引用", len(referencing), strings.Join(referencing, ", "))})
				return
			}
			if err := h.repo.DeleteAgent(id); err != nil {
				respond(c, err)
				return
			}
			c.Status(http.StatusNoContent)
			return
		}
		v, _ := h.repo.Agent(id)
		c.JSON(200, v)
	case "/api/agents/:id/revisions":
		c.JSON(200, h.repo.Revisions("agent", id))
	case "/api/agents/:id/revisions/:rev":
		revision, ok := h.repo.Revision(c.Param("rev"))
		if !ok || revision.Kind != "agent" || revision.EntityID != id {
			c.JSON(http.StatusNotFound, map[string]string{"error": "agent revision not found"})
			return
		}
		c.JSON(http.StatusOK, revision)
	case "/api/agents/:id/revisions/:rev/restore":
		stored, err := h.repo.RestoreAgentRevision(c.Param("rev"))
		if err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, stored)
	case "/api/agents/:id/clone":
		var req struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			ProjectID string `json:"projectId"`
		}
		if !decode(c, &req) {
			return
		}
		if req.ID == "" || req.Name == "" {
			c.JSON(400, map[string]string{"error": "id and name are required"})
			return
		}
		v, _ := h.repo.Agent(id)
		if _, ok := h.repo.Project(strings.TrimSpace(req.ProjectID)); !ok {
			c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
			return
		}
		taken, _ := h.repo.Agent(req.ID)
		if !h.requireProjectWriter(c, strings.TrimSpace(req.ProjectID), taken.ProjectID) {
			return
		}
		if v.Shared {
			if err := h.shareAgentAssets(v); err != nil {
				respond(c, err)
				return
			}
		}
		userID := int64(0)
		if current, ok := h.requestUser(c.Request, true); ok {
			userID = current.ID
		}
		mcpServers, err := h.cloneAgentMCPServers(v.MCPServers, strings.TrimSpace(req.ProjectID), userID)
		if err != nil {
			if errors.Is(err, errPersonalMCPCloneRequiresLogin) {
				c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error()})
			} else {
				respond(c, err)
			}
			return
		}
		v.ID, v.Name = req.ID, req.Name
		v.ProjectID = strings.TrimSpace(req.ProjectID)
		v.MCPServers = mcpServers
		v.Shared = false
		v.Version = 1
		v.UpdatedAt = time.Time{}
		if err := h.repo.PutAgent(v); err != nil {
			respond(c, err)
			return
		}
		stored, _ := h.repo.Agent(v.ID)
		c.JSON(201, stored)
	default:
		c.Status(http.StatusNotFound)
	}
}

var errPersonalMCPCloneRequiresLogin = errors.New("复制包含个人 MCP 的角色前请先登录")

func (h *catalogHandlers) shareAgentAssets(agent domain.AgentDefinition) error {
	for _, id := range agent.Rules {
		item, ok := h.repo.Rule(id)
		if !ok || item.Shared || (item.ProjectID != "" && item.ProjectID != agent.ProjectID) {
			continue
		}
		item.Shared = true
		if err := h.repo.PutRule(item); err != nil {
			return fmt.Errorf("share rule %q: %w", id, err)
		}
	}
	for _, id := range agent.Skills {
		item, ok := h.repo.Skill(id)
		if !ok || item.Shared || (item.ProjectID != "" && item.ProjectID != agent.ProjectID) {
			continue
		}
		item.Shared = true
		if err := h.repo.PutSkill(item); err != nil {
			return fmt.Errorf("share skill %q: %w", id, err)
		}
	}
	return nil
}

func (h *catalogHandlers) cloneAgentMCPServers(ids []string, projectID string, userID int64) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		source, ok := h.repo.MCPServer(id)
		if !ok || source.Scope == "" {
			continue
		}
		ownerKey := projectID
		if source.Scope == "project" {
			if source.ProjectID == projectID {
				out = append(out, source.ID)
				continue
			}
			for _, existing := range h.repo.MCPServers() {
				if source.MarketID != "" && existing.Scope == "project" && existing.ProjectID == projectID && existing.MarketID == source.MarketID {
					out = append(out, existing.ID)
					goto nextMCP
				}
			}
			source.ProjectID, source.OwnerUserID = projectID, 0
		} else if source.Scope == "personal" {
			if userID <= 0 {
				return nil, errPersonalMCPCloneRequiresLogin
			}
			if source.OwnerUserID == userID {
				out = append(out, source.ID)
				continue
			}
			ownerKey = strconv.FormatInt(userID, 10)
			source.ProjectID, source.OwnerUserID, source.MarketID = "", userID, ""
		} else {
			continue
		}
		source.ID = mcpInstallID("clone-"+id, source.Scope, ownerKey)
		source.UpdatedAt = time.Time{}
		if _, exists := h.repo.MCPServer(source.ID); !exists {
			if err := h.repo.PutMCPServer(source); err != nil {
				return nil, fmt.Errorf("clone MCP %q: %w", id, err)
			}
		}
		out = append(out, source.ID)
	nextMCP:
	}
	return out, nil
}

// normalizeGlobalRule keeps workflow bindings only while the rule is global.
// Each id must be a SOP this project can run: its own, a shared SOP, or a
// generic template. Turning the switch off drops the bindings.
func normalizeGlobalRule(st *repo.Store, v *domain.RuleDefinition) error {
	if v == nil {
		return nil
	}
	if !v.Global {
		v.WorkflowIDs = nil
		return nil
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(v.WorkflowIDs))
	for _, raw := range v.WorkflowIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		wf, ok := st.Workflow(id)
		if !ok {
			return fmt.Errorf("SOP %q 不存在", id)
		}
		if !wf.Generic && wf.ProjectID != v.ProjectID && !wf.Shared {
			return fmt.Errorf("SOP %q 不属于当前项目", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	v.WorkflowIDs = ids
	return nil
}

func (h *catalogHandlers) rules(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.repo.Rules()
		if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
			filtered := make([]domain.RuleDefinition, 0, len(items))
			for _, item := range items {
				if item.ProjectID == projectID {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		c.JSON(200, items)
		return
	}
	if c.Request.Method == http.MethodDelete {
		h.deleteCatalogAsset(c, "规则", "rule not found", func(id string) (bool, bool) {
			item, ok := h.repo.Rule(id)
			return item.Shared, ok
		}, func(id string) []string { return h.agentNamesReferencing("rule", id) }, "角色", h.repo.DeleteRule)
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.RuleDefinition
		if decode(c, &v) {
			v.ID, v.ProjectID = strings.TrimSpace(v.ID), strings.TrimSpace(v.ProjectID)
			if v.ID == "" || v.Name == "" || v.ProjectID == "" {
				c.JSON(400, map[string]string{"error": "id, name and projectId are required"})
				return
			}
			if _, ok := h.repo.Project(v.ProjectID); !ok {
				c.JSON(404, map[string]string{"error": "project not found"})
				return
			}
			if existing, ok := h.repo.Rule(v.ID); ok && existing.ProjectID != "" && existing.ProjectID != v.ProjectID {
				c.JSON(409, map[string]string{"error": "rule id is already used by another project"})
				return
			} else if ok && existing.Shared && !v.Shared && h.sharedAgentReferences("rule", v.ID) {
				c.JSON(409, map[string]string{"error": "该规则仍被共享角色引用，不能取消共享"})
				return
			}
			if err := normalizeGlobalRule(h.repo, &v); err != nil {
				c.JSON(400, map[string]string{"error": err.Error()})
				return
			}
			respond(c, h.repo.PutRule(v))
		}
	}
}
func (h *catalogHandlers) skills(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.repo.Skills()
		if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
			filtered := make([]domain.SkillDefinition, 0, len(items))
			for _, item := range items {
				if item.ProjectID == projectID {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		c.JSON(200, items)
		return
	}
	if c.Request.Method == http.MethodDelete {
		h.deleteCatalogAsset(c, "技能", "skill not found", func(id string) (bool, bool) {
			item, ok := h.repo.Skill(id)
			return item.Shared, ok
		}, func(id string) []string { return h.agentNamesReferencing("skill", id) }, "角色", h.repo.DeleteSkill)
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.SkillDefinition
		if decode(c, &v) {
			v.ID, v.ProjectID = strings.TrimSpace(v.ID), strings.TrimSpace(v.ProjectID)
			if v.ID == "" || v.Name == "" || v.ProjectID == "" {
				c.JSON(400, map[string]string{"error": "id, name and projectId are required"})
				return
			}
			if _, ok := h.repo.Project(v.ProjectID); !ok {
				c.JSON(404, map[string]string{"error": "project not found"})
				return
			}
			if existing, ok := h.repo.Skill(v.ID); ok && existing.ProjectID != "" && existing.ProjectID != v.ProjectID {
				c.JSON(409, map[string]string{"error": "skill id is already used by another project"})
				return
			} else if ok && existing.Shared && !v.Shared && h.sharedAgentReferences("skill", v.ID) {
				c.JSON(409, map[string]string{"error": "该技能仍被共享角色引用，不能取消共享"})
				return
			}
			// The binary file manifest is owned by the skill_files table;
			// the SKILL.md body and text Files are owned by uploaded files
			// (or existing content if the client never sends them). Client
			// payloads cannot add, remove or reorder any of these.
			if existing, ok := h.repo.Skill(v.ID); ok {
				v.FileRefs = existing.FileRefs
				v.Files = existing.Files
				v.Content = existing.Content
			} else {
				v.FileRefs = nil
				v.Files = nil
			}
			respond(c, h.repo.PutSkill(v))
		}
	}
}

func (h *catalogHandlers) sharedAgentReferences(kind, id string) bool {
	for _, agent := range h.repo.Agents() {
		if !agent.Shared {
			continue
		}
		refs := agent.Rules
		if kind == "skill" {
			refs = agent.Skills
		}
		for _, ref := range refs {
			if ref == id {
				return true
			}
		}
	}
	return false
}

func (h *catalogHandlers) deleteCatalogAsset(c *gin.Context, kind, notFound string, lookup func(id string) (shared bool, ok bool), refs func(id string) []string, refKind string, del func(id string) error) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	shared, ok := lookup(id)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": notFound})
		return
	}
	if kind == "沙箱" && (id == "local" || id == repo.GenericDockerSandboxID) {
		c.JSON(http.StatusConflict, map[string]string{"error": fmt.Sprintf("内置沙箱 %s 不能删除", id)})
		return
	}
	if kind == "Dockerfile" && id == repo.GenericBackendDockerfileID {
		c.JSON(http.StatusConflict, map[string]string{"error": "内置 Dockerfile generic-backend 不能删除"})
		return
	}
	if shared {
		c.JSON(http.StatusConflict, map[string]string{"error": fmt.Sprintf("共享状态的%s不能删除，请先取消共享", kind)})
		return
	}
	if names := refs(id); len(names) > 0 {
		c.JSON(http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s仍被 %d 个%s引用（%s），请先解除引用", kind, len(names), refKind, strings.Join(names, ", "))})
		return
	}
	if err := del(id); err != nil {
		respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *catalogHandlers) agentNamesReferencing(kind, id string) []string {
	var names []string
	for _, agent := range h.repo.Agents() {
		refs := agent.Rules
		if kind == "skill" {
			refs = agent.Skills
		}
		for _, ref := range refs {
			if ref == id {
				names = append(names, agent.Name)
				break
			}
		}
	}
	return names
}

func (h *catalogHandlers) sandboxNamesReferencing(kind, id string) []string {
	var names []string
	for _, sandbox := range h.repo.Sandboxes() {
		if (kind == "dockerfile" && sandbox.DockerfileID == id) || (kind == "environment" && sandbox.EnvironmentID == id) {
			names = append(names, sandbox.Name)
		}
	}
	return names
}

func (h *catalogHandlers) workflowNamesReferencingSandbox(id string) []string {
	var names []string
	for _, workflow := range h.repo.Workflows() {
		if workflow.SandboxProfileID == id {
			names = append(names, workflow.Name)
		}
	}
	return names
}

func (h *catalogHandlers) executors(c *gin.Context) {
	c.JSON(200, repo.BuiltInExecutors())
}
func (h *catalogHandlers) dockerfiles(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.repo.Dockerfiles()
		if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
			items = filterEnvironmentAssets(items, projectID, func(item domain.DockerfileDefinition) (string, bool) { return item.ProjectID, item.Shared })
		}
		c.JSON(200, items)
		return
	}
	if c.Request.Method == http.MethodDelete {
		h.deleteCatalogAsset(c, "Dockerfile", "dockerfile not found", func(id string) (bool, bool) {
			item, ok := h.repo.Dockerfile(id)
			return item.Shared, ok
		}, func(id string) []string { return h.sandboxNamesReferencing("dockerfile", id) }, "沙箱", h.repo.DeleteDockerfile)
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.DockerfileDefinition
		if decode(c, &v) {
			v.ID, v.ProjectID = strings.TrimSpace(v.ID), strings.TrimSpace(v.ProjectID)
			if err := h.validateEnvironmentAsset(v.ID, v.Name, v.ProjectID, "Dockerfile"); err != nil {
				respond(c, err)
				return
			}
			if existing, ok := h.repo.Dockerfile(v.ID); ok && existing.ProjectID != "" && existing.ProjectID != v.ProjectID {
				c.JSON(http.StatusConflict, map[string]string{"error": "Dockerfile 标识已被其他项目使用"})
				return
			} else if ok && existing.Shared && !v.Shared && h.sharedEnvironmentReference("dockerfile", v.ID) {
				c.JSON(http.StatusConflict, map[string]string{"error": "该 Dockerfile 仍被共享沙箱引用，不能取消共享"})
				return
			}
			respond(c, h.repo.PutDockerfile(v))
		}
	}
}
func (h *catalogHandlers) environments(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.repo.Environments()
		if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
			items = filterEnvironmentAssets(items, projectID, func(item domain.EnvironmentDefinition) (string, bool) { return item.ProjectID, item.Shared })
		}
		c.JSON(200, items)
		return
	}
	if c.Request.Method == http.MethodDelete {
		h.deleteCatalogAsset(c, "环境安装脚本", "environment not found", func(id string) (bool, bool) {
			item, ok := h.repo.Environment(id)
			return item.Shared, ok
		}, func(id string) []string { return h.sandboxNamesReferencing("environment", id) }, "沙箱", h.repo.DeleteEnvironment)
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.EnvironmentDefinition
		if decode(c, &v) {
			v.ID, v.ProjectID = strings.TrimSpace(v.ID), strings.TrimSpace(v.ProjectID)
			if err := h.validateEnvironmentAsset(v.ID, v.Name, v.ProjectID, "环境安装脚本"); err != nil {
				respond(c, err)
				return
			}
			if existing, ok := h.repo.Environment(v.ID); ok && existing.ProjectID != "" && existing.ProjectID != v.ProjectID {
				c.JSON(http.StatusConflict, map[string]string{"error": "环境安装脚本标识已被其他项目使用"})
				return
			} else if ok && existing.Shared && !v.Shared && h.sharedEnvironmentReference("environment", v.ID) {
				c.JSON(http.StatusConflict, map[string]string{"error": "该环境安装脚本仍被共享沙箱引用，不能取消共享"})
				return
			}
			respond(c, h.repo.PutEnvironment(v))
		}
	}
}
func (h *catalogHandlers) sandboxes(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.repo.Sandboxes()
		if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
			items = filterEnvironmentAssets(items, projectID, func(item domain.SandboxProfile) (string, bool) { return item.ProjectID, item.Shared })
		}
		c.JSON(200, items)
		return
	}
	if c.Request.Method == http.MethodDelete {
		h.deleteCatalogAsset(c, "沙箱", "sandbox not found", func(id string) (bool, bool) {
			item, ok := h.repo.Sandbox(id)
			return item.Shared, ok
		}, func(id string) []string { return h.workflowNamesReferencingSandbox(id) }, "流程", h.repo.DeleteSandbox)
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.SandboxProfile
		if decode(c, &v) {
			v.ID, v.ProjectID = strings.TrimSpace(v.ID), strings.TrimSpace(v.ProjectID)
			if err := h.validateEnvironmentAsset(v.ID, v.Name, v.ProjectID, "沙箱"); err != nil {
				respond(c, err)
				return
			}
			if existing, ok := h.repo.Sandbox(v.ID); ok && existing.ProjectID != "" && existing.ProjectID != v.ProjectID {
				c.JSON(http.StatusConflict, map[string]string{"error": "沙箱标识已被其他项目使用"})
				return
			} else if ok && existing.Shared && !v.Shared && (v.ID == repo.GenericDockerSandboxID || h.sharedEnvironmentReference("sandbox", v.ID)) {
				c.JSON(http.StatusConflict, map[string]string{"error": "该沙箱仍被共享 SOP 引用，不能取消共享"})
				return
			}
			if err := h.shareSandboxDependencies(v); err != nil {
				respond(c, err)
				return
			}
			respond(c, h.repo.PutSandbox(v))
		}
	}
}

func filterEnvironmentAssets[T any](items []T, projectID string, scope func(T) (string, bool)) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		owner, shared := scope(item)
		if owner == projectID || (owner != "" && shared) {
			out = append(out, item)
		}
	}
	return out
}

func (h *catalogHandlers) validateEnvironmentAsset(id, name, projectID, kind string) error {
	if id == "" || strings.TrimSpace(name) == "" || projectID == "" {
		return fmt.Errorf("%s 需要 id、name 和 projectId", kind)
	}
	if _, ok := h.repo.Project(projectID); !ok {
		return fmt.Errorf("项目不存在")
	}
	return nil
}

func (h *catalogHandlers) sharedEnvironmentReference(kind, id string) bool {
	if kind == "sandbox" {
		for _, workflow := range h.repo.Workflows() {
			if workflow.Shared && workflow.SandboxProfileID == id {
				return true
			}
		}
		return false
	}
	for _, sandbox := range h.repo.Sandboxes() {
		if !sandbox.Shared {
			continue
		}
		if (kind == "dockerfile" && sandbox.DockerfileID == id) || (kind == "environment" && sandbox.EnvironmentID == id) {
			return true
		}
	}
	return false
}

func (h *catalogHandlers) shareSandboxDependencies(sandbox domain.SandboxProfile) error {
	if sandbox.DockerfileID != "" {
		item, ok := h.repo.Dockerfile(sandbox.DockerfileID)
		if !ok {
			return fmt.Errorf("dockerfile %q not found", sandbox.DockerfileID)
		}
		if item.ProjectID != sandbox.ProjectID && !item.Shared {
			return fmt.Errorf("Dockerfile %q 不属于当前项目且尚未共享", item.Name)
		}
		if sandbox.Shared && item.ProjectID == sandbox.ProjectID && !item.Shared {
			item.Shared = true
			if err := h.repo.PutDockerfile(item); err != nil {
				return err
			}
		}
	}
	if sandbox.EnvironmentID != "" {
		item, ok := h.repo.Environment(sandbox.EnvironmentID)
		if !ok {
			return fmt.Errorf("environment %q not found", sandbox.EnvironmentID)
		}
		if item.ProjectID != sandbox.ProjectID && !item.Shared {
			return fmt.Errorf("环境安装脚本 %q 不属于当前项目且尚未共享", item.Name)
		}
		if sandbox.Shared && item.ProjectID == sandbox.ProjectID && !item.Shared {
			item.Shared = true
			if err := h.repo.PutEnvironment(item); err != nil {
				return err
			}
		}
	}
	return nil
}
func (h *catalogHandlers) workflows(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		c.JSON(200, h.repo.Workflows())
		return
	}
	if c.Request.Method == http.MethodPut {
		var v domain.WorkflowDefinition
		if decode(c, &v) && v.ID != "" {
			needsNotificationAccess := len(v.Notifications) > 0
			for _, node := range v.Nodes {
				if len(node.Notifications) > 0 {
					needsNotificationAccess = true
				}
			}
			projectID := v.ProjectID
			previousProjectID := ""
			if previous, exists := h.repo.Workflow(v.ID); exists {
				previousProjectID = previous.ProjectID
				if (needsNotificationAccess || len(previous.Notifications) > 0) && !h.notifications.access(c, previous.ProjectID, true) {
					return
				}
				if projectID == "" {
					projectID = previous.ProjectID
				}
				if len(previous.Notifications) > 0 {
					needsNotificationAccess = true
				}
				for _, node := range previous.Nodes {
					if len(node.Notifications) > 0 {
						needsNotificationAccess = true
					}
				}
			}
			if needsNotificationAccess && !h.notifications.access(c, projectID, true) {
				return
			}
			if !h.requireProjectWriter(c, v.ProjectID, previousProjectID) {
				return
			}

			if err := domain.ValidateWorkflowPreparation(v); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			if err := domain.ValidateWorkflowMetricStages(v); err != nil {
				c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if _, generic := h.repo.GenericTemplateByWorkflow(v.ID); generic {
				if !h.isPlatformAdmin(c.Request) {
					c.JSON(http.StatusConflict, map[string]string{"error": "通用模板为只读，请先复制到本项目再修改"})
					return
				}
				if strings.TrimSpace(v.SandboxProfileID) == "" {
					c.JSON(http.StatusBadRequest, map[string]string{"error": "workflow sandboxProfileId is required"})
					return
				}
				if _, exists := h.repo.Sandbox(v.SandboxProfileID); !exists {
					c.JSON(http.StatusBadRequest, map[string]string{"error": "sandbox profile not found"})
					return
				}
				v, err := domain.AttachPreviewBuild(v)
				if err != nil {
					c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
				stored, err := h.repo.UpdateGenericWorkflow(v)
				if err != nil {
					if strings.Contains(err.Error(), "revision conflict") {
						c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
					} else {
						respond(c, err)
					}
					return
				}
				c.JSON(200, stored)
				return
			}
			if err := h.validateWorkflowAgentScopes(v); err != nil {
				c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			v, err := domain.AttachPreviewBuild(v)
			if err != nil {
				c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if strings.TrimSpace(v.SandboxProfileID) == "" {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "workflow sandboxProfileId is required"})
				return
			}
			profile, exists := h.repo.Sandbox(v.SandboxProfileID)
			if !exists {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "sandbox profile not found"})
				return
			}
			if v.ProjectID != "" && profile.ProjectID != "" && profile.ProjectID != v.ProjectID && !profile.Shared {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "沙箱不属于当前项目且尚未共享"})
				return
			}
			if v.Shared && !profile.Shared {
				c.JSON(http.StatusBadRequest, map[string]string{"error": "共享 SOP 只能使用已共享的沙箱"})
				return
			}
			if v.Version == 0 {
				v.Version = 1
			}
			// A payload that already carries a revision is an update. PutWorkflow
			// treats a missing row as a create, which would resurrect a deleted
			// SOP from a stale editor tab (and, with no projectId left on the
			// payload, list it in the shared catalog). Brand-new templates are
			// saved with revision 0. Internal callers may still seed a row with
			// a revision; only this client save is rejected.
			if v.Revision > 0 {
				if _, exists := h.repo.Workflow(v.ID); !exists {
					c.JSON(http.StatusConflict, map[string]string{"error": fmt.Sprintf("workflow %s not found", v.ID)})
					return
				}
			}
			if err := h.repo.PutWorkflow(v); err != nil {
				if strings.Contains(err.Error(), "revision conflict") {
					c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
				} else {
					respond(c, err)
				}
				return
			}
			stored, _ := h.repo.Workflow(v.ID)
			c.JSON(200, stored)
		}
	}
}
func (h *catalogHandlers) workflow(c *gin.Context) {
	id := c.Param("id")
	// Generic template ids are reserved: the built-in catalog always wins so a
	// legacy store row with the same id can never shadow the template.
	v, ok := h.repo.Workflow(id)
	if !ok {
		c.JSON(404, map[string]string{"error": "workflow not found"})
		return
	}
	if c.Request.Method != http.MethodGet && workflowHasNotifications(v) && !h.notifications.access(c, v.ProjectID, true) {
		return
	}
	if c.Request.Method != http.MethodGet && c.FullPath() != "/api/workflows/:id/clone" && !h.requireProjectWriter(c, v.ProjectID) {
		return
	}
	switch c.FullPath() {
	case "/api/workflows/:id":
		if c.Request.Method == http.MethodGet {
			c.JSON(200, v)
			return
		}
		if v.Generic {
			// Ordinary users cannot edit or delete a platform template in place.
			// Platform admins save via PUT /api/workflows; delete stays forbidden.
			c.JSON(http.StatusConflict, map[string]string{"error": "通用模板不可删除，平台管理员请直接保存修改"})
			return
		}
		for _, run := range h.repo.Runs() {
			if run.WorkflowID == id && (run.Status == "running" || run.Status == "waiting") {
				c.JSON(409, map[string]string{"error": "该 SOP 还有进行中的运行，请先终止后再删除"})
				return
			}
		}
		var boundIssues []string
		for _, issue := range h.repo.Issues() {
			if issue.WorkflowID == id && issue.Status != "closed" {
				boundIssues = append(boundIssues, issue.ID)
			}
		}
		if len(boundIssues) > 0 {
			c.JSON(409, map[string]string{"error": fmt.Sprintf("该 SOP 仍被 %d 个未关闭的 Issue 绑定（%s），请先改绑", len(boundIssues), strings.Join(boundIssues, ", "))})
			return
		}
		if err := h.repo.DeleteWorkflow(id); err != nil {
			respond(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	case "/api/workflows/:id/revisions":
		c.JSON(200, h.repo.Revisions("workflow", id))
	case "/api/workflows/:id/revisions/:rev":
		rev, ok := h.repo.Revision(c.Param("rev"))
		if !ok || rev.EntityID != id {
			c.JSON(404, map[string]string{"error": "revision not found"})
			return
		}
		c.JSON(200, rev)
	case "/api/workflows/:id/revisions/:rev/restore":
		revision, exists := h.repo.Revision(c.Param("rev"))
		if !exists || revision.EntityID != id {
			c.JSON(404, gin.H{"error": "revision not found"})
			return
		}
		var restored domain.WorkflowDefinition
		if json.Unmarshal(revision.Payload, &restored) != nil {
			c.JSON(400, gin.H{"error": "invalid workflow revision"})
			return
		}
		if workflowHasNotifications(restored) && !h.notifications.access(c, v.ProjectID, true) {
			return
		}
		stored, err := h.repo.RestoreWorkflowRevision(c.Param("rev"))
		if err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, stored)
	case "/api/workflows/:id/agents":
		template, ok := h.repo.GenericTemplateByWorkflow(id)
		if !ok {
			c.JSON(http.StatusNotFound, map[string]string{"error": "generic template not found"})
			return
		}
		if c.Request.Method == http.MethodGet {
			c.JSON(200, template.Agents)
			return
		}
		if !h.isPlatformAdmin(c.Request) {
			c.JSON(http.StatusConflict, map[string]string{"error": "只有平台管理员可以编辑通用模板角色"})
			return
		}
		var agent domain.AgentDefinition
		if !decode(c, &agent) {
			return
		}
		if strings.TrimSpace(agent.ExecutorID) == "" {
			agent.ExecutorID = "codebuddy"
		}
		builtIn, ok := repo.BuiltInExecutor(agent.ExecutorID)
		if !ok {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "CLI 仅支持 codebuddy、codex 或 claude"})
			return
		}
		if err := domain.ValidateAgentEffort(agent.Effort, builtIn.Kind); err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := domain.ValidateVerificationGates(agent.Verification); err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		saved, err := h.repo.UpdateGenericAgent(id, agent)
		if err != nil {
			if strings.Contains(err.Error(), "不属于") {
				c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			respond(c, err)
			return
		}
		c.JSON(200, saved)
	case "/api/workflows/:id/publish":
		if v.Generic {
			if !h.isPlatformAdmin(c.Request) {
				c.JSON(http.StatusConflict, map[string]string{"error": "通用模板为只读，请先复制到本项目再修改"})
				return
			}
			stored, err := h.repo.PublishGenericWorkflow(id)
			if err != nil {
				respond(c, err)
				return
			}
			c.JSON(200, stored)
			return
		}
		v.Status = "published"
		v.Version++
		if err := h.repo.PutWorkflow(v); err != nil {
			respond(c, err)
			return
		}
		stored, _ := h.repo.Workflow(id)
		c.JSON(200, stored)
	case "/api/workflows/:id/clone":
		var req struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Owner     string `json:"owner"`
			ProjectID string `json:"projectId"`
		}
		if !decode(c, &req) {
			return
		}
		if req.ID == "" || req.Name == "" {
			c.JSON(400, map[string]string{"error": "id and name are required"})
			return
		}
		// A generic template id is reserved: no clone, whether its source is a
		// regular SOP or the template itself, may occupy it, otherwise the
		// store row would shadow the built-in catalog entry.
		if _, taken := h.repo.GenericTemplateByWorkflow(req.ID); taken {
			c.JSON(http.StatusConflict, map[string]string{"error": "该 ID 与平台通用模板冲突，请换一个 ID"})
			return
		}
		taken, _ := h.repo.Workflow(req.ID)
		if !h.requireProjectWriter(c, strings.TrimSpace(req.ProjectID), taken.ProjectID) {
			return
		}
		if v.Generic {
			stored, err := h.adoptGenericWorkflow(v, req.ID, req.Name, req.Owner, req.ProjectID)
			if err != nil {
				respond(c, err)
				return
			}
			c.JSON(http.StatusCreated, stored)
			return
		}
		v.ID, v.Name, v.Owner = req.ID, req.Name, req.Owner
		v.ProjectID = strings.TrimSpace(req.ProjectID)
		v.Shared = false
		v.Status, v.Version, v.Revision = "draft", 1, 0
		v.CreatedAt, v.UpdatedAt = time.Time{}, time.Time{}
		if err := h.repo.PutWorkflow(v); err != nil {
			respond(c, err)
			return
		}
		stored, _ := h.repo.Workflow(v.ID)
		c.JSON(201, stored)
	default:
		c.Status(http.StatusNotFound)
	}
}

func workflowHasNotifications(workflow domain.WorkflowDefinition) bool {
	if len(workflow.Notifications) > 0 {
		return true
	}
	for _, node := range workflow.Nodes {
		if len(node.Notifications) > 0 {
			return true
		}
	}
	return false
}

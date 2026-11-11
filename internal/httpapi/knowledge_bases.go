package httpapi

// 知识中心管理接口：本项目知识库与来源、托管文档、公开 Git 来源登记、
// 触发同步、试搜与读原文。handler 挂已有的 knowledgeHandlers（不新增
// struct），业务规则在 knowledge.Service 的新方法里；引擎由本文件从
// h.repo 装配，不经 S7 负责的设置/拨号代码。

import (
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/knowledge"
	kbengine "agentflow/internal/knowledge/engine"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// newKnowledgeKeywordIndex 是关键词适配器的装配点：生产用 MySQL ngram 全文
// 索引；没有 MySQL 全文索引的环境（sqlite 测试）可替换为内存假适配器。
// 引擎的其余部分（写正文、切块、读原文）始终是真的，只有检索召回可替换。
var newKnowledgeKeywordIndex = func(s *repo.Store) kbengine.KeywordIndex {
	return kbengine.NewMySQLKeywordIndex(s)
}

// kbUpstream 从控制面/内容面 Repo 装配内置引擎。*repo.Store 同时满足
// ControlStore、ContentStore 与 FulltextQuerier，无需往 deps 加字段。
func (h *knowledgeHandlers) kbUpstream() knowledge.Upstream {
	return kbengine.NewEngine(h.repo, h.repo, newKnowledgeKeywordIndex(h.repo))
}

// 路由: g.GET("/api/knowledge/bases", h.basesOverview)
//
// 打开知识中心时调用：幂等保证托管库，并返回本项目的知识库与来源列表。
func (h *knowledgeHandlers) basesOverview(c *gin.Context) {
	if !h.requireManagementScope(c, c.Query("projectId")) {
		return
	}
	out, err := h.svc.Overview(c.Query("projectId"))
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/bases/documents", h.basesDocuments)
//
//	g.POST("/api/knowledge/bases/documents", h.basesDocuments)
func (h *knowledgeHandlers) basesDocuments(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		if !h.requireManagementScope(c, c.Query("projectId")) {
			return
		}
		list, err := h.svc.DocumentList(c.Query("projectId"), c.Query("source"))
		render(c, list, err)
		return
	}
	var input struct {
		ProjectID string `json:"projectId"`
		SourceID  string `json:"sourceId"`
		Path      string `json:"path"`
		Content   string `json:"content"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.requireManagementScope(c, input.ProjectID) {
		return
	}
	out, err := h.svc.CreateHostedDocument(
		c.Request.Context(), h.kbUpstream(), input.ProjectID, input.SourceID, input.Path, input.Content,
	)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/bases/document", h.basesDocument)
//
// 读原文抽屉：读已索引版本并按行截取，出处版本是修订号或 commit。
func (h *knowledgeHandlers) basesDocument(c *gin.Context) {
	if !h.requireManagementScope(c, c.Query("projectId")) {
		return
	}
	start, _ := strconv.Atoi(c.Query("start"))
	end, _ := strconv.Atoi(c.Query("end"))
	doc, err := h.svc.ReadDocument(
		c.Request.Context(), h.kbUpstream(),
		c.Query("projectId"), c.Query("source"), c.Query("path"), start, end,
	)
	if err != nil {
		render(c, doc, err)
		return
	}
	fresh := h.svc.JudgeFreshness(c.Query("projectId"), doc.Source, doc.Path, c.Query("commit"))
	doc.Freshness = fresh.Status
	if fresh.Status == knowledge.FreshnessChanged {
		doc.CurrentVersion = fresh.Current
	}
	render(c, doc, nil)
}

// 路由: g.POST("/api/knowledge/bases/sources", h.basesSources)
//
// 登记 Git 来源（地址、分支、路径前缀），状态置为待同步。
func (h *knowledgeHandlers) basesSources(c *gin.Context) {
	var input knowledge.GitSourceInput
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	src, err := h.svc.RegisterGitSource(input)
	render(c, src, err)
}

// 路由: g.DELETE("/api/knowledge/bases/sources", h.basesSourceDelete)
//
// 删除不再需要的 Git 来源：先删内容面正文/修订/索引块，再删控制面记录。
// 托管来源没有删除入口，后端也拒绝。
func (h *knowledgeHandlers) basesSourceDelete(c *gin.Context) {
	if !h.requireKnowledgeManager(c, c.Query("projectId")) {
		return
	}
	if err := h.svc.DeleteSource(c.Query("projectId"), c.Query("sourceId")); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// 路由: g.POST("/api/knowledge/bases/sync", h.basesSync)
//
// 同步按钮只把来源状态置为待同步；实际同步由 worker 循环（S6）执行。
func (h *knowledgeHandlers) basesSync(c *gin.Context) {
	var input struct {
		ProjectID string `json:"projectId"`
		SourceID  string `json:"sourceId"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	src, err := h.svc.TriggerSync(input.ProjectID, input.SourceID)
	render(c, src, err)
}

// 路由: g.POST("/api/knowledge/bases/search", h.basesSearch)
//
// 试搜：在当前项目授权来源里做关键词检索，返回结果与出处。
func (h *knowledgeHandlers) basesSearch(c *gin.Context) {
	var input struct {
		Query     string   `json:"query"`
		ProjectID string   `json:"projectId"`
		Sources   []string `json:"sources"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.requireManagementScope(c, input.ProjectID) {
		return
	}
	out, err := h.svc.TrialSearch(c.Request.Context(), h.kbUpstream(), input.ProjectID, input.Query, input.Sources)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/bases/vector", h.basesVector)
//
//	g.POST("/api/knowledge/bases/vector", h.basesVector)
//
// 某个知识库启用/停用向量检索。回显部署级向量状态与该知识库行；试搜结果
// 里的 mode / degraded 会随这里的开关变化。启用会即时把现有来源增量写进
// 向量库，停用会清掉该库来源的向量点。只有平台管理员可写。
func (h *knowledgeHandlers) basesVector(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, gin.H{"status": kbengine.CurrentVectorStatus()})
		return
	}
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以修改向量检索配置"})
		return
	}
	var input struct {
		ProjectID string `json:"projectId"`
		BaseID    string `json:"baseId"`
		Enabled   bool   `json:"enabled"`
	}
	if !decode(c, &input) {
		return
	}
	baseID := strings.TrimSpace(input.BaseID)
	base, ok := h.repo.KnowledgeBase(baseID)
	if !ok || base.ProjectID != strings.TrimSpace(input.ProjectID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "知识库不属于当前项目"})
		return
	}
	var err error
	if input.Enabled {
		err = kbengine.EnableVectorForBase(c.Request.Context(), baseID)
	} else {
		err = kbengine.DisableVectorForBase(c.Request.Context(), baseID)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, _ := h.repo.KnowledgeBase(baseID)
	c.JSON(http.StatusOK, gin.H{"status": kbengine.CurrentVectorStatus(), "base": updated})
}

func (h *knowledgeHandlers) requireKnowledgeManager(c *gin.Context, projectID string) bool {
	if strings.TrimSpace(projectID) == "" {
		return h.requireSharedAdmin(c)
	}
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
	if project, found := h.repo.Project(strings.TrimSpace(projectID)); found {
		if project.OwnerID == user.ID {
			return true
		}
		for _, member := range project.Members {
			if member.UserID == user.ID && !strings.EqualFold(strings.TrimSpace(member.Role), "viewer") {
				return true
			}
		}
	}
	c.JSON(403, gin.H{"error": "只有项目成员、项目 Owner 或平台管理员可以管理知识库来源凭据"})
	return false
}

func (h *knowledgeHandlers) basesSourceCredential(c *gin.Context) {
	var input struct {
		ProjectID  string                  `json:"projectId"`
		SourceID   string                  `json:"sourceId"`
		Credential knowledge.GitCredential `json:"credential"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	out, err := h.svc.SetSourceCredential(input.ProjectID, input.SourceID, input.Credential)
	render(c, out, err)
}

package httpapi

// 知识中心管理接口：本项目知识库与来源、托管文档、公开 Git 来源登记、
// 触发同步、试搜与读原文。handler 挂已有的 knowledgeHandlers（不新增
// struct），业务规则在 knowledge.Service 的新方法里；引擎由本文件从
// h.repo 装配，不经 S7 负责的设置/拨号代码。

import (
	"net/http"
	"strconv"

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
	out, err := h.svc.Overview(c.Query("projectId"))
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/bases/documents", h.basesDocuments)
//
//	g.POST("/api/knowledge/bases/documents", h.basesDocuments)
func (h *knowledgeHandlers) basesDocuments(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
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
	out, err := h.svc.CreateHostedDocument(
		c.Request.Context(), h.kbUpstream(), input.ProjectID, input.SourceID, input.Path, input.Content,
	)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/bases/document", h.basesDocument)
//
// 读原文抽屉：读已索引版本并按行截取，出处版本是修订号或 commit。
func (h *knowledgeHandlers) basesDocument(c *gin.Context) {
	start, _ := strconv.Atoi(c.Query("start"))
	end, _ := strconv.Atoi(c.Query("end"))
	doc, err := h.svc.ReadDocument(
		c.Request.Context(), h.kbUpstream(),
		c.Query("projectId"), c.Query("source"), c.Query("path"), start, end,
	)
	render(c, doc, err)
}

// 路由: g.POST("/api/knowledge/bases/sources", h.basesSources)
//
// 登记公开 Git 来源（地址、分支、路径前缀），状态置为待同步。
func (h *knowledgeHandlers) basesSources(c *gin.Context) {
	var input knowledge.GitSourceInput
	if !decode(c, &input) {
		return
	}
	src, err := h.svc.RegisterGitSource(input)
	render(c, src, err)
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
	out, err := h.svc.TrialSearch(c.Request.Context(), h.kbUpstream(), input.ProjectID, input.Query, input.Sources)
	render(c, out, err)
}

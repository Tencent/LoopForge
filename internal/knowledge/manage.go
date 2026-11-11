package knowledge

// 知识中心管理面：托管库的幂等保证、来源与文档清单、登记公开 Git 来源、
// 触发同步，以及试搜/读原文的授权收窄。业务规则放在 Service 的新方法里，
// 不改 service.go 里已有的设置/拨号代码。需要引擎的操作由调用方把
// Upstream 传进来（knowledge 包不能 import engine，否则循环依赖）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// 来源状态取值，写在控制面 knowledge_sources.status。触发同步只把状态置为
// 待同步；同步执行由 worker 循环负责，结束后写成就绪或错误。
const (
	SourceStatusPending = "pending" // 待同步
	SourceStatusSyncing = "syncing" // 同步中
	SourceStatusReady   = "ready"   // 就绪
	SourceStatusError   = "error"   // 错误
)

// 托管知识库用从 projectID 推导的定值 id：同一项目并发或重复创建都只落一行，
// 不依赖数据库唯一约束报错的翻译。控制面另有 (project_id, layer) 唯一索引兜底。
const (
	hostedBaseIDPrefix   = "kb-"
	hostedSourceIDPrefix = "kb-hosted-"
)

func hostedBaseID(projectID string) string   { return hostedBaseIDPrefix + projectID }
func hostedSourceID(projectID string) string { return hostedSourceIDPrefix + projectID }

// ManagedStore 是管理接口需要的持久化能力。*repo.Store 通过内嵌的
// KnowledgeRepo / KnowledgeContentRepo 满足它；Service 用类型断言从 s.store
// 取出，不改 Store 接口、不往 deps 加字段。
type ManagedStore interface {
	KnowledgeBases(projectID string) []domain.KnowledgeBase
	KnowledgeBase(id string) (domain.KnowledgeBase, bool)
	PutKnowledgeBase(domain.KnowledgeBase) error
	KnowledgeBindings(projectID string) []domain.KnowledgeBinding
	PutKnowledgeBinding(domain.KnowledgeBinding) error
	KnowledgeSources(baseID string) []domain.KnowledgeSource
	KnowledgeSource(id string) (domain.KnowledgeSource, bool)
	PutKnowledgeSource(domain.KnowledgeSource) error
	DeleteKnowledgeSource(id string) error
	DeleteKnowledgeSourceContent(sourceID string) error
	KnowledgeDocuments(sourceID string) []domain.KnowledgeDocument
}

func (s *Service) managed() (ManagedStore, error) {
	ms, ok := s.store.(ManagedStore)
	if !ok {
		return nil, apperr.New(apperr.Unavailable, "知识库管理存储不可用")
	}
	return ms, nil
}

// EnsureHostedBase 保证 projectID 有且只有一个项目层托管知识库，并把托管
// 来源与绑定补齐。建项目后与打开知识中心时都调用它；定值 id + 存在性检查
// 让并发调用也只产生一个库，且重复打开不会把托管来源的修订号/篇数清零。
func (s *Service) EnsureHostedBase(projectID string) (domain.KnowledgeBase, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.KnowledgeBase{}, apperr.New(apperr.Invalid, "项目标识不能为空")
	}
	ms, err := s.managed()
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	baseID := hostedBaseID(projectID)
	base, found := ms.KnowledgeBase(baseID)
	if !found {
		base = domain.KnowledgeBase{ID: baseID, ProjectID: projectID, Name: "项目知识库", Layer: "project"}
		if err := ms.PutKnowledgeBase(base); err != nil {
			// 并发创建：主键或 (project_id, layer) 冲突后重新读取已存在的行。
			if existing, ok := ms.KnowledgeBase(baseID); ok {
				base = existing
			} else {
				return domain.KnowledgeBase{}, err
			}
		}
	}
	sourceID := hostedSourceID(projectID)
	if _, ok := ms.KnowledgeSource(sourceID); !ok {
		src := domain.KnowledgeSource{
			ID: sourceID, BaseID: baseID, Kind: "hosted", Name: "托管文档",
			Status: SourceStatusReady, IndexStatus: SourceStatusReady,
		}
		if err := ms.PutKnowledgeSource(src); err != nil {
			return domain.KnowledgeBase{}, err
		}
	}
	// 绑定用主键 upsert，重复写无害。
	if err := ms.PutKnowledgeBinding(domain.KnowledgeBinding{
		ProjectID: projectID, BaseID: baseID, Permission: "submit",
	}); err != nil {
		return domain.KnowledgeBase{}, err
	}
	return base, nil
}

// ManagedSourceView 是来源的管理视图：展示同步状态与错误，去掉租约等内部
// 字段。名字与 service.go 里面向设置页的 SourceView 区分开。
type ManagedSourceView struct {
	ID           string     `json:"id"`
	BaseID       string     `json:"baseId"`
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	GitURL       string     `json:"gitUrl,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	PathPrefix   string     `json:"pathPrefix,omitempty"`
	HeadCommit   string     `json:"headCommit,omitempty"`
	HeadRevision int64      `json:"headRevision,omitempty"`
	Status       string     `json:"status,omitempty"`
	IndexStatus  string     `json:"indexStatus,omitempty"`
	DocCount     int        `json:"docCount,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	LastSyncedAt *time.Time `json:"lastSyncedAt,omitempty"`
}

func managedSourceView(src domain.KnowledgeSource) ManagedSourceView {
	return ManagedSourceView{
		ID: src.ID, BaseID: src.BaseID, Kind: src.Kind, Name: src.Name,
		GitURL: src.GitURL, Branch: src.Branch, PathPrefix: src.PathPrefix,
		HeadCommit: src.HeadCommit, HeadRevision: src.HeadRevision,
		Status: src.Status, IndexStatus: src.IndexStatus, DocCount: src.DocCount,
		LastError: src.LastError, LastSyncedAt: src.LastSyncedAt,
	}
}

// BaseView 是单个知识库及其来源清单。
type BaseView struct {
	domain.KnowledgeBase
	Sources []ManagedSourceView `json:"sources"`
}

// Overview 是知识中心打开时的第一个响应。
type Overview struct {
	Bases []BaseView `json:"bases"`
}

// Overview 幂等保证托管库后返回本项目的知识库与来源列表。
func (s *Service) Overview(projectID string) (Overview, error) {
	if _, err := s.EnsureHostedBase(projectID); err != nil {
		return Overview{}, err
	}
	ms, err := s.managed()
	if err != nil {
		return Overview{}, err
	}
	out := Overview{Bases: []BaseView{}}
	for _, b := range ms.KnowledgeBases(strings.TrimSpace(projectID)) {
		bv := BaseView{KnowledgeBase: b, Sources: []ManagedSourceView{}}
		for _, src := range ms.KnowledgeSources(b.ID) {
			bv.Sources = append(bv.Sources, managedSourceView(src))
		}
		out.Bases = append(out.Bases, bv)
	}
	return out, nil
}

// allowedSourceIDs 返回项目绑定的知识库下面的来源 id 集合。空集合表示该项
// 目没有可查来源，检索结果为空、读原文拒绝。
func allowedSourceIDs(ms ManagedStore, projectID string) []string {
	projectID = strings.TrimSpace(projectID)
	seen := map[string]bool{}
	out := []string{}
	for _, binding := range ms.KnowledgeBindings(projectID) {
		base, ok := ms.KnowledgeBase(binding.BaseID)
		if !ok || (base.ProjectID != "" && base.ProjectID != projectID) {
			continue
		}
		for _, src := range ms.KnowledgeSources(binding.BaseID) {
			if !seen[src.ID] {
				seen[src.ID] = true
				out = append(out, src.ID)
			}
		}
	}
	return out
}

func sourceInProject(ms ManagedStore, projectID, source string) bool {
	source = strings.TrimSpace(source)
	for _, id := range allowedSourceIDs(ms, projectID) {
		if id == source {
			return true
		}
	}
	return false
}

// scopeToProject 把请求点名的来源收窄到项目授权范围内：空请求扩展到全部
// 授权来源，点名范围外来源则拒绝。不复用会在切片里改语义的 ScopeSources。
func scopeToProject(allowed, requested []string) ([]string, error) {
	set := map[string]bool{}
	clean := make([]string, 0, len(allowed))
	for _, id := range allowed {
		id = strings.TrimSpace(id)
		if id != "" && !set[id] {
			set[id] = true
			clean = append(clean, id)
		}
	}
	if len(requested) == 0 {
		return clean, nil
	}
	out := make([]string, 0, len(requested))
	seen := map[string]bool{}
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if !set[id] {
			return nil, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// TrialResult 是一次试搜的结果与来源范围。
type TrialResult struct {
	Result    SearchResult `json:"result"`
	Sources   []string     `json:"sources"`
	LatencyMs int64        `json:"latencyMs"`
}

// TrialSearch 在项目授权来源里做一次试搜。
func (s *Service) TrialSearch(ctx context.Context, up Upstream, projectID, query string, sources []string) (TrialResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return TrialResult{}, apperr.New(apperr.Invalid, "请输入要检索的问题或关键词")
	}
	ms, err := s.managed()
	if err != nil {
		return TrialResult{}, err
	}
	scoped, err := scopeToProject(allowedSourceIDs(ms, projectID), sources)
	if err != nil {
		return TrialResult{}, err
	}
	started := time.Now()
	result, err := up.Search(ctx, query, scoped)
	if err != nil {
		return TrialResult{}, apperr.New(apperr.Upstream, "检索失败："+err.Error())
	}
	return TrialResult{Result: result, Sources: scoped, LatencyMs: time.Since(started).Milliseconds()}, nil
}

// ReadDocument 读已索引版本的原文，按行截取。点名其它项目的来源会被拒绝。
func (s *Service) ReadDocument(ctx context.Context, up Upstream, projectID, source, path string, start, end int) (Document, error) {
	source, path = strings.TrimSpace(source), strings.TrimSpace(path)
	if source == "" || path == "" {
		return Document{}, apperr.New(apperr.Invalid, "source 和 path 必填")
	}
	ms, err := s.managed()
	if err != nil {
		return Document{}, err
	}
	if !sourceInProject(ms, projectID, source) {
		return Document{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	doc, err := up.Fetch(ctx, source, path, start, end)
	if err != nil {
		return Document{}, apperr.New(apperr.Upstream, "读取原文失败："+err.Error())
	}
	return doc, nil
}

// CreateHostedDocument 在项目的托管来源里新建或覆盖一篇 Markdown。
func (s *Service) CreateHostedDocument(ctx context.Context, up Upstream, projectID, source, path, content string) (WriteResult, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return WriteResult{}, apperr.New(apperr.Invalid, "请选择来源")
	}
	ms, err := s.managed()
	if err != nil {
		return WriteResult{}, err
	}
	if !sourceInProject(ms, projectID, source) {
		return WriteResult{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(source)
	if !ok || src.Kind != "hosted" {
		return WriteResult{}, apperr.New(apperr.Invalid, "只能在托管来源新建或覆盖文档")
	}
	if strings.TrimSpace(content) == "" {
		return WriteResult{}, apperr.New(apperr.Invalid, "正文不能为空")
	}
	result, err := up.WriteDocument(ctx, source, path, content)
	if err != nil {
		return WriteResult{}, apperr.New(apperr.Invalid, err.Error())
	}
	return result, nil
}

// DocumentMeta 是来源里一篇文档的清单项（不含正文）。
type DocumentMeta struct {
	Path      string `json:"path"`
	Title     string `json:"title,omitempty"`
	LineCount int    `json:"lineCount,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
}

// DocumentList 列出某来源的文档清单，供读原文抽屉浏览。
func (s *Service) DocumentList(projectID, source string) ([]DocumentMeta, error) {
	ms, err := s.managed()
	if err != nil {
		return nil, err
	}
	if !sourceInProject(ms, projectID, source) {
		return nil, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	out := []DocumentMeta{}
	for _, d := range ms.KnowledgeDocuments(strings.TrimSpace(source)) {
		out = append(out, DocumentMeta{
			Path: d.Path, Title: d.Title, LineCount: d.LineCount, Commit: d.Commit, Revision: d.Revision,
		})
	}
	return out, nil
}

// GitSourceInput 是登记公开 Git 来源的入参。切片只支持不需要凭据的公开
// HTTPS 地址；路径前缀限定只索引此前缀下的 Markdown。
type GitSourceInput struct {
	ProjectID  string `json:"projectId"`
	BaseID     string `json:"baseId"`
	Name       string `json:"name"`
	GitURL     string `json:"gitUrl"`
	Branch     string `json:"branch"`
	PathPrefix string `json:"pathPrefix"`
}

func validatePublicGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", apperr.New(apperr.Invalid, "Git 地址必须是公开 http(s) 仓库地址")
	}
	if u.User != nil {
		return "", apperr.New(apperr.Invalid, "Git 地址不能包含凭据")
	}
	return raw, nil
}

// RegisterGitSource 登记一个公开 Git 来源，状态置为待同步，由 worker 执行同步。
// 定值 id 让相同配置重复登记只更新同一行，不产生第二个来源。
func (s *Service) RegisterGitSource(input GitSourceInput) (domain.KnowledgeSource, error) {
	projectID := strings.TrimSpace(input.ProjectID)
	gitURL, err := validatePublicGitURL(input.GitURL)
	if err != nil {
		return domain.KnowledgeSource{}, err
	}
	ms, err := s.managed()
	if err != nil {
		return domain.KnowledgeSource{}, err
	}
	baseID := strings.TrimSpace(input.BaseID)
	base, ok := ms.KnowledgeBase(baseID)
	if !ok || base.ProjectID != projectID {
		return domain.KnowledgeSource{}, apperr.New(apperr.Forbidden, "知识库不属于当前项目")
	}
	branch := strings.TrimSpace(input.Branch)
	if branch == "" {
		branch = "main"
	}
	prefix, ok := NormalizePathPrefix(input.PathPrefix)
	if !ok {
		return domain.KnowledgeSource{}, apperr.New(apperr.Invalid, "路径前缀必须是仓库内的相对目录")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "Git 来源"
	}
	seed := projectID + "\x00" + gitURL + "\x00" + branch + "\x00" + prefix
	sum := sha256.Sum256([]byte(seed))
	id := "kb-git-" + hex.EncodeToString(sum[:8])
	src := domain.KnowledgeSource{
		ID: id, BaseID: baseID, Kind: "git", Name: name,
		GitURL: gitURL, Branch: branch, PathPrefix: prefix,
		Status: SourceStatusPending,
	}
	if err := ms.PutKnowledgeSource(src); err != nil {
		return domain.KnowledgeSource{}, err
	}
	return src, nil
}

// TriggerSync 只把 Git 来源状态置为待同步并清空上次错误；同步执行归 worker
// 循环（S6），这里不克隆、不索引、不碰工作副本。
func (s *Service) TriggerSync(projectID, sourceID string) (domain.KnowledgeSource, error) {
	ms, err := s.managed()
	if err != nil {
		return domain.KnowledgeSource{}, err
	}
	if !sourceInProject(ms, projectID, sourceID) {
		return domain.KnowledgeSource{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(strings.TrimSpace(sourceID))
	if !ok {
		return domain.KnowledgeSource{}, apperr.New(apperr.NotFound, "来源不存在")
	}
	if src.Kind != "git" {
		return domain.KnowledgeSource{}, apperr.New(apperr.Invalid, "托管来源不需要同步")
	}
	src.Status = SourceStatusPending
	src.LastError = ""
	src.LeaseOwner = ""
	src.LeaseUntil = nil
	if err := ms.PutKnowledgeSource(src); err != nil {
		return domain.KnowledgeSource{}, err
	}
	return src, nil
}

// DeleteSource 先删内容面正文/修订/索引块，再删控制面来源记录。托管来源
// 是项目知识库的事实源，不从知识中心删除。
func (s *Service) DeleteSource(projectID, sourceID string) error {
	ms, err := s.managed()
	if err != nil {
		return err
	}
	sourceID = strings.TrimSpace(sourceID)
	if !sourceInProject(ms, projectID, sourceID) {
		return apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(sourceID)
	if !ok {
		return apperr.New(apperr.NotFound, "来源不存在")
	}
	if src.Kind == "hosted" {
		return apperr.New(apperr.Invalid, "托管来源不能删除")
	}
	if err := ms.DeleteKnowledgeSourceContent(sourceID); err != nil {
		return apperr.New(apperr.Upstream, "删除来源内容失败："+err.Error())
	}
	if err := ms.DeleteKnowledgeSource(sourceID); err != nil {
		return apperr.New(apperr.Upstream, "删除来源失败："+err.Error())
	}
	return nil
}

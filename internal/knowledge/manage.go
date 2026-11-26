package knowledge

// 知识中心管理面：托管库的幂等保证、来源与文档清单、登记 Git 来源、
// 触发同步，以及试搜/读原文的授权收窄。业务规则放在 Service 的新方法里，
// 不改 service.go 里已有的设置/拨号代码。需要引擎的操作由调用方把
// Upstream 传进来（knowledge 包不能 import engine，否则循环依赖）。

import (
	"agentflow/internal/knowledge/gitremote"
	"agentflow/internal/knowledge/publication"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// Keep existing short base IDs; hash long project identifiers to fit VARCHAR(64).
func hostedBaseID(projectID string) string {
	if len("kb-"+projectID) <= 64 {
		return "kb-" + projectID
	}
	return stableKnowledgeID("kb-project-", "project:"+projectID)
}

func stableKnowledgeID(prefix, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return prefix + hex.EncodeToString(sum[:16])
}

// HostedSourceID uses the owning base, independently of project/shared scope.
func HostedSourceID(baseID string) string    { return stableKnowledgeID("kb-hosted-", "hosted:"+baseID) }
func hostedSourceID(projectID string) string { return HostedSourceID(hostedBaseID(projectID)) }

// knowledgeSchemaRepairError is returned when the control plane still carries
// pre-054 IDs. The fix is deliberately an explicit operator action, not an
// in-process rename, because 054 also moves content rows and fences.
func knowledgeSchemaRepairError(subject string) error {
	return apperr.New(apperr.Unavailable, "检测到"+subject+"仍使用 054 之前的标识。请先停止所有 API/worker 实例，执行 bash scripts/knowledge-schema.sh apply 完成迁移后再启动；平台不会在此状态下新建第二个托管来源")
}

// ManagedStore 是管理接口需要的持久化能力。*repo.Store 通过内嵌的
// KnowledgeRepo / KnowledgeContentRepo 满足它；Service 用类型断言从 s.store
// 取出，不改 Store 接口、不往 deps 加字段。
type ManagedStore interface {
	KnowledgeBases(projectID string) []domain.KnowledgeBase
	KnowledgeBase(id string) (domain.KnowledgeBase, bool)
	PutKnowledgeBase(domain.KnowledgeBase) error
	KnowledgeBindings(projectID string) []domain.KnowledgeBinding
	PutKnowledgeBinding(domain.KnowledgeBinding) error
	DeleteKnowledgeBinding(projectID, baseID string) error
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
	if _, exists := ms.KnowledgeBase(projectID); exists {
		if own, found := ms.KnowledgeBase(hostedBaseID(projectID)); !found || !ownsBase(own, projectID) {
			return domain.KnowledgeBase{}, apperr.New(apperr.Conflict, "项目标识与已有知识库标识冲突")
		}
	}
	baseID := hostedBaseID(projectID)
	if legacyID := "kb-" + projectID; legacyID != baseID {
		if legacy, ok := ms.KnowledgeBase(legacyID); ok && ownsBase(legacy, projectID) {
			return domain.KnowledgeBase{}, knowledgeSchemaRepairError("项目托管知识库")
		}
	}
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
	if !ownsBase(base, projectID) {
		return domain.KnowledgeBase{}, apperr.New(apperr.Forbidden, "知识库不属于当前项目")
	}
	sourceID := HostedSourceID(base.ID)
	existingSource, sourceFound := ms.KnowledgeSource(sourceID)
	if sourceFound && (existingSource.BaseID != base.ID || existingSource.Kind != "hosted") {
		return domain.KnowledgeBase{}, apperr.New(apperr.Conflict, "托管来源标识冲突")
	}
	// Before 054 the hosted source ID was "kb-hosted-<projectID>". A new binary
	// must never create the hash-based replacement next to it (or silently use
	// it while the old row remains): the project would then have two hosted
	// sources and old documents would become unreachable.
	if legacySource, ok := ms.KnowledgeSource("kb-hosted-" + projectID); ok && legacySource.Kind == "hosted" && legacySource.BaseID == base.ID {
		return domain.KnowledgeBase{}, knowledgeSchemaRepairError("旧版托管来源")
	}
	if !sourceFound {
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
	CredentialConfigured bool       `json:"credentialConfigured"`
	ID                   string     `json:"id"`
	BaseID               string     `json:"baseId"`
	Kind                 string     `json:"kind"`
	Name                 string     `json:"name"`
	GitURL               string     `json:"gitUrl,omitempty"`
	Branch               string     `json:"branch,omitempty"`
	PathPrefix           string     `json:"pathPrefix,omitempty"`
	HeadCommit           string     `json:"headCommit,omitempty"`
	HeadRevision         int64      `json:"headRevision,omitempty"`
	Status               string     `json:"status,omitempty"`
	IndexStatus          string     `json:"indexStatus,omitempty"`
	DocCount             int        `json:"docCount,omitempty"`
	LastError            string     `json:"lastError,omitempty"`
	LastSyncedAt         *time.Time `json:"lastSyncedAt,omitempty"`
	MetadataChecks       bool       `json:"metadataChecks"`
	SyncStage            string     `json:"syncStage,omitempty"`
	// 向量重建进度只在处理同步的进程内可读，其他实例保持空值。
	VectorCurrent int    `json:"vectorCurrent,omitempty"`
	VectorTotal   int    `json:"vectorTotal,omitempty"`
	VectorPhase   string `json:"vectorPhase,omitempty"`
}

func managedSourceView(src domain.KnowledgeSource) ManagedSourceView {
	return ManagedSourceView{
		ID: src.ID, BaseID: src.BaseID, Kind: src.Kind, Name: src.Name,
		CredentialConfigured: src.CredentialConfigured,
		GitURL:               src.GitURL, Branch: src.Branch, PathPrefix: src.PathPrefix,
		HeadCommit: src.HeadCommit, HeadRevision: src.HeadRevision,
		Status: src.Status, IndexStatus: src.IndexStatus, DocCount: src.DocCount,
		LastError: src.LastError, LastSyncedAt: src.LastSyncedAt,
		MetadataChecks: src.MetadataChecks, SyncStage: src.SyncStage,
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
	if strings.TrimSpace(projectID) != "" {
		if _, err := s.EnsureHostedBase(projectID); err != nil {
			return Overview{}, err
		}
	}
	ms, err := s.managed()
	if err != nil {
		return Overview{}, err
	}
	out := Overview{Bases: []BaseView{}}
	for _, b := range visibleBases(ms, projectID) {
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
	for _, base := range visibleBases(ms, projectID) {
		for _, src := range ms.KnowledgeSources(base.ID) {
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
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := time.Now()
	result, err := up.Search(ctx, query, scoped)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return TrialResult{}, apperr.New(apperr.Upstream, "检索超时，请稍后重试")
		}
		return TrialResult{}, apperr.New(apperr.Upstream, "检索失败："+err.Error())
	}
	rawHits := len(result.Results)
	s.applyLifecycle(&result)
	s.annotateSearchVersions(&result)
	if rawHits == 0 {
		_ = s.RecordGap(projectID, query, GapActorFrom(ctx))
	}
	s.rankOrigins(&result)
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
	doc.BaseOrigin = s.sourceOrigin(source)
	if doc.URL == "" {
		doc.URL = s.SourceFileURL(doc.Source, doc.Commit, doc.Path, doc.StartLine, doc.EndLine)
	}
	s.annotateDocumentVersion(&doc)
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
	if !sourceWritable(ms, projectID, source) {
		return WriteResult{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(source)
	if !ok || src.Kind != "hosted" || source != HostedSourceID(src.BaseID) {
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

// GitSourceInput 登记 HTTPS 或 SSH 来源，凭据独立配置。
// 路径前缀限定只索引此前缀下的 Markdown。
type GitSourceInput struct {
	Credential *GitCredential `json:"credential,omitempty"`
	ProjectID  string         `json:"projectId"`
	BaseID     string         `json:"baseId"`
	Name       string         `json:"name"`
	GitURL     string         `json:"gitUrl"`
	Branch     string         `json:"branch"`
	PathPrefix string         `json:"pathPrefix"`
}

func validatePublicGitURL(raw string) (string, error) {
	_, err := gitremote.Parse(raw)
	if err != nil {
		return "", apperr.New(apperr.Invalid, "Git 地址必须是无内嵌凭据的 HTTPS 或 SSH 仓库地址")
	}
	return strings.TrimSpace(raw), nil
}

// RegisterGitSource 登记一个 Git 来源，状态置为待同步，由 worker 执行同步。
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
	if !ok || !ownsBase(base, projectID) {
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
	seed := "git:" + baseID + "\x00" + gitURL + "\x00" + branch + "\x00" + prefix
	id := stableKnowledgeID("kb-git-", seed)
	// Legacy Git IDs remain stable because source IDs authenticate encrypted
	// credentials. Reuse only an exact configuration under the verified owner.
	for _, existing := range ms.KnowledgeSources(baseID) {
		if existing.Kind == "git" && existing.GitURL == gitURL && existing.Branch == branch && existing.PathPrefix == prefix {
			id = existing.ID
			break
		}
	}
	if existing, found := ms.KnowledgeSource(id); found && existing.BaseID != baseID {
		return domain.KnowledgeSource{}, apperr.New(apperr.Conflict, "来源标识已被其他知识库使用")
	}
	src := domain.KnowledgeSource{
		ID: id, BaseID: baseID, Kind: "git", Name: name,
		GitURL: gitURL, Branch: branch, PathPrefix: prefix,
		Status: SourceStatusPending,
	}
	if input.Credential != nil {
		if err := validateGitCredential(*input.Credential, gitURL); err != nil {
			return domain.KnowledgeSource{}, apperr.New(apperr.Invalid, err.Error())
		}
		ciphertext, err := encryptGitCredential(src.ID, *input.Credential)
		if err != nil {
			return domain.KnowledgeSource{}, apperr.New(apperr.Invalid, err.Error())
		}
		writer, ok := ms.(interface {
			PutKnowledgeSourceWithCredential(domain.KnowledgeSource, string) error
		})
		if !ok {
			return domain.KnowledgeSource{}, apperr.New(apperr.Unavailable, "来源凭据存储不可用")
		}
		if err := writer.PutKnowledgeSourceWithCredential(src, ciphertext); err != nil {
			return domain.KnowledgeSource{}, apperr.New(apperr.Upstream, "保存来源凭据失败")
		}
		src.CredentialConfigured = true
	} else {
		if err := ms.PutKnowledgeSource(src); err != nil {
			return domain.KnowledgeSource{}, err
		}
		if saved, ok := ms.KnowledgeSource(src.ID); ok {
			src.CredentialConfigured = saved.CredentialConfigured
		}
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
	if !sourceWritable(ms, projectID, sourceID) {
		return domain.KnowledgeSource{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(strings.TrimSpace(sourceID))
	if !ok {
		return domain.KnowledgeSource{}, apperr.New(apperr.NotFound, "来源不存在")
	}
	if src.Kind != "git" {
		return domain.KnowledgeSource{}, apperr.New(apperr.Invalid, "托管来源不需要同步")
	}
	if queue, ok := ms.(interface{ RequestKnowledgeSync(string) error }); ok {
		if err := queue.RequestKnowledgeSync(src.ID); err != nil {
			return domain.KnowledgeSource{}, err
		}
		src, _ = ms.KnowledgeSource(src.ID)
	} else {
		src.Status = SourceStatusPending
		src.LastError = ""
		if err := ms.PutKnowledgeSource(src); err != nil {
			return domain.KnowledgeSource{}, err
		}
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
	if !sourceWritable(ms, projectID, sourceID) {
		return apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	src, ok := ms.KnowledgeSource(sourceID)
	if !ok {
		return apperr.New(apperr.NotFound, "来源不存在")
	}
	if src.Kind == "hosted" {
		return apperr.New(apperr.Invalid, "托管来源不能删除")
	}
	marker, marked := ms.(interface {
		BeginKnowledgeSourceDelete(string, string) (bool, error)
		DeleteKnowledgeSourceMarked(string, string) error
	})
	deleteOwner := "delete-" + NewID()
	deleteCtx := context.Background()
	if marked {
		ok, err := marker.BeginKnowledgeSourceDelete(sourceID, deleteOwner)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.New(apperr.Conflict, "来源正在同步或写回，请稍后再删除")
		}
		current, found := ms.KnowledgeSource(sourceID)
		if !found || current.LeaseOwner != deleteOwner {
			return apperr.New(apperr.Conflict, "来源删除租约已变更")
		}
		deleteCtx = publication.WithFence(deleteCtx, sourceID, current.LeaseVersion)
	} else if src.LeaseOwner != "" && src.LeaseUntil != nil && src.LeaseUntil.After(time.Now().UTC()) {
		return apperr.New(apperr.Conflict, "来源正在同步或写回，请稍后再删除")
	}
	if cleaner, ok := s.upstream.(interface {
		DeleteSourceIndex(context.Context, string) error
	}); ok {
		if err := cleaner.DeleteSourceIndex(context.Background(), sourceID); err != nil {
			return err
		}
	}
	var contentErr error
	if cleaner, ok := ms.(interface {
		DeleteKnowledgeSourceContentContext(context.Context, string) error
	}); ok {
		contentErr = cleaner.DeleteKnowledgeSourceContentContext(deleteCtx, sourceID)
	} else {
		contentErr = ms.DeleteKnowledgeSourceContent(sourceID)
	}
	if err := contentErr; err != nil {
		return apperr.New(apperr.Upstream, "删除来源内容失败："+err.Error())
	}
	if cleaner, ok := s.store.(interface {
		DeleteKnowledgeGovernanceSource(string) error
	}); ok {
		// 健康检查结果是派生数据；删除失败不阻断来源删除，下次扫描会重算。
		_ = cleaner.DeleteKnowledgeGovernanceSource(sourceID)
	}
	if deleter, ok := ms.(interface {
		DeleteKnowledgeWritebacksBySource(string) error
	}); ok {
		if err := deleter.DeleteKnowledgeWritebacksBySource(sourceID); err != nil {
			return apperr.New(apperr.Upstream, "删除来源写回记录失败："+err.Error())
		}
	}
	var deleteErr error
	if marked {
		deleteErr = marker.DeleteKnowledgeSourceMarked(sourceID, deleteOwner)
	} else {
		deleteErr = ms.DeleteKnowledgeSource(sourceID)
	}
	if err := deleteErr; err != nil {
		return apperr.New(apperr.Upstream, "删除来源失败："+err.Error())
	}
	return nil
}

// SetSourceCredential replaces the grant without ever reading it back to HTTP.
// The owning base authorizes changes, independently of read bindings.
func (s *Service) SetSourceCredential(projectID, sourceID string, credential GitCredential) (ManagedSourceView, error) {
	ms, err := s.managed()
	if err != nil {
		return ManagedSourceView{}, err
	}
	src, ok := ms.KnowledgeSource(strings.TrimSpace(sourceID))
	if !ok {
		return ManagedSourceView{}, apperr.New(apperr.NotFound, "来源不存在")
	}
	base, ok := ms.KnowledgeBase(src.BaseID)
	if !ok || !ownsBase(base, projectID) {
		return ManagedSourceView{}, apperr.New(apperr.Forbidden, "知识库不属于当前项目")
	}
	if src.Kind != "git" {
		return ManagedSourceView{}, apperr.New(apperr.Invalid, "托管来源无需 Git 凭据")
	}
	if err := validateGitCredential(credential, src.GitURL); err != nil {
		return ManagedSourceView{}, apperr.New(apperr.Invalid, err.Error())
	}
	ciphertext, err := encryptGitCredential(src.ID, credential)
	if err != nil {
		return ManagedSourceView{}, apperr.New(apperr.Invalid, err.Error())
	}
	writer, ok := ms.(interface{ SetKnowledgeSourceCredential(string, string) error })
	if !ok {
		return ManagedSourceView{}, apperr.New(apperr.Unavailable, "来源凭据存储不可用")
	}
	if err := writer.SetKnowledgeSourceCredential(src.ID, ciphertext); err != nil {
		return ManagedSourceView{}, apperr.New(apperr.Upstream, "替换来源凭据失败")
	}
	src.CredentialConfigured = true
	src.Status = SourceStatusPending
	src.LastError = ""
	return managedSourceView(src), nil
}

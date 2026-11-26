package knowledge

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/knowledge/publication"
	"agentflow/internal/mcpclient"
)

const CallTimeout = 30 * time.Second

// Store is the persistence the 知识库 module needs.
type Store interface {
	KnowledgeBindings(string) []domain.KnowledgeBinding
	KnowledgeSources(string) []domain.KnowledgeSource
	KnowledgeDocuments(string) []domain.KnowledgeDocument
	KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission
	KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool)
	KnowledgeSubmissionIDsByArtifact(artifactIDs []string) map[string]string
	PutKnowledgeSubmission(domain.KnowledgeSubmission) error
	DeleteKnowledgeSubmission(id string) error
	KnowledgeArtifacts(projectID string, limit int) []domain.KnowledgeCandidate
	Artifact(runID, artifactID string) (domain.Artifact, bool)
	RunHeader(id string) (domain.Run, error)
	Agents() []domain.AgentDefinition
	Workflows() []domain.WorkflowDefinition
}

// Upstream is the built-in knowledge engine boundary.
type Upstream interface {
	Tools(ctx context.Context) ([]mcpclient.Tool, error)
	ListSources(ctx context.Context) ([]Source, error)
	Search(ctx context.Context, query string, sources []string) (SearchResult, error)
	Fetch(ctx context.Context, source, path string, start, end int) (Document, error)
	WriteDocument(ctx context.Context, source, path, content string) (WriteResult, error)
}

// Service owns project source scoping and the review publishing queue.
type Service struct {
	store    Store
	upstream Upstream
	settings *domain.KnowledgeSettings
	now      func() time.Time
	newID    func() string
}

func NewService(store Store, newID func() string, upstream Upstream) *Service {
	return &Service{
		store:    store,
		upstream: upstream,
		now:      time.Now,
		newID:    newID,
	}
}

type CheckResult struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	LatencyMs int64    `json:"latencyMs"`
	Sources   []Source `json:"sources"`
	Tools     []string `json:"tools,omitempty"`
}

// Check checks the injected engine.
func (s *Service) Check(ctx context.Context) CheckResult {
	s = s.Snapshot()
	client, err := s.connect()
	return s.checkUpstream(ctx, client, err)
}

func (s *Service) checkUpstream(ctx context.Context, client Upstream, err error) CheckResult {
	if err != nil {
		return CheckResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := s.now()
	sources, err := client.ListSources(ctx)
	latency := s.now().Sub(started).Milliseconds()
	if err != nil {
		return CheckResult{Error: err.Error(), LatencyMs: latency}
	}
	if sources == nil {
		sources = []Source{}
	}
	tools := []string{}
	if list, err := client.Tools(ctx); err == nil {
		for _, tool := range list {
			tools = append(tools, tool.Name)
		}
	}
	return CheckResult{OK: true, LatencyMs: latency, Sources: sources, Tools: tools}
}

type SourceView struct {
	Source
	Allowed bool `json:"allowed"`
}

// Sources lists upstream sources, marking which ones projectID may read.
func (s *Service) Sources(ctx context.Context, projectID string) ([]SourceView, error) {
	s = s.Snapshot()
	client, err := s.connect()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	list, err := client.ListSources(ctx)
	if err != nil {
		return nil, apperr.New(apperr.Upstream, "知识库不可达："+err.Error())
	}
	allowed := s.AllowedSources(projectID)
	out := make([]SourceView, 0, len(list))
	for _, src := range list {
		if s.sourceAllowed(allowed, src.ID) {
			if s.Builtin() {
				src.BaseOrigin = s.sourceOrigin(src.ID)
			}
			out = append(out, SourceView{Source: src, Allowed: true})
		}
	}
	return out, nil
}

type SearchOutput struct {
	Result    SearchResult `json:"result"`
	Sources   []string     `json:"sources"`
	LatencyMs int64        `json:"latencyMs"`
}

// Search runs a trial search limited to the sources projectID may read.
func (s *Service) Search(ctx context.Context, projectID, query string, sources []string) (SearchOutput, error) {
	s = s.Snapshot()
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchOutput{}, apperr.New(apperr.Invalid, "请输入要检索的问题或关键词")
	}
	allowed := s.AllowedSources(projectID)
	scoped, err := s.scopeSources(allowed, sources)
	if err != nil {
		return SearchOutput{}, apperr.New(apperr.Invalid, err.Error())
	}
	if s.Builtin() && len(scoped) == 0 {
		return SearchOutput{Result: SearchResult{Query: query, Mode: "lexical", Results: []SearchHit{}}, Sources: []string{}}, nil
	}
	client, err := s.connect()
	if err != nil {
		return SearchOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := s.now()
	result, err := client.Search(ctx, query, scoped)
	if err != nil {
		return SearchOutput{}, apperr.New(apperr.Upstream, "检索失败："+err.Error())
	}
	if !s.Builtin() {
		result.Results = slices.DeleteFunc(result.Results, func(hit SearchHit) bool {
			return !s.sourceAllowed(scoped, hit.Source)
		})
		return SearchOutput{Result: result, Sources: scoped, LatencyMs: s.now().Sub(started).Milliseconds()}, nil
	}
	rawHits := len(result.Results)
	s.applyLifecycle(&result)
	s.annotateSearchVersions(&result)
	if rawHits == 0 {
		_ = s.RecordGap(projectID, query, GapActorFrom(ctx))
	}
	s.rankOrigins(&result)
	return SearchOutput{Result: result, Sources: scoped, LatencyMs: s.now().Sub(started).Milliseconds()}, nil
}

// Document fetches a source document (optionally a line range) if the
// source is within projectID's scope.
func (s *Service) Document(ctx context.Context, projectID, source, path string, start, end int) (Document, error) {
	s = s.Snapshot()
	source, path = strings.TrimSpace(source), strings.TrimSpace(path)
	if source == "" || path == "" {
		return Document{}, apperr.New(apperr.Invalid, "source 和 path 必填")
	}
	if !s.sourceAllowed(s.AllowedSources(projectID), source) {
		return Document{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	client, err := s.connect()
	if err != nil {
		return Document{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := client.Fetch(ctx, source, path, start, end)
	if err != nil {
		return Document{}, apperr.New(apperr.Upstream, "读取原文失败："+err.Error())
	}
	if !s.Builtin() {
		return doc, nil
	}
	doc.BaseOrigin = s.sourceOrigin(source)
	if doc.URL == "" {
		doc.URL = s.SourceFileURL(doc.Source, doc.Commit, doc.Path, doc.StartLine, doc.EndLine)
	}
	s.annotateDocumentVersion(&doc)
	return doc, nil
}

type UsageAgent struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	ProjectID string   `json:"projectId,omitempty"`
	Shared    bool     `json:"shared,omitempty"`
	Workflows []string `json:"workflows"`
}

// Usage lists the agents visible to projectID that have the KB enabled, with
// the workflows that use them, plus the number of visible agents.
func (s *Service) Usage(projectID string) ([]UsageAgent, int) {
	projectID = strings.TrimSpace(projectID)
	workflowsByAgent := map[string][]string{}
	for _, wf := range s.store.Workflows() {
		for _, node := range wf.Nodes {
			if node.AgentID != "" {
				workflowsByAgent[node.AgentID] = appendUnique(workflowsByAgent[node.AgentID], wf.Name)
			}
		}
	}
	agents := make([]UsageAgent, 0)
	total := 0
	for _, agent := range s.store.Agents() {
		if projectID != "" && agent.ProjectID != projectID && !agent.Shared {
			continue
		}
		total++
		if !agent.KnowledgeBase {
			continue
		}
		wfs := workflowsByAgent[agent.ID]
		if wfs == nil {
			wfs = []string{}
		}
		agents = append(agents, UsageAgent{ID: agent.ID, Name: agent.Name, ProjectID: agent.ProjectID, Shared: agent.Shared, Workflows: wfs})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, total
}

// Candidates lists recent run artifacts that look like distilled knowledge,
// annotated with their queue entry if already submitted.
func (s *Service) Candidates(projectID string) []domain.KnowledgeCandidate {
	list := s.store.KnowledgeArtifacts(strings.TrimSpace(projectID), 100)
	ids := make([]string, 0, len(list))
	for _, item := range list {
		ids = append(ids, item.Artifact.ID)
	}
	queued := s.store.KnowledgeSubmissionIDsByArtifact(ids)
	for i := range list {
		list[i].SubmissionID = queued[list[i].Artifact.ID]
	}
	return list
}

func (s *Service) Submissions(projectID, status string) []domain.KnowledgeSubmission {
	provider := s.Settings().EffectiveProvider()
	if store, ok := s.store.(interface {
		KnowledgeSubmissionsForProvider(projectID, status, provider string, limit int) []domain.KnowledgeSubmission
	}); ok {
		return store.KnowledgeSubmissionsForProvider(projectID, status, provider, 200)
	}
	items := s.store.KnowledgeSubmissions(projectID, status, 200)
	return slices.DeleteFunc(items, func(item domain.KnowledgeSubmission) bool {
		return item.EffectiveProvider() != provider
	})
}

func (s *Service) Submission(id string) (domain.KnowledgeSubmission, error) {
	item, ok := s.store.KnowledgeSubmission(id)
	if !ok || item.EffectiveProvider() != s.Settings().EffectiveProvider() {
		return item, apperr.New(apperr.NotFound, "此知识引擎中没有该审核条目")
	}
	return item, nil
}

// SubmissionForRead is the detail read used by the review UI. It refuses to
// render a missing content-plane body as an empty document, so an operator
// cannot save or publish a body that was silently lost.
func (s *Service) SubmissionForRead(id string) (domain.KnowledgeSubmission, error) {
	item, err := s.Submission(id)
	if err != nil {
		return item, err
	}
	if item.ContentMissing {
		return item, apperr.New(apperr.Upstream, "审核正文在内容库中缺失；已阻止按空正文展示和发布。请先恢复内容库后再操作")
	}
	return item, nil
}

func knowledgeSubmissionHasBody(item domain.KnowledgeSubmission) bool {
	return !item.ContentMissing && strings.TrimSpace(item.Content) != ""
}

type SubmissionInput struct {
	RunID      string `json:"runId"`
	ArtifactID string `json:"artifactId"`
	ProjectID  string `json:"projectId"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

// Submit queues knowledge for review, either from a run artifact (whose
// front matter supplies defaults) or from raw content.
func (s *Service) Submit(input SubmissionInput, actor string) (domain.KnowledgeSubmission, error) {
	item := domain.KnowledgeSubmission{
		ID: "kbs-" + s.itemID(), ProjectID: strings.TrimSpace(input.ProjectID),
		Status: domain.KnowledgeSubmissionPending, CreatedBy: actor,
		Provider: s.Settings().EffectiveProvider(),
	}
	content := input.Content
	if input.ArtifactID != "" {
		run, err := s.store.RunHeader(input.RunID)
		if err != nil {
			return item, err
		}
		if run.IsPersonalWorkspace() {
			return item, apperr.New(apperr.Forbidden, "个人工作区产物不能提交到共享知识库")
		}
		if run.ID == "" {
			return item, apperr.New(apperr.NotFound, "运行产物不存在")
		}
		if existing := s.store.KnowledgeSubmissionIDsByArtifact([]string{input.ArtifactID})[input.ArtifactID]; existing != "" {
			return item, apperr.New(apperr.Conflict, "该产物已加入沉淀队列").With("id", existing)
		}
		art, ok := s.store.Artifact(input.RunID, input.ArtifactID)
		if !ok {
			return item, apperr.New(apperr.NotFound, "运行产物不存在")
		}
		item.RunID, item.NodeID, item.ArtifactID = art.RunID, art.NodeID, art.ID
		content = art.Content
		item.ProjectID, item.IssueID, item.WorkflowID = run.ExecutionContext.ProjectID, run.ExecutionContext.IssueID, run.WorkflowID
	}
	parsed := ParseDistilled(content)
	item.Content = parsed.Body
	item.Fingerprint = KnowledgeFingerprint(item.Content)
	item.Title = strings.TrimSpace(firstNonEmpty(input.Title, parsed.Title, "未命名知识"))
	item.Source = strings.TrimSpace(firstNonEmpty(input.Source, parsed.Source))
	item.Path = strings.TrimSpace(firstNonEmpty(input.Path, parsed.Path))
	if strings.TrimSpace(item.Content) == "" {
		return item, apperr.New(apperr.Invalid, "沉淀内容不能为空")
	}
	return s.save(item)
}

type SubmissionEdit struct {
	Title      string `json:"title"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	ReviewNote string `json:"reviewNote"`
}

// Edit revises a queued item and moves it back to pending review.
func (s *Service) Edit(id string, input SubmissionEdit) (domain.KnowledgeSubmission, error) {
	item, err := s.mutable(id, "已写回知识库的条目不能再编辑")
	if err != nil {
		return item, err
	}
	if strings.EqualFold(strings.TrimSpace(item.Action), domain.KnowledgeSubmissionActionRetract) {
		// 下线条目只允许审核备注变化；正文、来源和路径在创建时已锁定，避免
		// 审批前被改成另一篇文档。
		if input.ReviewNote != "" {
			item.ReviewNote = strings.TrimSpace(input.ReviewNote)
		}
		item.Status = domain.KnowledgeSubmissionPending
		item.Error = ""
		return s.save(item)
	}
	item.Title = strings.TrimSpace(firstNonEmpty(input.Title, item.Title))
	item.Source = strings.TrimSpace(input.Source)
	item.Path = strings.TrimSpace(input.Path)
	if strings.TrimSpace(input.Content) != "" {
		item.Content = input.Content
		item.ContentMissing = false
	}
	if !knowledgeSubmissionHasBody(item) {
		return item, apperr.New(apperr.Upstream, "审核正文在内容库中缺失，已阻止保存；请先恢复内容库，或重新提交完整正文")
	}
	// 控制面只留指纹；正文写入内容面 staging，保存路径由 repo 负责拆分。
	item.Fingerprint = KnowledgeFingerprint(item.Content)
	item.ReviewNote = input.ReviewNote
	item.Status = domain.KnowledgeSubmissionPending
	item.Error = ""
	return s.save(item)
}

func (s *Service) Delete(id string) error {
	item, err := s.mutable(id, "已写回知识库的条目不能删除")
	if err != nil {
		return err
	}
	if deleter, ok := s.store.(interface {
		DeleteKnowledgeSubmissionCAS(domain.KnowledgeSubmission) error
	}); ok {
		if err := deleter.DeleteKnowledgeSubmissionCAS(item); err != nil {
			return err
		}
		if cleaner, ok := s.store.(interface{ DeleteKnowledgeWritebackBySubmission(string) error }); ok {
			return cleaner.DeleteKnowledgeWritebackBySubmission(item.ID)
		}
		return nil
	}
	if deleter, ok := s.store.(interface {
		DeleteKnowledgeWritebackBySubmission(string) error
	}); ok {
		if err := deleter.DeleteKnowledgeWritebackBySubmission(item.ID); err != nil {
			return apperr.New(apperr.Upstream, "删除写回记录失败："+err.Error())
		}
	}
	return s.store.DeleteKnowledgeSubmission(item.ID)
}

func (s *Service) Reject(id, note, actor string) (domain.KnowledgeSubmission, error) {
	item, err := s.mutable(id, "已写回知识库的条目不能驳回")
	if err != nil {
		return item, err
	}
	item.Status = domain.KnowledgeSubmissionRejected
	item.ReviewNote = strings.TrimSpace(note)
	item.ReviewedBy = actor
	return s.save(item)
}

// Publish writes a reviewed item into the upstream KB. It requires a source
// within the item's project scope; upstream failures are recorded on the item.
func (s *Service) Publish(ctx context.Context, id, actor string) (domain.KnowledgeSubmission, error) {
	s = s.Snapshot()
	item, err := s.mutable(id, "该条目已写回知识库")
	if err != nil {
		return item, err
	}
	if !strings.EqualFold(strings.TrimSpace(item.Action), domain.KnowledgeSubmissionActionRetract) && !knowledgeSubmissionHasBody(item) {
		return item, apperr.New(apperr.Upstream, "审核正文在内容库中缺失，已阻止发布；请先恢复内容库或重新提交完整正文")
	}
	previousStatus := item.Status
	item.Status = domain.KnowledgeSubmissionPublishing
	item, err = s.save(item)
	if err != nil {
		return item, err
	}
	ctx, publishCancel := context.WithTimeout(ctx, 3*CallTimeout)
	defer publishCancel()
	ctx = publication.WithIntent(ctx, publication.Intent{Project: item.ProjectID, Fingerprint: KnowledgeFingerprint(item.Content), ID: item.ID, Version: item.Version})
	claimVersion := item.Version
	defer func() {
		current, ok := s.store.KnowledgeSubmission(id)
		if ok && current.Status == domain.KnowledgeSubmissionPublishing && current.Version == claimVersion {
			current.Status = previousStatus
			current.Error = item.Error
			current.ReviewedBy = item.ReviewedBy
			current.GateChecks = item.GateChecks
			_ = s.store.PutKnowledgeSubmission(current)
		}
	}()
	if s.Builtin() {
		if strings.EqualFold(strings.TrimSpace(item.Action), domain.KnowledgeSubmissionActionRetract) {
			return s.publishRetraction(ctx, item, actor)
		}
		item, err = s.checkPublishGates(item)
		if err != nil {
			return item, err
		}
	} else if !s.Settings().WriteEnabled {
		return item, apperr.New(apperr.Forbidden, "LightCloud 写回已关闭，请管理员开启审核后写回")
	}
	source := strings.TrimSpace(item.Source)
	if source == "" {
		return item, apperr.New(apperr.Invalid, "请先选择写入的知识库来源")
	}
	if !s.writableSource(item.ProjectID, source) {
		return item, apperr.New(apperr.Forbidden, "该知识库来源不在项目授权范围内")
	}
	path, ok := NormalizeDocPath(item.Path)
	if !ok {
		return item, apperr.New(apperr.Invalid, "写入路径必须是仓库内的 .md 相对路径，例如 guides/xxx.md")
	}
	content := item.Content
	if title := strings.TrimSpace(item.Title); title != "" && !strings.HasPrefix(strings.TrimSpace(content), "#") {
		content = "# " + title + "\n\n" + content
	}
	client, err := s.connect()
	if err != nil {
		return item, err
	}
	item.Path = path
	item.ReviewedBy = actor
	if src, ok := s.builtinSourceByID(source); ok && src.Kind == "git" {
		writer, ok := client.(GitWritebacker)
		if !ok {
			return item, apperr.New(apperr.Unavailable, "内置知识引擎不支持 Git 来源写回")
		}
		ctx, cancel := context.WithTimeout(ctx, 3*CallTimeout)
		defer cancel()
		finalized := false
		finish := func(result WriteResult) error {
			item.Status = domain.KnowledgeSubmissionPublished
			item.Commit = result.Commit
			item.Branch = result.Branch
			item.MRURL = result.MRURL
			item.MRProvider = result.MRProvider
			item.MRStatus = result.MRStatus
			item.WriteNote = result.Message
			item.WriteError = ""
			item.Error = ""
			item.PublishedAt = s.now().UTC()
			var saveErr error
			item, saveErr = s.save(item)
			return saveErr
		}
		ctx = WithWritebackFinalizer(ctx, func(result WriteResult) error {
			err := finish(result)
			if err == nil {
				finalized = true
			}
			return err
		})
		result, err := writer.WriteGitDocument(ctx, WritebackRequest{
			SubmissionID: item.ID, ProjectID: item.ProjectID, Source: source,
			Path: path, Content: content, Title: item.Title,
		})
		if err != nil {
			item.Error = err.Error()
			item.MRStatus = domain.KnowledgeWritebackFailed
			return item, apperr.New(apperr.Upstream, "写回知识库失败："+err.Error())
		}
		if !finalized {
			if err := finish(result); err != nil {
				return item, err
			}
		}
		return item, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	result, err := client.WriteDocument(ctx, source, path, content)
	if err != nil {
		item.Error = err.Error()
		return item, apperr.New(apperr.Upstream, "写回知识库失败："+err.Error())
	}
	item.Status = domain.KnowledgeSubmissionPublished
	item.Commit = result.Commit
	item.Error = ""
	item.PublishedAt = s.now().UTC()
	return s.save(item)
}

func (s *Service) builtinSourceByID(id string) (domain.KnowledgeSource, bool) {
	if !s.Builtin() {
		return domain.KnowledgeSource{}, false
	}
	return s.sourceByID(id)
}

// sourceByID reads the control-plane source when the store supports it. Tests
// with a narrow fake store fall back to the hosted write path.
func (s *Service) sourceByID(id string) (domain.KnowledgeSource, bool) {
	lookup, ok := s.store.(interface {
		KnowledgeSource(string) (domain.KnowledgeSource, bool)
	})
	if !ok {
		return domain.KnowledgeSource{}, false
	}
	return lookup.KnowledgeSource(id)
}

// mutable loads an item that has not been published yet.
func (s *Service) mutable(id, publishedMessage string) (domain.KnowledgeSubmission, error) {
	if recovery, ok := s.store.(interface{ RecoverExpiredKnowledgePublications() error }); ok {
		if err := recovery.RecoverExpiredKnowledgePublications(); err != nil {
			return domain.KnowledgeSubmission{}, err
		}
	}

	item, err := s.Submission(id)
	if err != nil {
		return item, err
	}
	if item.Status == domain.KnowledgeSubmissionPublishing {
		return item, apperr.New(apperr.Conflict, "条目正在发布，请稍后重试")
	}
	if item.Status == domain.KnowledgeSubmissionPublished {
		return item, apperr.New(apperr.Conflict, publishedMessage)
	}
	if item.Status == domain.KnowledgeSubmissionRetracted {
		return item, apperr.New(apperr.Conflict, "已撤回的条目不能再修改")
	}
	return item, nil
}

func (s *Service) save(item domain.KnowledgeSubmission) (domain.KnowledgeSubmission, error) {
	if err := s.store.PutKnowledgeSubmission(item); err != nil {
		return item, err
	}
	saved, _ := s.store.KnowledgeSubmission(item.ID)
	return saved, nil
}

// connect selects the configured engine behind the common Upstream interface.
func (s *Service) connect() (Upstream, error) {
	settings := s.Settings()
	switch settings.EffectiveProvider() {
	case domain.KnowledgeProviderLightCloud:
		if !settings.ExternalReady() {
			return nil, apperr.New(apperr.Unavailable, errLightCloudNotConfigured.Error())
		}
		return NewClient(settings)
	case domain.KnowledgeProviderBuiltin:
		if s.upstream != nil {
			return s.upstream, nil
		}
	}
	return nil, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
}

func (s *Service) scopeSources(allowed, requested []string) ([]string, error) {
	if !s.Builtin() && len(allowed) == 0 {
		return cleanList(requested), nil
	}
	return ScopeSources(allowed, requested)
}

func (s *Service) sourceAllowed(allowed []string, source string) bool {
	return (!s.Builtin() && len(allowed) == 0) || SourceAllowed(allowed, source)
}

func (s *Service) writableSource(project, source string) bool {
	if !s.Builtin() {
		return strings.TrimSpace(project) != "" && s.sourceAllowed(s.AllowedSources(project), source)
	}
	return s.projectWritableSource(project, source)
}

func (s *Service) AllowedSources(project string) []string {
	if !s.Builtin() {
		return cleanList(s.Settings().SourcesForProject(project))
	}
	if strings.TrimSpace(project) == "" {
		return []string{}
	}
	if ms, err := s.managed(); err == nil {
		return allowedSourceIDs(ms, project)
	}
	out := []string{}
	for _, binding := range s.store.KnowledgeBindings(project) {
		for _, src := range s.store.KnowledgeSources(binding.BaseID) {
			out = append(out, src.ID)
		}
	}
	return cleanList(out)
}
func (s *Service) Tools(ctx context.Context) ([]mcpclient.Tool, error) {
	u, err := s.connect()
	if err != nil {
		return nil, err
	}
	return u.Tools(ctx)
}
func (s *Service) DocumentVersion(ctx context.Context, project, source, path, commit string, start, end int) (Document, error) {
	doc, err := s.Document(ctx, project, source, path, start, end)
	if err != nil {
		return doc, err
	}
	if commit != "" && commit != doc.Commit {
		return Document{}, apperr.New(apperr.Invalid, "当前只提供已索引版本")
	}
	return doc, nil
}
func stripBearer(token string) string { return strings.TrimPrefix(token, "Bearer ") }

func cleanSources(in []string) []string {
	out := cleanList(in)
	if out == nil {
		out = []string{}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// SourceDetail returns only public source metadata and document summaries.
func (s *Service) SourceDetail(ctx context.Context, project, source string) (any, error) {
	s = s.Snapshot()
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, apperr.New(apperr.Invalid, "source 必填")
	}
	if !s.sourceAllowed(s.AllowedSources(project), source) {
		return nil, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	if !s.Builtin() {
		up, err := s.connect()
		if err != nil {
			return nil, err
		}
		if details, ok := up.(interface {
			GetSource(context.Context, string) (json.RawMessage, error)
		}); ok {
			ctx, cancel := context.WithTimeout(ctx, CallTimeout)
			defer cancel()
			return details.GetSource(ctx, source)
		}
	}
	list, err := s.Sources(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, src := range list {
		if src.ID == source {
			docs := []map[string]any{}
			for _, doc := range s.store.KnowledgeDocuments(source) {
				docs = append(docs, map[string]any{"path": doc.Path, "title": doc.Title, "commit": doc.Commit, "revision": doc.Revision, "line_count": doc.LineCount})
			}
			return map[string]any{"source": src.Source, "documents": docs}, nil
		}
	}
	return nil, apperr.New(apperr.NotFound, "来源不存在")
}

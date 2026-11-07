package knowledge

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const CallTimeout = 30 * time.Second

// Store is the persistence the 知识库 module needs.
type Store interface {
	KnowledgeSettings() domain.KnowledgeSettings
	PutKnowledgeSettings(domain.KnowledgeSettings) error
	KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission
	KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool)
	KnowledgeSubmissionIDsByArtifact(artifactIDs []string) map[string]string
	PutKnowledgeSubmission(domain.KnowledgeSubmission) error
	DeleteKnowledgeSubmission(id string) error
	KnowledgeArtifacts(projectID string, limit int) []domain.KnowledgeCandidate
	Artifact(runID, artifactID string) (domain.Artifact, bool)
	Run(id string) (domain.Run, bool)
	Agents() []domain.AgentDefinition
	Workflows() []domain.WorkflowDefinition
}

// Upstream is the subset of the KB MCP client the service calls.
type Upstream interface {
	Tools(ctx context.Context) ([]mcpclient.Tool, error)
	ListSources(ctx context.Context) ([]Source, error)
	Search(ctx context.Context, query string, sources []string) (SearchResult, error)
	Fetch(ctx context.Context, source, path string, start, end int) (Document, error)
	WriteDocument(ctx context.Context, source, path, content string) (WriteResult, error)
}

// Service holds the 知识库 business rules: settings validation, source
// scoping, and the review queue that writes distilled knowledge upstream.
type Service struct {
	store Store
	dial  func(domain.KnowledgeSettings) (Upstream, error)
	now   func() time.Time
	newID func() string
}

func NewService(store Store, newID func() string) *Service {
	return &Service{
		store: store,
		dial:  func(s domain.KnowledgeSettings) (Upstream, error) { return NewClient(s) },
		now:   time.Now,
		newID: newID,
	}
}

func (s *Service) Settings() domain.KnowledgeSettings { return s.store.KnowledgeSettings() }

type SettingsInput struct {
	Enabled        bool                `json:"enabled"`
	URL            string              `json:"url"`
	Token          string              `json:"token"`
	ClearToken     bool                `json:"clearToken"`
	WriteEnabled   bool                `json:"writeEnabled"`
	DefaultSources []string            `json:"defaultSources"`
	ProjectSources map[string][]string `json:"projectSources"`
}

// UpdateSettings replaces the settings. An empty token keeps the saved one
// unless ClearToken is set.
func (s *Service) UpdateSettings(input SettingsInput, actor string) (domain.KnowledgeSettings, error) {
	current := s.store.KnowledgeSettings()
	rawURL := strings.TrimSpace(input.URL)
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return current, apperr.New(apperr.Invalid, "知识库地址必须是 http(s)://host/mcp 形式")
		}
	}
	next := domain.KnowledgeSettings{
		Enabled: input.Enabled, URL: rawURL, WriteEnabled: input.WriteEnabled,
		DefaultSources: cleanSources(input.DefaultSources), UpdatedBy: actor,
	}
	if input.ProjectSources != nil {
		next.ProjectSources = map[string][]string{}
		for project, list := range input.ProjectSources {
			if project = strings.TrimSpace(project); project != "" {
				next.ProjectSources[project] = cleanSources(list)
			}
		}
	}
	switch token := strings.TrimSpace(input.Token); {
	case token != "":
		next.Token = stripBearer(token)
	case !input.ClearToken:
		next.Token = current.Token
	}
	if next.Enabled && (next.URL == "" || next.Token == "") {
		return current, apperr.New(apperr.Invalid, "启用前需要填写知识库地址和 Token")
	}
	if err := s.store.PutKnowledgeSettings(next); err != nil {
		return current, err
	}
	return s.store.KnowledgeSettings(), nil
}

type CheckResult struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	LatencyMs int64    `json:"latencyMs"`
	Sources   []Source `json:"sources"`
	Tools     []string `json:"tools,omitempty"`
}

// Check probes the upstream with the saved settings, overridden by any
// unsaved URL/token so admins can verify a token before enabling it.
func (s *Service) Check(ctx context.Context, overrideURL, overrideToken string) CheckResult {
	settings := s.store.KnowledgeSettings()
	if u := strings.TrimSpace(overrideURL); u != "" {
		settings.URL = u
	}
	if t := strings.TrimSpace(overrideToken); t != "" {
		settings.Token = stripBearer(t)
	}
	client, err := s.dial(settings)
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
	settings := s.store.KnowledgeSettings()
	client, err := s.connect(settings)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	list, err := client.ListSources(ctx)
	if err != nil {
		return nil, apperr.New(apperr.Upstream, "知识库不可达："+err.Error())
	}
	allowed := settings.SourcesForProject(projectID)
	out := make([]SourceView, 0, len(list))
	for _, src := range list {
		out = append(out, SourceView{Source: src, Allowed: SourceAllowed(allowed, src.ID)})
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
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchOutput{}, apperr.New(apperr.Invalid, "请输入要检索的问题或关键词")
	}
	settings := s.store.KnowledgeSettings()
	scoped, err := ScopeSources(settings.SourcesForProject(projectID), sources)
	if err != nil {
		return SearchOutput{}, apperr.New(apperr.Invalid, err.Error())
	}
	client, err := s.connect(settings)
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
	return SearchOutput{Result: result, Sources: scoped, LatencyMs: s.now().Sub(started).Milliseconds()}, nil
}

// Document fetches a source document (optionally a line range) if the
// source is within projectID's scope.
func (s *Service) Document(ctx context.Context, projectID, source, path string, start, end int) (Document, error) {
	source, path = strings.TrimSpace(source), strings.TrimSpace(path)
	if source == "" || path == "" {
		return Document{}, apperr.New(apperr.Invalid, "source 和 path 必填")
	}
	settings := s.store.KnowledgeSettings()
	if !SourceAllowed(settings.SourcesForProject(projectID), source) {
		return Document{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	client, err := s.connect(settings)
	if err != nil {
		return Document{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := client.Fetch(ctx, source, path, start, end)
	if err != nil {
		return Document{}, apperr.New(apperr.Upstream, "读取原文失败："+err.Error())
	}
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
	return s.store.KnowledgeSubmissions(projectID, status, 200)
}

func (s *Service) Submission(id string) (domain.KnowledgeSubmission, error) {
	item, ok := s.store.KnowledgeSubmission(id)
	if !ok {
		return item, apperr.New(apperr.NotFound, "沉淀条目不存在")
	}
	return item, nil
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
		ID: "kbs-" + s.newID(), ProjectID: strings.TrimSpace(input.ProjectID),
		Status: domain.KnowledgeSubmissionPending, CreatedBy: actor,
	}
	content := input.Content
	if input.ArtifactID != "" {
		if existing := s.store.KnowledgeSubmissionIDsByArtifact([]string{input.ArtifactID})[input.ArtifactID]; existing != "" {
			return item, apperr.New(apperr.Conflict, "该产物已加入沉淀队列").With("id", existing)
		}
		art, ok := s.store.Artifact(input.RunID, input.ArtifactID)
		if !ok {
			return item, apperr.New(apperr.NotFound, "运行产物不存在")
		}
		item.RunID, item.NodeID, item.ArtifactID = art.RunID, art.NodeID, art.ID
		content = art.Content
		if run, ok := s.store.Run(art.RunID); ok {
			item.ProjectID, item.IssueID, item.WorkflowID = run.ExecutionContext.ProjectID, run.ExecutionContext.IssueID, run.WorkflowID
		}
	}
	parsed := ParseDistilled(content)
	item.Content = parsed.Body
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
	item.Title = strings.TrimSpace(firstNonEmpty(input.Title, item.Title))
	item.Source = strings.TrimSpace(input.Source)
	item.Path = strings.TrimSpace(input.Path)
	if strings.TrimSpace(input.Content) != "" {
		item.Content = input.Content
	}
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

// Publish writes a reviewed item into the upstream KB. It requires the
// platform-level write switch and a source within the item's project scope;
// upstream failures are recorded on the item.
func (s *Service) Publish(ctx context.Context, id, actor string) (domain.KnowledgeSubmission, error) {
	item, err := s.mutable(id, "该条目已写回知识库")
	if err != nil {
		return item, err
	}
	settings := s.store.KnowledgeSettings()
	if !settings.Ready() {
		return item, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	if !settings.WriteEnabled {
		return item, apperr.New(apperr.Forbidden, "平台未开启知识写回：请管理员在「接入配置」中打开「允许审核后写回」")
	}
	source := strings.TrimSpace(item.Source)
	if source == "" {
		return item, apperr.New(apperr.Invalid, "请先选择写入的知识库来源")
	}
	if !SourceAllowed(settings.SourcesForProject(item.ProjectID), source) {
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
	client, err := s.dial(settings)
	if err != nil {
		return item, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	result, err := client.WriteDocument(ctx, source, path, content)
	item.Path = path
	item.ReviewedBy = actor
	if err != nil {
		item.Error = err.Error()
		_ = s.store.PutKnowledgeSubmission(item)
		return item, apperr.New(apperr.Upstream, "写回知识库失败："+err.Error())
	}
	item.Status = domain.KnowledgeSubmissionPublished
	item.Commit = result.Commit
	item.Error = ""
	item.PublishedAt = s.now().UTC()
	return s.save(item)
}

// mutable loads an item that has not been published yet.
func (s *Service) mutable(id, publishedMessage string) (domain.KnowledgeSubmission, error) {
	item, err := s.Submission(id)
	if err != nil {
		return item, err
	}
	if item.Status == domain.KnowledgeSubmissionPublished {
		return item, apperr.New(apperr.Conflict, publishedMessage)
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

// connect dials the upstream, reporting missing configuration as Unavailable.
func (s *Service) connect(settings domain.KnowledgeSettings) (Upstream, error) {
	client, err := s.dial(settings)
	if err != nil {
		return nil, apperr.New(apperr.Unavailable, err.Error())
	}
	return client, nil
}

func stripBearer(token string) string { return strings.TrimPrefix(token, "Bearer ") }

func cleanSources(in []string) []string {
	out, _ := ScopeSources(nil, in)
	if out == nil {
		out = []string{}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
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

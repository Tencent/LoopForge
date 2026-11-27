package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

// TODO(knowledge-legacy-compat): 历史平台级单知识库（lightcloud kb）与沉淀审核队列的
// 向后兼容服务层，对应旧路由 `/api/knowledge/{settings,check,sources,search,document,candidates,submissions}`。
// 新功能请一律使用用户级连接器 Service（见 service.go / connector_*.go）。
// 下线条件：外部调用方、上游 E2E 探针（tokenOwner 等）及历史沉淀数据全部迁移完毕后，
// 可整体删除本文件及 internal/{domain,repo,httpapi}/*legacy*.go、internal/repo/run_knowledge.go。

const legacyToolWrite = "kb_write_document"

// ErrLegacyNotConfigured is returned when the legacy platform-wide KB is not enabled or configured.
var ErrLegacyNotConfigured = errors.New("知识库未接入：请在「知识库 → 接入配置」填写地址和 Token 并启用")

// LegacyStore is the persistence subset required by LegacyService.
type LegacyStore interface {
	KnowledgeSettings() domain.KnowledgeSettings
	PutKnowledgeSettings(domain.KnowledgeSettings) error
	KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission
	KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool)
	KnowledgeSubmissionIDsByArtifact(artifactIDs []string) map[string]string
	PutKnowledgeSubmission(domain.KnowledgeSubmission) error
	DeleteKnowledgeSubmission(id string) error
	KnowledgeArtifacts(projectID string, limit int) []domain.KnowledgeCandidate
	Artifact(runID, artifactID string) (domain.Artifact, bool)
	RunHeader(id string) (domain.Run, error)
}

// LegacySource mirrors the upstream lightcloud kb source descriptor.
type LegacySource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	IndexStatus string `json:"index_status,omitempty"`
	DocCount    int    `json:"doc_count"`
}

// LegacySourceView annotates a legacy source with project scope status.
type LegacySourceView struct {
	LegacySource
	Allowed bool `json:"allowed"`
}

type LegacySearchHit struct {
	Source    string  `json:"source"`
	Commit    string  `json:"commit"`
	Path      string  `json:"path"`
	Title     string  `json:"title,omitempty"`
	Heading   string  `json:"heading,omitempty"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	Citation  string  `json:"citation"`
}

type LegacySearchResult struct {
	Query         string            `json:"query"`
	Mode          string            `json:"mode"`
	Degraded      bool              `json:"degraded"`
	DegradeReason string            `json:"degrade_reason,omitempty"`
	Results       []LegacySearchHit `json:"results"`
}

type LegacySearchOutput struct {
	Result    LegacySearchResult `json:"result"`
	Sources   []string           `json:"sources"`
	LatencyMs int64              `json:"latencyMs"`
}

type LegacyLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type LegacyDocument struct {
	Source    string       `json:"source"`
	Commit    string       `json:"commit"`
	Path      string       `json:"path"`
	StartLine int          `json:"start_line"`
	EndLine   int          `json:"end_line"`
	LineCount int          `json:"line_count"`
	Lines     []LegacyLine `json:"lines"`
	Citation  string       `json:"citation"`
}

type LegacyWriteResult struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

type LegacyCheckResult struct {
	OK        bool           `json:"ok"`
	Error     string         `json:"error,omitempty"`
	LatencyMs int64          `json:"latencyMs"`
	Sources   []LegacySource `json:"sources"`
	Tools     []string       `json:"tools,omitempty"`
}

type LegacySettingsInput struct {
	Enabled        bool                `json:"enabled"`
	URL            string              `json:"url"`
	Token          string              `json:"token"`
	ClearToken     bool                `json:"clearToken"`
	WriteEnabled   bool                `json:"writeEnabled"`
	DefaultSources []string            `json:"defaultSources"`
	ProjectSources map[string][]string `json:"projectSources"`
}

type LegacySubmissionInput struct {
	RunID      string `json:"runId"`
	ArtifactID string `json:"artifactId"`
	ProjectID  string `json:"projectId"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

type LegacySubmissionEdit struct {
	Title      string `json:"title"`
	Source     string `json:"source"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	ReviewNote string `json:"reviewNote"`
}

type legacyUpstream interface {
	Tools(ctx context.Context) ([]mcpclient.Tool, error)
	ListSources(ctx context.Context) ([]LegacySource, error)
	Search(ctx context.Context, query string, sources []string) (LegacySearchResult, error)
	Fetch(ctx context.Context, source, path string, start, end int) (LegacyDocument, error)
	WriteDocument(ctx context.Context, source, path, content string) (LegacyWriteResult, error)
}

// LegacyService implements backward-compatible platform KB settings, trial
// retrieval, and the distilled-knowledge review queue.
//
// Deprecated: TODO(knowledge-legacy-compat) kept only for backward compatibility.
type LegacyService struct {
	store LegacyStore
	dial  func(domain.KnowledgeSettings) (legacyUpstream, error)
	now   func() time.Time
	newID func() string
}

// NewLegacyService constructs the backward-compatibility service backed by store.
func NewLegacyService(store LegacyStore, newID func() string) *LegacyService {
	return &LegacyService{
		store: store,
		dial:  func(s domain.KnowledgeSettings) (legacyUpstream, error) { return newLegacyClient(s) },
		now:   time.Now,
		newID: newID,
	}
}

func (s *LegacyService) Settings() domain.KnowledgeSettings {
	return s.store.KnowledgeSettings()
}

// UpdateSettings replaces legacy platform KB settings; omitting Token keeps the
// saved credential unless ClearToken is set.
func (s *LegacyService) UpdateSettings(input LegacySettingsInput, actor string) (domain.KnowledgeSettings, error) {
	current := s.store.KnowledgeSettings()
	rawURL := strings.TrimSpace(input.URL)
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return current, apperr.New(apperr.Invalid, "知识库地址必须是 http(s)://host/mcp 形式")
		}
		if err := validateConnectorEndpointIP(rawURL, true, "知识库"); err != nil {
			return current, err
		}
	}
	next := domain.KnowledgeSettings{
		Enabled:        input.Enabled,
		URL:            rawURL,
		WriteEnabled:   input.WriteEnabled,
		Token:          resolveLegacySettingsToken(input, current.Token),
		DefaultSources: cleanLegacySources(input.DefaultSources),
		ProjectSources: cleanLegacyProjectSources(input.ProjectSources),
		UpdatedBy:      actor,
	}
	if next.Enabled && (next.URL == "" || next.Token == "") {
		return current, apperr.New(apperr.Invalid, "启用前需要填写知识库地址和 Token")
	}
	if err := s.store.PutKnowledgeSettings(next); err != nil {
		return current, err
	}
	return s.store.KnowledgeSettings(), nil
}

func resolveLegacySettingsToken(input LegacySettingsInput, current string) string {
	switch token := strings.TrimSpace(input.Token); {
	case token != "":
		return strings.TrimPrefix(token, "Bearer ")
	case !input.ClearToken:
		return current
	}
	return ""
}

func cleanLegacySources(in []string) []string {
	out, _ := LegacyScopeSources(nil, in)
	if out == nil {
		out = []string{}
	}
	return out
}

func cleanLegacyProjectSources(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := map[string][]string{}
	for project, list := range in {
		if project = strings.TrimSpace(project); project != "" {
			out[project] = cleanLegacySources(list)
		}
	}
	return out
}

// Check probes the legacy upstream with saved settings overridden by any
// unsaved URL/token.
func (s *LegacyService) Check(ctx context.Context, overrideURL, overrideToken string) LegacyCheckResult {
	settings := s.store.KnowledgeSettings()
	if u := strings.TrimSpace(overrideURL); u != "" {
		settings.URL = u
	}
	if t := strings.TrimSpace(overrideToken); t != "" {
		settings.Token = strings.TrimPrefix(t, "Bearer ")
	}
	client, err := s.dial(settings)
	if err != nil {
		return LegacyCheckResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := s.now()
	sources, err := client.ListSources(ctx)
	latency := s.now().Sub(started).Milliseconds()
	if err != nil {
		return LegacyCheckResult{Error: err.Error(), LatencyMs: latency}
	}
	if sources == nil {
		sources = []LegacySource{}
	}
	tools := []string{}
	if list, err := client.Tools(ctx); err == nil {
		for _, tool := range list {
			tools = append(tools, tool.Name)
		}
	}
	return LegacyCheckResult{OK: true, LatencyMs: latency, Sources: sources, Tools: tools}
}

// Sources lists legacy upstream sources, marking which ones projectID may read.
func (s *LegacyService) Sources(ctx context.Context, projectID string) ([]LegacySourceView, error) {
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
	out := make([]LegacySourceView, 0, len(list))
	for _, src := range list {
		out = append(out, LegacySourceView{LegacySource: src, Allowed: LegacySourceAllowed(allowed, src.ID)})
	}
	return out, nil
}

// Search runs a legacy trial search limited to projectID's allowed sources.
func (s *LegacyService) Search(ctx context.Context, projectID, query string, sources []string) (LegacySearchOutput, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return LegacySearchOutput{}, apperr.New(apperr.Invalid, "请输入要检索的问题或关键词")
	}
	settings := s.store.KnowledgeSettings()
	scoped, err := LegacyScopeSources(settings.SourcesForProject(projectID), sources)
	if err != nil {
		return LegacySearchOutput{}, apperr.New(apperr.Invalid, err.Error())
	}
	client, err := s.connect(settings)
	if err != nil {
		return LegacySearchOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := s.now()
	result, err := client.Search(ctx, query, scoped)
	if err != nil {
		return LegacySearchOutput{}, apperr.New(apperr.Upstream, "检索失败："+err.Error())
	}
	return LegacySearchOutput{Result: result, Sources: scoped, LatencyMs: s.now().Sub(started).Milliseconds()}, nil
}

// Document fetches a legacy source document if the source is within projectID's scope.
func (s *LegacyService) Document(ctx context.Context, projectID, source, docPath string, start, end int) (LegacyDocument, error) {
	source, docPath = strings.TrimSpace(source), strings.TrimSpace(docPath)
	if source == "" || docPath == "" {
		return LegacyDocument{}, apperr.New(apperr.Invalid, "source 和 path 必填")
	}
	settings := s.store.KnowledgeSettings()
	if !LegacySourceAllowed(settings.SourcesForProject(projectID), source) {
		return LegacyDocument{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	client, err := s.connect(settings)
	if err != nil {
		return LegacyDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := client.Fetch(ctx, source, docPath, start, end)
	if err != nil {
		return LegacyDocument{}, apperr.New(apperr.Upstream, "读取原文失败："+err.Error())
	}
	return doc, nil
}

// Candidates lists recent run artifacts named knowledge.md annotated with their
// review-queue submission ID if already queued.
func (s *LegacyService) Candidates(projectID string) []domain.KnowledgeCandidate {
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

func (s *LegacyService) Submissions(projectID, status string) []domain.KnowledgeSubmission {
	return s.store.KnowledgeSubmissions(projectID, status, 200)
}

func (s *LegacyService) Submission(id string) (domain.KnowledgeSubmission, error) {
	item, ok := s.store.KnowledgeSubmission(id)
	if !ok {
		return item, apperr.New(apperr.NotFound, "沉淀条目不存在")
	}
	return item, nil
}

// Submit queues distilled knowledge for human review, refusing artifacts from
// personal workspaces so private notes never enter the shared queue.
func (s *LegacyService) Submit(input LegacySubmissionInput, actor string) (domain.KnowledgeSubmission, error) {
	item := domain.KnowledgeSubmission{
		ID:        "kbs-" + s.newID(),
		ProjectID: strings.TrimSpace(input.ProjectID),
		Status:    domain.KnowledgeSubmissionPending,
		CreatedBy: actor,
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
	item.Title = strings.TrimSpace(firstNonEmpty(input.Title, parsed.Title, "未命名知识"))
	item.Source = strings.TrimSpace(firstNonEmpty(input.Source, parsed.Source))
	item.Path = strings.TrimSpace(firstNonEmpty(input.Path, parsed.Path))
	if strings.TrimSpace(item.Content) == "" {
		return item, apperr.New(apperr.Invalid, "沉淀内容不能为空")
	}
	return s.save(item)
}

func (s *LegacyService) Edit(id string, input LegacySubmissionEdit) (domain.KnowledgeSubmission, error) {
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

func (s *LegacyService) Delete(id string) error {
	item, err := s.mutable(id, "已写回知识库的条目不能删除")
	if err != nil {
		return err
	}
	return s.store.DeleteKnowledgeSubmission(item.ID)
}

func (s *LegacyService) Reject(id, note, actor string) (domain.KnowledgeSubmission, error) {
	item, err := s.mutable(id, "已写回知识库的条目不能驳回")
	if err != nil {
		return item, err
	}
	item.Status = domain.KnowledgeSubmissionRejected
	item.ReviewNote = strings.TrimSpace(note)
	item.ReviewedBy = actor
	return s.save(item)
}

func (s *LegacyService) Publish(ctx context.Context, id, actor string) (domain.KnowledgeSubmission, error) {
	item, err := s.mutable(id, "该条目已写回知识库")
	if err != nil {
		return item, err
	}
	settings := s.store.KnowledgeSettings()
	if !settings.Ready() {
		return item, apperr.New(apperr.Unavailable, ErrLegacyNotConfigured.Error())
	}
	if !settings.WriteEnabled {
		return item, apperr.New(apperr.Forbidden, "平台未开启知识写回：请管理员在「接入配置」中打开「允许审核后写回」")
	}
	source := strings.TrimSpace(item.Source)
	if source == "" {
		return item, apperr.New(apperr.Invalid, "请先选择写入的知识库来源")
	}
	if !LegacySourceAllowed(settings.SourcesForProject(item.ProjectID), source) {
		return item, apperr.New(apperr.Forbidden, "该知识库来源不在项目授权范围内")
	}
	docPath, ok := NormalizeDocPath(item.Path)
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
	result, err := client.WriteDocument(ctx, source, docPath, content)
	item.Path = docPath
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

func (s *LegacyService) mutable(id, publishedMessage string) (domain.KnowledgeSubmission, error) {
	item, err := s.Submission(id)
	if err != nil {
		return item, err
	}
	if item.Status == domain.KnowledgeSubmissionPublished {
		return item, apperr.New(apperr.Conflict, publishedMessage)
	}
	return item, nil
}

func (s *LegacyService) save(item domain.KnowledgeSubmission) (domain.KnowledgeSubmission, error) {
	if err := s.store.PutKnowledgeSubmission(item); err != nil {
		return item, err
	}
	saved, _ := s.store.KnowledgeSubmission(item.ID)
	return saved, nil
}

func (s *LegacyService) connect(settings domain.KnowledgeSettings) (legacyUpstream, error) {
	if !settings.Ready() {
		return nil, apperr.New(apperr.Unavailable, ErrLegacyNotConfigured.Error())
	}
	client, err := s.dial(settings)
	if err != nil {
		return nil, apperr.New(apperr.Unavailable, err.Error())
	}
	return client, nil
}

// legacyClient calls the legacy lightcloud kb MCP using the platform token.
type legacyClient struct {
	mcp *mcpclient.Client
}

var (
	legacyClientsMu sync.Mutex
	legacyClients   = map[string]*mcpclient.Client{}
)

func newLegacyClient(settings domain.KnowledgeSettings) (*legacyClient, error) {
	rawURL, token := strings.TrimSpace(settings.URL), strings.TrimSpace(settings.Token)
	if rawURL == "" || token == "" {
		return nil, ErrLegacyNotConfigured
	}
	if err := validateConnectorEndpointIP(rawURL, true, "知识库"); err != nil {
		return nil, err
	}
	key := rawURL + "\x00" + token
	legacyClientsMu.Lock()
	defer legacyClientsMu.Unlock()
	c, ok := legacyClients[key]
	if !ok {
		c = mcpclient.New(rawURL, map[string]string{"Authorization": "Bearer " + token}).WithTimeout(CallTimeout)
		legacyClients[key] = c
	}
	return &legacyClient{mcp: c}, nil
}

func (c *legacyClient) Tools(ctx context.Context) ([]mcpclient.Tool, error) {
	return c.mcp.ListToolsContext(ctx)
}

func (c *legacyClient) ListSources(ctx context.Context) ([]LegacySource, error) {
	var out struct {
		Sources []LegacySource `json:"sources"`
	}
	if err := c.callJSON(ctx, ToolListSources, nil, &out); err != nil {
		return nil, err
	}
	if out.Sources == nil {
		out.Sources = []LegacySource{}
	}
	return out.Sources, nil
}

func (c *legacyClient) Search(ctx context.Context, query string, sources []string) (LegacySearchResult, error) {
	args := map[string]any{"query": query}
	if len(sources) > 0 {
		args["sources"] = sources
	}
	var out LegacySearchResult
	if err := c.callJSON(ctx, ToolSearch, args, &out); err != nil {
		return LegacySearchResult{}, err
	}
	if out.Results == nil {
		out.Results = []LegacySearchHit{}
	}
	return out, nil
}

func (c *legacyClient) Fetch(ctx context.Context, source, docPath string, start, end int) (LegacyDocument, error) {
	args := map[string]any{"source": source, "path": docPath}
	if start > 0 {
		args["start_line"] = start
	}
	if end > 0 {
		args["end_line"] = end
	}
	var out LegacyDocument
	err := c.callJSON(ctx, ToolFetch, args, &out)
	return out, err
}

func (c *legacyClient) WriteDocument(ctx context.Context, source, docPath, content string) (LegacyWriteResult, error) {
	var out LegacyWriteResult
	err := c.callJSON(ctx, legacyToolWrite, map[string]any{"source": source, "path": docPath, "content": content}, &out)
	return out, err
}

func (c *legacyClient) callJSON(ctx context.Context, tool string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	text, err := c.mcp.CallToolContext(ctx, tool, args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("知识库 %s 返回无法解析：%s", tool, clipLegacy(text, 200))
	}
	return nil
}

// LegacyScopeSources intersects requested sources with the project's allowed list.
func LegacyScopeSources(allowed, requested []string) ([]string, error) {
	requested = CleanList(requested)
	allowed = CleanList(allowed)
	if len(allowed) == 0 {
		return requested, nil
	}
	if len(requested) == 0 {
		return allowed, nil
	}
	for _, s := range requested {
		if !LegacySourceAllowed(allowed, s) {
			return nil, fmt.Errorf("知识库来源 %q 不在当前项目的授权范围", s)
		}
	}
	return requested, nil
}

func LegacySourceAllowed(allowed []string, source string) bool {
	allowed = CleanList(allowed)
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(allowed, strings.TrimSpace(source))
}

func clipLegacy(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// Distilled holds optional front-matter metadata parsed from a legacy submission.
type Distilled struct {
	Title  string
	Source string
	Path   string
	Body   string
}

var legacyFrontMatterLine = regexp.MustCompile(`^([A-Za-z_]+)\s*:\s*(.*)$`)

// ParseDistilled reads optional YAML-ish front matter (title/source/path) and
// falls back to the first Markdown heading for the title.
func ParseDistilled(content string) Distilled {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	out := Distilled{Body: text}
	if header, body, ok := splitDistilledFrontMatter(text); ok {
		out.Body = body
		for _, line := range strings.Split(header, "\n") {
			m := legacyFrontMatterLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			value := strings.Trim(strings.TrimSpace(m[2]), `"'`)
			switch strings.ToLower(m[1]) {
			case "title":
				out.Title = value
			case "source":
				out.Source = value
			case "path":
				out.Path = value
			}
		}
	}
	if out.Title == "" {
		for _, line := range strings.Split(out.Body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				out.Title = strings.TrimSpace(strings.TrimLeft(line, "#"))
				break
			}
		}
	}
	return out
}

func splitDistilledFrontMatter(text string) (header, body string, ok bool) {
	if !strings.HasPrefix(text, "---\n") {
		return "", text, false
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", text, false
	}
	rest := text[4+end+4:]
	return text[4 : 4+end], strings.TrimLeft(strings.TrimPrefix(rest, "\n"), "\n"), true
}

var legacyUnsafePathChars = regexp.MustCompile(`[^\p{L}\p{N}._/-]+`)

// NormalizeDocPath keeps legacy upstream write paths repository-relative Markdown.
func NormalizeDocPath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = legacyUnsafePathChars.ReplaceAllString(p, "-")
	p = strings.TrimLeft(path.Clean("/"+p), "/")
	if p == "" || p == "." || strings.HasPrefix(p, "../") || !strings.HasSuffix(strings.ToLower(p), ".md") {
		return "", false
	}
	return p, true
}

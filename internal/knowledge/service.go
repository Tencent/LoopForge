package knowledge

import (
	"context"
	"io"
	"net/url"
	"slices"
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
		dial:  func(s domain.KnowledgeSettings) (Upstream, error) { return NewUpstream(s) },
		now:   time.Now,
		newID: newID,
	}
}

func (s *Service) Settings() domain.KnowledgeSettings { return s.store.KnowledgeSettings() }

type SettingsInput struct {
	Provider       string              `json:"provider"`
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
	provider := normalizeProvider(input.Provider, current.Provider)
	rawURL := strings.TrimSpace(input.URL)
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			msg := "知识库地址必须是 http(s)://host/mcp 形式"
			if provider == domain.KnowledgeProviderWeKnora {
				msg = "WeKnora 地址必须是 http(s)://host 形式"
			}
			return current, apperr.New(apperr.Invalid, msg)
		}
		if provider == domain.KnowledgeProviderWeKnora {
			rawURL = NormalizeWeKnoraBaseURL(rawURL)
		}
	}
	next := domain.KnowledgeSettings{
		Provider: provider,
		Enabled:  input.Enabled, URL: rawURL, WriteEnabled: input.WriteEnabled,
		Token:          resolveSettingsToken(input, current.Token),
		Connections:    current.Connections,
		DefaultSources: cleanSources(input.DefaultSources),
		ProjectSources: cleanProjectSources(input.ProjectSources), UpdatedBy: actor,
	}
	if len(next.Connections) == 1 && next.URL != "" && next.Token != "" {
		next.Connections[0].URL = next.URL
		next.Connections[0].Token = next.Token
		next.Connections[0].Provider = next.Provider
		next.Connections[0].Enabled = next.Enabled
	}
	if next.Enabled && (next.URL == "" || next.Token == "") {
		return current, apperr.New(apperr.Invalid, "启用前需要填写知识库地址和 Token")
	}
	if err := s.store.PutKnowledgeSettings(next); err != nil {
		return current, err
	}
	return s.store.KnowledgeSettings(), nil
}

func normalizeProvider(input, current string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case domain.KnowledgeProviderWeKnora:
		return domain.KnowledgeProviderWeKnora
	case domain.KnowledgeProviderLightCloud:
		return domain.KnowledgeProviderLightCloud
	case "":
		if strings.ToLower(strings.TrimSpace(current)) == domain.KnowledgeProviderWeKnora {
			return domain.KnowledgeProviderWeKnora
		}
		return domain.KnowledgeProviderLightCloud
	default:
		return domain.KnowledgeProviderLightCloud
	}
}

// cleanProjectSources normalizes the per-project source lists, dropping
// blank project keys. A nil map stays nil so "no overrides" is preserved.
func cleanProjectSources(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := map[string][]string{}
	for project, list := range in {
		if project = strings.TrimSpace(project); project != "" {
			out[project] = cleanSources(list)
		}
	}
	return out
}

// resolveSettingsToken picks the token to save: a new token (without its
// Bearer prefix), else the current one unless the input asks to clear it.
func resolveSettingsToken(input SettingsInput, current string) string {
	switch token := strings.TrimSpace(input.Token); {
	case token != "":
		return stripBearer(token)
	case !input.ClearToken:
		return current
	}
	return ""
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
	return s.CheckWithProvider(ctx, "", overrideURL, overrideToken)
}

// CheckWithProvider probes the upstream with optional provider/URL/token overrides.
func (s *Service) CheckWithProvider(ctx context.Context, overrideProvider, overrideURL, overrideToken string) CheckResult {
	settings := s.store.KnowledgeSettings()
	if strings.TrimSpace(overrideProvider) != "" {
		settings.Provider = normalizeProvider(overrideProvider, settings.Provider)
	}
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

// Sources lists upstream sources across all active connections, marking which ones projectID may read.
func (s *Service) Sources(ctx context.Context, projectID string) ([]SourceView, error) {
	settings := s.store.KnowledgeSettings()
	conns := settings.ActiveConnections()
	if len(conns) == 0 {
		return nil, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	allowed := settings.SourcesForProject(projectID)
	multi := len(conns) > 1
	out := make([]SourceView, 0)
	var firstErr error
	for _, conn := range conns {
		client, err := s.dial(settingsForConnection(settings, conn))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		list, err := client.ListSources(cctx)
		cancel()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, src := range list {
			scopedID := EncodeScopedSourceID(settings, conn.ID, src.ID)
			if multi && conn.Name != "" {
				src.Name = "[" + conn.Name + "] " + src.Name
			}
			src.ID = scopedID
			out = append(out, SourceView{Source: src, Allowed: SourceAllowed(allowed, scopedID)})
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, apperr.New(apperr.Upstream, "知识库不可达："+firstErr.Error())
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
	conns := settings.ActiveConnections()
	if len(conns) == 0 {
		return SearchOutput{}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	scoped, err := ScopeSources(settings.SourcesForProject(projectID), sources)
	if err != nil {
		return SearchOutput{}, apperr.New(apperr.Invalid, err.Error())
	}
	if len(conns) == 1 {
		client, err := s.connect(settingsForConnection(settings, conns[0]))
		if err != nil {
			return SearchOutput{}, err
		}
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		started := s.now()
		result, err := client.Search(cctx, query, scoped)
		if err != nil {
			return SearchOutput{}, apperr.New(apperr.Upstream, "检索失败："+err.Error())
		}
		return SearchOutput{Result: result, Sources: scoped, LatencyMs: s.now().Sub(started).Milliseconds()}, nil
	}

	grouped := map[string][]string{}
	if len(scoped) == 0 {
		for _, c := range conns {
			grouped[c.ID] = nil
		}
	} else {
		for _, sid := range scoped {
			conn, rawID, ok := DecodeScopedSourceID(settings, sid)
			if ok {
				grouped[conn.ID] = append(grouped[conn.ID], rawID)
			}
		}
	}
	started := s.now()
	var merged []SearchHit
	var firstErr error
	for _, conn := range conns {
		rawIDs, want := grouped[conn.ID]
		if !want {
			continue
		}
		client, err := s.dial(settingsForConnection(settings, conn))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		res, err := client.Search(cctx, query, rawIDs)
		cancel()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, h := range res.Results {
			h.Source = EncodeScopedSourceID(settings, conn.ID, h.Source)
			merged = append(merged, h)
		}
	}
	if len(merged) == 0 && firstErr != nil {
		return SearchOutput{}, apperr.New(apperr.Upstream, "检索失败："+firstErr.Error())
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	if merged == nil {
		merged = []SearchHit{}
	}
	return SearchOutput{
		Result:    SearchResult{Query: query, Mode: "hybrid", Results: merged},
		Sources:   scoped,
		LatencyMs: s.now().Sub(started).Milliseconds(),
	}, nil
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
	conn, rawSource, ok := DecodeScopedSourceID(settings, source)
	if !ok {
		return Document{}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	client, err := s.connect(settingsForConnection(settings, conn))
	if err != nil {
		return Document{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := client.Fetch(ctx, rawSource, path, start, end)
	if err != nil {
		return Document{}, apperr.New(apperr.Upstream, "读取原文失败："+err.Error())
	}
	doc.Source = source
	return doc, nil
}

// Documents lists documents inside a WeKnora knowledge base when authorized for projectID.
func (s *Service) Documents(ctx context.Context, projectID, sourceID, keyword string, page, pageSize int) (WeKnoraDocumentPage, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Invalid, "knowledge base id 必填")
	}
	settings := s.store.KnowledgeSettings()
	if !settings.Ready() {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), sourceID) {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	conn, rawKBID, ok := DecodeScopedSourceID(settings, sourceID)
	if !ok {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	if conn.Provider != "" && conn.Provider != domain.KnowledgeProviderWeKnora {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Invalid, "仅 WeKnora 引擎支持文档列表管理")
	}
	wc, err := NewWeKnoraClient(settingsForConnection(settings, conn))
	if err != nil {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Unavailable, err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	out, err := wc.ListDocuments(ctx, rawKBID, keyword, page, pageSize)
	if err != nil {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Upstream, "获取文档列表失败："+err.Error())
	}
	return out, nil
}

// UploadDocument uploads a file to a WeKnora knowledge base when authorized for projectID.
func (s *Service) UploadDocument(ctx context.Context, projectID, sourceID, filename string, r io.Reader) (WeKnoraDocument, error) {
	sourceID = strings.TrimSpace(sourceID)
	filename = strings.TrimSpace(filename)
	if sourceID == "" || filename == "" {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "知识库和文件名必填")
	}
	settings := s.store.KnowledgeSettings()
	if !settings.Ready() {
		return WeKnoraDocument{}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	if settings.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "仅 WeKnora 引擎支持文件上传")
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), sourceID) {
		return WeKnoraDocument{}, apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	wc, err := NewWeKnoraClient(settings)
	if err != nil {
		return WeKnoraDocument{}, apperr.New(apperr.Unavailable, err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	doc, err := wc.UploadDocument(ctx, sourceID, filename, r)
	if err != nil {
		return WeKnoraDocument{}, apperr.New(apperr.Upstream, "上传文档失败："+err.Error())
	}
	return doc, nil
}

func (s *Service) requireWeKnoraClient() (domain.KnowledgeSettings, *WeKnoraClient, error) {
	settings := s.store.KnowledgeSettings()
	if !settings.Ready() {
		return settings, nil, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	if settings.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return settings, nil, apperr.New(apperr.Invalid, "仅 WeKnora 引擎支持该操作")
	}
	wc, err := NewWeKnoraClient(settings)
	if err != nil {
		return settings, nil, apperr.New(apperr.Unavailable, err.Error())
	}
	return settings, wc, nil
}

func (s *Service) WeKnoraModels(ctx context.Context) ([]WeKnoraModel, error) {
	_, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	models, err := wc.ListModels(ctx)
	if err != nil {
		return nil, apperr.New(apperr.Upstream, "获取 WeKnora 模型列表失败："+err.Error())
	}
	return models, nil
}

func (s *Service) CreateWeKnoraKB(ctx context.Context, projectID string, input WeKnoraCreateKBInput) (weKnoraKB, error) {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return weKnoraKB{}, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return weKnoraKB{}, apperr.New(apperr.Invalid, "知识库名称不能为空")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kb, err := wc.CreateKnowledgeBase(ctx, input)
	if err != nil {
		return weKnoraKB{}, apperr.New(apperr.Upstream, "创建知识库失败："+err.Error())
	}
	// If this project has an explicit allowlist, auto-authorize the newly created KB for it.
	if pid := strings.TrimSpace(projectID); pid != "" {
		if list, ok := settings.ProjectSources[pid]; ok && len(list) > 0 && !slices.Contains(list, kb.ID) {
			settings.ProjectSources[pid] = append(list, kb.ID)
			_ = s.store.PutKnowledgeSettings(settings)
		}
	}
	return kb, nil
}

func (s *Service) UpdateWeKnoraKB(ctx context.Context, projectID, kbID, name, description string) (weKnoraKB, error) {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return weKnoraKB{}, err
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), kbID) {
		return weKnoraKB{}, apperr.New(apperr.Forbidden, "该知识库不在当前项目的授权范围")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kb, err := wc.UpdateKnowledgeBase(ctx, kbID, name, description)
	if err != nil {
		return weKnoraKB{}, apperr.New(apperr.Upstream, "更新知识库失败："+err.Error())
	}
	return kb, nil
}

func (s *Service) DeleteWeKnoraKB(ctx context.Context, projectID, kbID string) error {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return err
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), kbID) {
		return apperr.New(apperr.Forbidden, "该知识库不在当前项目的授权范围")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if err := wc.DeleteKnowledgeBase(ctx, kbID); err != nil {
		return apperr.New(apperr.Upstream, "删除知识库失败："+err.Error())
	}
	return nil
}

func (s *Service) CreateWeKnoraURLDoc(ctx context.Context, projectID, kbID, rawURL string) (WeKnoraDocument, error) {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return WeKnoraDocument{}, err
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), kbID) {
		return WeKnoraDocument{}, apperr.New(apperr.Forbidden, "该知识库不在当前项目的授权范围")
	}
	if strings.TrimSpace(rawURL) == "" {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "网页或文档 URL 不能为空")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	doc, err := wc.CreateDocumentFromURL(ctx, kbID, rawURL)
	if err != nil {
		return WeKnoraDocument{}, apperr.New(apperr.Upstream, "URL 导入失败："+err.Error())
	}
	return doc, nil
}

func (s *Service) CreateWeKnoraManualDoc(ctx context.Context, projectID, kbID, title, content string, publish bool) (WeKnoraDocument, error) {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return WeKnoraDocument{}, err
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), kbID) {
		return WeKnoraDocument{}, apperr.New(apperr.Forbidden, "该知识库不在当前项目的授权范围")
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(content) == "" {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "标题和正文不能为空")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := wc.CreateManualDocument(ctx, kbID, title, content, publish)
	if err != nil {
		return WeKnoraDocument{}, apperr.New(apperr.Upstream, "创建文档失败："+err.Error())
	}
	return doc, nil
}

func (s *Service) UpdateWeKnoraManualDoc(ctx context.Context, projectID, docID, title, content string, publish bool) (WeKnoraDocument, error) {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return WeKnoraDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if allowed := settings.SourcesForProject(projectID); len(allowed) > 0 {
		kbID, err := wc.GetDocumentKBID(ctx, docID)
		if err != nil {
			return WeKnoraDocument{}, apperr.New(apperr.Upstream, "校验文档归属失败："+err.Error())
		}
		if !SourceAllowed(allowed, kbID) {
			return WeKnoraDocument{}, apperr.New(apperr.Forbidden, "该文档所属知识库不在当前项目的授权范围")
		}
	}
	doc, err := wc.UpdateManualDocument(ctx, docID, title, content, publish)
	if err != nil {
		return WeKnoraDocument{}, apperr.New(apperr.Upstream, "更新文档失败："+err.Error())
	}
	return doc, nil
}

func (s *Service) ReparseWeKnoraDoc(ctx context.Context, projectID, docID string) error {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if allowed := settings.SourcesForProject(projectID); len(allowed) > 0 {
		kbID, err := wc.GetDocumentKBID(ctx, docID)
		if err != nil {
			return apperr.New(apperr.Upstream, "校验文档归属失败："+err.Error())
		}
		if !SourceAllowed(allowed, kbID) {
			return apperr.New(apperr.Forbidden, "该文档所属知识库不在当前项目的授权范围")
		}
	}
	if err := wc.ReparseDocument(ctx, docID); err != nil {
		return apperr.New(apperr.Upstream, "触发重解析失败："+err.Error())
	}
	return nil
}

func (s *Service) DeleteWeKnoraDoc(ctx context.Context, projectID, docID string) error {
	settings, wc, err := s.requireWeKnoraClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if allowed := settings.SourcesForProject(projectID); len(allowed) > 0 {
		kbID, err := wc.GetDocumentKBID(ctx, docID)
		if err != nil {
			return apperr.New(apperr.Upstream, "校验文档归属失败："+err.Error())
		}
		if !SourceAllowed(allowed, kbID) {
			return apperr.New(apperr.Forbidden, "该文档所属知识库不在当前项目的授权范围")
		}
	}
	if err := wc.DeleteDocument(ctx, docID); err != nil {
		return apperr.New(apperr.Upstream, "删除文档失败："+err.Error())
	}
	return nil
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
	conn, rawSource, ok := DecodeScopedSourceID(settings, source)
	if !ok {
		return item, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	connSettings := settingsForConnection(settings, conn)
	var docPath string
	if connSettings.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
		docPath = strings.TrimSpace(item.Path)
	} else {
		var ok bool
		docPath, ok = NormalizeDocPath(item.Path)
		if !ok {
			return item, apperr.New(apperr.Invalid, "写入路径必须是仓库内的 .md 相对路径，例如 guides/xxx.md")
		}
	}
	content := item.Content
	if title := strings.TrimSpace(item.Title); title != "" && !strings.HasPrefix(strings.TrimSpace(content), "#") {
		content = "# " + title + "\n\n" + content
	}
	client, err := s.dial(connSettings)
	if err != nil {
		return item, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	result, err := client.WriteDocument(ctx, rawSource, docPath, content)
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

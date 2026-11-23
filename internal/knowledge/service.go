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
	// MutateKnowledgeSettings applies an in-place change to the latest stored
	// settings inside a transaction; mutate returns true when it changed them.
	MutateKnowledgeSettings(func(*domain.KnowledgeSettings) bool) error
	MutateKnowledgeSettingsIfFresh(func(*domain.KnowledgeSettings) (bool, error)) (bool, error)
	DeleteKnowledgeConnectionToken(connID string) error
	ManagedWeKnoraAdminPassword() string
	PutManagedWeKnoraAdminPassword(password string) error
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
	store        Store
	dial         func(domain.KnowledgeSettings) (Upstream, error)
	now          func() time.Time
	newID        func() string
	capabilities *capabilityCache
}

func NewService(store Store, newID func() string) *Service {
	return &Service{
		store:        store,
		dial:         func(s domain.KnowledgeSettings) (Upstream, error) { return NewUpstream(s) },
		now:          time.Now,
		newID:        newID,
		capabilities: newCapabilityCache(),
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
	before := append([]domain.KnowledgeConnection(nil), current.Connections...)
	provider := normalizeProvider(input.Provider, current.Provider)
	rawURL := strings.TrimSpace(input.URL)
	if rawURL != "" {
		if provider == domain.KnowledgeProviderWeKnora {
			// WeKnora 的落库入口与客户端构造共用一套校验，避免带查询参数的
			// 地址被保存后拼坏整条请求链路或在回显中泄露密钥。
			normalized, err := ValidateOutboundWeKnoraURL(rawURL)
			if err != nil {
				return current, apperr.New(apperr.Invalid, err.Error())
			}
			rawURL = normalized
		} else {
			u, err := url.Parse(rawURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return current, apperr.New(apperr.Invalid, "知识库地址必须是 http(s)://host/mcp 形式")
			}
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
	if next.Enabled {
		if len(next.Connections) > 0 {
			if len(next.ActiveConnections()) == 0 {
				return current, apperr.New(apperr.Invalid, "启用前需要至少启用一个有效的知识库连接")
			}
		} else if next.URL == "" || next.Token == "" {
			return current, apperr.New(apperr.Invalid, "启用前需要填写知识库地址和 Token")
		}
	}
	if err := s.store.PutKnowledgeSettings(next); err != nil {
		return current, err
	}
	s.invalidateChangedConnectionCapabilities(before, next.Connections)
	return s.store.KnowledgeSettings(), nil
}

// invalidateChangedConnectionCapabilities drops cached verdicts only for
// connections that were added, removed or reconfigured. Unrelated connections
// keep their probe results instead of paying for a fresh detection.
func (s *Service) invalidateChangedConnectionCapabilities(before, after []domain.KnowledgeConnection) {
	if len(before) == 0 && len(after) == 0 {
		s.capabilities.invalidate("")
		return
	}
	prev := make(map[string]string, len(before))
	for _, conn := range before {
		prev[conn.ID] = capabilityFingerprint(conn)
	}
	next := make(map[string]struct{}, len(after))
	for _, conn := range after {
		next[conn.ID] = struct{}{}
		if fp, ok := prev[conn.ID]; !ok || fp != capabilityFingerprint(conn) {
			s.capabilities.invalidate(conn.ID)
		}
	}
	for id := range prev {
		if _, ok := next[id]; !ok {
			s.capabilities.invalidate(id)
		}
	}
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
	OK           bool                           `json:"ok"`
	Error        string                         `json:"error,omitempty"`
	LatencyMs    int64                          `json:"latencyMs"`
	Sources      []Source                       `json:"sources"`
	Tools        []string                       `json:"tools,omitempty"`
	ConnectionID string                         `json:"connectionId,omitempty"`
	Capabilities *domain.ConnectionCapabilities `json:"capabilities,omitempty"`
}

// Check probes the upstream with the saved settings, overridden by any
// unsaved URL/token so admins can verify a token before enabling it.
func (s *Service) Check(ctx context.Context, overrideURL, overrideToken string) CheckResult {
	return s.CheckWithProvider(ctx, "", overrideURL, overrideToken)
}

// CheckWithProvider probes the upstream with optional provider/URL/token overrides.
func (s *Service) CheckWithProvider(ctx context.Context, overrideProvider, overrideURL, overrideToken string) CheckResult {
	return s.CheckConnection(ctx, "", overrideProvider, overrideURL, overrideToken)
}

// CheckConnection probes one explicitly named connection (or the first enabled
// one when the caller has no per-connection UI). Capability probes never flip
// OK: a reachable instance with unprobeable management endpoints is still
// reported as connected, with the inconclusive dimensions marked unknown.
func (s *Service) CheckConnection(ctx context.Context, connectionID, overrideProvider, overrideURL, overrideToken string) CheckResult {
	settings := s.store.KnowledgeSettings()
	topProvider := settings.EffectiveProvider()
	connectionID = strings.TrimSpace(connectionID)
	var conn domain.KnowledgeConnection
	switch {
	case connectionID != "" && !isLegacyDefaultConnection(settings, connectionID):
		found := false
		for _, c := range settings.Connections {
			if c.ID == connectionID {
				conn, found = c, true
				break
			}
		}
		if !found {
			return CheckResult{ConnectionID: connectionID, Error: "指定的知识库连接不存在"}
		}
	case len(settings.Connections) > 0:
		active := settings.ActiveConnections()
		if len(active) == 0 {
			return CheckResult{Error: "没有已启用的知识库连接"}
		}
		conn = active[0]
	default:
		conn = domain.KnowledgeConnection{ID: "default", Name: "默认连接", Provider: topProvider, URL: settings.URL, Token: settings.Token, Enabled: settings.Enabled}
	}
	// Historical rows may omit the per-connection provider; materialize it the
	// same way the management resolver does so both paths agree on the provider.
	if strings.TrimSpace(conn.Provider) == "" {
		conn.Provider = topProvider
	}
	settings.Provider, settings.URL, settings.Token = conn.EffectiveProvider(), conn.URL, conn.Token
	if strings.TrimSpace(overrideProvider) != "" {
		settings.Provider = normalizeProvider(overrideProvider, settings.Provider)
		conn.Provider = settings.Provider
	}
	if u := strings.TrimSpace(overrideURL); u != "" {
		settings.URL, conn.URL = u, u
	}
	if t := strings.TrimSpace(overrideToken); t != "" {
		settings.Token = stripBearer(t)
		conn.Token = settings.Token
	}
	client, err := s.dial(settings)
	if err != nil {
		return CheckResult{ConnectionID: conn.ID, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := s.now()
	sources, err := client.ListSources(ctx)
	latency := s.now().Sub(started).Milliseconds()
	if err != nil {
		return CheckResult{ConnectionID: conn.ID, Error: err.Error(), LatencyMs: latency}
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
	// A user-initiated check must observe the upstream now, not a stale cached
	// verdict from before a permission or deployment fix.
	s.capabilities.invalidate(conn.ID)
	capabilities := s.connectionCapabilities(ctx, conn, settings)
	return CheckResult{OK: true, LatencyMs: latency, Sources: sources, Tools: tools, ConnectionID: conn.ID, Capabilities: &capabilities}
}

// connectionCapabilities probes and caches the management capability set of
// one connection. Non-WeKnora providers are known to have no management API.
func (s *Service) connectionCapabilities(ctx context.Context, conn domain.KnowledgeConnection, settings domain.KnowledgeSettings) domain.ConnectionCapabilities {
	if conn.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return capabilitiesWithReason(domain.CapabilityUnavailable, capabilityUnsupportedProviderReason)
	}
	wc, err := NewWeKnoraClient(settingsForConnection(settings, conn))
	if err != nil {
		return capabilitiesWithReason(domain.CapabilityUnknown, "无法创建 WeKnora 客户端："+err.Error())
	}
	return s.capabilities.get(ctx, conn, func(probeCtx context.Context) domain.ConnectionCapabilities {
		return probeConnectionCapabilities(probeCtx, conn, wc)
	})
}

type SourceView struct {
	Source
	Allowed bool `json:"allowed"`
}

// SourcesForConnection lists the knowledge bases of exactly one configured
// connection, including a disabled one, so the console can still manage a
// paused connection explicitly. The project allow-list still applies: a
// disabled connection is manageable, not an authorization bypass.
func (s *Service) SourcesForConnection(ctx context.Context, projectID, connectionID string) ([]SourceView, error) {
	settings, conn, _, err := s.resolveExplicitWeKnoraConnection(connectionID)
	if err != nil {
		return nil, err
	}
	client, err := s.dial(settingsForConnection(settings, conn))
	if err != nil {
		return nil, apperr.New(apperr.Unavailable, err.Error())
	}
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	list, err := client.ListSources(cctx)
	cancel()
	if err != nil {
		return nil, apperr.New(apperr.Upstream, "知识库不可达："+err.Error())
	}
	allowed := settings.SourcesForProject(projectID)
	out := make([]SourceView, 0, len(list))
	for _, src := range list {
		scopedID := EncodeScopedSourceID(settings, conn.ID, src.ID)
		src.ID = scopedID
		out = append(out, SourceView{Source: src, Allowed: SourceAllowed(allowed, scopedID)})
	}
	return out, nil
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
	Status    string       `json:"status,omitempty"`
	Warning   string       `json:"warning,omitempty"`
	Sources   []string     `json:"sources"`
	LatencyMs int64        `json:"latencyMs"`
}

// Search runs a trial search limited to the sources projectID may read.
func (s *Service) Search(ctx context.Context, projectID, query string, sources []string) (SearchOutput, error) {
	out, _, err := s.ExecuteSearch(ctx, projectID, query, sources, 10, ExecuteOptions{})
	return out, err
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
	rawPath := path
	if conn.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
		if cid, rawDoc, cutOK := strings.Cut(path, "::"); cutOK {
			if cid != conn.ID {
				return Document{}, apperr.New(apperr.Invalid, "文档与知识库连接不匹配")
			}
			rawPath = rawDoc
		}
	}
	client, err := s.connect(settingsForConnection(settings, conn))
	if err != nil {
		return Document{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := client.Fetch(ctx, rawSource, rawPath, start, end)
	if err != nil {
		return Document{}, apperr.New(apperr.Upstream, "读取原文失败："+err.Error())
	}
	doc.Source = source
	return doc, nil
}

// Documents lists documents inside a WeKnora knowledge base when authorized for projectID.
func (s *Service) Documents(ctx context.Context, projectID, connectionID, sourceID, keyword string, page, pageSize int) (WeKnoraDocumentPage, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Invalid, "knowledge base id 必填")
	}
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, sourceID)
	if err != nil {
		return WeKnoraDocumentPage{}, err
	}
	if err := requireWeKnoraClient(conn, wc); err != nil {
		return WeKnoraDocumentPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	out, err := wc.ListDocuments(ctx, rawKBID, keyword, page, pageSize)
	if err != nil {
		return WeKnoraDocumentPage{}, apperr.New(apperr.Upstream, "获取文档列表失败："+err.Error())
	}
	for i := range out.Documents {
		out.Documents[i] = scopeWeKnoraDoc(settings, conn.ID, out.Documents[i])
	}
	return out, nil
}

// UploadDocument uploads a file to a WeKnora knowledge base when authorized for projectID.
func (s *Service) UploadDocument(ctx context.Context, projectID, connectionID, sourceID, filename string, r io.Reader) (WeKnoraDocument, error) {
	return s.UploadDocumentReader(ctx, projectID, connectionID, sourceID, filename, r)
}

// UploadDocumentReader streams a file into a knowledge base after the
// doc_write capability gate has approved the connection.
func (s *Service) UploadDocumentReader(ctx context.Context, projectID, connectionID, sourceID, filename string, r io.Reader) (WeKnoraDocument, error) {
	sourceID = strings.TrimSpace(sourceID)
	filename = strings.TrimSpace(filename)
	if sourceID == "" || filename == "" {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "知识库和文件名必填")
	}
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, sourceID)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapDocWrite); err != nil {
		return WeKnoraDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	s.ensureWeKnoraKBEmbeddingModel(ctx, wc, rawKBID)
	doc, err := wc.UploadDocument(ctx, rawKBID, filename, r)
	if err != nil {
		return WeKnoraDocument{}, s.managementError(conn, domain.CapDocWrite, "上传文档", err)
	}
	return scopeWeKnoraDoc(settings, conn.ID, doc), nil
}

// isLegacyDefaultConnection reports whether connectionID names the implicit
// "default" connection. Legacy single-URL deployments expose exactly that id
// through the API, so it must behave like an omitted id there; with explicit
// connections configured it stays an unknown id.
func isLegacyDefaultConnection(settings domain.KnowledgeSettings, connectionID string) bool {
	return connectionID == "default" && len(settings.Connections) == 0
}

// resolveExplicitWeKnoraConnection selects exactly the connection the caller
// named. Multi-connection deployments have no implicit fallback: a missing id
// is a client error so a write never lands on an arbitrary instance. A legacy
// single-URL deployment maps its top-level settings onto the implicit
// "default" connection. Disabled connections stay manageable as long as their
// URL and token are configured.
func (s *Service) resolveExplicitWeKnoraConnection(connectionID string) (domain.KnowledgeSettings, domain.KnowledgeConnection, *WeKnoraClient, error) {
	settings := s.store.KnowledgeSettings()
	connectionID = strings.TrimSpace(connectionID)
	var conn domain.KnowledgeConnection
	switch {
	case connectionID != "" && !isLegacyDefaultConnection(settings, connectionID):
		found := false
		for _, c := range settings.Connections {
			if c.ID == connectionID {
				conn, found = c, true
				break
			}
		}
		if !found {
			return settings, domain.KnowledgeConnection{}, nil, apperr.New(apperr.NotFound, "指定的知识库连接不存在")
		}
	case len(settings.Connections) > 0:
		return settings, domain.KnowledgeConnection{}, nil, apperr.New(apperr.Invalid, "请指定知识库连接 connectionId")
	case strings.TrimSpace(settings.URL) != "" && strings.TrimSpace(settings.Token) != "":
		conn = domain.KnowledgeConnection{ID: "default", Name: "默认连接", Provider: settings.EffectiveProvider(), URL: settings.URL, Token: settings.Token, Enabled: settings.Enabled}
	default:
		return settings, domain.KnowledgeConnection{}, nil, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	if strings.TrimSpace(conn.Provider) == "" {
		conn.Provider = settings.EffectiveProvider()
	}
	if strings.TrimSpace(conn.URL) == "" || strings.TrimSpace(conn.Token) == "" {
		return settings, conn, nil, apperr.New(apperr.Invalid, "知识库连接缺少地址或 Token")
	}
	if conn.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return settings, conn, nil, nil
	}
	wc, err := NewWeKnoraClient(settingsForConnection(settings, conn))
	if err != nil {
		return settings, conn, nil, apperr.New(apperr.Unavailable, err.Error())
	}
	return settings, conn, wc, nil
}

// requireWeKnoraClient rejects management calls on non-WeKnora providers and
// on connections whose client could not be built.
func requireWeKnoraClient(conn domain.KnowledgeConnection, wc *WeKnoraClient) error {
	if conn.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return apperr.New(apperr.Invalid, "仅 WeKnora 引擎支持该操作")
	}
	if wc == nil {
		return apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	return nil
}

// requireCapability is the write gate: it resolves the cached verdict for one
// capability and turns unavailable or inconclusive probes into 503 errors that
// name the dimension, so callers never reach the upstream business endpoint.
func (s *Service) requireCapability(ctx context.Context, conn domain.KnowledgeConnection, wc *WeKnoraClient, capability domain.KnowledgeCapability) error {
	caps := s.cachedCapabilities(ctx, conn, wc)
	status := caps.Capabilities[capability]
	if status.Status != domain.CapabilityAvailable {
		return capabilityUnavailableError(conn.ID, capability, status)
	}
	return nil
}

// cachedCapabilities probes (or reuses) the capability set for a connection.
func (s *Service) cachedCapabilities(ctx context.Context, conn domain.KnowledgeConnection, wc *WeKnoraClient) domain.ConnectionCapabilities {
	if conn.EffectiveProvider() != domain.KnowledgeProviderWeKnora {
		return capabilitiesWithReason(domain.CapabilityUnavailable, capabilityUnsupportedProviderReason)
	}
	if wc == nil {
		return capabilitiesWithReason(domain.CapabilityUnknown, "未配置 WeKnora 客户端，无法探测能力")
	}
	return s.capabilities.get(ctx, conn, func(probeCtx context.Context) domain.ConnectionCapabilities {
		return probeConnectionCapabilities(probeCtx, conn, wc)
	})
}

// managementError maps a business request failure: a credential/route denial
// demotes the cached verdict and reports capability_unavailable, everything
// else stays an upstream error. preview_download is excluded because its two
// endpoints (preview and download) have independent permissions: one denial
// says nothing about the other, and demoting it would block the working one.
func (s *Service) managementError(conn domain.KnowledgeConnection, capability domain.KnowledgeCapability, action string, err error) error {
	if status, denied := s.capabilities.demoteDenied(conn, capability, err); denied {
		return capabilityUnavailableError(conn.ID, capability, status)
	}
	return apperr.New(apperr.Upstream, action+"失败："+err.Error())
}

// decodeWeKnoraSource resolves a scoped knowledge base id without applying
// project scope. A `connID::rawID` prefix wins; an explicit connectionId must
// agree with it. Bare ids are only accepted for the legacy single connection.
func (s *Service) decodeWeKnoraSource(connectionID, scopedKBID string) (domain.KnowledgeSettings, domain.KnowledgeConnection, *WeKnoraClient, string, error) {
	connectionID = strings.TrimSpace(connectionID)
	scopedKBID = strings.TrimSpace(scopedKBID)
	if scopedKBID == "" {
		return s.store.KnowledgeSettings(), domain.KnowledgeConnection{}, nil, "", apperr.New(apperr.Invalid, "knowledge base id 必填")
	}
	if strings.Contains(connectionID, "::") {
		return s.store.KnowledgeSettings(), domain.KnowledgeConnection{}, nil, "", apperr.New(apperr.Invalid, "connectionId 不能包含 ::")
	}
	settings := s.store.KnowledgeSettings()
	rawKBID := scopedKBID
	if id, raw, ok := strings.Cut(scopedKBID, "::"); ok {
		id = strings.TrimSpace(id)
		// A scoped id has exactly one prefix. Extra separators mean the caller
		// sent a non-canonical id: reject it instead of authorizing one id and
		// addressing another.
		if id == "" || strings.Contains(raw, "::") {
			return settings, domain.KnowledgeConnection{}, nil, "", apperr.New(apperr.Invalid, "知识库 ID 格式非法")
		}
		if connectionID != "" && connectionID != id {
			return settings, domain.KnowledgeConnection{}, nil, "", apperr.New(apperr.Invalid, "文档与知识库连接不匹配")
		}
		connectionID, rawKBID = id, strings.TrimSpace(raw)
	}
	if strings.TrimSpace(rawKBID) == "" {
		return settings, domain.KnowledgeConnection{}, nil, "", apperr.New(apperr.Invalid, "knowledge base id 必填")
	}
	if connectionID == "" && len(settings.Connections) > 0 {
		return settings, domain.KnowledgeConnection{}, nil, "", apperr.New(apperr.Invalid, "请指定知识库连接 connectionId")
	}
	settings, conn, wc, err := s.resolveExplicitWeKnoraConnection(connectionID)
	if err != nil {
		return settings, conn, wc, "", err
	}
	return settings, conn, wc, rawKBID, nil
}

func (s *Service) resolveWeKnoraSource(projectID, connectionID, scopedKBID string) (domain.KnowledgeSettings, domain.KnowledgeConnection, *WeKnoraClient, string, error) {
	settings, conn, wc, rawKBID, err := s.decodeWeKnoraSource(connectionID, scopedKBID)
	if err != nil {
		return settings, conn, wc, "", err
	}
	if !SourceAllowed(settings.SourcesForProject(projectID), EncodeScopedSourceID(settings, conn.ID, rawKBID)) {
		return settings, conn, wc, "", apperr.New(apperr.Forbidden, "该知识库来源不在当前项目的授权范围")
	}
	return settings, conn, wc, rawKBID, nil
}

func (s *Service) resolveWeKnoraDoc(ctx context.Context, projectID, connectionID, scopedDocID string) (domain.KnowledgeSettings, domain.KnowledgeConnection, *WeKnoraClient, string, error) {
	settings, conn, wc, rawDocID, err := s.decodeWeKnoraSource(connectionID, scopedDocID)
	if err != nil {
		return settings, conn, wc, "", err
	}
	if err := s.authorizeWeKnoraDoc(ctx, projectID, settings, conn, wc, rawDocID); err != nil {
		return settings, conn, wc, "", err
	}
	return settings, conn, wc, rawDocID, nil
}

// resolveWeKnoraDocGated applies the capability gate before the parent
// knowledge base lookup, so an unavailable management endpoint is rejected
// without issuing any business request to the upstream instance.
func (s *Service) resolveWeKnoraDocGated(ctx context.Context, projectID, connectionID, scopedDocID string, capability domain.KnowledgeCapability) (domain.KnowledgeSettings, domain.KnowledgeConnection, *WeKnoraClient, string, error) {
	settings, conn, wc, rawDocID, err := s.decodeWeKnoraSource(connectionID, scopedDocID)
	if err != nil {
		return settings, conn, wc, "", err
	}
	if err := s.requireCapability(ctx, conn, wc, capability); err != nil {
		return settings, conn, wc, "", err
	}
	if err := s.authorizeWeKnoraDoc(ctx, projectID, settings, conn, wc, rawDocID); err != nil {
		return settings, conn, wc, "", err
	}
	return settings, conn, wc, rawDocID, nil
}

// authorizeWeKnoraDoc verifies that the document's parent knowledge base is
// inside the project's allow-list. With no allow-list the document is scoped
// by the project's own data only, matching resolveWeKnoraSource.
func (s *Service) authorizeWeKnoraDoc(ctx context.Context, projectID string, settings domain.KnowledgeSettings, conn domain.KnowledgeConnection, wc *WeKnoraClient, rawDocID string) error {
	allowed := settings.SourcesForProject(projectID)
	if len(allowed) == 0 {
		return nil
	}
	if err := requireWeKnoraClient(conn, wc); err != nil {
		return err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	rawKBID, err := wc.GetDocumentKBID(lookupCtx, rawDocID)
	if err != nil {
		return apperr.New(apperr.Upstream, "校验文档归属失败："+err.Error())
	}
	if !SourceAllowed(allowed, EncodeScopedSourceID(settings, conn.ID, rawKBID)) {
		return apperr.New(apperr.Forbidden, "该文档所属知识库不在当前项目的授权范围")
	}
	return nil
}

func scopeWeKnoraDoc(settings domain.KnowledgeSettings, connID string, doc WeKnoraDocument) WeKnoraDocument {
	doc.ID = EncodeScopedSourceID(settings, connID, doc.ID)
	if strings.TrimSpace(doc.KnowledgeBaseID) != "" {
		doc.KnowledgeBaseID = EncodeScopedSourceID(settings, connID, doc.KnowledgeBaseID)
	}
	// The manual body can be large and is only needed by the detail endpoint;
	// list and mutation responses stay small.
	doc.Metadata = nil
	return doc
}

func (s *Service) WeKnoraModels(ctx context.Context, connectionID string) ([]WeKnoraModel, error) {
	_, conn, wc, err := s.resolveExplicitWeKnoraConnection(connectionID)
	if err != nil {
		return nil, err
	}
	if err := requireWeKnoraClient(conn, wc); err != nil {
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

func (s *Service) CreateWeKnoraModel(ctx context.Context, input WeKnoraCreateModelInput) (WeKnoraModel, error) {
	_, conn, wc, err := s.resolveExplicitWeKnoraConnection(input.ConnectionID)
	if err != nil {
		return WeKnoraModel{}, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return WeKnoraModel{}, apperr.New(apperr.Invalid, "模型名称不能为空")
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapKBManage); err != nil {
		return WeKnoraModel{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	model, err := wc.CreateModel(ctx, input)
	if err != nil {
		return WeKnoraModel{}, s.managementError(conn, domain.CapKBManage, "创建 WeKnora 模型", err)
	}
	return model, nil
}

func (s *Service) CreateWeKnoraKB(ctx context.Context, projectID string, input WeKnoraCreateKBInput) (weKnoraKB, error) {
	settings, conn, wc, err := s.resolveExplicitWeKnoraConnection(input.ConnectionID)
	if err != nil {
		return weKnoraKB{}, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return weKnoraKB{}, apperr.New(apperr.Invalid, "知识库名称不能为空")
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapKBManage); err != nil {
		return weKnoraKB{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kb, err := wc.CreateKnowledgeBase(ctx, input)
	if err != nil {
		return weKnoraKB{}, s.managementError(conn, domain.CapKBManage, "创建知识库", err)
	}
	kb.ID = EncodeScopedSourceID(settings, conn.ID, kb.ID)
	if err := s.authorizeCreatedKB(projectID, kb.ID); err != nil {
		// The upstream create already succeeded; report a partial failure with
		// the new id so the caller can grant access instead of retrying blindly.
		return weKnoraKB{}, apperr.New(apperr.Upstream, "知识库已在上游创建（"+kb.ID+"），但授权白名单保存失败，请稍后在接入配置中手动授权："+err.Error()).With("id", kb.ID)
	}
	return kb, nil
}

// authorizeCreatedKB adds a freshly created upstream KB to the restricted
// allow-list that applies to projectID (or to DefaultSources for global
// creation) so the creator is not denied immediately after a successful
// create. The mutation re-reads the latest settings inside a transaction, so a
// concurrent create/delete or an admin edit cannot be overwritten. Empty lists
// mean "unrestricted" and need no entry.
func (s *Service) authorizeCreatedKB(projectID, scopedID string) error {
	pid := strings.TrimSpace(projectID)
	return s.store.MutateKnowledgeSettings(func(cur *domain.KnowledgeSettings) bool {
		if pid != "" {
			allowed := cur.SourcesForProject(pid)
			if len(allowed) == 0 || slices.Contains(allowed, scopedID) {
				return false
			}
			if cur.ProjectSources == nil {
				cur.ProjectSources = make(map[string][]string)
			}
			base := cur.ProjectSources[pid]
			if len(base) == 0 {
				// The project inherited DefaultSources; materialize its own
				// entry so only this project gains the new KB.
				base = slices.Clone(cur.DefaultSources)
			}
			cur.ProjectSources[pid] = append(base, scopedID)
			return true
		}
		if len(cur.DefaultSources) == 0 || slices.Contains(cur.DefaultSources, scopedID) {
			return false
		}
		cur.DefaultSources = append(slices.Clone(cur.DefaultSources), scopedID)
		return true
	})
}

// GetWeKnoraKB returns the management detail of one knowledge base.
func (s *Service) GetWeKnoraKB(ctx context.Context, projectID, connectionID, kbID string) (weKnoraKB, error) {
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, kbID)
	if err != nil {
		return weKnoraKB{}, err
	}
	if err := requireWeKnoraClient(conn, wc); err != nil {
		return weKnoraKB{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kb, err := wc.GetKnowledgeBase(ctx, rawKBID)
	if err != nil {
		return weKnoraKB{}, apperr.New(apperr.Upstream, "获取知识库详情失败："+err.Error())
	}
	kb.ID = EncodeScopedSourceID(settings, conn.ID, kb.ID)
	return kb, nil
}

func (s *Service) UpdateWeKnoraKB(ctx context.Context, projectID, connectionID, kbID, name, description string) (weKnoraKB, error) {
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, kbID)
	if err != nil {
		return weKnoraKB{}, err
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapKBManage); err != nil {
		return weKnoraKB{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kb, err := wc.UpdateKnowledgeBase(ctx, rawKBID, name, description)
	if err != nil {
		return weKnoraKB{}, s.managementError(conn, domain.CapKBManage, "更新知识库", err)
	}
	kb.ID = EncodeScopedSourceID(settings, conn.ID, kb.ID)
	return kb, nil
}

// PatchWeKnoraKB updates only the supplied fields; nil means "leave as is".
func (s *Service) PatchWeKnoraKB(ctx context.Context, projectID, connectionID, kbID string, name, description *string) (weKnoraKB, error) {
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, kbID)
	if err != nil {
		return weKnoraKB{}, err
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapKBManage); err != nil {
		return weKnoraKB{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kb, err := wc.PatchKnowledgeBase(ctx, rawKBID, name, description)
	if err != nil {
		return weKnoraKB{}, s.managementError(conn, domain.CapKBManage, "更新知识库", err)
	}
	kb.ID = EncodeScopedSourceID(settings, conn.ID, kb.ID)
	return kb, nil
}

func (s *Service) DeleteWeKnoraKB(ctx context.Context, projectID, connectionID, kbID string) error {
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, kbID)
	if err != nil {
		return err
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapKBManage); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if err := wc.DeleteKnowledgeBase(ctx, rawKBID); err != nil {
		return s.managementError(conn, domain.CapKBManage, "删除知识库", err)
	}
	scopedID := EncodeScopedSourceID(settings, conn.ID, rawKBID)
	if err := s.pruneSourceAllowlists(scopedID); err != nil {
		// The upstream delete already happened; surface the stale local grant
		// instead of silently keeping (or losing) it.
		return apperr.New(apperr.Upstream, "知识库已在上游删除（"+scopedID+"），但本地授权白名单清理失败，请稍后在接入配置中手动处理："+err.Error()).With("id", scopedID)
	}
	return nil
}

// pruneSourceAllowlists drops a deleted knowledge base from project and default
// allow-lists. A list is left untouched when removing the id would empty it:
// SourceAllowed treats len == 0 as unrestricted, so clearing the last entry
// would widen access to every source instead of narrowing it.
func (s *Service) pruneSourceAllowlists(scopedID string) error {
	return s.store.MutateKnowledgeSettings(func(settings *domain.KnowledgeSettings) bool {
		changed := false
		for pid, list := range settings.ProjectSources {
			remaining := withoutSource(list, scopedID)
			if len(remaining) == len(list) || len(remaining) == 0 {
				continue
			}
			settings.ProjectSources[pid] = remaining
			changed = true
		}
		if remaining := withoutSource(settings.DefaultSources, scopedID); len(remaining) != len(settings.DefaultSources) && len(remaining) > 0 {
			settings.DefaultSources = remaining
			changed = true
		}
		return changed
	})
}

func withoutSource(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, id := range list {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) CreateWeKnoraURLDoc(ctx context.Context, projectID, connectionID, kbID, rawURL string) (WeKnoraDocument, error) {
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, kbID)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	if strings.TrimSpace(rawURL) == "" {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "网页或文档 URL 不能为空")
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapDocWrite); err != nil {
		return WeKnoraDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*CallTimeout)
	defer cancel()
	s.ensureWeKnoraKBEmbeddingModel(ctx, wc, rawKBID)
	doc, err := wc.CreateDocumentFromURL(ctx, rawKBID, rawURL)
	if err != nil {
		return WeKnoraDocument{}, s.managementError(conn, domain.CapDocWrite, "URL 导入", err)
	}
	return scopeWeKnoraDoc(settings, conn.ID, doc), nil
}

func (s *Service) CreateWeKnoraManualDoc(ctx context.Context, projectID, connectionID, kbID, title, content string, publish bool) (WeKnoraDocument, error) {
	settings, conn, wc, rawKBID, err := s.resolveWeKnoraSource(projectID, connectionID, kbID)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(content) == "" {
		return WeKnoraDocument{}, apperr.New(apperr.Invalid, "标题和正文不能为空")
	}
	if err := s.requireCapability(ctx, conn, wc, domain.CapDocWrite); err != nil {
		return WeKnoraDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if publish {
		s.ensureWeKnoraKBEmbeddingModel(ctx, wc, rawKBID)
	}
	doc, err := wc.CreateManualDocument(ctx, rawKBID, title, content, publish)
	if err != nil {
		return WeKnoraDocument{}, s.managementError(conn, domain.CapDocWrite, "创建文档", err)
	}
	return scopeWeKnoraDoc(settings, conn.ID, doc), nil
}

// GetWeKnoraDocument returns the management detail of one document, including
// its parent knowledge base (already authorized through resolveWeKnoraDoc).
func (s *Service) GetWeKnoraDocument(ctx context.Context, projectID, connectionID, docID string) (domain.KnowledgeDocumentDetail, error) {
	settings, conn, wc, rawDocID, err := s.resolveWeKnoraDoc(ctx, projectID, connectionID, docID)
	if err != nil {
		return domain.KnowledgeDocumentDetail{}, err
	}
	if err := requireWeKnoraClient(conn, wc); err != nil {
		return domain.KnowledgeDocumentDetail{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	doc, err := wc.GetKnowledge(ctx, rawDocID)
	if err != nil {
		return domain.KnowledgeDocumentDetail{}, apperr.New(apperr.Upstream, "获取文档详情失败："+err.Error())
	}
	content, publishStatus := doc.ManualMetadata()
	return domain.KnowledgeDocumentDetail{
		ID:              EncodeScopedSourceID(settings, conn.ID, doc.ID),
		KnowledgeBaseID: EncodeScopedSourceID(settings, conn.ID, doc.KnowledgeBaseID),
		Title:           doc.Title,
		Type:            doc.Type,
		FileName:        doc.FileName,
		FileType:        doc.FileType,
		ParseStatus:     doc.ParseStatus,
		ErrorMessage:    doc.ErrorMessage,
		CreatedAt:       doc.CreatedAt,
		UpdatedAt:       doc.UpdatedAt,
		Content:         content,
		PublishStatus:   publishStatus,
	}, nil
}

func (s *Service) UpdateWeKnoraManualDoc(ctx context.Context, projectID, connectionID, docID, title, content string, publish bool) (WeKnoraDocument, error) {
	settings, conn, wc, rawDocID, err := s.resolveWeKnoraDocGated(ctx, projectID, connectionID, docID, domain.CapDocWrite)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if publish {
		if rawKBID, kbErr := wc.GetDocumentKBID(ctx, rawDocID); kbErr == nil && rawKBID != "" {
			s.ensureWeKnoraKBEmbeddingModel(ctx, wc, rawKBID)
		}
	}
	doc, err := wc.UpdateManualDocument(ctx, rawDocID, title, content, publish)
	if err != nil {
		return WeKnoraDocument{}, s.managementError(conn, domain.CapDocWrite, "更新文档", err)
	}
	return scopeWeKnoraDoc(settings, conn.ID, doc), nil
}

func (s *Service) ReparseWeKnoraDoc(ctx context.Context, projectID, connectionID, docID string) error {
	_, conn, wc, rawDocID, err := s.resolveWeKnoraDocGated(ctx, projectID, connectionID, docID, domain.CapReparse)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if rawKBID, kbErr := wc.GetDocumentKBID(ctx, rawDocID); kbErr == nil && rawKBID != "" {
		s.ensureWeKnoraKBEmbeddingModel(ctx, wc, rawKBID)
	}
	if err := wc.ReparseDocument(ctx, rawDocID); err != nil {
		return s.managementError(conn, domain.CapReparse, "触发重解析", err)
	}
	return nil
}

// ensureWeKnoraKBEmbeddingModel best-effort repairs a legacy knowledge base
// whose embedding_model_id is empty by binding the first active Embedding model
// configured on the same WeKnora tenant before document indexing starts.
func (s *Service) ensureWeKnoraKBEmbeddingModel(ctx context.Context, wc *WeKnoraClient, rawKBID string) {
	rawKBID = strings.TrimSpace(rawKBID)
	if wc == nil || rawKBID == "" {
		return
	}
	kb, err := wc.GetKnowledgeBase(ctx, rawKBID)
	if err != nil || strings.TrimSpace(kb.EmbeddingModelID) != "" {
		return
	}
	models, err := wc.ListModels(ctx)
	if err != nil {
		return
	}
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(m.Type), "Embedding") {
			continue
		}
		_, _ = wc.PatchKnowledgeBaseEmbeddingModel(ctx, rawKBID, id)
		return
	}
}

func (s *Service) DeleteWeKnoraDoc(ctx context.Context, projectID, connectionID, docID string) error {
	_, conn, wc, rawDocID, err := s.resolveWeKnoraDocGated(ctx, projectID, connectionID, docID, domain.CapDocWrite)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if err := wc.DeleteDocument(ctx, rawDocID); err != nil {
		return s.managementError(conn, domain.CapDocWrite, "删除文档", err)
	}
	return nil
}

// ListWeKnoraChunks returns one page of parsed passages for a document.
func (s *Service) ListWeKnoraChunks(ctx context.Context, projectID, connectionID, docID string, page, pageSize int) (domain.KnowledgeChunkList, error) {
	_, conn, wc, rawDocID, err := s.resolveWeKnoraDocGated(ctx, projectID, connectionID, docID, domain.CapChunkRead)
	if err != nil {
		return domain.KnowledgeChunkList{}, err
	}
	page, pageSize = normalizeChunkPage(page, pageSize)
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	chunks, total, err := wc.ListChunks(ctx, rawDocID, page, pageSize)
	if err != nil {
		return domain.KnowledgeChunkList{}, s.managementError(conn, domain.CapChunkRead, "获取文档分块", err)
	}
	out := domain.KnowledgeChunkList{Chunks: make([]domain.KnowledgeChunk, 0, len(chunks)), Total: total, Page: page, PageSize: pageSize}
	for _, chunk := range chunks {
		out.Chunks = append(out.Chunks, domain.KnowledgeChunk{
			ID: chunk.ID, Content: chunk.Content, ChunkIndex: chunk.ChunkIndex, StartAt: chunk.StartAt, EndAt: chunk.EndAt,
		})
	}
	return out, nil
}

// OpenWeKnoraPreview streams the upstream preview of one document. The caller
// owns the returned body and must close it. Preview and download are gated
// separately because upstream permissions differ per route.
func (s *Service) OpenWeKnoraPreview(ctx context.Context, projectID, connectionID, docID string) (*StreamResponse, error) {
	_, conn, wc, rawDocID, err := s.resolveWeKnoraDocGated(ctx, projectID, connectionID, docID, domain.CapPreview)
	if err != nil {
		return nil, err
	}
	resp, err := wc.OpenPreview(ctx, rawDocID)
	if err != nil {
		return nil, s.managementError(conn, domain.CapPreview, "预览文档", err)
	}
	return resp, nil
}

// OpenWeKnoraDownload streams the original file of one document. The caller
// owns the returned body and must close it.
func (s *Service) OpenWeKnoraDownload(ctx context.Context, projectID, connectionID, docID string) (*StreamResponse, error) {
	_, conn, wc, rawDocID, err := s.resolveWeKnoraDocGated(ctx, projectID, connectionID, docID, domain.CapDownload)
	if err != nil {
		return nil, err
	}
	resp, err := wc.OpenDownload(ctx, rawDocID)
	if err != nil {
		return nil, s.managementError(conn, domain.CapDownload, "下载文档", err)
	}
	return resp, nil
}

// WeKnoraCapabilities returns the probe verdicts for one connection, using the
// cache unless it expired, the connection fingerprinted differently, or the
// caller asked for a forced re-probe.
func (s *Service) WeKnoraCapabilities(ctx context.Context, connectionID string, refresh bool) (domain.ConnectionCapabilities, error) {
	_, conn, wc, err := s.resolveExplicitWeKnoraConnection(connectionID)
	if err != nil {
		return domain.ConnectionCapabilities{}, err
	}
	if refresh {
		// Bump the generation first so an older in-flight probe cannot write
		// its verdict back over the fresh one.
		s.capabilities.invalidate(conn.ID)
	}
	return s.cachedCapabilities(ctx, conn, wc), nil
}

func normalizeChunkPage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize
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
		return item, apperr.New(apperr.Conflict, "原知识库连接已删除或停用，请重新选择写入来源")
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

package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	ToolSearch      = "kb_search"
	ToolFetch       = "kb_fetch"
	ToolListSources = "kb_list_sources"
	ToolGetSource   = "kb_get_source"
)

var knownReadOnlyMCPTools = []string{
	ToolSearch,
	ToolFetch,
	ToolListSources,
	ToolGetSource,
	WeKnoraToolListKnowledgeBases,
	WeKnoraToolSearchKnowledge,
	WeKnoraToolListDocuments,
	WeKnoraToolReadDocument,
}

type mcpFactory struct{}

func newMCPFactory() ConnectorFactory { return mcpFactory{} }

func (mcpFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               domain.ConnectorTypeMCP,
		Name:               "通用知识 MCP（Streamable HTTP）",
		Description:        "接入遵循 Streamable HTTP MCP 协议的任意只读知识库服务（含 LightCloud 等），仅放行 readOnlyHint 工具",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthBearer,
		Fields: []domain.ConnectorFieldSpec{
			{
				Key:         "endpoint",
				Label:       "MCP 服务地址",
				Kind:        "url",
				Placeholder: "https://kb-mcp.example.com/mcp",
				Required:    true,
				Help:        "填写 http(s):// Streamable HTTP MCP Endpoint（不能内嵌账号密码）",
			},
			{
				Key:         "token",
				Label:       "Bearer Token（可选）",
				Kind:        "secret",
				Placeholder: "Bearer Token（留空则不发送或使用自定义请求头）",
				Required:    false,
				Secret:      true,
				Help:        "将自动放入 Authorization: Bearer 头，保存后 AES-GCM 加密存储",
			},
			{
				Key:         "headers",
				Label:       "认证请求头（可选）",
				Kind:        "kv",
				Placeholder: "X-API-Key: your-key",
				Required:    false,
				Secret:      true,
				Help:        "每行填写一个 Header: Value（例如 Authorization: <TOKEN> 或 X-Lark-MCP-TAT: xxx），保存后加密存储",
			},
			{
				Key:         "allowedTools",
				Label:       "允许工具白名单（可选）",
				Kind:        "text",
				Placeholder: "例如：fetch-doc, search-doc 或 manage.search_file, get_content",
				Required:    false,
				Help:        "逗号或换行分隔；留空按默认只读策略放行，配置后仅放行列表内的工具（推荐用于飞书远程端点与腾讯文档）",
			},
			{
				Key:      "oauth",
				Label:    "OAuth 2.0 授权",
				Kind:     "oauth",
				Required: false,
				Help:     "开启后按 OAuth 2.0 / MCP OAuth 2.1 完成授权，访问令牌到期自动刷新",
			},
		},
	}
}

func (mcpFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, _ bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", cfg, secret, apperr.New(apperr.Invalid, "MCP 服务地址不能为空")
	}
	for _, r := range endpoint {
		if r < 0x20 || r == 0x7f {
			return "", cfg, secret, apperr.New(apperr.Invalid, "MCP 地址包含非法控制字符")
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(u.Host) == "" {
		return "", cfg, secret, apperr.New(apperr.Invalid, "MCP 服务地址必须是 http(s)://host/mcp 形式")
	}
	if u.User != nil {
		return "", cfg, secret, apperr.New(apperr.Invalid, "MCP 服务地址不能内嵌账号密码")
	}
	if err := validateConnectorEndpointIP(endpoint, true, "MCP "); err != nil {
		return "", cfg, secret, err
	}
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)
	cfg.AllowedTools = CleanList(cfg.AllowedTools)

	secret.Token = stripBearer(secret.Token)
	secret.Headers = normalizeHeaderMap(secret.Headers)

	mode := oauthMode(cfg)
	switch mode {
	case OAuthModeNone, OAuthModeAuto, OAuthModePreset, OAuthModeCustom:
	default:
		return "", cfg, secret, apperr.New(apperr.Invalid, "oauthMode 取值无效")
	}
	if mode != OAuthModeNone {
		for _, raw := range []string{
			firstNonEmpty(secret.OAuthTokenURL, cfg.Extra["oauthTokenUrl"]),
			cfg.Extra["oauthAuthorizeUrl"],
		} {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			if err := validateAuthURL(raw); err != nil {
				return "", cfg, secret, apperr.New(apperr.Invalid, err.Error())
			}
		}
		secret.AuthType = domain.ConnectorAuthOAuth
	} else if secret.AuthType == domain.ConnectorAuthOAuth {
		secret.AuthType = ""
	}

	if strings.TrimSpace(secret.AuthType) == "" {
		if len(secret.Headers) > 0 && secret.Token == "" {
			secret.AuthType = domain.ConnectorAuthHeaders
		} else {
			secret.AuthType = domain.ConnectorAuthBearer
		}
	}
	return endpoint, cfg, secret, nil
}

func (mcpFactory) New(spec ConnectorSpec) (Connector, error) {
	headers := mcpConnectorHeaders(spec.Config, spec.Secret)
	timeout := CallTimeout
	if spec.Config.TimeoutSec > 0 {
		timeout = time.Duration(spec.Config.TimeoutSec) * time.Second
	}
	client := getOrCreateMCPClient(spec.OwnerUserID, strings.TrimSpace(spec.ID), strings.TrimSpace(spec.Endpoint), headers, timeout)
	return &mcpConnector{
		spec:    spec,
		client:  client,
		secret:  spec.Secret,
		resolve: spec.ResolveSecret,
	}, nil
}

// mcpConnectorHeaders merges configured headers with the current access token.
// OAuth connectors may pick a custom header (e.g. Feishu's X-Lark-MCP-UAT).
func mcpConnectorHeaders(cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret) map[string]string {
	headers := map[string]string{}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	for k, v := range secret.Headers {
		headers[k] = v
	}
	if token := strings.TrimSpace(secret.Token); token != "" {
		header, prefix := "Authorization", "Bearer "
		if oauthModeActive(cfg) {
			header, prefix = oauthTokenHeader(cfg)
		}
		headers[header] = prefix + token
	}
	return headers
}

const (
	mcpToolsCacheTTL          = 3 * time.Minute
	mcpToolsCacheMax          = 256
	defaultConnectorMaxConcur = 3
	maxConnectorRetryAttempts = 2
)

var (
	mcpClientsMu sync.Mutex
	mcpClients   = map[string]*mcpclient.Client{}

	mcpToolsCacheMu   sync.Mutex
	mcpToolsCache     = map[string]mcpToolsCacheEntry{}
	mcpToolsInflight  = map[string]*mcpToolsFlight{}
	mcpSemaphoresMu   sync.Mutex
	mcpSemaphores     = map[string]*mcpSemaphoreEntry{}
	mcpRetryBaseDelay = 500 * time.Millisecond
)

type mcpSemaphoreEntry struct {
	sem  chan struct{}
	refs int
}

type mcpToolsCacheEntry struct {
	tools     []mcpclient.Tool
	expiresAt time.Time
}

type mcpToolsFlight struct {
	done  chan struct{}
	tools []mcpclient.Tool
	err   error
}

// ResetMCPToolsCacheForTest clears the process-wide MCP tool list cache in tests.
func ResetMCPToolsCacheForTest() {
	mcpToolsCacheMu.Lock()
	mcpToolsCache = map[string]mcpToolsCacheEntry{}
	mcpToolsInflight = map[string]*mcpToolsFlight{}
	mcpToolsCacheMu.Unlock()
}

func isReusableMCPConnectorID(connectorID string) bool {
	connectorID = strings.TrimSpace(connectorID)
	return connectorID != "" && connectorID != "probe"
}

func getOrCreateMCPClient(ownerUserID int64, connectorID, endpoint string, headers map[string]string, timeout time.Duration) *mcpclient.Client {
	connectorID = strings.TrimSpace(connectorID)
	newGuardedClient := func() *mcpclient.Client {
		return mcpclient.New(endpoint, headers).
			WithTransport(newGuardedHTTPTransport(true, "MCP ")).
			WithTimeout(timeout)
	}
	if !isReusableMCPConnectorID(connectorID) {
		return newGuardedClient()
	}
	cacheKey := fmt.Sprintf("u:%d|id:%s|%s|%d", ownerUserID, connectorID, endpoint, timeout)

	mcpClientsMu.Lock()
	defer mcpClientsMu.Unlock()
	if c, ok := mcpClients[cacheKey]; ok {
		c.SetHeaders(headers)
		return c
	}
	if len(mcpClients) >= 256 {
		mcpClients = map[string]*mcpclient.Client{}
	}
	c := newGuardedClient()
	mcpClients[cacheKey] = c
	return c
}

func acquireConnectorSlot(ctx context.Context, ownerUserID int64, connectorID string, maxConcur int) (func(), error) {
	if !isReusableMCPConnectorID(connectorID) {
		return func() {}, nil
	}
	if maxConcur <= 0 {
		maxConcur = defaultConnectorMaxConcur
	}
	if maxConcur > 16 {
		maxConcur = 16
	}
	key := fmt.Sprintf("u:%d|%s|%d", ownerUserID, connectorID, maxConcur)
	mcpSemaphoresMu.Lock()
	entry, ok := mcpSemaphores[key]
	if !ok || cap(entry.sem) != maxConcur {
		entry = &mcpSemaphoreEntry{sem: make(chan struct{}, maxConcur)}
		mcpSemaphores[key] = entry
	}
	entry.refs++
	mcpSemaphoresMu.Unlock()
	release := func(acquired bool) {
		if acquired {
			<-entry.sem
		}
		mcpSemaphoresMu.Lock()
		entry.refs--
		if entry.refs == 0 && mcpSemaphores[key] == entry {
			delete(mcpSemaphores, key)
		}
		mcpSemaphoresMu.Unlock()
	}
	select {
	case entry.sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { release(true) }) }, nil
	case <-ctx.Done():
		release(false)
		return nil, ctx.Err()
	}
}

func connectorMaxConcurrency(cfg domain.KnowledgeConnectorConfig) int {
	if raw := strings.TrimSpace(cfg.Extra["max_concurrency"]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 16 {
				return 16
			}
			return n
		}
	}
	return defaultConnectorMaxConcur
}

type mcpConnector struct {
	spec    ConnectorSpec
	client  *mcpclient.Client
	resolve func(ctx context.Context, forceRefresh bool, failedToken string) (domain.KnowledgeConnectorSecret, error)

	mu     sync.Mutex
	secret domain.KnowledgeConnectorSecret
}

func (c *mcpConnector) Type() string { return domain.ConnectorTypeMCP }

func (c *mcpConnector) Check(ctx context.Context) CheckResult {
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := time.Now()
	tools, err := c.tools(cctx, true)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return CheckResult{
			ConnectorID: c.spec.ID,
			OK:          false,
			Error:       c.authError(err).Error(),
			LatencyMs:   latency,
		}
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sources, _ := c.ListSources(cctx)
	return CheckResult{
		ConnectorID: c.spec.ID,
		OK:          true,
		LatencyMs:   latency,
		Sources:     sources,
		Tools:       names,
	}
}

func (c *mcpConnector) toolsCacheKey() string {
	allowed := append([]string(nil), c.spec.Config.AllowedTools...)
	sort.Strings(allowed)
	return fmt.Sprintf("u:%d|%s|%s|%d|%s",
		c.spec.OwnerUserID,
		strings.TrimSpace(c.spec.ID),
		strings.TrimSpace(c.spec.Endpoint),
		c.spec.UpdatedAt.UTC().UnixNano(),
		strings.Join(allowed, ","),
	)
}

func (c *mcpConnector) Tools(ctx context.Context) ([]mcpclient.Tool, error) {
	return c.tools(ctx, false)
}

func (c *mcpConnector) tools(ctx context.Context, forceRefresh bool) ([]mcpclient.Tool, error) {
	key := c.toolsCacheKey()
	useCache := isReusableMCPConnectorID(c.spec.ID)
	if useCache && !forceRefresh {
		now := time.Now()
		mcpToolsCacheMu.Lock()
		if entry, ok := mcpToolsCache[key]; ok && now.Before(entry.expiresAt) {
			out := append([]mcpclient.Tool(nil), entry.tools...)
			mcpToolsCacheMu.Unlock()
			return out, nil
		}
		if flight, ok := mcpToolsInflight[key]; ok {
			mcpToolsCacheMu.Unlock()
			select {
			case <-flight.done:
				return append([]mcpclient.Tool(nil), flight.tools...), flight.err
			case <-ctx.Done():
				return nil, c.safeError(ctx.Err())
			}
		}
		flight := &mcpToolsFlight{done: make(chan struct{})}
		mcpToolsInflight[key] = flight
		mcpToolsCacheMu.Unlock()

		fetchCtx, fetchCancel := context.WithTimeout(context.WithoutCancel(ctx), CallTimeout)
		tools, err := c.fetchToolsUpstream(fetchCtx)
		fetchCancel()
		mcpToolsCacheMu.Lock()
		delete(mcpToolsInflight, key)
		if err == nil {
			evictExpiredMCPToolsLocked(time.Now())
			mcpToolsCache[key] = mcpToolsCacheEntry{
				tools:     append([]mcpclient.Tool(nil), tools...),
				expiresAt: time.Now().Add(mcpToolsCacheTTL),
			}
		}
		flight.tools = append([]mcpclient.Tool(nil), tools...)
		flight.err = err
		close(flight.done)
		mcpToolsCacheMu.Unlock()
		return tools, err
	}

	tools, err := c.fetchToolsUpstream(ctx)
	if useCache && err == nil {
		mcpToolsCacheMu.Lock()
		evictExpiredMCPToolsLocked(time.Now())
		mcpToolsCache[key] = mcpToolsCacheEntry{
			tools:     append([]mcpclient.Tool(nil), tools...),
			expiresAt: time.Now().Add(mcpToolsCacheTTL),
		}
		mcpToolsCacheMu.Unlock()
	}
	return tools, err
}

func evictExpiredMCPToolsLocked(now time.Time) {
	for k, entry := range mcpToolsCache {
		if !now.Before(entry.expiresAt) {
			delete(mcpToolsCache, k)
		}
	}
	if len(mcpToolsCache) >= mcpToolsCacheMax {
		mcpToolsCache = map[string]mcpToolsCacheEntry{}
	}
}

func (c *mcpConnector) fetchToolsUpstream(ctx context.Context) ([]mcpclient.Tool, error) {
	release, slotErr := acquireConnectorSlot(ctx, c.spec.OwnerUserID, c.spec.ID, connectorMaxConcurrency(c.spec.Config))
	if slotErr != nil {
		return nil, c.safeError(slotErr)
	}
	defer release()

	used, err := c.syncSecret(ctx, false, "")
	if err != nil {
		return nil, c.authError(err)
	}
	var all []mcpclient.Tool
	err = c.retryReadOnly(ctx, func() error {
		var listErr error
		all, listErr = c.client.ListToolsContext(ctx)
		if listErr != nil && c.canRefresh() && isUnauthorized(listErr) {
			if _, refreshErr := c.syncSecret(ctx, true, used.Token); refreshErr != nil {
				return refreshErr
			}
			all, listErr = c.client.ListToolsContext(ctx)
		}
		return listErr
	})
	if err != nil {
		logKnowledgeMCPTools(c.spec, 0, 0, nil, err)
		return nil, c.authError(err)
	}
	out := make([]mcpclient.Tool, 0, len(all))
	var blocked []string
	allowSet := buildAllowedToolSet(c.spec.Config.AllowedTools)
	for _, t := range all {
		allowed, reason := describeReadOnlyMCPTool(t, allowSet)
		if allowed {
			out = append(out, t)
		} else {
			blocked = append(blocked, strings.TrimSpace(t.Name)+"("+reason+")")
		}
	}
	logKnowledgeMCPTools(c.spec, len(all), len(out), blocked, nil)
	return out, nil
}

func buildAllowedToolSet(allowed []string) map[string]struct{} {
	if len(allowed) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if name != "" {
			set[name] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func isAllowedReadOnlyMCPTool(t mcpclient.Tool) bool {
	allowed, _ := describeReadOnlyMCPTool(t, nil)
	return allowed
}

func describeReadOnlyMCPTool(t mcpclient.Tool, allowSet map[string]struct{}) (bool, string) {
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return false, "empty_name"
	}
	if len(allowSet) > 0 {
		if _, ok := allowSet[name]; ok {
			return true, "allowed_by_allowlist"
		}
		return false, "not_in_allowlist"
	}
	var hints struct {
		ReadOnly    *bool `json:"readOnlyHint"`
		Destructive *bool `json:"destructiveHint"`
	}
	if len(t.Annotations) > 0 && json.Unmarshal(t.Annotations, &hints) == nil {
		if hints.Destructive != nil && *hints.Destructive {
			return false, "destructiveHint"
		}
		if hints.ReadOnly != nil {
			if *hints.ReadOnly {
				return true, "readOnlyHint"
			}
			return false, "readOnlyHint=false"
		}
	}
	return true, "unannotated_allowed"
}

func isMCPSourceListTool(name string) bool {
	switch strings.TrimSpace(name) {
	case WeKnoraToolListKnowledgeBases, ToolListSources, "ragflow_list_datasets", "query_space_list":
		return true
	default:
		return false
	}
}

func (c *mcpConnector) ListSources(ctx context.Context) ([]Source, error) {
	tools, err := c.Tools(ctx)
	if err != nil {
		return nil, err
	}
	var sourceTool string
	for _, candidate := range []string{WeKnoraToolListKnowledgeBases, ToolListSources, "ragflow_list_datasets", "query_space_list"} {
		for _, t := range tools {
			if t.Name == candidate {
				sourceTool = candidate
				break
			}
		}
		if sourceTool != "" {
			break
		}
	}
	if sourceTool == "" {
		return []Source{}, nil
	}
	text, _, err := c.callTool(ctx, sourceTool, map[string]any{}, true)
	if err != nil {
		return nil, c.safeError(err)
	}
	return parseSourcesFromMCPText(text), nil
}

func parseSourcesFromMCPText(raw string) []Source {
	var parsed any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return []Source{}
	}
	var out []Source
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				id := firstNonEmpty(
					strings.TrimSpace(argString(m["id"])),
					strings.TrimSpace(argString(m["dataset_id"])),
					strings.TrimSpace(argString(m["space_id"])),
				)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				name := firstNonEmpty(
					argString(m["name"]),
					argString(m["title"]),
					argString(m["dataset_name"]),
					argString(m["space_name"]),
					id,
				)
				docCount := argInt(m["doc_count"], 0)
				if docCount == 0 {
					docCount = argInt(m["knowledge_count"], 0)
				}
				if docCount == 0 {
					docCount = argInt(m["document_count"], 0)
				}
				out = append(out, Source{
					ID:          id,
					Name:        name,
					Description: strings.TrimSpace(argString(m["description"])),
					Status:      firstNonEmpty(argString(m["status"]), argString(m["type"]), "ready"),
					DocCount:    docCount,
				})
			}
		case map[string]any:
			for _, key := range []string{"knowledge_bases", "sources", "datasets", "spaces", "space_list", "items", "data"} {
				if sub, ok := x[key]; ok {
					walk(sub)
				}
			}
		}
	}
	walk(parsed)
	if out == nil {
		return []Source{}
	}
	return out
}

func (c *mcpConnector) CallTool(ctx context.Context, toolName string, args map[string]any, scope SourceScope) (CallResult, error) {
	toolName = strings.TrimSpace(toolName)
	tools, err := c.Tools(ctx)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	var matched *mcpclient.Tool
	for i := range tools {
		if tools[i].Name == toolName {
			matched = &tools[i]
			break
		}
	}
	if matched == nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("MCP 连接器未开放只读工具 %q", toolName))
	}

	callArgs, err := applyScopeToMCPArgs(*matched, args, scope)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, err.Error())
	}

	started := time.Now()
	mayWrite := IsKnowledgeToolMayWrite(*matched)
	text, structured, callErr := c.callTool(ctx, toolName, callArgs, !mayWrite)
	latency := time.Since(started).Milliseconds()
	if callErr != nil {
		authErr := c.authError(callErr)
		res := CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}
		logKnowledgeDebugCall(c.spec, "upstream_mcp", toolName, scope, callArgs, res, authErr)
		return res, authErr
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" && len(structured) > 0 {
		trimmed = strings.TrimSpace(string(structured))
	}
	if len(scope.Allowlist()) > 0 && isMCPSourceListTool(toolName) {
		trimmed = filterMCPSourcesJSON(trimmed, scope)
	}
	trimmed = truncateRunes(trimmed, 32000)

	if strings.TrimSpace(trimmed) == "" {
		return CallResult{
			Status:    domain.KnowledgeStatusEmpty,
			Text:      trimmed,
			Citeable:  !domain.IsKnowledgeCatalogTool(toolName),
			LatencyMs: latency,
		}, nil
	}

	rule := resolveKnowledgeToolRule(c.spec.Config.Extra, toolName)
	extractInput := trimmed
	if len(structured) > 0 {
		extractInput = strings.TrimSpace(string(structured))
	}
	targets, evidences, isJSON, jsonListCount := ExtractEvidencesWithRule(extractInput, c.spec.ID, &rule)
	status := domain.KnowledgeStatusHit
	hits := len(evidences)
	if isJSON && jsonListCount >= 0 {
		hits = jsonListCount
	} else if !isJSON && len(targets) > 0 && hits == 0 {
		hits = len(targets)
	}
	switch {
	case isJSON && jsonListCount == 0:
		status = domain.KnowledgeStatusEmpty
		evidences = nil
	case len(evidences) == 0 && !domain.IsKnowledgeCatalogTool(toolName):
		status = domain.KnowledgeStatusUnstructured
	}
	if domain.IsKnowledgeCatalogTool(toolName) {
		evidences = nil
	}
	res := CallResult{
		Status:    status,
		Text:      trimmed,
		Hits:      hits,
		Targets:   targets,
		Evidences: evidences,
		Citeable:  !domain.IsKnowledgeCatalogTool(toolName),
		LatencyMs: latency,
	}
	logKnowledgeDebugCall(c.spec, "upstream_mcp", toolName, scope, callArgs, res, nil)
	return res, nil
}

func applyScopeToMCPArgs(tool mcpclient.Tool, args map[string]any, scope SourceScope) (map[string]any, error) {
	cloned := make(map[string]any, len(args))
	for k, v := range args {
		cloned[k] = v
	}
	allowed := scope.Allowlist()
	if len(allowed) == 0 {
		if strings.TrimSpace(scope.Mode) == "allowlist" {
			return nil, errors.New("知识库范围为空")
		}
		return cloned, nil
	}
	for _, singleKey := range []string{"source", "knowledge_base_id", "kb_id", "dataset_id", "space_id"} {
		if val := strings.TrimSpace(argString(cloned[singleKey])); val != "" && !scope.Allows(val) {
			return nil, fmt.Errorf("知识库来源 %q 不在当前授权范围内", val)
		}
	}
	hasArrayArg := false
	for _, listKey := range []string{"knowledge_base_ids", "sources", "dataset_ids", "space_ids"} {
		if _, exists := cloned[listKey]; exists {
			req := argStringList(cloned[listKey])
			filtered, err := scope.FilterIDs(req)
			if err != nil {
				return nil, err
			}
			cloned[listKey] = filtered
			hasArrayArg = true
		}
	}
	if !hasArrayArg && !domain.IsKnowledgeReadTool(tool.Name) && tool.Name != ToolGetSource && tool.Name != WeKnoraToolListDocuments {
		props := toolSchemaProperties(tool.InputSchema)
		if _, ok := props["knowledge_base_ids"]; ok {
			cloned["knowledge_base_ids"] = allowed
		} else if _, ok := props["dataset_ids"]; ok {
			cloned["dataset_ids"] = allowed
		} else if _, ok := props["space_ids"]; ok {
			cloned["space_ids"] = allowed
		} else if _, ok := props["sources"]; ok || tool.Name == ToolSearch {
			cloned["sources"] = allowed
		}
	}
	return cloned, nil
}

func toolSchemaProperties(schema json.RawMessage) map[string]any {
	if len(schema) == 0 {
		return nil
	}
	var obj struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(schema, &obj) == nil {
		return obj.Properties
	}
	return nil
}

func filterMCPSourcesJSON(raw string, scope SourceScope) string {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw
	}
	changed := false
	for _, key := range []string{"knowledge_bases", "sources", "datasets", "spaces", "space_list"} {
		arr, ok := root[key].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, item := range arr {
			if m, isMap := item.(map[string]any); isMap {
				id := firstNonEmpty(
					strings.TrimSpace(argString(m["id"])),
					strings.TrimSpace(argString(m["dataset_id"])),
					strings.TrimSpace(argString(m["space_id"])),
				)
				if id != "" && !scope.Allows(id) {
					continue
				}
			}
			kept = append(kept, item)
		}
		root[key] = kept
		changed = true
	}
	if !changed {
		return raw
	}
	b, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(b)
}

// syncSecret refreshes the live credential and pushes the resulting auth
// header onto the shared HTTP client.
func (c *mcpConnector) syncSecret(ctx context.Context, force bool, failedToken string) (domain.KnowledgeConnectorSecret, error) {
	if c.resolve == nil {
		return c.currentSecret(), nil
	}
	secret, err := c.resolve(ctx, force, failedToken)
	if err != nil {
		return c.currentSecret(), err
	}
	c.mu.Lock()
	c.secret = secret
	c.mu.Unlock()
	c.client.SetHeaders(mcpConnectorHeaders(c.spec.Config, secret))
	return secret, nil
}

func (c *mcpConnector) currentSecret() domain.KnowledgeConnectorSecret {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.secret
}

func (c *mcpConnector) canRefresh() bool {
	return c.resolve != nil && strings.TrimSpace(c.currentSecret().RefreshToken) != ""
}

// callTool performs one tool call under the connector concurrency semaphore,
// retrying once on OAuth 401 refresh and (for read-only tools) up to 2 times
// on 429/5xx/EOF transient errors with backoff.
func (c *mcpConnector) callTool(ctx context.Context, name string, args map[string]any, retryable bool) (string, json.RawMessage, error) {
	release, slotErr := acquireConnectorSlot(ctx, c.spec.OwnerUserID, c.spec.ID, connectorMaxConcurrency(c.spec.Config))
	if slotErr != nil {
		return "", nil, c.safeError(slotErr)
	}
	defer release()

	used, err := c.syncSecret(ctx, false, "")
	if err != nil {
		return "", nil, c.authError(err)
	}
	lastToken := used.Token
	var text string
	var structured json.RawMessage
	invoke := func() error {
		var callErr error
		text, structured, callErr = c.client.CallToolStructuredContext(ctx, name, args)
		if callErr != nil && c.canRefresh() && isUnauthorized(callErr) {
			refreshed, refreshErr := c.syncSecret(ctx, true, lastToken)
			if refreshErr != nil {
				return refreshErr
			}
			lastToken = refreshed.Token
			text, structured, callErr = c.client.CallToolStructuredContext(ctx, name, args)
		}
		return callErr
	}
	if retryable {
		err = c.retryReadOnly(ctx, invoke)
	} else {
		err = invoke()
	}
	return text, structured, err
}

func (c *mcpConnector) retryReadOnly(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt <= maxConnectorRetryAttempts; attempt++ {
		err = fn()
		if err == nil || attempt == maxConnectorRetryAttempts || !isRetryableMCPError(err) {
			return err
		}
		delay := mcpRetryDelay(attempt, err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func isRetryableMCPError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *mcpclient.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset by peer") || strings.Contains(msg, "broken pipe")
}

func mcpRetryDelay(attempt int, err error) time.Duration {
	var httpErr *mcpclient.HTTPError
	if errors.As(err, &httpErr) && strings.TrimSpace(httpErr.RetryAfter) != "" {
		if secs, parseErr := strconv.Atoi(strings.TrimSpace(httpErr.RetryAfter)); parseErr == nil && secs > 0 {
			if secs > 5 {
				secs = 5
			}
			return time.Duration(secs) * time.Second
		}
	}
	base := mcpRetryBaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	if attempt == 0 {
		return base
	}
	return base * 3
}

func isUnauthorized(err error) bool {
	var httpErr *mcpclient.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

// authError returns domain errors untouched (they already carry safe wording)
// and funnels everything else through safeError.
func (c *mcpConnector) authError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	return c.safeError(err)
}

func (c *mcpConnector) safeError(err error) error {
	if err == nil {
		return nil
	}
	var timed net.Error
	var httpErr *mcpclient.HTTPError
	var rpcErr *mcpclient.RPCError
	msg := RedactSecrets(err.Error(), c.spec.Secret)
	msg = RedactSecrets(msg, c.currentSecret())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timed) && timed.Timeout()):
		return apperr.New(apperr.Upstream, "知识库 MCP 服务超时")
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests:
		return apperr.New(apperr.Upstream, "上游知识库接口触发频率限制，请稍后重试")
	case strings.Contains(msg, "gateway HTTP 403:") || strings.Contains(msg, "gateway HTTP 401:"):
		return apperr.New(apperr.Upstream, "知识库 MCP 服务拒绝访问")
	case errors.As(err, &rpcErr) && rpcErr.Code == -32602:
		detail := RedactSecrets(strings.TrimSpace(rpcErr.Message), c.spec.Secret)
		detail = RedactSecrets(detail, c.currentSecret())
		if detail != "" && !strings.Contains(detail, "http://") && !strings.Contains(detail, "https://") {
			return apperr.New(apperr.Invalid, "知识库工具参数错误："+truncateRunes(detail, 180))
		}
		return apperr.New(apperr.Invalid, "知识库工具参数错误")
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest:
		return apperr.New(apperr.Invalid, "知识库工具参数错误")
	case strings.HasPrefix(msg, "mcp tool ") && !strings.Contains(msg, "http://") && !strings.Contains(msg, "https://"):
		return apperr.New(apperr.Upstream, truncateRunes(strings.TrimSpace(msg), 200))
	default:
		return apperr.New(apperr.Upstream, "知识库 MCP 服务不可用或调用失败")
	}
}

var knowledgeWriteVerbs = map[string]bool{
	"create": true, "add": true, "insert": true, "append": true, "write": true,
	"edit": true, "update": true, "patch": true, "put": true, "set": true,
	"delete": true, "del": true, "remove": true, "rm": true, "drop": true,
	"move": true, "rename": true, "copy": true, "upload": true, "share": true,
	"grant": true, "revoke": true, "publish": true, "archive": true,
	"restore": true, "clear": true,
	"post": true, "send": true, "submit": true, "exec": true, "execute": true,
	"modify": true, "replace": true, "merge": true, "destroy": true, "purge": true,
	"upsert": true,
}

// IsKnowledgeToolMayWrite reports whether an MCP tool may mutate external data
// based on MCP annotations or write-verb naming tokens.
func IsKnowledgeToolMayWrite(t mcpclient.Tool) bool {
	var hints struct {
		ReadOnly    *bool `json:"readOnlyHint"`
		Destructive *bool `json:"destructiveHint"`
	}
	if len(t.Annotations) > 0 && json.Unmarshal(t.Annotations, &hints) == nil {
		if hints.Destructive != nil && *hints.Destructive {
			return true
		}
		if hints.ReadOnly != nil {
			return !*hints.ReadOnly
		}
	}
	for _, tok := range splitToolNameTokens(t.Name) {
		if knowledgeWriteVerbs[tok] {
			return true
		}
	}
	return false
}

func splitToolNameTokens(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		if r == '.' || r == '_' || r == '-' || r == '/' || r == ':' || unicode.IsSpace(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && len(cur) > 0 {
			prev := cur[len(cur)-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) {
				flush()
			} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

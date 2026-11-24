package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

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
				Help:        "每行填写一个 Header: Value（例如 X-API-Key: xxx），保存后加密存储",
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
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)

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
	client := getOrCreateMCPClient(strings.TrimSpace(spec.Endpoint), headers, timeout)
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

var (
	mcpClientsMu sync.Mutex
	mcpClients   = map[string]*mcpclient.Client{}
)

func getOrCreateMCPClient(endpoint string, headers map[string]string, timeout time.Duration) *mcpclient.Client {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(strings.ToLower(k) + ":" + headers[k] + "\n"))
	}
	cacheKey := fmt.Sprintf("%s|%s|%d", endpoint, hex.EncodeToString(h.Sum(nil)), timeout)

	mcpClientsMu.Lock()
	defer mcpClientsMu.Unlock()
	if c, ok := mcpClients[cacheKey]; ok {
		return c
	}
	if len(mcpClients) >= 256 {
		mcpClients = map[string]*mcpclient.Client{}
	}
	c := mcpclient.New(endpoint, headers).WithTimeout(timeout)
	mcpClients[cacheKey] = c
	return c
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
	tools, err := c.Tools(cctx)
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

func (c *mcpConnector) Tools(ctx context.Context) ([]mcpclient.Tool, error) {
	used, err := c.syncSecret(ctx, false, "")
	if err != nil {
		return nil, c.authError(err)
	}
	all, err := c.client.ListToolsContext(ctx)
	if err != nil && c.canRefresh() && isUnauthorized(err) {
		if _, refreshErr := c.syncSecret(ctx, true, used.Token); refreshErr != nil {
			return nil, c.authError(refreshErr)
		}
		all, err = c.client.ListToolsContext(ctx)
	}
	if err != nil {
		logKnowledgeMCPTools(c.spec, 0, 0, nil, err)
		return nil, c.safeError(err)
	}
	out := make([]mcpclient.Tool, 0, len(all))
	var blocked []string
	for _, t := range all {
		allowed, reason := describeReadOnlyMCPTool(t)
		if allowed {
			out = append(out, t)
		} else {
			blocked = append(blocked, strings.TrimSpace(t.Name)+"("+reason+")")
		}
	}
	logKnowledgeMCPTools(c.spec, len(all), len(out), blocked, nil)
	return out, nil
}

func isAllowedReadOnlyMCPTool(t mcpclient.Tool) bool {
	allowed, _ := describeReadOnlyMCPTool(t)
	return allowed
}

func describeReadOnlyMCPTool(t mcpclient.Tool) (bool, string) {
	if strings.TrimSpace(t.Name) == "" {
		return false, "empty_name"
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

func (c *mcpConnector) ListSources(ctx context.Context) ([]Source, error) {
	tools, err := c.Tools(ctx)
	if err != nil {
		return nil, err
	}
	var sourceTool string
	for _, candidate := range []string{WeKnoraToolListKnowledgeBases, ToolListSources} {
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
	text, err := c.callTool(ctx, sourceTool, map[string]any{})
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
				id := strings.TrimSpace(argString(m["id"]))
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				name := firstNonEmpty(argString(m["name"]), argString(m["title"]), id)
				docCount := argInt(m["doc_count"], 0)
				if docCount == 0 {
					docCount = argInt(m["knowledge_count"], 0)
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
			for _, key := range []string{"knowledge_bases", "sources", "items", "data"} {
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
	text, callErr := c.callTool(ctx, toolName, callArgs)
	latency := time.Since(started).Milliseconds()
	if callErr != nil {
		authErr := c.authError(callErr)
		res := CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}
		logKnowledgeDebugCall(c.spec, "upstream_mcp", toolName, scope, callArgs, res, authErr)
		return res, authErr
	}

	trimmed := strings.TrimSpace(text)
	if len(scope.Allowlist()) > 0 && (toolName == WeKnoraToolListKnowledgeBases || toolName == ToolListSources) {
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

	targets, evidences, isJSON, jsonListCount := ExtractEvidences(trimmed, c.spec.ID)
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
	for _, singleKey := range []string{"source", "knowledge_base_id", "kb_id"} {
		if val := strings.TrimSpace(argString(cloned[singleKey])); val != "" && !scope.Allows(val) {
			return nil, fmt.Errorf("知识库来源 %q 不在当前授权范围内", val)
		}
	}
	hasArrayArg := false
	for _, listKey := range []string{"knowledge_base_ids", "sources"} {
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
	for _, key := range []string{"knowledge_bases", "sources"} {
		arr, ok := root[key].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, item := range arr {
			if m, isMap := item.(map[string]any); isMap {
				if id := strings.TrimSpace(argString(m["id"])); id != "" && !scope.Allows(id) {
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

// callTool performs one tool call, retrying once after a forced refresh when
// the gateway answers 401.
func (c *mcpConnector) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	used, err := c.syncSecret(ctx, false, "")
	if err != nil {
		return "", c.authError(err)
	}
	text, err := c.client.CallToolContext(ctx, name, args)
	if err != nil && c.canRefresh() && isUnauthorized(err) {
		if _, refreshErr := c.syncSecret(ctx, true, used.Token); refreshErr != nil {
			return "", c.authError(refreshErr)
		}
		text, err = c.client.CallToolContext(ctx, name, args)
	}
	return text, err
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
	msg := RedactSecrets(err.Error(), c.spec.Secret)
	msg = RedactSecrets(msg, c.currentSecret())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timed) && timed.Timeout()):
		return apperr.New(apperr.Upstream, "知识库 MCP 服务超时")
	case strings.Contains(msg, "gateway HTTP 403:") || strings.Contains(msg, "gateway HTTP 401:"):
		return apperr.New(apperr.Upstream, "知识库 MCP 服务拒绝访问")
	case strings.HasPrefix(msg, "mcp tool ") && !strings.Contains(msg, "http://") && !strings.Contains(msg, "https://"):
		return apperr.New(apperr.Upstream, truncateRunes(strings.TrimSpace(msg), 200))
	default:
		return apperr.New(apperr.Upstream, "知识库 MCP 服务不可用或调用失败")
	}
}

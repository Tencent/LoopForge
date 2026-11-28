package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	obsidianRootSourceID   = "__root__"
	obsidianRootSourceName = "Vault 根目录文档"
	obsidianMaxListDepth   = 3
	obsidianMaxListDocs    = 200
)

type obsidianFactory struct{}

func newObsidianFactory() ConnectorFactory { return obsidianFactory{} }

func (obsidianFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               domain.ConnectorTypeObsidian,
		Name:               "Obsidian 本地知识库",
		Description:        "通过 Local REST API 插件只读检索 Obsidian Vault 目录、笔记全文与标签元数据",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthBearer,
		Fields: []domain.ConnectorFieldSpec{
			{
				Key:         "endpoint",
				Label:       "Local REST API 地址",
				Kind:        "url",
				Placeholder: "https://127.0.0.1:27124",
				Required:    true,
				Help:        "Local REST API 地址（HTTPS 默认 27124，HTTP 默认 27123；Docker 部署需在插件设置绑定 0.0.0.0）",
			},
			{
				Key:         "token",
				Label:       "API Key",
				Kind:        "secret",
				Placeholder: "填写 Local REST API Key（编辑留空则保留原密钥）",
				Required:    true,
				Secret:      true,
				Help:        "在 Obsidian「设置 → Local REST API → Copy API Key」获取",
			},
			{
				Key:         "tlsInsecureSkipVerify",
				Label:       "信任本地自签名证书（true/false）",
				Kind:        "text",
				Placeholder: "true",
				Required:    false,
				Help:        "默认 true（仅对本地/内网地址生效），用于直连插件默认的 27124 自签名 HTTPS 端口",
			},
		},
	}
}

func resolveObsidianSkipTLS(endpoint string, extra map[string]string) bool {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	if raw := strings.TrimSpace(extra["tlsInsecureSkipVerify"]); raw != "" {
		return parseBoolExtra(extra, "tlsInsecureSkipVerify", false)
	}
	return isLocalOrPrivateHost(u.Hostname())
}

func (obsidianFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, hasExistingSecret bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	normalized, err := validateNativeBaseURL(endpoint, "", "Obsidian")
	if err != nil {
		return "", cfg, secret, err
	}
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)
	cfg.Extra = normalizeExtraMap(cfg.Extra)

	// Enforce local/private-only self-signed TLS boundary at validation time.
	skipTLS := resolveObsidianSkipTLS(normalized, cfg.Extra)
	if _, err := newNativeHTTPClient(nativeDefaultTimeout, normalized, skipTLS); err != nil {
		return "", cfg, secret, err
	}

	secret.AuthType = firstNonEmpty(strings.TrimSpace(secret.AuthType), domain.ConnectorAuthBearer)
	secret.Token = stripTokenPrefix(secret.Token)
	secret.Headers = normalizeHeaderMap(secret.Headers)
	if strings.TrimSpace(secret.Token) == "" && !hasExistingSecret {
		return "", cfg, secret, apperr.New(apperr.Invalid, "请填写 Obsidian Local REST API Key")
	}
	return normalized, cfg, secret, nil
}

func (obsidianFactory) New(spec ConnectorSpec) (Connector, error) {
	base, err := validateNativeBaseURL(spec.Endpoint, "", "Obsidian")
	if err != nil {
		return nil, err
	}
	tok := stripTokenPrefix(spec.Secret.Token)
	if tok == "" {
		return nil, apperr.New(apperr.Invalid, "Obsidian 连接器缺少 API Key 凭据")
	}
	timeout := nativeDefaultTimeout
	if spec.Config.TimeoutSec > 0 {
		timeout = time.Duration(spec.Config.TimeoutSec) * time.Second
	}
	skipTLS := resolveObsidianSkipTLS(base, spec.Config.Extra)
	client, err := newNativeHTTPClient(timeout, base, skipTLS)
	if err != nil {
		return nil, err
	}
	spec.Endpoint = base
	spec.Secret.Token = tok
	return &obsidianConnector{
		spec:    spec,
		baseURL: base,
		http:    client,
	}, nil
}

type obsidianConnector struct {
	spec    ConnectorSpec
	baseURL string
	http    *http.Client
}

func (c *obsidianConnector) Type() string { return domain.ConnectorTypeObsidian }

func (c *obsidianConnector) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return nativeReadOnlyToolDefinitions(), nil
}

func (c *obsidianConnector) Check(ctx context.Context) CheckResult {
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := time.Now()

	var statusResp struct {
		Authenticated bool   `json:"authenticated"`
		Status        string `json:"status"`
	}
	if err := c.doJSON(cctx, http.MethodGet, "/", "", &statusResp); err != nil {
		return CheckResult{
			ConnectorID: c.spec.ID,
			OK:          false,
			Error:       RedactSecrets(err.Error(), c.spec.Secret),
			LatencyMs:   time.Since(started).Milliseconds(),
		}
	}
	if !statusResp.Authenticated {
		return CheckResult{
			ConnectorID: c.spec.ID,
			OK:          false,
			Error:       "Obsidian API Key 鉴权未通过，请核对 Local REST API 插件密钥",
			LatencyMs:   time.Since(started).Milliseconds(),
		}
	}

	sources, err := c.ListSources(cctx)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return CheckResult{
			ConnectorID: c.spec.ID,
			OK:          false,
			Error:       RedactSecrets(err.Error(), c.spec.Secret),
			LatencyMs:   latency,
		}
	}
	return CheckResult{
		ConnectorID: c.spec.ID,
		OK:          true,
		LatencyMs:   latency,
		Sources:     sources,
		Tools:       nativeReadOnlyToolNames(),
	}
}

func (c *obsidianConnector) ListSources(ctx context.Context) ([]Source, error) {
	files, err := c.listVaultDir(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []Source
	hasRootMarkdown := false
	seen := map[string]bool{}
	for _, entry := range files {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.HasSuffix(entry, "/") {
			dir := strings.TrimSuffix(entry, "/")
			if dir == "" || strings.HasPrefix(dir, ".") || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, Source{
				ID:     dir,
				Name:   dir,
				Status: "ready",
			})
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry), ".md") {
			hasRootMarkdown = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if hasRootMarkdown {
		out = append(out, Source{
			ID:     obsidianRootSourceID,
			Name:   obsidianRootSourceName,
			Status: "ready",
		})
	}
	return out, nil
}

func (c *obsidianConnector) CallTool(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	started := time.Now()
	res, err := c.callToolInner(ctx, tool, args, scope)
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(started).Milliseconds()
	}
	logKnowledgeDebugCall(c.spec, "upstream_obsidian", strings.TrimSpace(tool), scope, args, res, err)
	return res, err
}

func (c *obsidianConnector) callToolInner(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	switch strings.TrimSpace(tool) {
	case WeKnoraToolListKnowledgeBases:
		started := time.Now()
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		sources, err := c.ListSources(cctx)
		if err != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
		}
		return buildListKBsResult(sources, scope, started)
	case WeKnoraToolListDocuments:
		kbID := strings.TrimSpace(argString(args["knowledge_base_id"]))
		keyword := strings.TrimSpace(argString(args["keyword"]))
		page := argInt(args["page"], 1)
		pageSize := argInt(args["page_size"], 20)
		return c.execListDocs(ctx, kbID, keyword, page, pageSize, scope)
	case WeKnoraToolSearchKnowledge:
		query := strings.TrimSpace(argString(args["query"]))
		if query == "" {
			return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "query 不能为空")
		}
		reqIDs := extractSearchKBIDs(args)
		limit := argInt(args["limit"], 10)
		return c.execSearch(ctx, query, reqIDs, limit, scope)
	case WeKnoraToolReadDocument:
		docID := extractReadDocID(args)
		offset := argInt(args["offset"], 0)
		limit := argInt(args["limit"], 20)
		query := strings.TrimSpace(argString(args["query"]))
		return c.execReadDoc(ctx, docID, offset, limit, query, scope)
	default:
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, fmt.Sprintf("Obsidian 连接器不支持只读工具 %q", tool))
	}
}

func cleanVaultRelPath(raw string) (relPath string, topSource string, err error) {
	return cleanVaultPath(raw, true)
}

func cleanVaultPath(raw string, requireMarkdown bool) (relPath string, topSource string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", apperr.New(apperr.Invalid, "文档路径不能为空")
	}
	if strings.Contains(raw, "\\") || strings.ContainsRune(raw, 0) {
		return "", "", apperr.New(apperr.Invalid, "文档路径包含非法字符")
	}
	trimmed := strings.TrimPrefix(raw, "/")
	parts := strings.Split(trimmed, "/")
	cleaned := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			return "", "", apperr.New(apperr.Invalid, "文档路径不允许包含 .. 穿越")
		}
		if strings.HasPrefix(seg, ".") {
			return "", "", apperr.New(apperr.Invalid, "文档路径不允许访问隐藏目录或文件")
		}
		cleaned = append(cleaned, seg)
	}
	if len(cleaned) == 0 {
		return "", "", apperr.New(apperr.Invalid, "文档路径不能为空")
	}
	relPath = strings.Join(cleaned, "/")
	if requireMarkdown && !strings.HasSuffix(strings.ToLower(relPath), ".md") {
		return "", "", apperr.New(apperr.Invalid, "Obsidian 连接器仅允许读取 .md 文档")
	}
	if len(cleaned) == 1 {
		topSource = obsidianRootSourceID
	} else {
		topSource = cleaned[0]
	}
	return relPath, topSource, nil
}

func escapeVaultPath(relPath string) string {
	if relPath == "" {
		return ""
	}
	parts := strings.Split(relPath, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

func (c *obsidianConnector) listVaultDir(ctx context.Context, relDir string) ([]string, error) {
	endpoint := "/vault/"
	if relDir != "" {
		endpoint = "/vault/" + escapeVaultPath(relDir) + "/"
	}
	var resp struct {
		Files []string `json:"files"`
	}
	if err := c.doJSON(ctx, http.MethodGet, endpoint, "", &resp); err != nil {
		return nil, err
	}
	files := make([]string, 0, len(resp.Files))
	for _, entry := range resp.Files {
		trimmed := strings.TrimSpace(entry)
		visible := trimmed != ""
		for _, segment := range strings.Split(strings.TrimSuffix(trimmed, "/"), "/") {
			if strings.HasPrefix(segment, ".") {
				visible = false
				break
			}
		}
		if visible {
			files = append(files, trimmed)
		}
	}
	return files, nil
}

func (c *obsidianConnector) execListDocs(ctx context.Context, kbID, keyword string, page, pageSize int, scope SourceScope) (CallResult, error) {
	if kbID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_base_id 不能为空")
	}
	if !scope.Allows(kbID) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("知识库来源 %q 不在当前授权范围内", kbID))
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var paths []string
	if kbID == obsidianRootSourceID {
		entries, err := c.listVaultDir(cctx, "")
		if err != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry, "/") && strings.HasSuffix(strings.ToLower(entry), ".md") {
				paths = append(paths, entry)
			}
		}
	} else {
		cleanDir, _, err := cleanVaultPath(kbID, false)
		if err != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable}, err
		}
		if err := c.collectVaultMarkdown(cctx, cleanDir, 1, &paths); err != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
		}
	}
	sort.Strings(paths)

	type docItem struct {
		ID              string `json:"id"`
		KnowledgeID     string `json:"knowledge_id"`
		KnowledgeBaseID string `json:"knowledge_base_id"`
		Title           string `json:"title"`
		FileName        string `json:"file_name"`
	}
	kwLow := strings.ToLower(keyword)
	filtered := make([]docItem, 0, len(paths))
	for _, p := range paths {
		base := path.Base(p)
		title := strings.TrimSuffix(base, path.Ext(base))
		if kwLow != "" && !strings.Contains(strings.ToLower(p), kwLow) && !strings.Contains(strings.ToLower(title), kwLow) {
			continue
		}
		filtered = append(filtered, docItem{
			ID:              p,
			KnowledgeID:     p,
			KnowledgeBaseID: kbID,
			Title:           title,
			FileName:        base,
		})
	}
	paged, total, page, pageSize := paginateSlice(filtered, page, pageSize)
	status := domain.KnowledgeStatusHit
	if len(paged) == 0 {
		status = domain.KnowledgeStatusEmpty
	}
	var targets []string
	for _, doc := range paged {
		if len(targets) >= maxKnowledgeReadTargets {
			break
		}
		if lbl := clipCatalogText(firstNonEmpty(doc.FileName, doc.Title, doc.ID), 120); lbl != "" {
			targets = append(targets, lbl)
		}
	}
	raw, err := json.Marshal(map[string]any{
		"documents": paged,
		"total":     total,
		"page":      page,
		"pageSize":  pageSize,
	})
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(paged),
		Targets:   targets,
		Citeable:  false,
		LatencyMs: time.Since(started).Milliseconds(),
	}, err
}

func (c *obsidianConnector) collectVaultMarkdown(ctx context.Context, dir string, depth int, out *[]string) error {
	if ctx.Err() != nil || depth > obsidianMaxListDepth || len(*out) >= obsidianMaxListDocs {
		return nil
	}
	entries, err := c.listVaultDir(ctx, dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if len(*out) >= obsidianMaxListDocs {
			return nil
		}
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.HasSuffix(entry, "/") {
			sub := strings.TrimSuffix(entry, "/")
			if sub == "" || strings.HasPrefix(sub, ".") {
				continue
			}
			if depth < obsidianMaxListDepth {
				_ = c.collectVaultMarkdown(ctx, dir+"/"+sub, depth+1, out)
			}
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry), ".md") {
			*out = append(*out, dir+"/"+entry)
		}
	}
	return nil
}

func (c *obsidianConnector) execSearch(ctx context.Context, query string, reqIDs []string, limit int, scope SourceScope) (CallResult, error) {
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	scopedIDs, err := scope.FilterIDs(reqIDs)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, err.Error())
	}

	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var hits []struct {
		Filename string  `json:"filename"`
		Score    float64 `json:"score"`
		Matches  []struct {
			Context string `json:"context"`
		} `json:"matches"`
	}
	searchPath := "/search/simple/?query=" + url.QueryEscape(query) + "&contextLength=160"
	if err := c.doJSON(cctx, http.MethodPost, searchPath, "", &hits); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	type hitPayload struct {
		Source      string  `json:"source"`
		SourceName  string  `json:"source_name,omitempty"`
		KnowledgeID string  `json:"knowledge_id"`
		Title       string  `json:"title,omitempty"`
		FileName    string  `json:"file_name,omitempty"`
		Snippet     string  `json:"snippet"`
		Score       float64 `json:"score,omitempty"`
	}
	results := make([]hitPayload, 0, min(len(hits), limit))
	evidences := make([]domain.KnowledgeEvidence, 0, min(len(hits), limit))
	var targets []string
	seenTarget := map[string]bool{}

	for _, h := range hits {
		if len(results) >= limit {
			break
		}
		relPath, topSource, pathErr := cleanVaultRelPath(h.Filename)
		if pathErr != nil {
			continue
		}
		if !scope.Allows(topSource) {
			continue
		}
		if len(scopedIDs) > 0 && !slices.Contains(scopedIDs, topSource) {
			continue
		}
		var contexts []string
		for _, m := range h.Matches {
			if s := strings.TrimSpace(m.Context); s != "" {
				contexts = append(contexts, s)
			}
		}
		snippet := clipText(strings.Join(contexts, "\n...\n"), 500)
		fileName := path.Base(relPath)
		title := strings.TrimSuffix(fileName, path.Ext(fileName))
		sourceName := topSource
		if topSource == obsidianRootSourceID {
			sourceName = obsidianRootSourceName
		}

		results = append(results, hitPayload{
			Source:      topSource,
			SourceName:  sourceName,
			KnowledgeID: relPath,
			Title:       title,
			FileName:    fileName,
			Snippet:     snippet,
			Score:       h.Score,
		})
		ev := domain.KnowledgeEvidence{
			ConnectionID: c.spec.ID,
			SourceID:     topSource,
			SourceName:   clipCatalogText(sourceName, 120),
			DocumentID:   relPath,
			Title:        clipCatalogText(title, 120),
			FileName:     clipCatalogText(fileName, 120),
		}
		evidences = append(evidences, ev)
		if lbl := clipCatalogText(firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID), 120); lbl != "" && !seenTarget[lbl] && len(targets) < maxKnowledgeReadTargets {
			seenTarget[lbl] = true
			targets = append(targets, lbl)
		}
	}

	status := domain.KnowledgeStatusHit
	if len(results) == 0 {
		status = domain.KnowledgeStatusEmpty
	}
	raw, err := json.Marshal(map[string]any{
		"query":   query,
		"mode":    "simple",
		"status":  status,
		"results": results,
	})
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(results),
		Targets:   targets,
		Evidences: evidences,
		Citeable:  true,
		LatencyMs: time.Since(started).Milliseconds(),
	}, err
}

func (c *obsidianConnector) execReadDoc(ctx context.Context, docID string, offset, limit int, query string, scope SourceScope) (CallResult, error) {
	relPath, topSource, err := cleanVaultRelPath(docID)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	if !scope.Allows(topSource) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("文档 %q 所属目录 %q 不在当前授权范围内", relPath, topSource))
	}

	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	raw, contentType, err := c.doRaw(cctx, http.MethodGet, "/vault/"+escapeVaultPath(relPath), "application/vnd.olrapi.note+json")
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	content := string(raw)
	var extraFields map[string]any
	if strings.Contains(strings.ToLower(contentType), "json") {
		var note struct {
			Content     string         `json:"content"`
			Tags        []string       `json:"tags,omitempty"`
			Frontmatter map[string]any `json:"frontmatter,omitempty"`
		}
		if json.Unmarshal(raw, &note) == nil && (note.Content != "" || len(note.Tags) > 0 || len(note.Frontmatter) > 0) {
			content = note.Content
			if len(note.Tags) > 0 {
				extraFields = map[string]any{"tags": note.Tags}
			}
		}
	}

	fileName := path.Base(relPath)
	title := strings.TrimSuffix(fileName, path.Ext(fileName))
	sourceName := topSource
	if topSource == obsidianRootSourceID {
		sourceName = obsidianRootSourceName
	}

	return buildReadDocumentResult(nativeReadDocParams{
		ConnectionID: c.spec.ID,
		SourceID:     topSource,
		SourceName:   sourceName,
		DocumentID:   relPath,
		Title:        title,
		FileName:     fileName,
		Content:      content,
		Offset:       offset,
		Limit:        limit,
		Query:        query,
		ExtraFields:  extraFields,
		Started:      started,
	})
}

func (c *obsidianConnector) doRaw(ctx context.Context, method, pathStr, accept string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathStr, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.spec.Secret.Token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := doRequestWithDockerFallback(c.http, req)
	if err != nil {
		return nil, "", fmt.Errorf("Obsidian 请求失败：%s", RedactSecrets(err.Error(), c.spec.Secret))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, nativeMaxErrorBytes))
		msg := RedactSecrets(clipText(strings.TrimSpace(string(raw)), 240), c.spec.Secret)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, "", apperr.New(apperr.Upstream, "Obsidian API Key 鉴权失败，请核对 Local REST API 插件密钥")
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, "", fmt.Errorf("Obsidian HTTP %d: %s", resp.StatusCode, msg)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, nativeMaxResponseBytes))
	if err != nil {
		return nil, "", fmt.Errorf("Obsidian %s 响应读取失败：%s", pathStr, RedactSecrets(err.Error(), c.spec.Secret))
	}
	return raw, resp.Header.Get("Content-Type"), nil
}

func (c *obsidianConnector) doJSON(ctx context.Context, method, pathStr, accept string, out any) error {
	raw, _, err := c.doRaw(ctx, method, pathStr, accept)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Obsidian %s 返回无法解析：%s", pathStr, RedactSecrets(clipText(string(raw), 200), c.spec.Secret))
		}
	}
	return nil
}

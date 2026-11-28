package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const siyuanMetaTTL = 60 * time.Second

type siyuanFactory struct{}

func newSiYuanFactory() ConnectorFactory { return siyuanFactory{} }

func (siyuanFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               domain.ConnectorTypeSiYuan,
		Name:               "思源笔记（SiYuan）",
		Description:        "通过思源内置内核 API（:6806）只读检索本地或局域网笔记本、块全文与 Markdown 原文",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthAPIKey,
		Fields: []domain.ConnectorFieldSpec{
			{
				Key:         "endpoint",
				Label:       "思源服务地址",
				Kind:        "url",
				Placeholder: "http://127.0.0.1:6806",
				Required:    true,
				Help:        "思源笔记服务地址；Docker 部署请开启思源「设置 → 关于 → 网络伺服」",
			},
			{
				Key:         "token",
				Label:       "API Token",
				Kind:        "secret",
				Placeholder: "填写思源 API Token（编辑留空则保留原密钥）",
				Required:    true,
				Secret:      true,
				Help:        "在思源笔记「设置 → 关于 → API token」复制，使用 Authorization: Token 鉴权",
			},
		},
	}
}

func (siyuanFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, hasExistingSecret bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	normalized, err := validateNativeBaseURL(endpoint, "", "思源笔记")
	if err != nil {
		return "", cfg, secret, err
	}
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)
	cfg.Extra = normalizeExtraMap(cfg.Extra)

	secret.AuthType = firstNonEmpty(strings.TrimSpace(secret.AuthType), domain.ConnectorAuthAPIKey)
	secret.Token = stripTokenPrefix(secret.Token)
	secret.Headers = normalizeHeaderMap(secret.Headers)
	if strings.TrimSpace(secret.Token) == "" && !hasExistingSecret {
		return "", cfg, secret, apperr.New(apperr.Invalid, "请填写思源笔记 API Token")
	}
	return normalized, cfg, secret, nil
}

func (siyuanFactory) New(spec ConnectorSpec) (Connector, error) {
	base, err := validateNativeBaseURL(spec.Endpoint, "", "思源笔记")
	if err != nil {
		return nil, err
	}
	tok := stripTokenPrefix(spec.Secret.Token)
	if tok == "" {
		return nil, apperr.New(apperr.Invalid, "思源笔记连接器缺少 API Token 凭据")
	}
	timeout := nativeDefaultTimeout
	if spec.Config.TimeoutSec > 0 {
		timeout = time.Duration(spec.Config.TimeoutSec) * time.Second
	}
	client, err := newNativeHTTPClient(timeout, base, false)
	if err != nil {
		return nil, err
	}
	spec.Endpoint = base
	spec.Secret.Token = tok
	return &siyuanConnector{
		spec:    spec,
		baseURL: base,
		http:    client,
	}, nil
}

type siyuanConnector struct {
	spec    ConnectorSpec
	baseURL string
	http    *http.Client

	mu      sync.Mutex
	nbNames kbNamesEntry
}

func (c *siyuanConnector) Type() string { return domain.ConnectorTypeSiYuan }

func (c *siyuanConnector) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return nativeReadOnlyToolDefinitions(), nil
}

func (c *siyuanConnector) Check(ctx context.Context) CheckResult {
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := time.Now()
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

type siyuanNotebook struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Closed bool   `json:"closed"`
}

func (c *siyuanConnector) ListSources(ctx context.Context) ([]Source, error) {
	var data struct {
		Notebooks []siyuanNotebook `json:"notebooks"`
	}
	if err := c.doPost(ctx, "/api/notebook/lsNotebooks", map[string]any{}, &data); err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(data.Notebooks))
	names := make(map[string]string, len(data.Notebooks))
	for _, nb := range data.Notebooks {
		if nb.Closed || strings.TrimSpace(nb.ID) == "" {
			continue
		}
		out = append(out, Source{
			ID:     nb.ID,
			Name:   firstNonEmpty(nb.Name, nb.ID),
			Status: "ready",
		})
		names[nb.ID] = firstNonEmpty(nb.Name, nb.ID)
	}
	c.mu.Lock()
	c.nbNames = kbNamesEntry{names: names, expiresAt: time.Now().Add(siyuanMetaTTL)}
	c.mu.Unlock()
	return out, nil
}

func (c *siyuanConnector) cachedNotebookNames(ctx context.Context) map[string]string {
	now := time.Now()
	c.mu.Lock()
	if c.nbNames.names != nil && now.Before(c.nbNames.expiresAt) {
		out := make(map[string]string, len(c.nbNames.names))
		for k, v := range c.nbNames.names {
			out[k] = v
		}
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()

	sources, err := c.ListSources(ctx)
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(sources))
	for _, s := range sources {
		out[s.ID] = s.Name
	}
	return out
}

func (c *siyuanConnector) CallTool(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	started := time.Now()
	res, err := c.callToolInner(ctx, tool, args, scope)
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(started).Milliseconds()
	}
	logKnowledgeDebugCall(c.spec, "upstream_siyuan", strings.TrimSpace(tool), scope, args, res, err)
	return res, err
}

func (c *siyuanConnector) callToolInner(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
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
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, fmt.Sprintf("思源笔记连接器不支持只读工具 %q", tool))
	}
}

func (c *siyuanConnector) execListDocs(ctx context.Context, kbID, keyword string, page, pageSize int, scope SourceScope) (CallResult, error) {
	if kbID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_base_id 不能为空")
	}
	if !scope.Allows(kbID) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("知识库来源 %q 不在当前授权范围内", kbID))
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var data struct {
		Files []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Path     string `json:"path"`
			MTime    int64  `json:"mtime"`
			SubCount int    `json:"subFileCount"`
		} `json:"files"`
	}
	if err := c.doPost(cctx, "/api/filetree/listDocsByPath", map[string]any{
		"notebook":     kbID,
		"path":         "/",
		"maxListCount": 200,
	}, &data); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	type docItem struct {
		ID              string `json:"id"`
		KnowledgeID     string `json:"knowledge_id"`
		KnowledgeBaseID string `json:"knowledge_base_id"`
		Title           string `json:"title"`
		Path            string `json:"path,omitempty"`
	}
	kwLow := strings.ToLower(keyword)
	filtered := make([]docItem, 0, len(data.Files))
	for _, f := range data.Files {
		title := strings.TrimSuffix(strings.TrimSpace(f.Name), ".sy")
		if title == "" {
			title = f.ID
		}
		if kwLow != "" && !strings.Contains(strings.ToLower(title), kwLow) {
			continue
		}
		filtered = append(filtered, docItem{
			ID:              f.ID,
			KnowledgeID:     f.ID,
			KnowledgeBaseID: kbID,
			Title:           title,
			Path:            f.Path,
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
		if lbl := clipCatalogText(firstNonEmpty(doc.Title, doc.ID), 120); lbl != "" {
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

func (c *siyuanConnector) execSearch(ctx context.Context, query string, reqIDs []string, limit int, scope SourceScope) (CallResult, error) {
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

	nbNames := c.cachedNotebookNames(cctx)
	reqBody := map[string]any{
		"query":  query,
		"method": 0,
		"types": map[string]bool{
			"document":   true,
			"heading":    true,
			"paragraph":  true,
			"codeBlock":  true,
			"listItem":   true,
			"table":      true,
			"blockquote": true,
		},
		"page":     1,
		"pageSize": 20,
	}
	if len(scopedIDs) > 0 {
		reqBody["paths"] = scopedIDs
	}

	var data struct {
		Blocks []struct {
			ID       string `json:"id"`
			RootID   string `json:"rootID"`
			Box      string `json:"box"`
			HPath    string `json:"hPath"`
			Markdown string `json:"markdown"`
			Content  string `json:"content"`
			FContent string `json:"fContent"`
		} `json:"blocks"`
	}
	if err := c.doPost(cctx, "/api/search/fullTextSearchBlock", reqBody, &data); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	type hitPayload struct {
		Source      string `json:"source"`
		SourceName  string `json:"source_name,omitempty"`
		KnowledgeID string `json:"knowledge_id"`
		BlockID     string `json:"block_id,omitempty"`
		Title       string `json:"title,omitempty"`
		Snippet     string `json:"snippet"`
	}
	results := make([]hitPayload, 0, min(len(data.Blocks), limit))
	evidences := make([]domain.KnowledgeEvidence, 0, min(len(data.Blocks), limit))
	var targets []string
	seenTarget := map[string]bool{}

	for _, blk := range data.Blocks {
		if len(results) >= limit {
			break
		}
		box := strings.TrimSpace(blk.Box)
		if !scope.Allows(box) {
			continue
		}
		if len(scopedIDs) > 0 && !slices.Contains(scopedIDs, box) {
			continue
		}
		snippet := strings.TrimSpace(blk.Markdown)
		if snippet == "" {
			snippet = stripSiYuanHighlightTags(blk.Content)
		}
		docID := firstNonEmpty(blk.RootID, blk.ID)
		title := strings.TrimPrefix(strings.TrimSpace(blk.HPath), "/")
		title = firstNonEmpty(title, stripSiYuanHighlightTags(blk.FContent), docID)
		sourceName := nbNames[box]

		results = append(results, hitPayload{
			Source:      box,
			SourceName:  sourceName,
			KnowledgeID: docID,
			BlockID:     blk.ID,
			Title:       title,
			Snippet:     clipText(snippet, 500),
		})
		locator := ""
		if blk.ID != "" && blk.ID != docID {
			locator = "block " + blk.ID
		}
		ev := domain.KnowledgeEvidence{
			ConnectionID: c.spec.ID,
			SourceID:     box,
			SourceName:   clipCatalogText(sourceName, 120),
			DocumentID:   docID,
			Title:        clipCatalogText(title, 120),
			Locator:      locator,
		}
		evidences = append(evidences, ev)
		if lbl := clipCatalogText(firstNonEmpty(ev.Title, ev.DocumentID), 120); lbl != "" && !seenTarget[lbl] && len(targets) < maxKnowledgeReadTargets {
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
		"mode":    "fulltext",
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

func (c *siyuanConnector) execReadDoc(ctx context.Context, docID string, offset, limit int, query string, scope SourceScope) (CallResult, error) {
	if docID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_id 不能为空")
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var blockInfo struct {
		Box    string `json:"box"`
		RootID string `json:"rootID"`
		Path   string `json:"path"`
		HPath  string `json:"hPath"`
	}
	if err := c.doPost(cctx, "/api/block/getBlockInfo", map[string]any{"id": docID}, &blockInfo); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}
	box := strings.TrimSpace(blockInfo.Box)
	if !scope.Allows(box) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("文档 %q 所属笔记本 %q 不在当前授权范围内", docID, box))
	}

	targetID := firstNonEmpty(blockInfo.RootID, docID)
	var exported struct {
		HPath   string `json:"hPath"`
		Content string `json:"content"`
	}
	if err := c.doPost(cctx, "/api/export/exportMdContent", map[string]any{"id": targetID}, &exported); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	nbNames := c.cachedNotebookNames(cctx)
	title := strings.TrimPrefix(strings.TrimSpace(firstNonEmpty(exported.HPath, blockInfo.HPath)), "/")
	return buildReadDocumentResult(nativeReadDocParams{
		ConnectionID: c.spec.ID,
		SourceID:     box,
		SourceName:   nbNames[box],
		DocumentID:   targetID,
		Title:        firstNonEmpty(title, targetID),
		Content:      exported.Content,
		Offset:       offset,
		Limit:        limit,
		Query:        query,
		Started:      started,
	})
}

func (c *siyuanConnector) doPost(ctx context.Context, pathStr string, payload any, out any) error {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+pathStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+c.spec.Secret.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := doRequestWithDockerFallback(c.http, req)
	if err != nil {
		return fmt.Errorf("思源笔记请求失败：%s", RedactSecrets(err.Error(), c.spec.Secret))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, nativeMaxErrorBytes))
		msg := RedactSecrets(clipText(strings.TrimSpace(string(raw)), 240), c.spec.Secret)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return apperr.New(apperr.Upstream, "思源笔记鉴权失败，请核对 API Token")
		}
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("思源笔记 HTTP %d: %s", resp.StatusCode, msg)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, nativeMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("思源笔记 %s 响应读取失败：%s", pathStr, RedactSecrets(err.Error(), c.spec.Secret))
	}
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("思源笔记 %s 返回无法解析：%s", pathStr, RedactSecrets(clipText(string(raw), 200), c.spec.Secret))
	}
	if env.Code != 0 {
		msg := RedactSecrets(strings.TrimSpace(env.Msg), c.spec.Secret)
		low := strings.ToLower(msg)
		if env.Code == -1 && (strings.Contains(low, "auth") || strings.Contains(low, "token") || strings.Contains(msg, "鉴权") || strings.Contains(msg, "权限")) {
			return fmt.Errorf("思源笔记鉴权失败，请检查 API Token（%s）", msg)
		}
		if msg == "" {
			msg = fmt.Sprintf("code=%d", env.Code)
		}
		return fmt.Errorf("思源笔记接口 %s 返回错误（code=%d）：%s", pathStr, env.Code, msg)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("思源笔记 %s 数据解析失败：%s", pathStr, RedactSecrets(err.Error(), c.spec.Secret))
		}
	}
	return nil
}

func stripSiYuanHighlightTags(s string) string {
	r := strings.NewReplacer(
		"<mark>", "", "</mark>", "",
		"<MARK>", "", "</MARK>", "",
		"<b>", "", "</b>", "",
		"<B>", "", "</B>", "",
	)
	return strings.TrimSpace(r.Replace(s))
}

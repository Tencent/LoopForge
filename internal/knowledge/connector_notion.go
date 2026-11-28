package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	notionDefaultBaseURL    = "https://api.notion.com"
	notionAPIVersion        = "2026-03-11"
	notionCacheTTL          = 60 * time.Second
	notionMax429Retries     = 2
	notionMaxRetryAfter     = 5 * time.Second
	notionMaxBlockPages     = 3
	notionMaxBlockChildReqs = 12
	notionSearchPreviewMax  = 5
	notionSearchPreviewConc = 3
)

type notionFactory struct{}

func newNotionFactory() ConnectorFactory { return notionFactory{} }

func (notionFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               domain.ConnectorTypeNotion,
		Name:               "Notion 知识库",
		Description:        "通过 Notion Internal Integration Token 只读检索已授权的页面、数据库与结构化块正文",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthBearer,
		Fields: []domain.ConnectorFieldSpec{
			{
				Key:         "endpoint",
				Label:       "Notion API 地址（可选）",
				Kind:        "url",
				Placeholder: notionDefaultBaseURL,
				Required:    false,
				Help:        "默认 https://api.notion.com，留空自动使用官方地址",
			},
			{
				Key:         "token",
				Label:       "Integration Token",
				Kind:        "secret",
				Placeholder: "ntn_...",
				Required:    true,
				Secret:      true,
				Help:        "填入 Notion Internal Integration Secret，并在目标页面右上角 ••• → Connections 添加该集成",
			},
		},
	}
}

func (notionFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, hasExistingSecret bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	normalized, err := validateNativeBaseURL(endpoint, notionDefaultBaseURL, "Notion")
	if err != nil {
		return "", cfg, secret, err
	}
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)
	cfg.Extra = normalizeExtraMap(cfg.Extra)

	secret.AuthType = firstNonEmpty(strings.TrimSpace(secret.AuthType), domain.ConnectorAuthBearer)
	secret.Token = stripTokenPrefix(secret.Token)
	secret.Headers = normalizeHeaderMap(secret.Headers)
	if strings.TrimSpace(secret.Token) == "" && !hasExistingSecret {
		return "", cfg, secret, apperr.New(apperr.Invalid, "请填写 Notion Integration Token")
	}
	return normalized, cfg, secret, nil
}

func (notionFactory) New(spec ConnectorSpec) (Connector, error) {
	base, err := validateNativeBaseURL(spec.Endpoint, notionDefaultBaseURL, "Notion")
	if err != nil {
		return nil, err
	}
	tok := stripTokenPrefix(spec.Secret.Token)
	if tok == "" {
		return nil, apperr.New(apperr.Invalid, "Notion 连接器缺少 Integration Token 凭据")
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
	return &notionConnector{
		spec:    spec,
		baseURL: base,
		http:    client,
	}, nil
}

type notionHierarchyCache struct {
	sources   []Source
	rootByID  map[string]string
	rootNames map[string]string
	objects   []notionObject
	expiresAt time.Time
}

type notionConnector struct {
	spec    ConnectorSpec
	baseURL string
	http    *http.Client

	mu    sync.Mutex
	cache notionHierarchyCache
}

func (c *notionConnector) Type() string { return domain.ConnectorTypeNotion }

func (c *notionConnector) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return nativeReadOnlyToolDefinitions(), nil
}

func normalizeNotionID(raw string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
}

func notionScopeAllows(scope SourceScope, id string) bool {
	if strings.TrimSpace(scope.Mode) != "allowlist" {
		return true
	}
	norm := normalizeNotionID(id)
	if norm == "" {
		return false
	}
	for _, allowed := range scope.Allowlist() {
		if normalizeNotionID(allowed) == norm {
			return true
		}
	}
	return false
}

func notionFilterScopeIDs(scope SourceScope, requested []string) ([]string, error) {
	req := CleanList(requested)
	allowed := scope.Allowlist()
	if len(allowed) == 0 {
		if strings.TrimSpace(scope.Mode) == "allowlist" {
			return nil, apperr.New(apperr.Forbidden, "知识库范围为空")
		}
		out := make([]string, 0, len(req))
		for _, id := range req {
			out = append(out, normalizeNotionID(id))
		}
		return out, nil
	}
	normAllowed := make([]string, 0, len(allowed))
	for _, id := range allowed {
		normAllowed = append(normAllowed, normalizeNotionID(id))
	}
	if len(req) == 0 {
		return normAllowed, nil
	}
	out := make([]string, 0, len(req))
	for _, id := range req {
		nid := normalizeNotionID(id)
		if !slices.Contains(normAllowed, nid) {
			return nil, apperr.New(apperr.Forbidden, fmt.Sprintf("知识库来源 %q 不在当前授权范围内", id))
		}
		out = append(out, nid)
	}
	return out, nil
}

func (c *notionConnector) Check(ctx context.Context) CheckResult {
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := time.Now()

	var me map[string]any
	if err := c.doJSON(cctx, http.MethodGet, "/v1/users/me", nil, &me); err != nil {
		return CheckResult{
			ConnectorID: c.spec.ID,
			OK:          false,
			Error:       RedactSecrets(err.Error(), c.spec.Secret),
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

type notionRichText struct {
	PlainText string `json:"plain_text"`
}

type notionParent struct {
	Type         string `json:"type"`
	PageID       string `json:"page_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	DataSourceID string `json:"data_source_id,omitempty"`
	BlockID      string `json:"block_id,omitempty"`
	Workspace    bool   `json:"workspace,omitempty"`
}

func (p notionParent) parentID() string {
	switch p.Type {
	case "page_id":
		return normalizeNotionID(p.PageID)
	case "database_id":
		return normalizeNotionID(p.DatabaseID)
	case "data_source_id":
		return normalizeNotionID(p.DataSourceID)
	case "block_id":
		return normalizeNotionID(p.BlockID)
	default:
		return ""
	}
}

type notionObject struct {
	Object     string                     `json:"object"`
	ID         string                     `json:"id"`
	URL        string                     `json:"url,omitempty"`
	Archived   bool                       `json:"archived,omitempty"`
	InTrash    bool                       `json:"in_trash,omitempty"`
	Parent     notionParent               `json:"parent"`
	Title      []notionRichText           `json:"title,omitempty"`
	Properties map[string]json.RawMessage `json:"properties,omitempty"`
}

func extractNotionRichText(items []notionRichText) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, rt := range items {
		b.WriteString(rt.PlainText)
	}
	return strings.TrimSpace(b.String())
}

func extractNotionTitle(obj notionObject) string {
	if s := extractNotionRichText(obj.Title); s != "" {
		return s
	}
	for _, rawProp := range obj.Properties {
		var prop struct {
			Type  string           `json:"type"`
			Title []notionRichText `json:"title"`
		}
		if json.Unmarshal(rawProp, &prop) == nil && prop.Type == "title" {
			if s := extractNotionRichText(prop.Title); s != "" {
				return s
			}
		}
	}
	return firstNonEmpty(obj.ID, "Untitled")
}

func (c *notionConnector) loadHierarchy(ctx context.Context, forceRefresh bool) (notionHierarchyCache, error) {
	now := time.Now()
	c.mu.Lock()
	if !forceRefresh && len(c.cache.rootByID) > 0 && now.Before(c.cache.expiresAt) {
		snap := c.cache
		c.mu.Unlock()
		return snap, nil
	}
	c.mu.Unlock()

	var resp struct {
		Results []notionObject `json:"results"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/search", map[string]any{"page_size": 100}, &resp); err != nil {
		return notionHierarchyCache{}, err
	}

	visible := make([]notionObject, 0, len(resp.Results))
	byID := make(map[string]notionObject, len(resp.Results))
	for _, obj := range resp.Results {
		if obj.InTrash || obj.Archived {
			continue
		}
		nid := normalizeNotionID(obj.ID)
		if nid == "" {
			continue
		}
		visible = append(visible, obj)
		byID[nid] = obj
	}

	rootByID := make(map[string]string, len(visible))
	var resolveRoot func(nid string, depth int) string
	resolveRoot = func(nid string, depth int) string {
		if r, ok := rootByID[nid]; ok {
			return r
		}
		obj, ok := byID[nid]
		if !ok || depth > 12 {
			return nid
		}
		if obj.Parent.Type == "workspace" {
			rootByID[nid] = nid
			return nid
		}
		pid := obj.Parent.parentID()
		if pid == "" {
			rootByID[nid] = nid
			return nid
		}
		if _, exists := byID[pid]; !exists {
			rootByID[nid] = nid
			return nid
		}
		r := resolveRoot(pid, depth+1)
		rootByID[nid] = r
		return r
	}

	rootSet := map[string]bool{}
	var sources []Source
	rootNames := map[string]string{}
	for _, obj := range visible {
		nid := normalizeNotionID(obj.ID)
		root := resolveRoot(nid, 0)
		if root == nid && !rootSet[nid] {
			rootSet[nid] = true
			title := extractNotionTitle(obj)
			rootNames[nid] = title
			sources = append(sources, Source{
				ID:     nid,
				Name:   title,
				Status: "ready",
			})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })

	snap := notionHierarchyCache{
		sources:   sources,
		rootByID:  rootByID,
		rootNames: rootNames,
		objects:   visible,
		expiresAt: time.Now().Add(notionCacheTTL),
	}
	c.mu.Lock()
	c.cache = snap
	c.mu.Unlock()
	return snap, nil
}

func (c *notionConnector) ListSources(ctx context.Context) ([]Source, error) {
	snap, err := c.loadHierarchy(ctx, true)
	if err != nil {
		return nil, err
	}
	return append([]Source(nil), snap.sources...), nil
}

func (c *notionConnector) resolveRootForObject(ctx context.Context, obj notionObject, snap notionHierarchyCache) string {
	nid := normalizeNotionID(obj.ID)
	if r, ok := snap.rootByID[nid]; ok && r != "" {
		return r
	}
	if pid := obj.Parent.parentID(); pid != "" {
		if r, ok := snap.rootByID[pid]; ok && r != "" {
			return r
		}
	}
	return nid
}

func (c *notionConnector) CallTool(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	started := time.Now()
	res, err := c.callToolInner(ctx, tool, args, scope)
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(started).Milliseconds()
	}
	logKnowledgeDebugCall(c.spec, "upstream_notion", strings.TrimSpace(tool), scope, args, res, err)
	return res, err
}

func (c *notionConnector) callToolInner(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	switch strings.TrimSpace(tool) {
	case WeKnoraToolListKnowledgeBases:
		started := time.Now()
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		snap, err := c.loadHierarchy(cctx, false)
		if err != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
		}
		kept := make([]Source, 0, len(snap.sources))
		var targets []string
		for _, src := range snap.sources {
			if !notionScopeAllows(scope, src.ID) {
				continue
			}
			kept = append(kept, src)
			if len(targets) < maxKnowledgeReadTargets {
				targets = append(targets, firstNonEmpty(src.Name, src.ID))
			}
		}
		status := domain.KnowledgeStatusHit
		if len(kept) == 0 {
			status = domain.KnowledgeStatusEmpty
		}
		raw, err := json.Marshal(map[string]any{"knowledge_bases": kept, "status": status})
		return CallResult{
			Status:    status,
			Text:      string(raw),
			Hits:      len(kept),
			Targets:   targets,
			Citeable:  false,
			LatencyMs: time.Since(started).Milliseconds(),
		}, err
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
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, fmt.Sprintf("Notion 连接器不支持只读工具 %q", tool))
	}
}

func (c *notionConnector) execListDocs(ctx context.Context, kbID, keyword string, page, pageSize int, scope SourceScope) (CallResult, error) {
	if kbID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_base_id 不能为空")
	}
	if !notionScopeAllows(scope, kbID) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("知识库来源 %q 不在当前授权范围内", kbID))
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	snap, err := c.loadHierarchy(cctx, false)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	normKB := normalizeNotionID(kbID)
	kwLow := strings.ToLower(keyword)
	type docItem struct {
		ID              string `json:"id"`
		KnowledgeID     string `json:"knowledge_id"`
		KnowledgeBaseID string `json:"knowledge_base_id"`
		Title           string `json:"title"`
		Object          string `json:"object,omitempty"`
		URL             string `json:"url,omitempty"`
	}
	var docs []docItem
	for _, obj := range snap.objects {
		nid := normalizeNotionID(obj.ID)
		root := snap.rootByID[nid]
		if root != normKB && nid != normKB {
			continue
		}
		if obj.Object != "page" && nid != normKB {
			continue
		}
		title := extractNotionTitle(obj)
		if kwLow != "" && !strings.Contains(strings.ToLower(title), kwLow) {
			continue
		}
		docs = append(docs, docItem{
			ID:              nid,
			KnowledgeID:     nid,
			KnowledgeBaseID: normKB,
			Title:           title,
			Object:          obj.Object,
			URL:             obj.URL,
		})
	}
	paged, total, page, pageSize := paginateSlice(docs, page, pageSize)
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

func (c *notionConnector) execSearch(ctx context.Context, query string, reqIDs []string, limit int, scope SourceScope) (CallResult, error) {
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	scopedIDs, err := notionFilterScopeIDs(scope, reqIDs)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, err
	}

	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	snap, _ := c.loadHierarchy(cctx, false)

	var resp struct {
		Results []notionObject `json:"results"`
	}
	if err := c.doJSON(cctx, http.MethodPost, "/v1/search", map[string]any{
		"query":     query,
		"page_size": 20,
	}, &resp); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	type matchedObj struct {
		obj        notionObject
		normID     string
		rootID     string
		rootName   string
		title      string
		previewTxt string
	}
	candidates := make([]matchedObj, 0, min(len(resp.Results), limit))
	for _, obj := range resp.Results {
		if len(candidates) >= limit {
			break
		}
		if obj.InTrash || obj.Archived {
			continue
		}
		nid := normalizeNotionID(obj.ID)
		rootID := c.resolveRootForObject(cctx, obj, snap)
		if !notionScopeAllows(scope, rootID) && !notionScopeAllows(scope, nid) {
			continue
		}
		if len(scopedIDs) > 0 && !slices.Contains(scopedIDs, rootID) && !slices.Contains(scopedIDs, nid) {
			continue
		}
		candidates = append(candidates, matchedObj{
			obj:      obj,
			normID:   nid,
			rootID:   rootID,
			rootName: snap.rootNames[rootID],
			title:    extractNotionTitle(obj),
		})
	}

	// Concurrently fetch first-screen blocks for top hits to extract snippets.
	previewN := min(len(candidates), notionSearchPreviewMax)
	if previewN > 0 {
		sem := make(chan struct{}, notionSearchPreviewConc)
		var wg sync.WaitGroup
		for i := 0; i < previewN; i++ {
			if candidates[i].obj.Object != "page" {
				continue
			}
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-cctx.Done():
					return
				}
				blocks, bErr := c.fetchBlockChildrenPage(cctx, candidates[idx].obj.ID, "", 30)
				if bErr != nil || len(blocks.Results) == 0 {
					return
				}
				md := notionBlocksToMarkdown(blocks.Results, 0)
				candidates[idx].previewTxt = extractKeywordSnippet(md, query, 280)
			}(i)
		}
		wg.Wait()
	}

	type hitPayload struct {
		Source      string `json:"source"`
		SourceName  string `json:"source_name,omitempty"`
		KnowledgeID string `json:"knowledge_id"`
		Title       string `json:"title,omitempty"`
		URL         string `json:"url,omitempty"`
		Snippet     string `json:"snippet"`
	}
	results := make([]hitPayload, 0, len(candidates))
	evidences := make([]domain.KnowledgeEvidence, 0, len(candidates))
	var targets []string
	seenTarget := map[string]bool{}

	for _, item := range candidates {
		snippet := firstNonEmpty(item.previewTxt, item.title)
		results = append(results, hitPayload{
			Source:      item.rootID,
			SourceName:  item.rootName,
			KnowledgeID: item.normID,
			Title:       item.title,
			URL:         item.obj.URL,
			Snippet:     snippet,
		})
		ev := domain.KnowledgeEvidence{
			ConnectionID: c.spec.ID,
			SourceID:     item.rootID,
			SourceName:   clipCatalogText(item.rootName, 120),
			DocumentID:   item.normID,
			Title:        clipCatalogText(item.title, 120),
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
		"mode":    "search",
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

func (c *notionConnector) execReadDoc(ctx context.Context, docID string, offset, limit int, query string, scope SourceScope) (CallResult, error) {
	if docID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_id 不能为空")
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var pageObj notionObject
	if err := c.doJSON(cctx, http.MethodGet, "/v1/pages/"+url.PathEscape(docID), nil, &pageObj); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}
	if pageObj.InTrash || pageObj.Archived {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.NotFound, "目标 Notion 页面已归档或已删除")
	}

	snap, _ := c.loadHierarchy(cctx, false)
	nid := normalizeNotionID(firstNonEmpty(pageObj.ID, docID))
	rootID := c.resolveRootForObject(cctx, pageObj, snap)
	if !notionScopeAllows(scope, rootID) && !notionScopeAllows(scope, nid) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("页面 %q 不在当前授权知识库范围内", docID))
	}

	budget := notionMaxBlockChildReqs
	md, err := c.renderBlockTreeMarkdown(cctx, firstNonEmpty(pageObj.ID, docID), 0, &budget)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	extra := map[string]any{}
	if pageObj.URL != "" {
		extra["url"] = pageObj.URL
	}
	return buildReadDocumentResult(nativeReadDocParams{
		ConnectionID: c.spec.ID,
		SourceID:     rootID,
		SourceName:   snap.rootNames[rootID],
		DocumentID:   nid,
		Title:        extractNotionTitle(pageObj),
		Content:      md,
		Offset:       offset,
		Limit:        limit,
		Query:        query,
		ExtraFields:  extra,
		Started:      started,
	})
}

type notionBlock struct {
	ID          string                     `json:"id"`
	Type        string                     `json:"type"`
	HasChildren bool                       `json:"has_children"`
	Children    []notionBlock              `json:"-"`
	Raw         map[string]json.RawMessage `json:"-"`
}

func (b *notionBlock) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var head struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		HasChildren bool   `json:"has_children"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	b.ID = head.ID
	b.Type = head.Type
	b.HasChildren = head.HasChildren
	b.Raw = raw
	return nil
}

type notionBlockPage struct {
	Results    []notionBlock `json:"results"`
	HasMore    bool          `json:"has_more"`
	NextCursor string        `json:"next_cursor"`
}

func (c *notionConnector) fetchBlockChildrenPage(ctx context.Context, blockID, cursor string, pageSize int) (notionBlockPage, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	q := url.Values{}
	q.Set("page_size", strconv.Itoa(pageSize))
	if cursor != "" {
		q.Set("start_cursor", cursor)
	}
	var out notionBlockPage
	err := c.doJSON(ctx, http.MethodGet, "/v1/blocks/"+url.PathEscape(blockID)+"/children?"+q.Encode(), nil, &out)
	return out, err
}

func (c *notionConnector) renderBlockTreeMarkdown(ctx context.Context, blockID string, depth int, childBudget *int) (string, error) {
	var all []notionBlock
	cursor := ""
	for page := 0; page < notionMaxBlockPages; page++ {
		res, err := c.fetchBlockChildrenPage(ctx, blockID, cursor, 100)
		if err != nil {
			if page == 0 {
				return "", err
			}
			break
		}
		all = append(all, res.Results...)
		if !res.HasMore || strings.TrimSpace(res.NextCursor) == "" {
			break
		}
		cursor = res.NextCursor
	}

	if depth < 2 && childBudget != nil {
		for i := range all {
			if !all[i].HasChildren || *childBudget <= 0 || ctx.Err() != nil {
				continue
			}
			if all[i].Type == "child_page" || all[i].Type == "child_database" {
				continue
			}
			*childBudget--
			sub, err := c.fetchBlockChildrenPage(ctx, all[i].ID, "", 100)
			if err == nil && len(sub.Results) > 0 {
				all[i].Children = sub.Results
			}
		}
	}
	return notionBlocksToMarkdown(all, depth), nil
}

func notionBlocksToMarkdown(blocks []notionBlock, depth int) string {
	var lines []string
	indent := strings.Repeat("  ", depth)
	for _, blk := range blocks {
		line := renderSingleNotionBlock(blk, indent)
		if line != "" {
			lines = append(lines, line)
		}
		if len(blk.Children) > 0 {
			if sub := notionBlocksToMarkdown(blk.Children, depth+1); sub != "" {
				lines = append(lines, sub)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func renderSingleNotionBlock(blk notionBlock, indent string) string {
	rawPayload := blk.Raw[blk.Type]
	switch blk.Type {
	case "heading_1", "heading_2", "heading_3":
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		prefix := "# "
		if blk.Type == "heading_2" {
			prefix = "## "
		} else if blk.Type == "heading_3" {
			prefix = "### "
		}
		return indent + prefix + txt
	case "paragraph":
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		return indent + txt
	case "bulleted_list_item":
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		return indent + "- " + txt
	case "numbered_list_item":
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		return indent + "1. " + txt
	case "to_do":
		var payload struct {
			RichText []notionRichText `json:"rich_text"`
			Checked  bool             `json:"checked"`
		}
		if json.Unmarshal(rawPayload, &payload) != nil {
			return ""
		}
		txt := extractNotionRichText(payload.RichText)
		if txt == "" {
			return ""
		}
		box := "[ ] "
		if payload.Checked {
			box = "[x] "
		}
		return indent + "- " + box + txt
	case "quote", "callout":
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		return indent + "> " + txt
	case "code":
		var payload struct {
			RichText []notionRichText `json:"rich_text"`
			Language string           `json:"language"`
		}
		if json.Unmarshal(rawPayload, &payload) != nil {
			return ""
		}
		code := extractNotionRichText(payload.RichText)
		lang := strings.TrimSpace(payload.Language)
		if lang == "plain text" {
			lang = ""
		}
		return indent + "```" + lang + "\n" + code + "\n" + indent + "```"
	case "table_row":
		var payload struct {
			Cells [][]notionRichText `json:"cells"`
		}
		if json.Unmarshal(rawPayload, &payload) != nil || len(payload.Cells) == 0 {
			return ""
		}
		cols := make([]string, 0, len(payload.Cells))
		for _, cell := range payload.Cells {
			cols = append(cols, strings.ReplaceAll(extractNotionRichText(cell), "|", "\\|"))
		}
		return indent + "| " + strings.Join(cols, " | ") + " |"
	case "divider":
		return indent + "---"
	case "toggle":
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		return indent + "- " + txt
	case "child_page", "child_database":
		var payload struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(rawPayload, &payload) == nil && strings.TrimSpace(payload.Title) != "" {
			return indent + "- " + strings.TrimSpace(payload.Title)
		}
		return ""
	default:
		txt := extractBlockRichText(rawPayload)
		if txt == "" {
			return ""
		}
		return indent + txt
	}
}

func extractBlockRichText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		RichText []notionRichText `json:"rich_text"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return extractNotionRichText(payload.RichText)
}

func (c *notionConnector) doJSON(ctx context.Context, method, pathStr string, payload any, out any) error {
	var bodyBytes []byte
	if payload != nil {
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}

	for attempt := 0; attempt <= notionMax429Retries; attempt++ {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathStr, bodyReader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.spec.Secret.Token)
		req.Header.Set("Notion-Version", notionAPIVersion)
		req.Header.Set("Accept", "application/json")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("Notion 请求失败：%s", RedactSecrets(err.Error(), c.spec.Secret))
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < notionMax429Retries {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, nativeMaxErrorBytes))
			resp.Body.Close()
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}

		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, nativeMaxErrorBytes))
			msg := parseNotionError(raw, resp.Status)
			msg = RedactSecrets(msg, c.spec.Secret)
			if resp.StatusCode == http.StatusUnauthorized {
				return apperr.New(apperr.Upstream, "Notion Integration Token 无效，请核对密钥")
			}
			if resp.StatusCode == http.StatusForbidden {
				return apperr.New(apperr.Upstream, "Notion 集成无权访问目标页面，请在页面右上角 ••• → Connections 关联该集成")
			}
			return fmt.Errorf("Notion HTTP %d: %s", resp.StatusCode, msg)
		}

		raw, err := io.ReadAll(io.LimitReader(resp.Body, nativeMaxResponseBytes))
		if err != nil {
			return fmt.Errorf("Notion %s 响应读取失败：%s", pathStr, RedactSecrets(err.Error(), c.spec.Secret))
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("Notion %s 返回无法解析：%s", pathStr, RedactSecrets(clipText(string(raw), 200), c.spec.Secret))
			}
		}
		return nil
	}
	return apperr.New(apperr.Upstream, "Notion API 触发频控（429），请稍后重试")
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if sec, err := strconv.Atoi(raw); err == nil && sec > 0 {
		d := time.Duration(sec) * time.Second
		if d > notionMaxRetryAfter {
			return notionMaxRetryAfter
		}
		return d
	}
	return time.Second
}

func parseNotionError(raw []byte, fallback string) string {
	var body struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if json.Unmarshal(raw, &body) == nil && strings.TrimSpace(body.Message) != "" {
		return strings.TrimSpace(body.Message)
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return clipText(s, 240)
	}
	return fallback
}

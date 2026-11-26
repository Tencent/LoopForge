package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	WeKnoraToolListKnowledgeBases = "list_knowledge_bases"
	WeKnoraToolSearchKnowledge    = "search_knowledge"
	WeKnoraToolListDocuments      = "list_documents"
	WeKnoraToolReadDocument       = "read_document"

	weknoraDefaultTimeout   = 60 * time.Second
	weknoraMaxErrorBytes    = 1 << 20
	weknoraMaxResponseBytes = 8 << 20
	weknoraMaxRedirects     = 5
	weknoraSearchConc       = 4
	weknoraMetaTTL          = 60 * time.Second
)

type weknoraFactory struct{}

func newWeKnoraFactory() ConnectorFactory { return weknoraFactory{} }

func (weknoraFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               domain.ConnectorTypeWeKnora,
		Name:               "WeKnora 知识库",
		Description:        "通过只读 REST API 接入个人或团队 WeKnora 实例（支持列子库、混合检索与分块原文读取）",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthAPIKey,
		Fields: []domain.ConnectorFieldSpec{
			{
				Key:         "endpoint",
				Label:       "WeKnora 服务地址",
				Kind:        "url",
				Placeholder: "https://weknora.example.com",
				Required:    true,
				Help:        "填写 WeKnora 基础地址（例如 http://host:8080，不能带查询参数）",
			},
			{
				Key:         "token",
				Label:       "API Key",
				Kind:        "secret",
				Placeholder: "填写 WeKnora API Key（编辑留空则保留原密钥）",
				Required:    true,
				Secret:      true,
				Help:        "使用 X-API-Key 请求头鉴权，保存后 AES-GCM 加密存储",
			},
		},
	}
}

func (weknoraFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, hasExistingSecret bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	normalized, err := ValidateOutboundWeKnoraURL(endpoint)
	if err != nil {
		return "", cfg, secret, err
	}
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)

	secret.AuthType = firstNonEmpty(strings.TrimSpace(secret.AuthType), domain.ConnectorAuthAPIKey)
	secret.Token = stripBearer(secret.Token)
	secret.Headers = normalizeHeaderMap(secret.Headers)
	if !secret.HasCredential() && !hasExistingSecret {
		return "", cfg, secret, apperr.New(apperr.Invalid, "请填写 WeKnora API Key")
	}
	return normalized, cfg, secret, nil
}

func (weknoraFactory) New(spec ConnectorSpec) (Connector, error) {
	base, err := ValidateOutboundWeKnoraURL(spec.Endpoint)
	if err != nil {
		return nil, err
	}
	if !spec.Secret.HasCredential() {
		return nil, apperr.New(apperr.Invalid, "WeKnora 连接器缺少 API Key 凭据")
	}
	timeout := weknoraDefaultTimeout
	if spec.Config.TimeoutSec > 0 {
		timeout = time.Duration(spec.Config.TimeoutSec) * time.Second
	}
	client := &http.Client{
		Timeout:       timeout,
		Transport:     newWeKnoraTransport(),
		CheckRedirect: checkWeKnoraRedirect,
	}
	spec.Endpoint = base
	return &weknoraConnector{
		spec:    spec,
		baseURL: base,
		http:    client,
		docMeta: map[string]docMetaEntry{},
	}, nil
}

// ValidateOutboundWeKnoraURL validates and normalizes a WeKnora base URL,
// stripping trailing slashes and an optional /api/v1 suffix.
func ValidateOutboundWeKnoraURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", apperr.New(apperr.Invalid, "WeKnora 服务地址不能为空")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", apperr.New(apperr.Invalid, "WeKnora 地址包含非法控制字符")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", apperr.New(apperr.Invalid, "WeKnora 地址无法解析，请检查格式")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", apperr.New(apperr.Invalid, "WeKnora 地址必须是 http(s)://host 形式")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", apperr.New(apperr.Invalid, "WeKnora 地址缺少主机名")
	}
	if u.User != nil {
		return "", apperr.New(apperr.Invalid, "WeKnora 地址不能内嵌账号密码")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", apperr.New(apperr.Invalid, "WeKnora 地址不能包含查询参数")
	}
	u.Fragment = ""
	s := strings.TrimRight(strings.TrimSpace(u.String()), "/")
	s = strings.TrimSuffix(s, "/api/v1")
	s = strings.TrimRight(s, "/")
	if s == "" {
		return "", apperr.New(apperr.Invalid, "WeKnora 服务地址不能为空")
	}
	if err := validateConnectorEndpointIP(s, true, "WeKnora "); err != nil {
		return "", err
	}
	return s, nil
}

func newWeKnoraTransport() *http.Transport {
	return newGuardedHTTPTransport(true, "WeKnora ")
}

func checkWeKnoraRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) > weknoraMaxRedirects {
		return fmt.Errorf("WeKnora 重定向次数超过 %d 次", weknoraMaxRedirects)
	}
	origin := via[0].URL
	if origin == nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
		return errors.New("WeKnora 拒绝跨源重定向，避免凭据泄露")
	}
	return nil
}

type kbNamesEntry struct {
	names     map[string]string
	expiresAt time.Time
}

type docMetaEntry struct {
	doc       weKnoraDocument
	expiresAt time.Time
}

type weknoraConnector struct {
	spec    ConnectorSpec
	baseURL string
	http    *http.Client

	mu        sync.Mutex
	kbNames   kbNamesEntry
	docMeta   map[string]docMetaEntry
}

func (c *weknoraConnector) Type() string { return domain.ConnectorTypeWeKnora }

func (c *weknoraConnector) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return WeKnoraReadOnlyToolDefinitions(), nil
}

// WeKnoraReadOnlyToolDefinitions returns the canonical 4 read-only tools for WeKnora.
func WeKnoraReadOnlyToolDefinitions() []mcpclient.Tool {
	readOnlyAnn := json.RawMessage(`{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false}`)
	return []mcpclient.Tool{
		{
			Name:        WeKnoraToolListKnowledgeBases,
			Description: "List accessible knowledge bases with id, name, description and document count.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolSearchKnowledge,
			Description: "Hybrid search across authorized knowledge bases and return matching passages with document ids and titles.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search question or keywords"},"knowledge_base_ids":{"type":"array","items":{"type":"string"},"description":"Optional knowledge base IDs to restrict search"},"limit":{"type":"integer","description":"Maximum passages to return (default 10, max 30)"}},"required":["query"]}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolListDocuments,
			Description: "List documents inside one authorized knowledge base.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"knowledge_base_id":{"type":"string","description":"Knowledge base ID"},"keyword":{"type":"string","description":"Optional title filter"},"page":{"type":"integer"},"page_size":{"type":"integer"}},"required":["knowledge_base_id"]}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolReadDocument,
			Description: "Read a document's text chunks in order by knowledge_id.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"knowledge_id":{"type":"string","description":"Document ID"},"offset":{"type":"integer","description":"0-based chunk offset"},"limit":{"type":"integer","description":"Chunks per page (default 20, max 100)"},"query":{"type":"string","description":"Optional case-insensitive keywords to filter chunks"}},"required":["knowledge_id"]}`),
			Annotations: readOnlyAnn,
		},
	}
}

func (c *weknoraConnector) Check(ctx context.Context) CheckResult {
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
	tools := make([]string, 0, 4)
	for _, t := range WeKnoraReadOnlyToolDefinitions() {
		tools = append(tools, t.Name)
	}
	return CheckResult{
		ConnectorID: c.spec.ID,
		OK:          true,
		LatencyMs:   latency,
		Sources:     sources,
		Tools:       tools,
	}
}

func (c *weknoraConnector) ListSources(ctx context.Context) ([]Source, error) {
	kbs, err := c.listKnowledgeBases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(kbs))
	for _, kb := range kbs {
		out = append(out, Source{
			ID:          kb.ID,
			Name:        kb.Name,
			Description: kb.Description,
			Status:      firstNonEmpty(kb.Type, "ready"),
			DocCount:    kb.KnowledgeCount,
		})
	}
	return out, nil
}

func (c *weknoraConnector) CallTool(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	started := time.Now()
	res, err := c.callToolInner(ctx, tool, args, scope)
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(started).Milliseconds()
	}
	logKnowledgeDebugCall(c.spec, "upstream_weknora", strings.TrimSpace(tool), scope, args, res, err)
	return res, err
}

func (c *weknoraConnector) callToolInner(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	switch strings.TrimSpace(tool) {
	case WeKnoraToolListKnowledgeBases:
		return c.execListKBs(ctx, scope)
	case WeKnoraToolSearchKnowledge:
		query := strings.TrimSpace(argString(args["query"]))
		if query == "" {
			return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "query 不能为空")
		}
		reqIDs := argStringList(args["knowledge_base_ids"])
		if len(reqIDs) == 0 {
			reqIDs = argStringList(args["sources"])
		}
		if len(reqIDs) == 0 {
			if single := strings.TrimSpace(argString(args["knowledge_base_id"])); single != "" {
				reqIDs = []string{single}
			}
		}
		limit := argInt(args["limit"], 10)
		return c.execSearch(ctx, query, reqIDs, limit, scope)
	case WeKnoraToolListDocuments:
		kbID := strings.TrimSpace(argString(args["knowledge_base_id"]))
		keyword := strings.TrimSpace(argString(args["keyword"]))
		page := argInt(args["page"], 1)
		pageSize := argInt(args["page_size"], 20)
		return c.execListDocs(ctx, kbID, keyword, page, pageSize, scope)
	case WeKnoraToolReadDocument:
		docID := strings.TrimSpace(argString(args["knowledge_id"]))
		if docID == "" {
			docID = strings.TrimSpace(argString(args["path"]))
		}
		offset := argInt(args["offset"], 0)
		limit := argInt(args["limit"], 20)
		query := strings.TrimSpace(argString(args["query"]))
		return c.execReadDoc(ctx, docID, offset, limit, query, scope)
	default:
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, fmt.Sprintf("知识库连接器不支持只读工具 %q", tool))
	}
}

type weKnoraKB struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Type           string `json:"type"`
	KnowledgeCount int    `json:"knowledge_count"`
}

type weKnoraDocument struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
	Type            string `json:"type"`
	FileName        string `json:"file_name"`
	FileType        string `json:"file_type"`
	ParseStatus     string `json:"parse_status"`
	ErrorMessage    string `json:"error_message,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type weKnoraSearchHit struct {
	ID                string  `json:"id"`
	Content           string  `json:"content"`
	KnowledgeID       string  `json:"knowledge_id"`
	ChunkIndex        int     `json:"chunk_index"`
	KnowledgeTitle    string  `json:"knowledge_title"`
	KnowledgeFilename string  `json:"knowledge_filename"`
	Score             float64 `json:"score"`
	StartAt           int     `json:"start_at"`
	EndAt             int     `json:"end_at"`
}

type weKnoraChunk struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	ChunkIndex int    `json:"chunk_index"`
	StartAt    int    `json:"start_at"`
	EndAt      int    `json:"end_at"`
}

func (c *weknoraConnector) execListKBs(ctx context.Context, scope SourceScope) (CallResult, error) {
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	kbs, err := c.listKnowledgeBases(cctx)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
	}
	kept := make([]weKnoraKB, 0, len(kbs))
	var targets []string
	for _, kb := range kbs {
		if !scope.Allows(kb.ID) {
			continue
		}
		kept = append(kept, kb)
		if len(targets) < maxKnowledgeReadTargets {
			if name := firstNonEmpty(kb.Name, kb.ID); name != "" {
				targets = append(targets, name)
			}
		}
	}
	status := domain.KnowledgeStatusHit
	if len(kept) == 0 {
		status = domain.KnowledgeStatusEmpty
	}
	raw, err := json.Marshal(map[string]any{
		"knowledge_bases": kept,
		"status":          status,
	})
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
	}
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(kept),
		Targets:   targets,
		Citeable:  false,
		LatencyMs: latency,
	}, nil
}

func (c *weknoraConnector) execSearch(ctx context.Context, query string, reqIDs []string, limit int, scope SourceScope) (CallResult, error) {
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

	kbNames := c.cachedKBNames(cctx)
	if len(scopedIDs) == 0 {
		for id := range kbNames {
			scopedIDs = append(scopedIDs, id)
		}
		sort.Strings(scopedIDs)
	}
	if len(scopedIDs) == 0 {
		raw, _ := json.Marshal(map[string]any{"query": query, "mode": "hybrid", "status": domain.KnowledgeStatusEmpty, "results": []any{}})
		return CallResult{Status: domain.KnowledgeStatusEmpty, Text: string(raw), Citeable: true, LatencyMs: time.Since(started).Milliseconds()}, nil
	}

	type kbHit struct {
		weKnoraSearchHit
		SourceKBID string
	}
	type kbRes struct {
		hits []kbHit
		err  error
	}
	ch := make(chan kbRes, len(scopedIDs))
	sem := make(chan struct{}, weknoraSearchConc)
	var wg sync.WaitGroup
	for _, kbID := range scopedIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-cctx.Done():
				ch <- kbRes{err: fmt.Errorf("kb %s: %w", id, cctx.Err())}
				return
			}
			var env struct {
				Success bool               `json:"success"`
				Data    []weKnoraSearchHit `json:"data"`
			}
			if err := c.doJSON(cctx, http.MethodPost, "/api/v1/knowledge-bases/"+url.PathEscape(id)+"/hybrid-search", map[string]any{
				"query_text":  query,
				"match_count": limit,
			}, &env); err != nil {
				ch <- kbRes{err: fmt.Errorf("kb %s: %w", id, err)}
				return
			}
			out := make([]kbHit, 0, len(env.Data))
			for _, h := range env.Data {
				out = append(out, kbHit{weKnoraSearchHit: h, SourceKBID: id})
			}
			ch <- kbRes{hits: out}
		}(kbID)
	}
	wg.Wait()
	close(ch)

	var all []kbHit
	var partialErrs []string
	succeeded := 0
	for r := range ch {
		if r.err != nil {
			partialErrs = append(partialErrs, r.err.Error())
			continue
		}
		succeeded++
		all = append(all, r.hits...)
	}

	latency := time.Since(started).Milliseconds()
	if succeeded == 0 && len(partialErrs) > 0 {
		warning := strings.Join(partialErrs, "；")
		return CallResult{
			Status:    domain.KnowledgeStatusUnavailable,
			Warning:   warning,
			LatencyMs: latency,
		}, apperr.New(apperr.Upstream, "检索失败："+partialErrs[0])
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		if all[i].SourceKBID != all[j].SourceKBID {
			return all[i].SourceKBID < all[j].SourceKBID
		}
		if all[i].KnowledgeID != all[j].KnowledgeID {
			return all[i].KnowledgeID < all[j].KnowledgeID
		}
		return all[i].ChunkIndex < all[j].ChunkIndex
	})
	if len(all) > limit {
		all = all[:limit]
	}

	type hitPayload struct {
		Source     string  `json:"source"`
		SourceName string  `json:"source_name,omitempty"`
		Path       string  `json:"path"`
		Title      string  `json:"title,omitempty"`
		FileName   string  `json:"file_name,omitempty"`
		ChunkIndex int     `json:"chunk_index"`
		StartLine  int     `json:"start_line"`
		EndLine    int     `json:"end_line"`
		Snippet    string  `json:"snippet"`
		Score      float64 `json:"score"`
	}
	results := make([]hitPayload, 0, len(all))
	evidences := make([]domain.KnowledgeEvidence, 0, len(all))
	var targets []string
	seenTarget := map[string]bool{}
	for _, h := range all {
		sourceName := kbNames[h.SourceKBID]
		fileName := strings.TrimSpace(h.KnowledgeFilename)
		title := firstNonEmpty(h.KnowledgeTitle, fileName, h.KnowledgeID)
		results = append(results, hitPayload{
			Source:     h.SourceKBID,
			SourceName: sourceName,
			Path:       h.KnowledgeID,
			Title:      title,
			FileName:   fileName,
			ChunkIndex: h.ChunkIndex,
			StartLine:  h.StartAt,
			EndLine:    h.EndAt,
			Snippet:    h.Content,
			Score:      h.Score,
		})
		ev := domain.KnowledgeEvidence{
			ConnectionID: c.spec.ID,
			SourceID:     h.SourceKBID,
			SourceName:   clipCatalogText(sourceName, 120),
			DocumentID:   h.KnowledgeID,
			Title:        clipCatalogText(title, 120),
			FileName:     clipCatalogText(fileName, 120),
			Locator:      fmt.Sprintf("#chunk-%d", h.ChunkIndex),
		}
		evidences = append(evidences, ev)
		if lbl := clipCatalogText(firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID), 120); lbl != "" && !seenTarget[lbl] && len(targets) < maxKnowledgeReadTargets {
			seenTarget[lbl] = true
			targets = append(targets, lbl)
		}
	}

	status := domain.KnowledgeStatusHit
	warning := ""
	if len(partialErrs) > 0 {
		status = domain.KnowledgeStatusPartial
		warning = "部分知识库检索失败，结果可能不完整：" + strings.Join(partialErrs, "；")
	} else if len(results) == 0 {
		status = domain.KnowledgeStatusEmpty
	}
	body := map[string]any{
		"query":   query,
		"mode":    "hybrid",
		"status":  status,
		"results": results,
	}
	if warning != "" {
		body["warning"] = warning
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
	}
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(results),
		Warning:   warning,
		Targets:   targets,
		Evidences: evidences,
		Citeable:  true,
		LatencyMs: latency,
	}, nil
}

func (c *weknoraConnector) execListDocs(ctx context.Context, kbID, keyword string, page, pageSize int, scope SourceScope) (CallResult, error) {
	if kbID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_base_id 不能为空")
	}
	if !scope.Allows(kbID) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("知识库来源 %q 不在当前授权范围内", kbID))
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	if keyword != "" {
		q.Set("keyword", keyword)
	}
	var env struct {
		Success bool              `json:"success"`
		Data    []weKnoraDocument `json:"data"`
		Total   int64             `json:"total"`
	}
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(kbID) + "/knowledge?" + q.Encode()
	if err := c.doJSON(cctx, http.MethodGet, endpoint, nil, &env); err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}
	if env.Data == nil {
		env.Data = []weKnoraDocument{}
	}
	status := domain.KnowledgeStatusHit
	if len(env.Data) == 0 {
		status = domain.KnowledgeStatusEmpty
	}
	var targets []string
	for _, doc := range env.Data {
		if len(targets) >= maxKnowledgeReadTargets {
			break
		}
		if lbl := clipCatalogText(firstNonEmpty(doc.FileName, doc.Title, doc.ID), 120); lbl != "" {
			targets = append(targets, lbl)
		}
	}
	raw, err := json.Marshal(map[string]any{
		"documents": env.Data,
		"total":     env.Total,
		"page":      page,
		"pageSize":  pageSize,
	})
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(env.Data),
		Targets:   targets,
		Citeable:  false,
		LatencyMs: time.Since(started).Milliseconds(),
	}, err
}

func (c *weknoraConnector) execReadDoc(ctx context.Context, docID string, offset, limit int, query string, scope SourceScope) (CallResult, error) {
	if docID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_id 不能为空")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	docMeta, metaErr := c.getDocMeta(cctx, docID)
	if strings.TrimSpace(scope.Mode) == "allowlist" {
		if metaErr != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, metaErr
		}
		if !scope.Allows(docMeta.KnowledgeBaseID) {
			return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("文档 %q 所属知识库 %q 不在当前授权范围内", docID, docMeta.KnowledgeBaseID))
		}
	}

	startPage := (offset / limit) + 1
	inPageOffset := offset % limit
	chunks1, total, err := c.listChunks(cctx, docID, startPage, limit)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}
	var collected []weKnoraChunk
	if inPageOffset < len(chunks1) {
		collected = append(collected, chunks1[inPageOffset:]...)
	}
	if len(collected) < limit && len(chunks1) == limit && (total <= 0 || int64(offset+len(collected)) < total) {
		if chunks2, _, err2 := c.listChunks(cctx, docID, startPage+1, limit); err2 == nil && len(chunks2) > 0 {
			need := limit - len(collected)
			if need > len(chunks2) {
				need = len(chunks2)
			}
			collected = append(collected, chunks2[:need]...)
		}
	}
	if len(collected) > limit {
		collected = collected[:limit]
	}
	if q := strings.ToLower(query); q != "" {
		words := strings.Fields(q)
		filtered := collected[:0]
		for _, ch := range collected {
			low := strings.ToLower(ch.Content)
			matched := true
			for _, w := range words {
				if !strings.Contains(low, w) {
					matched = false
					break
				}
			}
			if matched {
				filtered = append(filtered, ch)
			}
		}
		collected = filtered
	}
	if collected == nil {
		collected = []weKnoraChunk{}
	}

	status := domain.KnowledgeStatusHit
	if len(collected) == 0 {
		status = domain.KnowledgeStatusEmpty
	}

	kbName := ""
	if kbID := strings.TrimSpace(docMeta.KnowledgeBaseID); kbID != "" && len(collected) > 0 {
		kbName = c.cachedKBNames(cctx)[kbID]
	}
	title := firstNonEmpty(docMeta.Title, docMeta.FileName, docID)
	fileName := strings.TrimSpace(docMeta.FileName)

	payload := map[string]any{
		"knowledge_id": docID,
		"total":        total,
		"offset":       offset,
		"limit":        limit,
		"chunks":       collected,
		"title":        title,
	}
	if fileName != "" {
		payload["file_name"] = fileName
	}
	if docMeta.KnowledgeBaseID != "" {
		payload["knowledge_base_id"] = docMeta.KnowledgeBaseID
	}
	if kbName != "" {
		payload["knowledge_base_name"] = kbName
	}

	var evidences []domain.KnowledgeEvidence
	var targets []string
	if len(collected) > 0 {
		locator := summarizeWeKnoraChunkRange(collected)
		ev := domain.KnowledgeEvidence{
			ConnectionID: c.spec.ID,
			SourceID:     strings.TrimSpace(docMeta.KnowledgeBaseID),
			SourceName:   clipCatalogText(kbName, 120),
			DocumentID:   docID,
			Title:        clipCatalogText(title, 120),
			FileName:     clipCatalogText(fileName, 120),
			Locator:      locator,
		}
		evidences = append(evidences, ev)
		if lbl := clipCatalogText(firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID), 120); lbl != "" {
			targets = append(targets, lbl)
		}
	}

	raw, err := json.Marshal(payload)
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(collected),
		Targets:   targets,
		Evidences: evidences,
		Citeable:  true,
		LatencyMs: time.Since(started).Milliseconds(),
	}, err
}

func summarizeWeKnoraChunkRange(chunks []weKnoraChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	minIdx, maxIdx := chunks[0].ChunkIndex, chunks[0].ChunkIndex
	for _, ch := range chunks[1:] {
		if ch.ChunkIndex < minIdx {
			minIdx = ch.ChunkIndex
		}
		if ch.ChunkIndex > maxIdx {
			maxIdx = ch.ChunkIndex
		}
	}
	if minIdx == maxIdx {
		return fmt.Sprintf("#chunk-%d", minIdx)
	}
	return fmt.Sprintf("#chunk-%d..#chunk-%d", minIdx, maxIdx)
}

func (c *weknoraConnector) cachedKBNames(ctx context.Context) map[string]string {
	now := time.Now()
	c.mu.Lock()
	if c.kbNames.names != nil && now.Before(c.kbNames.expiresAt) {
		out := c.kbNames.names
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()

	kbs, err := c.listKnowledgeBases(ctx)
	if err != nil {
		return map[string]string{}
	}
	names := make(map[string]string, len(kbs))
	for _, kb := range kbs {
		if id := strings.TrimSpace(kb.ID); id != "" {
			names[id] = strings.TrimSpace(kb.Name)
		}
	}
	c.mu.Lock()
	c.kbNames = kbNamesEntry{names: names, expiresAt: now.Add(weknoraMetaTTL)}
	c.mu.Unlock()
	return names
}

func (c *weknoraConnector) getDocMeta(ctx context.Context, docID string) (weKnoraDocument, error) {
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.docMeta[docID]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.doc, nil
	}
	c.mu.Unlock()

	var env struct {
		Success bool            `json:"success"`
		Data    weKnoraDocument `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/knowledge/"+url.PathEscape(docID), nil, &env); err != nil {
		return weKnoraDocument{}, err
	}
	c.mu.Lock()
	if len(c.docMeta) >= 256 {
		c.docMeta = map[string]docMetaEntry{}
	}
	c.docMeta[docID] = docMetaEntry{doc: env.Data, expiresAt: now.Add(weknoraMetaTTL)}
	c.mu.Unlock()
	return env.Data, nil
}

func (c *weknoraConnector) listKnowledgeBases(ctx context.Context) ([]weKnoraKB, error) {
	var env struct {
		Success bool        `json:"success"`
		Data    []weKnoraKB `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/knowledge-bases", nil, &env); err != nil {
		return nil, err
	}
	if env.Data == nil {
		return []weKnoraKB{}, nil
	}
	return env.Data, nil
}

func (c *weknoraConnector) listChunks(ctx context.Context, docID string, page, pageSize int) ([]weKnoraChunk, int64, error) {
	endpoint := fmt.Sprintf("/api/v1/chunks/%s?page=%d&page_size=%d", url.PathEscape(docID), page, pageSize)
	var env struct {
		Success bool           `json:"success"`
		Data    []weKnoraChunk `json:"data"`
		Total   int64          `json:"total"`
	}
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &env); err != nil {
		return nil, 0, err
	}
	if env.Data == nil {
		env.Data = []weKnoraChunk{}
	}
	return env.Data, env.Total, nil
}

func (c *weknoraConnector) doJSON(ctx context.Context, method, pathStr string, payload any, out any) error {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathStr, bodyReader)
	if err != nil {
		return err
	}
	for k, v := range c.spec.Config.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.spec.Secret.Headers {
		req.Header.Set(k, v)
	}
	if tok := strings.TrimSpace(c.spec.Secret.Token); tok != "" {
		req.Header.Set("X-API-Key", tok)
	}
	if tid := strings.TrimSpace(c.spec.Config.Extra["tenantId"]); tid != "" {
		req.Header.Set("X-Tenant-ID", tid)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New(RedactSecrets(err.Error(), c.spec.Secret))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxErrorBytes))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("WeKnora HTTP %d: %s", resp.StatusCode, RedactSecrets(parseWeKnoraErrorMessage(resp.StatusCode, raw), c.spec.Secret))
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("WeKnora %s 响应读取失败：%w", pathStr, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("WeKnora %s 返回无法解析：%s", pathStr, clipText(string(raw), 200))
	}
	return nil
}

func parseWeKnoraErrorMessage(status int, raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return http.StatusText(status)
	}
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(text), &env) == nil {
		if len(env.Error) > 0 && !bytes.Equal(bytes.TrimSpace(env.Error), []byte("null")) {
			var str string
			if json.Unmarshal(env.Error, &str) == nil && strings.TrimSpace(str) != "" {
				return clipText(str, 300)
			}
			var obj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(env.Error, &obj) == nil && strings.TrimSpace(obj.Message) != "" {
				return clipText(obj.Message, 300)
			}
		}
		if strings.TrimSpace(env.Message) != "" {
			return clipText(env.Message, 300)
		}
	}
	return clipText(text, 200)
}

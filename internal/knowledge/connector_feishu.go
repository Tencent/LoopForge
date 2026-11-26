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
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	feishuDefaultBaseURL   = "https://open.feishu.cn"
	feishuTATRefreshLead   = 5 * time.Minute
	feishuDefaultTATExpire = 7200
	feishuNodeCacheTTL     = 5 * time.Minute
	feishuMaxTreeDepth     = 4
	feishuMaxSpaceNodes    = 500
	feishuSearchFetchLimit = 8
	feishuSearchFetchConc  = 4
)

type feishuFactory struct{}

func newFeishuFactory() ConnectorFactory { return feishuFactory{} }

func (feishuFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               domain.ConnectorTypeFeishu,
		Name:               "飞书知识库 / 云文档",
		Description:        "通过飞书企业自建应用（App ID + App Secret）自动续期读取已授权知识空间与新版云文档",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthHeaders,
		Fields: []domain.ConnectorFieldSpec{
			{
				Key:         "endpoint",
				Label:       "飞书开放平台地址（可选）",
				Kind:        "url",
				Placeholder: feishuDefaultBaseURL,
				Required:    false,
				Help:        "默认 https://open.feishu.cn（国际版 Lark 请填 https://open.larksuite.com）",
			},
			{
				Key:         "appId",
				Label:       "App ID",
				Kind:        "text",
				Placeholder: "cli_a58291...",
				Required:    true,
				Help:        "飞书开放平台企业自建应用的 App ID",
			},
			{
				Key:         "token",
				Label:       "App Secret",
				Kind:        "secret",
				Placeholder: "填写自建应用 App Secret（加密存储，自动换取并续期访问令牌）",
				Required:    true,
				Secret:      true,
				Help:        "开启应用机器人能力并开通 wiki:wiki:readonly、docx:document:readonly 后，将机器人添加为目标知识空间成员",
			},
		},
	}
}

func (feishuFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, hasExistingSecret bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	normalized, err := validateNativeBaseURL(endpoint, feishuDefaultBaseURL, "飞书")
	if err != nil {
		return "", cfg, secret, err
	}
	if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 300 {
		return "", cfg, secret, apperr.New(apperr.Invalid, "超时时间须在 0~300 秒之间")
	}
	cfg.Headers = normalizeHeaderMap(cfg.Headers)
	cfg.DefaultSources = CleanList(cfg.DefaultSources)
	cfg.Extra = normalizeExtraMap(cfg.Extra)

	appID := strings.TrimSpace(cfg.Extra["appId"])
	if appID == "" {
		return "", cfg, secret, apperr.New(apperr.Invalid, "请填写飞书自建应用 App ID")
	}

	secret.AuthType = firstNonEmpty(strings.TrimSpace(secret.AuthType), domain.ConnectorAuthHeaders)
	secret.Token = stripTokenPrefix(secret.Token)
	secret.Headers = normalizeHeaderMap(secret.Headers)
	appSecret := firstNonEmpty(secret.Token, secret.Headers["appSecret"])
	if appSecret == "" && !hasExistingSecret {
		return "", cfg, secret, apperr.New(apperr.Invalid, "请填写飞书自建应用 App Secret")
	}
	return normalized, cfg, secret, nil
}

func (feishuFactory) New(spec ConnectorSpec) (Connector, error) {
	base, err := validateNativeBaseURL(spec.Endpoint, feishuDefaultBaseURL, "飞书")
	if err != nil {
		return nil, err
	}
	appID := strings.TrimSpace(spec.Config.Extra["appId"])
	if appID == "" {
		return nil, apperr.New(apperr.Invalid, "飞书连接器缺少 App ID")
	}
	appSecret := firstNonEmpty(stripTokenPrefix(spec.Secret.Token), spec.Secret.Headers["appSecret"])
	if appSecret == "" {
		return nil, apperr.New(apperr.Invalid, "飞书连接器缺少 App Secret 凭据")
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
	return &feishuConnector{
		spec:       spec,
		baseURL:    base,
		appID:      appID,
		appSecret:  appSecret,
		http:       client,
		spaceNodes: map[string]feishuSpaceNodesCache{},
	}, nil
}

type feishuWikiNode struct {
	SpaceID     string `json:"space_id"`
	SpaceName   string `json:"space_name,omitempty"`
	NodeToken   string `json:"node_token"`
	ObjToken    string `json:"obj_token"`
	ObjType     string `json:"obj_type"`
	Title       string `json:"title"`
	PathTitle   string `json:"path_title,omitempty"`
	ObjEditTime string `json:"obj_edit_time,omitempty"`
	HasChild    bool   `json:"has_child,omitempty"`
}

type feishuSpaceNodesCache struct {
	nodes     []feishuWikiNode
	expiresAt time.Time
}

type feishuConnector struct {
	spec      ConnectorSpec
	baseURL   string
	appID     string
	appSecret string
	http      *http.Client

	tatMu        sync.Mutex
	tat          string
	tatExpiresAt time.Time

	cacheMu    sync.Mutex
	spaceNodes map[string]feishuSpaceNodesCache
}

func (c *feishuConnector) Type() string { return domain.ConnectorTypeFeishu }

func (c *feishuConnector) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return nativeReadOnlyToolDefinitions(), nil
}

func (c *feishuConnector) redact(text string) string {
	c.tatMu.Lock()
	tat := c.tat
	c.tatMu.Unlock()
	return c.redactWithTAT(text, tat)
}

func (c *feishuConnector) redactWithTAT(text, tat string) string {
	text = RedactSecrets(text, c.spec.Secret)
	if c.appSecret != "" {
		text = strings.ReplaceAll(text, c.appSecret, "***")
	}
	if tat != "" {
		text = strings.ReplaceAll(text, tat, "***")
	}
	return text
}

func (c *feishuConnector) getTAT(ctx context.Context, forceRefresh bool) (string, error) {
	c.tatMu.Lock()
	if !forceRefresh && c.tat != "" && time.Until(c.tatExpiresAt) > feishuTATRefreshLead {
		tok := c.tat
		c.tatMu.Unlock()
		return tok, nil
	}
	prevTAT := c.tat
	c.tatMu.Unlock()

	bodyBytes, err := json.Marshal(map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("飞书换取 tenant_access_token 失败：%s", c.redactWithTAT(err.Error(), prevTAT))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, nativeMaxErrorBytes))
	if err != nil {
		return "", fmt.Errorf("飞书令牌响应读取失败：%s", c.redactWithTAT(err.Error(), prevTAT))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("飞书换取令牌 HTTP %d: %s", resp.StatusCode, c.redactWithTAT(clipText(strings.TrimSpace(string(raw)), 240), prevTAT))
	}
	var env struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("飞书令牌响应无法解析：%s", c.redactWithTAT(clipText(string(raw), 200), prevTAT))
	}
	if env.Code != 0 || strings.TrimSpace(env.TenantAccessToken) == "" {
		return "", fmt.Errorf("飞书自建应用鉴权失败（code=%d）：%s", env.Code, c.redactWithTAT(firstNonEmpty(env.Msg, "请核对 App ID 与 App Secret"), prevTAT))
	}
	expireSec := env.Expire
	if expireSec <= 0 {
		expireSec = feishuDefaultTATExpire
	}
	newTAT := strings.TrimSpace(env.TenantAccessToken)
	c.tatMu.Lock()
	c.tat = newTAT
	c.tatExpiresAt = time.Now().Add(time.Duration(expireSec) * time.Second)
	c.tatMu.Unlock()
	return newTAT, nil
}

func (c *feishuConnector) doAPI(ctx context.Context, method, pathStr string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.getTAT(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathStr, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("飞书请求失败：%s", c.redact(err.Error()))
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, nativeMaxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("飞书 %s 响应读取失败：%s", pathStr, c.redact(readErr.Error()))
		}

		var env struct {
			Code int             `json:"code"`
			Msg  string          `json:"msg"`
			Data json.RawMessage `json:"data"`
		}
		unmarshalErr := json.Unmarshal(raw, &env)
		if unmarshalErr == nil && (env.Code == 99991663 || env.Code == 99991661) && attempt == 0 {
			continue
		}
		if unmarshalErr == nil && (env.Code == 1770032 || env.Code == 131006) {
			return apperr.New(apperr.Forbidden, fmt.Sprintf("飞书应用无权访问该知识库或文档（code=%d），请在飞书知识库「设置 → 成员设置」中添加该自建应用机器人为可阅读成员", env.Code))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := c.redact(firstNonEmpty(env.Msg, clipText(strings.TrimSpace(string(raw)), 240), resp.Status))
			return fmt.Errorf("飞书 HTTP %d: %s", resp.StatusCode, msg)
		}
		if unmarshalErr != nil {
			return fmt.Errorf("飞书 %s 返回无法解析：%s", pathStr, c.redact(clipText(string(raw), 200)))
		}
		if env.Code != 0 {
			return fmt.Errorf("飞书接口返回错误（code=%d）：%s", env.Code, c.redact(firstNonEmpty(env.Msg, "未知错误")))
		}
		if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("飞书 %s 数据解析失败：%s", pathStr, c.redact(err.Error()))
			}
		}
		return nil
	}
	return apperr.New(apperr.Upstream, "飞书访问令牌失效且刷新重试失败")
}

func (c *feishuConnector) Check(ctx context.Context) CheckResult {
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	started := time.Now()
	sources, err := c.ListSources(cctx)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return CheckResult{
			ConnectorID: c.spec.ID,
			OK:          false,
			Error:       c.redact(err.Error()),
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

func (c *feishuConnector) ListSources(ctx context.Context) ([]Source, error) {
	var out []Source
	pageToken := ""
	for page := 0; page < 5; page++ {
		q := url.Values{}
		q.Set("page_size", "50")
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		var data struct {
			Items []struct {
				SpaceID     string `json:"space_id"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"items"`
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		}
		if err := c.doAPI(ctx, http.MethodGet, "/open-apis/wiki/v2/spaces?"+q.Encode(), &data); err != nil {
			return nil, err
		}
		for _, item := range data.Items {
			if strings.TrimSpace(item.SpaceID) == "" {
				continue
			}
			out = append(out, Source{
				ID:          item.SpaceID,
				Name:        firstNonEmpty(item.Name, item.SpaceID),
				Description: strings.TrimSpace(item.Description),
				Status:      "ready",
			})
		}
		if !data.HasMore || strings.TrimSpace(data.PageToken) == "" {
			break
		}
		pageToken = data.PageToken
	}
	return out, nil
}

func (c *feishuConnector) listSpaceNodesCached(ctx context.Context, spaceID, spaceName string) ([]feishuWikiNode, error) {
	now := time.Now()
	c.cacheMu.Lock()
	if entry, ok := c.spaceNodes[spaceID]; ok && now.Before(entry.expiresAt) {
		nodes := append([]feishuWikiNode(nil), entry.nodes...)
		c.cacheMu.Unlock()
		return nodes, nil
	}
	c.cacheMu.Unlock()

	type bfsItem struct {
		parentToken string
		prefixTitle string
		depth       int
	}
	queue := []bfsItem{{parentToken: "", prefixTitle: "", depth: 1}}
	var collected []feishuWikiNode
	seen := map[string]bool{}

	for len(queue) > 0 && len(collected) < feishuMaxSpaceNodes && ctx.Err() == nil {
		cur := queue[0]
		queue = queue[1:]

		pageToken := ""
		for page := 0; page < 10 && len(collected) < feishuMaxSpaceNodes && ctx.Err() == nil; page++ {
			q := url.Values{}
			q.Set("page_size", "50")
			if cur.parentToken != "" {
				q.Set("parent_node_token", cur.parentToken)
			}
			if pageToken != "" {
				q.Set("page_token", pageToken)
			}
			var data struct {
				Items     []feishuWikiNode `json:"items"`
				HasMore   bool             `json:"has_more"`
				PageToken string           `json:"page_token"`
			}
			endpoint := "/open-apis/wiki/v2/spaces/" + url.PathEscape(spaceID) + "/nodes?" + q.Encode()
			if err := c.doAPI(ctx, http.MethodGet, endpoint, &data); err != nil {
				if len(collected) == 0 && cur.depth == 1 {
					return nil, err
				}
				break
			}
			for _, item := range data.Items {
				if len(collected) >= feishuMaxSpaceNodes {
					break
				}
				if item.NodeToken == "" || seen[item.NodeToken] {
					continue
				}
				seen[item.NodeToken] = true
				item.SpaceID = firstNonEmpty(item.SpaceID, spaceID)
				item.SpaceName = spaceName
				item.Title = firstNonEmpty(item.Title, item.NodeToken)
				if cur.prefixTitle != "" {
					item.PathTitle = cur.prefixTitle + " / " + item.Title
				} else {
					item.PathTitle = item.Title
				}
				collected = append(collected, item)
				if item.HasChild && cur.depth < feishuMaxTreeDepth {
					queue = append(queue, bfsItem{
						parentToken: item.NodeToken,
						prefixTitle: item.PathTitle,
						depth:       cur.depth + 1,
					})
				}
			}
			if !data.HasMore || strings.TrimSpace(data.PageToken) == "" {
				break
			}
			pageToken = data.PageToken
		}
	}

	c.cacheMu.Lock()
	c.spaceNodes[spaceID] = feishuSpaceNodesCache{
		nodes:     collected,
		expiresAt: time.Now().Add(feishuNodeCacheTTL),
	}
	c.cacheMu.Unlock()
	return append([]feishuWikiNode(nil), collected...), nil
}

func (c *feishuConnector) lookupCachedNode(id string) (feishuWikiNode, bool) {
	now := time.Now()
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	for _, entry := range c.spaceNodes {
		if !now.Before(entry.expiresAt) {
			continue
		}
		for _, n := range entry.nodes {
			if n.NodeToken == id || n.ObjToken == id {
				return n, true
			}
		}
	}
	return feishuWikiNode{}, false
}

func (c *feishuConnector) CallTool(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	started := time.Now()
	res, err := c.callToolInner(ctx, tool, args, scope)
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(started).Milliseconds()
	}
	logKnowledgeDebugCall(c.spec, "upstream_feishu", strings.TrimSpace(tool), scope, args, res, err)
	return res, err
}

func (c *feishuConnector) callToolInner(ctx context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
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
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, fmt.Sprintf("飞书连接器不支持只读工具 %q", tool))
	}
}

func (c *feishuConnector) execListDocs(ctx context.Context, kbID, keyword string, page, pageSize int, scope SourceScope) (CallResult, error) {
	if kbID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_base_id 不能为空")
	}
	if !scope.Allows(kbID) {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("知识库来源 %q 不在当前授权范围内", kbID))
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	nodes, err := c.listSpaceNodesCached(cctx, kbID, "")
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	type docItem struct {
		ID              string `json:"id"`
		KnowledgeID     string `json:"knowledge_id"`
		KnowledgeBaseID string `json:"knowledge_base_id"`
		ObjToken        string `json:"obj_token,omitempty"`
		ObjType         string `json:"obj_type,omitempty"`
		Title           string `json:"title"`
		PathTitle       string `json:"path_title,omitempty"`
	}
	kwLow := strings.ToLower(keyword)
	filtered := make([]docItem, 0, len(nodes))
	for _, n := range nodes {
		if kwLow != "" && !strings.Contains(strings.ToLower(n.Title), kwLow) && !strings.Contains(strings.ToLower(n.PathTitle), kwLow) {
			continue
		}
		filtered = append(filtered, docItem{
			ID:              n.NodeToken,
			KnowledgeID:     n.NodeToken,
			KnowledgeBaseID: kbID,
			ObjToken:        n.ObjToken,
			ObjType:         n.ObjType,
			Title:           n.Title,
			PathTitle:       n.PathTitle,
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

func (c *feishuConnector) execSearch(ctx context.Context, query string, reqIDs []string, limit int, scope SourceScope) (CallResult, error) {
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

	sources, err := c.ListSources(cctx)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}
	spaceNames := make(map[string]string, len(sources))
	targetSpaces := make([]string, 0, len(sources))
	for _, s := range sources {
		spaceNames[s.ID] = s.Name
		if !scope.Allows(s.ID) {
			continue
		}
		if len(scopedIDs) > 0 && !slices.Contains(scopedIDs, s.ID) {
			continue
		}
		targetSpaces = append(targetSpaces, s.ID)
	}
	if len(targetSpaces) == 0 {
		raw, _ := json.Marshal(map[string]any{"query": query, "mode": "wiki_scan", "status": domain.KnowledgeStatusEmpty, "results": []any{}})
		return CallResult{Status: domain.KnowledgeStatusEmpty, Text: string(raw), Citeable: true, LatencyMs: time.Since(started).Milliseconds()}, nil
	}

	var allNodes []feishuWikiNode
	for _, sid := range targetSpaces {
		nodes, nErr := c.listSpaceNodesCached(cctx, sid, spaceNames[sid])
		if nErr != nil {
			continue
		}
		allNodes = append(allNodes, nodes...)
	}

	words := strings.Fields(strings.ToLower(query))
	type scoredNode struct {
		node  feishuWikiNode
		score int
	}
	scored := make([]scoredNode, 0, len(allNodes))
	for _, n := range allNodes {
		if n.ObjToken == "" {
			continue
		}
		score := 0
		titleLow := strings.ToLower(n.Title)
		pathLow := strings.ToLower(n.PathTitle)
		for _, w := range words {
			if strings.Contains(titleLow, w) {
				score += 3
			} else if strings.Contains(pathLow, w) {
				score += 1
			}
		}
		scored = append(scored, scoredNode{node: n, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].node.ObjEditTime > scored[j].node.ObjEditTime
	})

	fetchN := min(len(scored), feishuSearchFetchLimit)
	type fetchedHit struct {
		node    feishuWikiNode
		score   int
		snippet string
	}
	hitsCh := make(chan fetchedHit, fetchN)
	sem := make(chan struct{}, feishuSearchFetchConc)
	var wg sync.WaitGroup

	for i := 0; i < fetchN; i++ {
		cand := scored[i]
		// Only fetch body for title matches or docx documents as fallback.
		if cand.score == 0 && cand.node.ObjType != "" && cand.node.ObjType != "docx" {
			continue
		}
		wg.Add(1)
		go func(item scoredNode) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-cctx.Done():
				return
			}
			content, rErr := c.fetchDocxRawContent(cctx, item.node.ObjToken)
			if rErr != nil {
				if item.score > 0 {
					hitsCh <- fetchedHit{node: item.node, score: item.score, snippet: item.node.PathTitle}
				}
				return
			}
			bodyLow := strings.ToLower(content)
			bodyMatches := 0
			for _, w := range words {
				if strings.Contains(bodyLow, w) {
					bodyMatches++
				}
			}
			totalScore := item.score + bodyMatches*2
			if totalScore == 0 {
				return
			}
			hitsCh <- fetchedHit{
				node:    item.node,
				score:   totalScore,
				snippet: extractKeywordSnippet(content, query, 280),
			}
		}(cand)
	}
	wg.Wait()
	close(hitsCh)

	var matched []fetchedHit
	for h := range hitsCh {
		matched = append(matched, h)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score > matched[j].score })
	if len(matched) > limit {
		matched = matched[:limit]
	}

	type hitPayload struct {
		Source      string `json:"source"`
		SourceName  string `json:"source_name,omitempty"`
		KnowledgeID string `json:"knowledge_id"`
		ObjToken    string `json:"obj_token,omitempty"`
		Title       string `json:"title,omitempty"`
		Snippet     string `json:"snippet"`
	}
	results := make([]hitPayload, 0, len(matched))
	evidences := make([]domain.KnowledgeEvidence, 0, len(matched))
	var targets []string
	seenTarget := map[string]bool{}

	for _, m := range matched {
		title := firstNonEmpty(m.node.PathTitle, m.node.Title, m.node.NodeToken)
		spaceName := firstNonEmpty(m.node.SpaceName, spaceNames[m.node.SpaceID])
		results = append(results, hitPayload{
			Source:      m.node.SpaceID,
			SourceName:  spaceName,
			KnowledgeID: m.node.NodeToken,
			ObjToken:    m.node.ObjToken,
			Title:       title,
			Snippet:     m.snippet,
		})
		ev := domain.KnowledgeEvidence{
			ConnectionID: c.spec.ID,
			SourceID:     m.node.SpaceID,
			SourceName:   clipCatalogText(spaceName, 120),
			DocumentID:   m.node.NodeToken,
			Title:        clipCatalogText(title, 120),
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
		"mode":    "wiki_scan",
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

func (c *feishuConnector) execReadDoc(ctx context.Context, docID string, offset, limit int, query string, scope SourceScope) (CallResult, error) {
	if docID == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "knowledge_id 不能为空")
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var spaceID, spaceName, objToken, title, resolvedDocID string
	resolvedDocID = docID

	// Support optional "space_id/obj_token" shorthand without colliding with "::".
	if s, t, ok := strings.Cut(docID, "/"); ok && strings.TrimSpace(s) != "" && strings.TrimSpace(t) != "" {
		spaceID = strings.TrimSpace(s)
		objToken = strings.TrimSpace(t)
		if strings.TrimSpace(scope.Mode) == "allowlist" {
			if !scope.Allows(spaceID) {
				return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("文档 %q 所属飞书知识空间 %q 不在当前授权范围内", docID, spaceID))
			}
			nodes, err := c.listSpaceNodesCached(cctx, spaceID, "")
			if err != nil {
				return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
			}
			matched := false
			for _, n := range nodes {
				if n.ObjToken == objToken || n.NodeToken == objToken {
					matched = true
					spaceName = firstNonEmpty(n.SpaceName, spaceName)
					objToken = firstNonEmpty(n.ObjToken, objToken)
					title = firstNonEmpty(n.PathTitle, n.Title)
					resolvedDocID = firstNonEmpty(n.NodeToken, docID)
					break
				}
			}
			if !matched {
				return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("文档 %q 不属于已授权的飞书知识空间 %q", objToken, spaceID))
			}
		}
	} else if cached, ok := c.lookupCachedNode(docID); ok {
		spaceID = cached.SpaceID
		spaceName = cached.SpaceName
		objToken = cached.ObjToken
		title = firstNonEmpty(cached.PathTitle, cached.Title)
		resolvedDocID = firstNonEmpty(cached.NodeToken, docID)
	} else {
		var nodeResp struct {
			Node struct {
				SpaceID   string `json:"space_id"`
				NodeToken string `json:"node_token"`
				ObjToken  string `json:"obj_token"`
				ObjType   string `json:"obj_type"`
				Title     string `json:"title"`
			} `json:"node"`
		}
		if err := c.doAPI(cctx, http.MethodGet, "/open-apis/wiki/v2/spaces/get_node?token="+url.QueryEscape(docID), &nodeResp); err == nil && strings.TrimSpace(nodeResp.Node.ObjToken) != "" {
			spaceID = strings.TrimSpace(nodeResp.Node.SpaceID)
			objToken = strings.TrimSpace(nodeResp.Node.ObjToken)
			title = strings.TrimSpace(nodeResp.Node.Title)
			resolvedDocID = firstNonEmpty(nodeResp.Node.NodeToken, docID)
		} else if strings.TrimSpace(scope.Mode) != "allowlist" {
			// When scope is "all", allow direct docx obj_token reading.
			objToken = docID
		} else if err != nil {
			return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
		}
	}

	if strings.TrimSpace(scope.Mode) == "allowlist" {
		if spaceID == "" || !scope.Allows(spaceID) {
			return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Forbidden, fmt.Sprintf("文档 %q 所属飞书知识空间 %q 不在当前授权范围内", docID, spaceID))
		}
	}
	if objToken == "" {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.NotFound, "无法解析飞书文档标识")
	}

	content, err := c.fetchDocxRawContent(cctx, objToken)
	if err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}

	return buildReadDocumentResult(nativeReadDocParams{
		ConnectionID: c.spec.ID,
		SourceID:     spaceID,
		SourceName:   spaceName,
		DocumentID:   resolvedDocID,
		Title:        firstNonEmpty(title, resolvedDocID),
		Content:      content,
		Offset:       offset,
		Limit:        limit,
		Query:        query,
		Started:      started,
	})
}

func (c *feishuConnector) fetchDocxRawContent(ctx context.Context, objToken string) (string, error) {
	var data struct {
		Content string `json:"content"`
	}
	if err := c.doAPI(ctx, http.MethodGet, "/open-apis/docx/v1/documents/"+url.PathEscape(objToken)+"/raw_content?lang=0", &data); err != nil {
		return "", err
	}
	return data.Content, nil
}

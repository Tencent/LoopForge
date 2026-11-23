package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

// ExecuteOptions narrows an execution to a bound KnowledgeRef allowlist when set.
type ExecuteOptions struct {
	ScopeOverride []string
}

// ExecutionResult is the unified output returned by Service.Execute* for
// Admin trial search, Chat gateway, and SOP proxy.
type ExecutionResult struct {
	Status        string   `json:"status"`
	Warning       string   `json:"warning,omitempty"`
	Text          string   `json:"-"`
	Hits          int      `json:"-"`
	PartialErrors []string `json:"partialErrors,omitempty"`
	LatencyMs     int64    `json:"latencyMs"`
}

// HasWeKnoraConnection reports whether any active connection uses WeKnora.
func HasWeKnoraConnection(settings domain.KnowledgeSettings) bool {
	for _, conn := range settings.ActiveConnections() {
		if conn.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
			return true
		}
	}
	return false
}

// ReadOnlyToolsForSettings returns the read-only MCP tool definitions for the
// active connections. When any WeKnora connection is active (including mixed
// LightCloud + WeKnora), the 4 standard tools are exposed.
func ReadOnlyToolsForSettings(ctx context.Context, settings domain.KnowledgeSettings) ([]mcpclient.Tool, error) {
	conns := settings.ActiveConnections()
	if len(conns) == 0 {
		return nil, ErrNotConfigured
	}
	if HasWeKnoraConnection(settings) {
		return WeKnoraReadOnlyToolDefinitions(), nil
	}
	var lastErr error
	for _, conn := range conns {
		c, err := NewClient(settingsForConnection(settings, conn))
		if err != nil {
			lastErr = err
			continue
		}
		tools, err := c.Tools(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		out := make([]mcpclient.Tool, 0, len(tools))
		for _, tool := range tools {
			if IsReadOnlyTool(tool.Name) {
				out = append(out, tool)
			}
		}
		return out, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("知识库不可达：%w", lastErr)
	}
	return nil, ErrNotConfigured
}

func effectiveAllowedSources(settings domain.KnowledgeSettings, projectID string, override []string) ([]string, error) {
	allowed := settings.SourcesForProject(projectID)
	if len(override) == 0 {
		return allowed, nil
	}
	scoped, err := ScopeSources(allowed, override)
	if err != nil || len(scoped) == 0 {
		return nil, apperr.New(apperr.Forbidden, "所选知识库范围已失效")
	}
	return scoped, nil
}

func connDisplayName(conn domain.KnowledgeConnection) string {
	if strings.TrimSpace(conn.Name) != "" {
		return conn.Name
	}
	if strings.TrimSpace(conn.ID) != "" {
		return conn.ID
	}
	return "default"
}

// ExecuteTool dispatches one read-only knowledge tool call (standard or legacy
// kb_* alias) through the unified Service execution layer.
func (s *Service) ExecuteTool(ctx context.Context, projectID, tool string, args map[string]any, opts ExecuteOptions) (ExecutionResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	switch tool {
	case WeKnoraToolListKnowledgeBases, ToolListSources:
		return s.ExecuteListSources(ctx, projectID, tool == ToolListSources, opts)

	case WeKnoraToolSearchKnowledge, ToolSearch:
		query := strings.TrimSpace(argString(args["query"]))
		if query == "" {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "query 不能为空")
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
		_, res, err := s.ExecuteSearch(ctx, projectID, query, reqIDs, limit, opts)
		return res, err

	case WeKnoraToolListDocuments:
		kbID := strings.TrimSpace(argString(args["knowledge_base_id"]))
		keyword := strings.TrimSpace(argString(args["keyword"]))
		page := argInt(args["page"], 1)
		pageSize := argInt(args["page_size"], 20)
		return s.ExecuteListDocuments(ctx, projectID, kbID, keyword, page, pageSize, opts)

	case WeKnoraToolReadDocument, ToolFetch:
		docID := strings.TrimSpace(argString(args["knowledge_id"]))
		if docID == "" {
			docID = strings.TrimSpace(argString(args["path"]))
		}
		sourceHint := strings.TrimSpace(argString(args["source"]))
		offset := argInt(args["offset"], 0)
		limit := argInt(args["limit"], 20)
		startLine := argInt(args["start_line"], 0)
		endLine := argInt(args["end_line"], 0)
		query := strings.TrimSpace(argString(args["query"]))
		return s.ExecuteReadDocument(ctx, projectID, docID, sourceHint, offset, limit, startLine, endLine, query, tool == ToolFetch, opts)

	case ToolGetSource:
		source := strings.TrimSpace(argString(args["source"]))
		return s.executeGetSource(ctx, projectID, source, opts)

	default:
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, fmt.Errorf("未知的知识库工具 %q", tool)
	}
}

// ExecuteListSources lists authorized knowledge bases/sources across all active connections.
func (s *Service) ExecuteListSources(ctx context.Context, projectID string, legacyShape bool, opts ExecuteOptions) (ExecutionResult, error) {
	started := s.now()
	settings := s.store.KnowledgeSettings()
	conns := settings.ActiveConnections()
	if len(conns) == 0 {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	allowed, err := effectiveAllowedSources(settings, projectID, opts.ScopeOverride)
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}

	multi := len(conns) > 1
	var keptKBs []weKnoraKB
	var keptSources []Source
	var partialErrors []string
	succeeded := 0

	for _, conn := range conns {
		client, err := s.connect(settingsForConnection(settings, conn))
		if err != nil {
			partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), err))
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		if conn.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
			if wc, ok := client.(*WeKnoraClient); ok {
				kbs, listErr := wc.ListKnowledgeBases(cctx)
				cancel()
				if listErr != nil {
					partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), listErr))
					continue
				}
				succeeded++
				for _, kb := range kbs {
					sid := EncodeScopedSourceID(settings, conn.ID, kb.ID)
					if !SourceAllowed(allowed, sid) {
						continue
					}
					if multi && conn.Name != "" {
						kb.Name = "[" + conn.Name + "] " + kb.Name
					}
					kb.ID = sid
					keptKBs = append(keptKBs, kb)
					keptSources = append(keptSources, Source{
						ID:       sid,
						Name:     kb.Name,
						Status:   firstNonEmpty(kb.Type, "ready"),
						DocCount: kb.KnowledgeCount,
					})
				}
				continue
			}
		}
		list, listErr := client.ListSources(cctx)
		cancel()
		if listErr != nil {
			partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), listErr))
			continue
		}
		succeeded++
		for _, src := range list {
			sid := EncodeScopedSourceID(settings, conn.ID, src.ID)
			if !SourceAllowed(allowed, sid) {
				continue
			}
			if multi && conn.Name != "" {
				src.Name = "[" + conn.Name + "] " + src.Name
			}
			src.ID = sid
			keptSources = append(keptSources, src)
			keptKBs = append(keptKBs, weKnoraKB{
				ID:             sid,
				Name:           src.Name,
				Type:           domain.KnowledgeProviderLightCloud,
				KnowledgeCount: src.DocCount,
			})
		}
	}

	latency := s.now().Sub(started).Milliseconds()
	if succeeded == 0 && len(partialErrors) > 0 {
		return ExecutionResult{
			Status:        domain.KnowledgeStatusUnavailable,
			Warning:       strings.Join(partialErrors, "；"),
			PartialErrors: partialErrors,
			LatencyMs:     latency,
		}, apperr.New(apperr.Upstream, "知识库不可达："+partialErrors[0])
	}

	status := domain.KnowledgeStatusHit
	warning := ""
	if len(partialErrors) > 0 {
		status = domain.KnowledgeStatusPartial
		warning = "部分知识库连接不可达，目录可能不完整：" + strings.Join(partialErrors, "；")
	} else if len(keptSources) == 0 {
		status = domain.KnowledgeStatusEmpty
	}

	if keptKBs == nil {
		keptKBs = []weKnoraKB{}
	}
	if keptSources == nil {
		keptSources = []Source{}
	}

	var raw []byte
	if legacyShape {
		payload := map[string]any{"sources": keptSources, "status": status}
		if warning != "" {
			payload["warning"] = warning
		}
		raw, err = json.Marshal(payload)
	} else {
		payload := map[string]any{"knowledge_bases": keptKBs, "status": status}
		if warning != "" {
			payload["warning"] = warning
		}
		raw, err = json.Marshal(payload)
	}
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
	}
	return ExecutionResult{
		Status:        status,
		Warning:       warning,
		Text:          string(raw),
		Hits:          len(keptSources),
		PartialErrors: partialErrors,
		LatencyMs:     latency,
	}, nil
}

// ExecuteSearch runs hybrid search across all matching active connections,
// aggregates per-connection/per-KB errors into the unified 4-state status,
// sorts globally by Score descending, and truncates to limit.
func (s *Service) ExecuteSearch(ctx context.Context, projectID, query string, sources []string, limit int, opts ExecuteOptions) (SearchOutput, ExecutionResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchOutput{}, ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, "请输入要检索的问题或关键词")
	}
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	settings := s.store.KnowledgeSettings()
	conns := settings.ActiveConnections()
	if len(conns) == 0 {
		return SearchOutput{}, ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, ErrNotConfigured.Error())
	}
	allowed, err := effectiveAllowedSources(settings, projectID, opts.ScopeOverride)
	if err != nil {
		return SearchOutput{}, ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	scoped, err := ScopeSources(allowed, sources)
	if err != nil {
		return SearchOutput{}, ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Invalid, err.Error())
	}

	grouped := map[string][]string{}
	var partialErrors []string
	if len(scoped) == 0 {
		for _, c := range conns {
			grouped[c.ID] = nil
		}
	} else {
		for _, sid := range scoped {
			conn, rawID, ok := DecodeScopedSourceID(settings, sid)
			if !ok {
				partialErrors = append(partialErrors, fmt.Sprintf("来源 %s 所属连接已删除或停用", sid))
				continue
			}
			grouped[conn.ID] = append(grouped[conn.ID], rawID)
		}
		if len(grouped) == 0 {
			warning := strings.Join(partialErrors, "；")
			return SearchOutput{}, ExecutionResult{
				Status:        domain.KnowledgeStatusUnavailable,
				Warning:       warning,
				PartialErrors: partialErrors,
			}, apperr.New(apperr.Unavailable, "知识库不可用：所选知识库所属连接已删除或停用")
		}
	}

	started := s.now()
	var merged []SearchHit
	succeededConns := 0

	for _, conn := range conns {
		rawIDs, want := grouped[conn.ID]
		if !want {
			continue
		}
		client, err := s.connect(settingsForConnection(settings, conn))
		if err != nil {
			partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), err))
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		if conn.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
			if wc, ok := client.(*WeKnoraClient); ok {
				var kbNames map[string]string
				if len(rawIDs) == 0 {
					if names, gotNames := s.weKnoraKBNamesDetailed(cctx, conn, wc); gotNames {
						kbNames = names
						if len(names) == 0 {
							cancel()
							succeededConns++
							continue
						}
						rawIDs = make([]string, 0, len(names))
						for id := range names {
							rawIDs = append(rawIDs, id)
						}
						sort.Strings(rawIDs)
					}
				}
				hits, kbErrs, searchErr := wc.HybridSearchDetailed(cctx, query, rawIDs, limit)
				if kbNames == nil && searchErr == nil && len(hits) > 0 {
					kbNames = s.weKnoraKBNames(cctx, conn, wc)
				}
				cancel()
				if searchErr != nil {
					partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), searchErr))
					continue
				}
				for _, ke := range kbErrs {
					partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), ke))
				}
				if len(kbErrs) == 0 || len(hits) > 0 || (len(rawIDs) > 0 && len(kbErrs) < len(rawIDs)) {
					succeededConns++
				}
				for _, h := range hits {
					scopedKB := EncodeScopedSourceID(settings, conn.ID, h.SourceKBID)
					scopedDoc := EncodeScopedSourceID(settings, conn.ID, h.KnowledgeID)
					title := firstNonEmpty(h.KnowledgeTitle, h.KnowledgeFilename, h.KnowledgeID)
					ci := h.ChunkIndex
					merged = append(merged, SearchHit{
						Source:     scopedKB,
						SourceName: kbNames[h.SourceKBID],
						Path:       scopedDoc,
						Title:      title,
						ChunkIndex: &ci,
						StartLine:  h.StartAt,
						EndLine:    h.EndAt,
						Snippet:    h.Content,
						Score:      h.Score,
						Citation:   fmt.Sprintf("%s#%s (chunk %d)", scopedKB, scopedDoc, h.ChunkIndex),
					})
				}
				continue
			}
		}
		res, searchErr := client.Search(cctx, query, rawIDs)
		cancel()
		if searchErr != nil {
			partialErrors = append(partialErrors, fmt.Sprintf("连接 %s: %v", connDisplayName(conn), searchErr))
			continue
		}
		succeededConns++
		for _, h := range res.Results {
			h.Source = EncodeScopedSourceID(settings, conn.ID, h.Source)
			if conn.EffectiveProvider() == domain.KnowledgeProviderWeKnora && h.Path != "" {
				h.Path = EncodeScopedSourceID(settings, conn.ID, h.Path)
			}
			merged = append(merged, h)
		}
	}

	latency := s.now().Sub(started).Milliseconds()
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		if merged[i].Source != merged[j].Source {
			return merged[i].Source < merged[j].Source
		}
		if merged[i].Path != merged[j].Path {
			return merged[i].Path < merged[j].Path
		}
		return merged[i].StartLine < merged[j].StartLine
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	if merged == nil {
		merged = []SearchHit{}
	}

	if succeededConns == 0 && len(partialErrors) > 0 {
		warning := strings.Join(partialErrors, "；")
		return SearchOutput{}, ExecutionResult{
			Status:        domain.KnowledgeStatusUnavailable,
			Warning:       warning,
			PartialErrors: partialErrors,
			LatencyMs:     latency,
		}, apperr.New(apperr.Upstream, "检索失败："+partialErrors[0])
	}

	status := domain.KnowledgeStatusHit
	warning := ""
	if len(partialErrors) > 0 {
		status = domain.KnowledgeStatusPartial
		warning = "部分知识库检索失败，结果可能不完整：" + strings.Join(partialErrors, "；")
	} else if len(merged) == 0 {
		status = domain.KnowledgeStatusEmpty
	}

	searchResult := SearchResult{
		Query:         query,
		Mode:          "hybrid",
		Degraded:      status == domain.KnowledgeStatusPartial,
		DegradeReason: warning,
		Results:       merged,
	}
	out := SearchOutput{
		Result:    searchResult,
		Status:    status,
		Warning:   warning,
		Sources:   scoped,
		LatencyMs: latency,
	}

	payload := map[string]any{
		"query":   query,
		"mode":    "hybrid",
		"status":  status,
		"results": merged,
	}
	if warning != "" {
		payload["warning"] = warning
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return SearchOutput{}, ExecutionResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
	}
	return out, ExecutionResult{
		Status:        status,
		Warning:       warning,
		Text:          string(raw),
		Hits:          len(merged),
		PartialErrors: partialErrors,
		LatencyMs:     latency,
	}, nil
}

// ExecuteListDocuments lists documents inside one scoped knowledge base.
func (s *Service) ExecuteListDocuments(ctx context.Context, projectID, kbID, keyword string, page, pageSize int, opts ExecuteOptions) (ExecutionResult, error) {
	started := s.now()
	kbID = strings.TrimSpace(kbID)
	if kbID == "" {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, errors.New("knowledge_base_id 不能为空")
	}
	settings := s.store.KnowledgeSettings()
	allowed, err := effectiveAllowedSources(settings, projectID, opts.ScopeOverride)
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	if !SourceAllowed(allowed, kbID) {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, fmt.Errorf("知识库来源 %q 不在当前项目的授权范围", kbID)
	}
	conn, rawKB, ok := DecodeScopedSourceID(settings, kbID)
	if !ok {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "知识库不可用：所属连接已删除或停用")
	}
	client, err := s.connect(settingsForConnection(settings, conn))
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	if conn.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
		wc, ok := client.(*WeKnoraClient)
		if !ok {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "当前连接不支持列出文档")
		}
		res, err := wc.ListDocuments(cctx, rawKB, keyword, page, pageSize)
		latency := s.now().Sub(started).Milliseconds()
		if err != nil {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
		}
		for i := range res.Documents {
			res.Documents[i] = scopeWeKnoraDoc(settings, conn.ID, res.Documents[i])
			res.Documents[i].KnowledgeBaseID = kbID
		}
		status := domain.KnowledgeStatusHit
		if len(res.Documents) == 0 {
			status = domain.KnowledgeStatusEmpty
		}
		raw, err := json.Marshal(res)
		return ExecutionResult{
			Status:    status,
			Text:      string(raw),
			Hits:      len(res.Documents),
			LatencyMs: latency,
		}, err
	}

	// LightCloud does not expose a document-listing endpoint; return empty page with hint.
	raw, err := json.Marshal(map[string]any{
		"documents": []WeKnoraDocument{},
		"total":     0,
		"page":      1,
		"page_size": 20,
		"note":      "该来源为 LightCloud 知识库，请直接调用 search_knowledge 检索文件后使用 read_document(knowledge_id=path, source=kb_id) 读取",
	})
	return ExecutionResult{
		Status:    domain.KnowledgeStatusEmpty,
		Text:      string(raw),
		Hits:      0,
		LatencyMs: s.now().Sub(started).Milliseconds(),
	}, err
}

// ExecuteReadDocument reads document chunks (WeKnora, with exact offset%limit
// in-page slicing and cross-page fill) or lines (LightCloud).
func (s *Service) ExecuteReadDocument(ctx context.Context, projectID, docID, sourceHint string, offset, limit, startLine, endLine int, query string, legacyFetch bool, opts ExecuteOptions) (ExecutionResult, error) {
	started := s.now()
	docID = strings.TrimSpace(docID)
	sourceHint = strings.TrimSpace(sourceHint)
	if docID == "" {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, errors.New("knowledge_id 不能为空")
	}
	settings := s.store.KnowledgeSettings()
	allowed, err := effectiveAllowedSources(settings, projectID, opts.ScopeOverride)
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}

	// LightCloud / explicit source path
	if legacyFetch || sourceHint != "" {
		if sourceHint == "" {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, errors.New("source 不能为空")
		}
		if !SourceAllowed(allowed, sourceHint) {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, fmt.Errorf("知识库来源 %q 不在当前项目的授权范围", sourceHint)
		}
		conn, rawSource, ok := DecodeScopedSourceID(settings, sourceHint)
		if !ok {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "知识库不可用：所属连接已删除或停用")
		}
		if conn.EffectiveProvider() == domain.KnowledgeProviderLightCloud {
			client, err := s.connect(settingsForConnection(settings, conn))
			if err != nil {
				return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
			}
			cctx, cancel := context.WithTimeout(ctx, CallTimeout)
			doc, err := client.Fetch(cctx, rawSource, docID, startLine, endLine)
			cancel()
			latency := s.now().Sub(started).Milliseconds()
			if err != nil {
				return ExecutionResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: latency}, err
			}
			doc.Source = sourceHint
			status := domain.KnowledgeStatusHit
			if len(doc.Lines) == 0 {
				status = domain.KnowledgeStatusEmpty
			}
			raw, err := json.Marshal(doc)
			return ExecutionResult{
				Status:    status,
				Text:      string(raw),
				Hits:      len(doc.Lines),
				LatencyMs: latency,
			}, err
		}
	}

	conn, rawDocID, ok := DecodeScopedSourceID(settings, docID)
	if !ok {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "文档不可用：所属连接已删除或停用")
	}
	if conn.EffectiveProvider() == domain.KnowledgeProviderLightCloud {
		if sourceHint == "" {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, errors.New("读取 LightCloud 文档需要提供 source 参数")
		}
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "知识库不可用：所属连接已删除或停用")
	}

	client, err := s.connect(settingsForConnection(settings, conn))
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	wc, ok := client.(*WeKnoraClient)
	if !ok {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "当前连接不支持分段读取文档")
	}

	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	var docMeta WeKnoraDocument
	if len(allowed) > 0 {
		doc, err := s.weKnoraDocMeta(cctx, conn, wc, rawDocID)
		if err != nil {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
		}
		docMeta = doc
		scopedKB := EncodeScopedSourceID(settings, conn.ID, strings.TrimSpace(doc.KnowledgeBaseID))
		if !SourceAllowed(allowed, scopedKB) {
			return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, fmt.Errorf("文档 %q 所属知识库 %q 不在当前项目的授权范围", docID, scopedKB)
		}
	} else if doc, metaErr := s.weKnoraDocMeta(cctx, conn, wc, rawDocID); metaErr == nil {
		docMeta = doc
	}

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	startPage := (offset / limit) + 1
	inPageOffset := offset % limit

	chunks1, total, err := wc.ListChunks(cctx, rawDocID, startPage, limit)
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: s.now().Sub(started).Milliseconds()}, err
	}
	var collected []weKnoraChunk
	if inPageOffset < len(chunks1) {
		collected = append(collected, chunks1[inPageOffset:]...)
	}
	if len(collected) < limit && len(chunks1) == limit && (total <= 0 || int64(offset+len(collected)) < total) {
		chunks2, _, err2 := wc.ListChunks(cctx, rawDocID, startPage+1, limit)
		if err2 == nil && len(chunks2) > 0 {
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
	payload := map[string]any{
		"knowledge_id": docID,
		"total":        total,
		"offset":       offset,
		"limit":        limit,
		"chunks":       collected,
	}
	if title := firstNonEmpty(docMeta.Title, docMeta.FileName); title != "" {
		payload["title"] = title
	}
	if rawKB := strings.TrimSpace(docMeta.KnowledgeBaseID); rawKB != "" {
		payload["knowledge_base_id"] = EncodeScopedSourceID(settings, conn.ID, rawKB)
		if len(collected) > 0 {
			if kbNames := s.weKnoraKBNames(cctx, conn, wc); kbNames[rawKB] != "" {
				payload["knowledge_base_name"] = kbNames[rawKB]
			}
		}
	}
	raw, err := json.Marshal(payload)
	return ExecutionResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(collected),
		LatencyMs: s.now().Sub(started).Milliseconds(),
	}, err
}

func (s *Service) executeGetSource(ctx context.Context, projectID, source string, opts ExecuteOptions) (ExecutionResult, error) {
	if source == "" {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, errors.New("source 不能为空")
	}
	settings := s.store.KnowledgeSettings()
	allowed, err := effectiveAllowedSources(settings, projectID, opts.ScopeOverride)
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	if !SourceAllowed(allowed, source) {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, fmt.Errorf("知识库来源 %q 不在当前项目的授权范围", source)
	}
	conn, rawSource, ok := DecodeScopedSourceID(settings, source)
	if !ok {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, apperr.New(apperr.Unavailable, "知识库不可用：所属连接已删除或停用")
	}
	client, err := NewClient(settingsForConnection(settings, conn))
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	text, err := client.Call(cctx, ToolGetSource, map[string]any{"source": rawSource})
	if err != nil {
		return ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	return ExecutionResult{Status: domain.KnowledgeStatusHit, Text: text, Hits: 1}, nil
}

func argString(v any) string {
	if v == nil {
		return ""
	}
	s := fmt.Sprint(v)
	if s == "<nil>" {
		return ""
	}
	return s
}

func argStringList(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(list) == "" {
			return nil
		}
		return strings.Split(list, ",")
	}
	return nil
}

func argInt(v any, fallback int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}

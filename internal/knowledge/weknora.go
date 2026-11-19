package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	WeKnoraToolListKnowledgeBases = "list_knowledge_bases"
	WeKnoraToolSearchKnowledge    = "search_knowledge"
	WeKnoraToolListDocuments      = "list_documents"
	WeKnoraToolReadDocument       = "read_document"
)

var WeKnoraReadOnlyTools = []string{
	WeKnoraToolListKnowledgeBases,
	WeKnoraToolSearchKnowledge,
	WeKnoraToolListDocuments,
	WeKnoraToolReadDocument,
}

func IsWeKnoraReadOnlyTool(name string) bool {
	for _, t := range WeKnoraReadOnlyTools {
		if t == name {
			return true
		}
	}
	return false
}

// NormalizeWeKnoraBaseURL strips trailing slashes and an optional /api/v1 suffix
// so admins can paste either http://weknora-app:8080 or http://.../api/v1.
func NormalizeWeKnoraBaseURL(raw string) string {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	s = strings.TrimSuffix(s, "/api/v1")
	return strings.TrimRight(s, "/")
}

type WeKnoraDocument struct {
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

type WeKnoraDocumentPage struct {
	Documents []WeKnoraDocument `json:"documents"`
	Total     int64             `json:"total"`
	Page      int               `json:"page"`
	PageSize  int               `json:"pageSize"`
}

type weKnoraKB struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Type             string `json:"type"`
	KnowledgeCount   int    `json:"knowledge_count"`
	EmbeddingModelID string `json:"embedding_model_id,omitempty"`
	SummaryModelID   string `json:"summary_model_id,omitempty"`
}

type WeKnoraModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // Embedding | KnowledgeQA
}

type WeKnoraCreateKBInput struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	EmbeddingModelID string `json:"embedding_model_id"`
	SummaryModelID   string `json:"summary_model_id"`
}

type weKnoraSearchHit struct {
	ID                string  `json:"id"`
	Content           string  `json:"content"`
	KnowledgeID       string  `json:"knowledge_id"`
	ChunkIndex        int     `json:"chunk_index"`
	KnowledgeTitle    string  `json:"knowledge_title"`
	KnowledgeFilename string  `json:"knowledge_filename"`
	Score             float64 `json:"score"`
	MatchType         any     `json:"match_type"`
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

// WeKnoraClient talks to a WeKnora server over its REST API (/api/v1/*) using
// X-API-Key authentication.
type WeKnoraClient struct {
	baseURL string
	apiKey  string
	http    *http.Client

	docKBMu sync.Mutex
	docKB   map[string]docKBCacheEntry
}

type docKBCacheEntry struct {
	kbID      string
	expiresAt time.Time
}

func NewWeKnoraClient(settings domain.KnowledgeSettings) (*WeKnoraClient, error) {
	base := NormalizeWeKnoraBaseURL(settings.URL)
	key := strings.TrimSpace(settings.Token)
	if base == "" || key == "" {
		return nil, ErrNotConfigured
	}
	return &WeKnoraClient{
		baseURL: base,
		apiKey:  key,
		http:    &http.Client{Timeout: 30 * time.Second},
		docKB:   map[string]docKBCacheEntry{},
	}, nil
}

func (c *WeKnoraClient) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	readOnlyAnn := json.RawMessage(`{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false}`)
	return []mcpclient.Tool{
		{
			Name:        WeKnoraToolListKnowledgeBases,
			Description: "List accessible WeKnora knowledge bases with id, name, description and document count.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolSearchKnowledge,
			Description: "Hybrid search across authorized WeKnora knowledge bases and return matching passages with document ids and titles.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search question or keywords"},"knowledge_base_ids":{"type":"array","items":{"type":"string"},"description":"Optional knowledge base IDs to restrict search"},"limit":{"type":"integer","description":"Maximum passages to return (default 10, max 30)"}},"required":["query"]}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolListDocuments,
			Description: "List documents inside one authorized WeKnora knowledge base.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"knowledge_base_id":{"type":"string","description":"Knowledge base ID"},"keyword":{"type":"string","description":"Optional title filter"},"page":{"type":"integer"},"page_size":{"type":"integer"}},"required":["knowledge_base_id"]}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolReadDocument,
			Description: "Read a document's text chunks in order by knowledge_id.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"knowledge_id":{"type":"string","description":"Document ID"},"offset":{"type":"integer","description":"0-based chunk offset"},"limit":{"type":"integer","description":"Chunks per page (default 20, max 100)"},"query":{"type":"string","description":"Optional case-insensitive keywords to filter chunks"}},"required":["knowledge_id"]}`),
			Annotations: readOnlyAnn,
		},
	}, nil
}

func (c *WeKnoraClient) ListSources(ctx context.Context) ([]Source, error) {
	kbs, err := c.ListKnowledgeBases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(kbs))
	for _, kb := range kbs {
		status := "ready"
		if kb.Type != "" {
			status = kb.Type
		}
		out = append(out, Source{
			ID:       kb.ID,
			Name:     kb.Name,
			Status:   status,
			DocCount: kb.KnowledgeCount,
		})
	}
	return out, nil
}

func (c *WeKnoraClient) ListKnowledgeBases(ctx context.Context) ([]weKnoraKB, error) {
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

func (c *WeKnoraClient) Search(ctx context.Context, query string, sources []string) (SearchResult, error) {
	hits, err := c.HybridSearch(ctx, query, sources, 10)
	if err != nil {
		return SearchResult{}, err
	}
	results := make([]SearchHit, 0, len(hits))
	for _, h := range hits {
		title := firstNonEmpty(h.KnowledgeTitle, h.KnowledgeFilename, h.KnowledgeID)
		results = append(results, SearchHit{
			Source:    h.SourceKBID,
			Path:      h.KnowledgeID,
			Title:     title,
			StartLine: h.StartAt,
			EndLine:   h.EndAt,
			Snippet:   h.Content,
			Score:     h.Score,
			Citation:  fmt.Sprintf("%s#%s (chunk %d)", h.SourceKBID, h.KnowledgeID, h.ChunkIndex),
		})
	}
	return SearchResult{
		Query:   query,
		Mode:    "hybrid",
		Results: results,
	}, nil
}

type weKnoraScopedHit struct {
	weKnoraSearchHit
	SourceKBID string `json:"knowledge_base_id"`
}

func (c *WeKnoraClient) HybridSearch(ctx context.Context, query string, kbIDs []string, limit int) ([]weKnoraScopedHit, error) {
	kbIDs = cleanList(kbIDs)
	if len(kbIDs) == 0 {
		kbs, err := c.ListKnowledgeBases(ctx)
		if err != nil {
			return nil, err
		}
		for _, kb := range kbs {
			kbIDs = append(kbIDs, kb.ID)
		}
	}
	if len(kbIDs) == 0 {
		return []weKnoraScopedHit{}, nil
	}
	if limit <= 0 || limit > 30 {
		limit = 10
	}

	type kbRes struct {
		hits []weKnoraScopedHit
		err  error
	}
	ch := make(chan kbRes, len(kbIDs))
	var wg sync.WaitGroup
	for _, kbID := range kbIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			payload := map[string]any{
				"query_text":  query,
				"match_count": limit,
			}
			var env struct {
				Success bool               `json:"success"`
				Data    []weKnoraSearchHit `json:"data"`
			}
			err := c.doJSON(ctx, http.MethodPost, "/api/v1/knowledge-bases/"+url.PathEscape(id)+"/hybrid-search", payload, &env)
			if err != nil {
				ch <- kbRes{err: err}
				return
			}
			out := make([]weKnoraScopedHit, 0, len(env.Data))
			for _, h := range env.Data {
				out = append(out, weKnoraScopedHit{weKnoraSearchHit: h, SourceKBID: id})
			}
			ch <- kbRes{hits: out}
		}(kbID)
	}
	wg.Wait()
	close(ch)

	var all []weKnoraScopedHit
	var firstErr error
	for r := range ch {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		all = append(all, r.hits...)
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (c *WeKnoraClient) Fetch(ctx context.Context, source, docPath string, start, end int) (Document, error) {
	chunks, total, err := c.ListChunks(ctx, docPath, 1, 100)
	if err != nil {
		return Document{}, err
	}
	lines := make([]Line, 0, len(chunks))
	for i, ch := range chunks {
		lines = append(lines, Line{Number: i + 1, Text: ch.Content})
	}
	return Document{
		Source:    source,
		Path:      docPath,
		StartLine: 1,
		EndLine:   len(lines),
		LineCount: int(total),
		Lines:     lines,
		Citation:  fmt.Sprintf("%s#%s", source, docPath),
	}, nil
}

func (c *WeKnoraClient) ListChunks(ctx context.Context, knowledgeID string, page, pageSize int) ([]weKnoraChunk, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	endpoint := fmt.Sprintf("/api/v1/chunks/%s?page=%d&page_size=%d", url.PathEscape(strings.TrimSpace(knowledgeID)), page, pageSize)
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

func (c *WeKnoraClient) ListDocuments(ctx context.Context, kbID, keyword string, page, pageSize int) (WeKnoraDocumentPage, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	if kw := strings.TrimSpace(keyword); kw != "" {
		q.Set("keyword", kw)
	}
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(kbID)) + "/knowledge?" + q.Encode()
	var env struct {
		Success bool              `json:"success"`
		Data    []WeKnoraDocument `json:"data"`
		Total   int64             `json:"total"`
	}
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &env); err != nil {
		return WeKnoraDocumentPage{}, err
	}
	if env.Data == nil {
		env.Data = []WeKnoraDocument{}
	}
	return WeKnoraDocumentPage{
		Documents: env.Data,
		Total:     env.Total,
		Page:      page,
		PageSize:  pageSize,
	}, nil
}

func (c *WeKnoraClient) GetDocumentKBID(ctx context.Context, knowledgeID string) (string, error) {
	knowledgeID = strings.TrimSpace(knowledgeID)
	if knowledgeID == "" {
		return "", fmt.Errorf("knowledge_id 不能为空")
	}
	now := time.Now()
	c.docKBMu.Lock()
	if entry, ok := c.docKB[knowledgeID]; ok && now.Before(entry.expiresAt) {
		c.docKBMu.Unlock()
		return entry.kbID, nil
	}
	c.docKBMu.Unlock()

	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/knowledge/"+url.PathEscape(knowledgeID), nil, &env); err != nil {
		return "", err
	}
	kbID := strings.TrimSpace(env.Data.KnowledgeBaseID)
	if kbID != "" {
		c.docKBMu.Lock()
		c.docKB[knowledgeID] = docKBCacheEntry{kbID: kbID, expiresAt: now.Add(60 * time.Second)}
		c.docKBMu.Unlock()
	}
	return kbID, nil
}

func (c *WeKnoraClient) WriteDocument(ctx context.Context, source, docPath, content string) (WriteResult, error) {
	title := extractMarkdownTitle(content)
	if title == "" && strings.TrimSpace(docPath) != "" {
		title = strings.TrimSuffix(path.Base(docPath), path.Ext(docPath))
	}
	if title == "" {
		title = "LoopForge 知识沉淀"
	}
	payload := map[string]any{
		"title":   title,
		"content": content,
		"status":  "publish",
	}
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(source)) + "/knowledge/manual"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, &env); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{
		Source: source,
		Path:   docPath,
		Commit: env.Data.ID,
	}, nil
}

func (c *WeKnoraClient) UploadDocument(ctx context.Context, kbID, filename string, r io.Reader) (WeKnoraDocument, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	if _, err := io.Copy(part, r); err != nil {
		return WeKnoraDocument{}, err
	}
	if err := writer.Close(); err != nil {
		return WeKnoraDocument{}, err
	}

	endpoint := c.baseURL + "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(kbID)) + "/knowledge/file"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return WeKnoraDocument{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WeKnoraDocument{}, parseWeKnoraError(resp.StatusCode, raw)
	}
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return WeKnoraDocument{}, fmt.Errorf("WeKnora 返回无法解析：%s", clip(string(raw), 200))
	}
	return env.Data, nil
}

func (c *WeKnoraClient) ListModels(ctx context.Context) ([]WeKnoraModel, error) {
	var env struct {
		Success bool           `json:"success"`
		Data    []WeKnoraModel `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/models", nil, &env); err != nil {
		return nil, err
	}
	if env.Data == nil {
		return []WeKnoraModel{}, nil
	}
	return env.Data, nil
}

func (c *WeKnoraClient) CreateKnowledgeBase(ctx context.Context, input WeKnoraCreateKBInput) (weKnoraKB, error) {
	var env struct {
		Success bool      `json:"success"`
		Data    weKnoraKB `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/knowledge-bases", input, &env); err != nil {
		return weKnoraKB{}, err
	}
	return env.Data, nil
}

func (c *WeKnoraClient) UpdateKnowledgeBase(ctx context.Context, kbID, name, description string) (weKnoraKB, error) {
	payload := map[string]any{
		"name":        strings.TrimSpace(name),
		"description": strings.TrimSpace(description),
	}
	var env struct {
		Success bool      `json:"success"`
		Data    weKnoraKB `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPut, "/api/v1/knowledge-bases/"+url.PathEscape(strings.TrimSpace(kbID)), payload, &env); err != nil {
		return weKnoraKB{}, err
	}
	return env.Data, nil
}

func (c *WeKnoraClient) DeleteKnowledgeBase(ctx context.Context, kbID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/knowledge-bases/"+url.PathEscape(strings.TrimSpace(kbID)), nil, nil)
}

func (c *WeKnoraClient) CreateDocumentFromURL(ctx context.Context, kbID, rawURL string) (WeKnoraDocument, error) {
	payload := map[string]any{"url": strings.TrimSpace(rawURL)}
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(kbID)) + "/knowledge/url"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, &env); err != nil {
		return WeKnoraDocument{}, err
	}
	return env.Data, nil
}

func (c *WeKnoraClient) CreateManualDocument(ctx context.Context, kbID, title, content string, publish bool) (WeKnoraDocument, error) {
	status := "publish"
	if !publish {
		status = "draft"
	}
	payload := map[string]any{
		"title":   strings.TrimSpace(title),
		"content": content,
		"status":  status,
	}
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(kbID)) + "/knowledge/manual"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, &env); err != nil {
		return WeKnoraDocument{}, err
	}
	return env.Data, nil
}

func (c *WeKnoraClient) UpdateManualDocument(ctx context.Context, docID, title, content string, publish bool) (WeKnoraDocument, error) {
	status := "publish"
	if !publish {
		status = "draft"
	}
	payload := map[string]any{
		"title":   strings.TrimSpace(title),
		"content": content,
		"status":  status,
	}
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	endpoint := "/api/v1/knowledge/manual/" + url.PathEscape(strings.TrimSpace(docID))
	if err := c.doJSON(ctx, http.MethodPut, endpoint, payload, &env); err != nil {
		return WeKnoraDocument{}, err
	}
	return env.Data, nil
}

func (c *WeKnoraClient) ReparseDocument(ctx context.Context, docID string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/knowledge/"+url.PathEscape(strings.TrimSpace(docID))+"/reparse", map[string]any{}, nil)
}

func (c *WeKnoraClient) DeleteDocument(ctx context.Context, docID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/knowledge/"+url.PathEscape(strings.TrimSpace(docID)), nil, nil)
}

func (c *WeKnoraClient) doJSON(ctx context.Context, method, pathStr string, payload any, out any) error {
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
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseWeKnoraError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("WeKnora %s 返回无法解析：%s", pathStr, clip(string(raw), 200))
	}
	return nil
}

func parseWeKnoraError(status int, raw []byte) error {
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
			return fmt.Errorf("WeKnora HTTP %d: %s", status, strings.TrimSpace(parsed.Error.Message))
		}
		if strings.TrimSpace(parsed.Message) != "" {
			return fmt.Errorf("WeKnora HTTP %d: %s", status, strings.TrimSpace(parsed.Message))
		}
	}
	msg := clip(string(raw), 200)
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("WeKnora HTTP %d: %s", status, msg)
}

func extractMarkdownTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if title != "" {
				return title
			}
		}
	}
	return ""
}

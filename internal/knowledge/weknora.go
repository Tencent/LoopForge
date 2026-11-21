package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
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

const (
	// weknoraRequestTimeout bounds ordinary JSON calls.
	weknoraRequestTimeout = 60 * time.Second
	// weknoraStreamTimeout bounds streaming uploads and preview/download flows.
	weknoraStreamTimeout = 10 * time.Minute
	// weknoraMaxErrorBytes caps how much of an upstream error body is parsed.
	weknoraMaxErrorBytes = 1 << 20
	// weknoraMaxResponseBytes caps ordinary JSON success bodies.
	weknoraMaxResponseBytes = 8 << 20
	// weknoraLogPathMax keeps request paths readable in logs and errors.
	weknoraLogPathMax = 160
	// weknoraMaxRedirects limits redirect hops for outbound requests.
	weknoraMaxRedirects = 5
)

// NormalizeWeKnoraBaseURL strips trailing slashes and an optional /api/v1 suffix
// so admins can paste either http://weknora-app:8080 or http://.../api/v1.
func NormalizeWeKnoraBaseURL(raw string) string {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	s = strings.TrimSuffix(s, "/api/v1")
	return strings.TrimRight(s, "/")
}

// ValidateOutboundWeKnoraURL normalizes and validates an admin-supplied WeKnora
// base URL. Private/container hosts stay reachable on purpose (managed mode
// points at http://weknora-app:8080), but credentials embedded in the URL,
// non-http(s) schemes and control characters are rejected.
func ValidateOutboundWeKnoraURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrNotConfigured
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("WeKnora 地址包含非法控制字符")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("WeKnora 地址无法解析：%w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("WeKnora 地址必须是 http(s)://host 形式")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", errors.New("WeKnora 地址缺少主机名")
	}
	if u.User != nil {
		return "", errors.New("WeKnora 地址不能内嵌账号密码")
	}
	u.Fragment = ""
	out := NormalizeWeKnoraBaseURL(u.String())
	if out == "" {
		return "", ErrNotConfigured
	}
	return out, nil
}

// truncateLogPath shortens a request path before it reaches logs or errors.
func truncateLogPath(p string) string {
	r := []rune(strings.TrimSpace(p))
	if len(r) <= weknoraLogPathMax {
		return string(r)
	}
	return string(r[:weknoraLogPathMax]) + "..."
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
	CreatedAt        string `json:"created_at,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

type WeKnoraModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // Embedding | KnowledgeQA
}

type WeKnoraCreateKBInput struct {
	ConnectionID     string `json:"connectionId,omitempty"`
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

// UpstreamError is a WeKnora REST failure. Structured reports whether the body
// matched a JSON error envelope, which is what separates a real WeKnora 404
// (router hit, AppError handled) from a bare gateway/Gin 404 (route missing).
type UpstreamError struct {
	Status     int
	Code       string
	Message    string
	Structured bool
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("WeKnora HTTP %d: %s", e.Status, e.Message)
}

// StreamResponse is an unbuffered upstream body for preview/download.
type StreamResponse struct {
	Body               io.ReadCloser
	ContentType        string
	ContentDisposition string
	ContentLength      int64
	StatusCode         int
}

// WeKnoraClientConfig builds a client explicitly; the domain settings path uses
// NewWeKnoraClient. Transport/HTTPClient are injection points for tests.
type WeKnoraClientConfig struct {
	BaseURL     string
	APIKey      string
	TenantID    string
	BearerToken string
	Transport   http.RoundTripper
	HTTPClient  *http.Client
}

// WeKnoraClient talks to a WeKnora server over its REST API (/api/v1/*) using
// X-API-Key authentication.
type WeKnoraClient struct {
	baseURL     string
	apiKey      string
	tenantID    string
	bearerToken string
	http        *http.Client
	stream      *http.Client

	docKBMu sync.Mutex
	docKB   map[string]docKBCacheEntry
}

type docKBCacheEntry struct {
	kbID      string
	expiresAt time.Time
}

func newWeKnoraTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// checkWeKnoraRedirect refuses cross-origin redirects so a semi-trusted
// connection URL cannot bounce credentials to a third party.
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

// NewWeKnoraClient builds a client from stored settings. It fails with
// ErrNotConfigured when URL or token is missing.
func NewWeKnoraClient(settings domain.KnowledgeSettings) (*WeKnoraClient, error) {
	key := strings.TrimSpace(settings.Token)
	if key == "" {
		return nil, ErrNotConfigured
	}
	return NewWeKnoraClientWithConfig(WeKnoraClientConfig{BaseURL: settings.URL, APIKey: key})
}

// NewWeKnoraClientWithConfig builds a client with explicit credentials and
// optional transport injection. The JSON client and the streaming client share
// one Transport and differ only in Timeout (60s vs 10m).
func NewWeKnoraClientWithConfig(cfg WeKnoraClientConfig) (*WeKnoraClient, error) {
	base, err := ValidateOutboundWeKnoraURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	bearer := strings.TrimSpace(cfg.BearerToken)
	if apiKey == "" && bearer == "" {
		return nil, ErrNotConfigured
	}

	baseClient := http.Client{}
	if cfg.HTTPClient != nil {
		baseClient = *cfg.HTTPClient
	}
	if cfg.Transport != nil {
		baseClient.Transport = cfg.Transport
	}
	if baseClient.Transport == nil {
		baseClient.Transport = newWeKnoraTransport()
	}
	baseClient.Timeout = weknoraRequestTimeout
	baseClient.CheckRedirect = checkWeKnoraRedirect

	streamClient := baseClient
	streamClient.Timeout = weknoraStreamTimeout

	return &WeKnoraClient{
		baseURL:     base,
		apiKey:      apiKey,
		tenantID:    strings.TrimSpace(cfg.TenantID),
		bearerToken: bearer,
		http:        &baseClient,
		stream:      &streamClient,
		docKB:       map[string]docKBCacheEntry{},
	}, nil
}

// WeKnoraReadOnlyToolDefinitions returns the 4 standard read-only MCP tool
// definitions exposed when any WeKnora connection (or mixed setup) is active.
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
			Description: "Read a document's text chunks in order by knowledge_id (or LightCloud source+path).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"knowledge_id":{"type":"string","description":"Document ID or file path"},"source":{"type":"string","description":"Optional source ID when reading a LightCloud document"},"offset":{"type":"integer","description":"0-based chunk offset"},"limit":{"type":"integer","description":"Chunks per page (default 20, max 100)"},"query":{"type":"string","description":"Optional case-insensitive keywords to filter chunks"}},"required":["knowledge_id"]}`),
			Annotations: readOnlyAnn,
		},
	}
}

func (c *WeKnoraClient) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return WeKnoraReadOnlyToolDefinitions(), nil
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

func (c *WeKnoraClient) GetKnowledgeBase(ctx context.Context, kbID string) (weKnoraKB, error) {
	var env struct {
		Success bool      `json:"success"`
		Data    weKnoraKB `json:"data"`
	}
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(kbID))
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &env); err != nil {
		return weKnoraKB{}, err
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

const weKnoraSearchConcurrency = 4

func (c *WeKnoraClient) HybridSearch(ctx context.Context, query string, kbIDs []string, limit int) ([]weKnoraScopedHit, error) {
	hits, partialErrs, err := c.HybridSearchDetailed(ctx, query, kbIDs, limit)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 && len(partialErrs) > 0 {
		return nil, partialErrs[0]
	}
	return hits, nil
}

// HybridSearchDetailed fans out hybrid-search across kbIDs with a 4-way bounded
// semaphore, respects ctx.Done() for queued requests, and returns both merged
// hits and per-KB errors so callers can distinguish partial vs unavailable.
func (c *WeKnoraClient) HybridSearchDetailed(ctx context.Context, query string, kbIDs []string, limit int) ([]weKnoraScopedHit, []error, error) {
	kbIDs = cleanList(kbIDs)
	if len(kbIDs) == 0 {
		kbs, err := c.ListKnowledgeBases(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, kb := range kbs {
			kbIDs = append(kbIDs, kb.ID)
		}
	}
	if len(kbIDs) == 0 {
		return []weKnoraScopedHit{}, nil, nil
	}
	if limit <= 0 || limit > 30 {
		limit = 10
	}

	type kbRes struct {
		hits []weKnoraScopedHit
		err  error
	}
	ch := make(chan kbRes, len(kbIDs))
	sem := make(chan struct{}, weKnoraSearchConcurrency)
	var wg sync.WaitGroup
	for _, kbID := range kbIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				ch <- kbRes{err: fmt.Errorf("kb %s: %w", id, ctx.Err())}
				return
			}
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
				ch <- kbRes{err: fmt.Errorf("kb %s: %w", id, err)}
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
	var partialErrs []error
	for r := range ch {
		if r.err != nil {
			partialErrs = append(partialErrs, r.err)
			continue
		}
		all = append(all, r.hits...)
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
	return all, partialErrs, nil
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

// GetKnowledge fetches one document's metadata (including its parent
// knowledge_base_id) from the upstream instance.
func (c *WeKnoraClient) GetKnowledge(ctx context.Context, docID string) (WeKnoraDocument, error) {
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	endpoint := "/api/v1/knowledge/" + url.PathEscape(strings.TrimSpace(docID))
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &env); err != nil {
		return WeKnoraDocument{}, err
	}
	return env.Data, nil
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

	doc, err := c.GetKnowledge(ctx, knowledgeID)
	if err != nil {
		return "", err
	}
	kbID := strings.TrimSpace(doc.KnowledgeBaseID)
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

// PostFile streams a multipart upload without buffering the whole file. The
// write side runs in one goroutine feeding an io.Pipe; closing the read end as
// soon as Do returns unblocks that goroutine when upstream answers early
// (401/403/413) without consuming the body.
func (c *WeKnoraClient) PostFile(ctx context.Context, endpoint string, fields map[string]string, fileField, filename string, file io.Reader, out any) error {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, pr)
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	if c.tenantID != "" {
		req.Header.Set("X-Tenant-ID", c.tenantID)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())

	go writeMultipart(pw, writer, fields, fileField, filename, file)

	defer func() { _ = pr.Close() }()
	resp, err := c.stream.Do(req)
	if err != nil {
		slog.DebugContext(ctx, "weknora upload failed", "path", truncateLogPath(endpoint), "error", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxErrorBytes))
		_, _ = io.Copy(io.Discard, resp.Body)
		return parseUpstreamError(resp.StatusCode, raw)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("WeKnora %s 响应读取失败：%w", truncateLogPath(endpoint), err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("WeKnora %s 返回无法解析：%s", truncateLogPath(endpoint), clip(string(raw), 200))
	}
	return nil
}

func writeMultipart(pw *io.PipeWriter, writer *multipart.Writer, fields map[string]string, fileField, filename string, file io.Reader) {
	var err error
	defer func() { _ = pw.CloseWithError(err) }()
	for k, v := range fields {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if err = writer.WriteField(k, v); err != nil {
			return
		}
	}
	part, partErr := writer.CreateFormFile(fileField, filename)
	if partErr != nil {
		err = partErr
		return
	}
	if _, err = io.Copy(part, file); err != nil {
		return
	}
	err = writer.Close()
}

func (c *WeKnoraClient) UploadDocument(ctx context.Context, kbID, filename string, r io.Reader) (WeKnoraDocument, error) {
	endpoint := "/api/v1/knowledge-bases/" + url.PathEscape(strings.TrimSpace(kbID)) + "/knowledge/file"
	var env struct {
		Success bool            `json:"success"`
		Data    WeKnoraDocument `json:"data"`
	}
	if err := c.PostFile(ctx, endpoint, nil, "file", filename, r, &env); err != nil {
		return WeKnoraDocument{}, err
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

// PatchKnowledgeBase updates only name/description, leaving chunking, VLM and
// index configuration untouched (PUT would overwrite them with zero values).
func (c *WeKnoraClient) PatchKnowledgeBase(ctx context.Context, kbID, name, description string) (weKnoraKB, error) {
	payload := map[string]any{
		"name":        strings.TrimSpace(name),
		"description": strings.TrimSpace(description),
	}
	var env struct {
		Success bool      `json:"success"`
		Data    weKnoraKB `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/knowledge-bases/"+url.PathEscape(strings.TrimSpace(kbID)), payload, &env); err != nil {
		return weKnoraKB{}, err
	}
	return env.Data, nil
}

// UpdateKnowledgeBase is kept for callers that still use the old name.
func (c *WeKnoraClient) UpdateKnowledgeBase(ctx context.Context, kbID, name, description string) (weKnoraKB, error) {
	return c.PatchKnowledgeBase(ctx, kbID, name, description)
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

// OpenPreview streams the parsed/original document preview. The upstream MIME
// is sanitized so embedding it in the console cannot execute active content.
func (c *WeKnoraClient) OpenPreview(ctx context.Context, docID string) (*StreamResponse, error) {
	return c.open(ctx, "/api/v1/knowledge/"+url.PathEscape(strings.TrimSpace(docID))+"/preview", nil, false)
}

// OpenDownload streams the original document as an attachment.
func (c *WeKnoraClient) OpenDownload(ctx context.Context, docID string) (*StreamResponse, error) {
	resp, err := c.open(ctx, "/api/v1/knowledge/"+url.PathEscape(strings.TrimSpace(docID))+"/download", nil, true)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.ContentDisposition)), "attachment") {
		resp.ContentDisposition = "attachment"
	}
	return resp, nil
}

// Open issues a GET on the streaming client and returns the untouched body so
// callers can forward previews/downloads without buffering them in memory.
func (c *WeKnoraClient) Open(ctx context.Context, pathStr string, query url.Values) (*StreamResponse, error) {
	return c.open(ctx, pathStr, query, false)
}

func (c *WeKnoraClient) open(ctx context.Context, pathStr string, query url.Values, forceAttachment bool) (*StreamResponse, error) {
	target := c.baseURL + pathStr
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	if c.tenantID != "" {
		req.Header.Set("X-Tenant-ID", c.tenantID)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	req.Header.Set("Accept", "*/*")

	resp, err := c.stream.Do(req)
	if err != nil {
		slog.DebugContext(ctx, "weknora open failed", "path", truncateLogPath(pathStr), "error", err)
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxErrorBytes))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, parseUpstreamError(resp.StatusCode, raw)
	}
	return &StreamResponse{
		Body:               resp.Body,
		ContentType:        SanitizeStreamContentType(resp.Header.Get("Content-Type"), forceAttachment),
		ContentDisposition: resp.Header.Get("Content-Disposition"),
		ContentLength:      resp.ContentLength,
		StatusCode:         resp.StatusCode,
	}, nil
}

// SanitizeStreamContentType downgrades active content to plain text. Browsers
// must never render upstream HTML/SVG/XML/JS inside the console origin, even
// when the file was uploaded by a trusted admin.
func SanitizeStreamContentType(raw string, forceAttachment bool) string {
	raw = strings.TrimSpace(raw)
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || mediaType == "" {
		if forceAttachment {
			return "application/octet-stream"
		}
		return "text/plain; charset=utf-8"
	}
	switch strings.ToLower(mediaType) {
	case "text/html", "application/xhtml+xml", "image/svg+xml",
		"text/xml", "application/xml",
		"text/javascript", "application/javascript", "application/x-javascript":
		return "text/plain; charset=utf-8"
	}
	return mediaType
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
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	if c.tenantID != "" {
		req.Header.Set("X-Tenant-ID", c.tenantID)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.DebugContext(ctx, "weknora request failed", "method", method, "path", truncateLogPath(pathStr), "error", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxErrorBytes))
		_, _ = io.Copy(io.Discard, resp.Body)
		return parseUpstreamError(resp.StatusCode, raw)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, weknoraMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("WeKnora %s 响应读取失败：%w", truncateLogPath(pathStr), err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("WeKnora %s 返回无法解析：%s", truncateLogPath(pathStr), clip(string(raw), 200))
	}
	return nil
}

// parseUpstreamError recognizes the three WeKnora error envelopes:
//
//	{"error":"message"}                    (api_key_gate)
//	{"error":{"code":...,"message":"..."}} (error_handler AppError)
//	{"message":"..."}                      (generic gateway)
//
// Anything else (plain "404 page not found") stays unstructured, which is how
// capability probes tell a real route 404 from a missing endpoint.
func parseUpstreamError(status int, raw []byte) *UpstreamError {
	e := &UpstreamError{Status: status}
	text := strings.TrimSpace(string(raw))
	if text != "" {
		var env struct {
			Error   json.RawMessage `json:"error"`
			Message string          `json:"message"`
		}
		if json.Unmarshal([]byte(text), &env) == nil {
			if len(env.Error) > 0 && !bytes.Equal(bytes.TrimSpace(env.Error), []byte("null")) {
				var str string
				if json.Unmarshal(env.Error, &str) == nil {
					if msg := strings.TrimSpace(str); msg != "" {
						e.Message, e.Structured = msg, true
					}
				} else {
					var obj struct {
						Code    any    `json:"code"`
						Message string `json:"message"`
					}
					if json.Unmarshal(env.Error, &obj) == nil {
						e.Message = strings.TrimSpace(obj.Message)
						e.Code = upstreamErrorCode(obj.Code)
						e.Structured = e.Message != "" || e.Code != ""
					}
				}
			}
			if !e.Structured {
				if msg := strings.TrimSpace(env.Message); msg != "" {
					e.Message, e.Structured = msg, true
				}
			}
		}
	}
	e.Message = clip(e.Message, 500)
	if e.Message == "" {
		// A structured envelope without any message (or a JSON body with no
		// recognized field) falls back to the status text; only non-JSON bodies
		// keep their raw text so "404 page not found" stays recognizable.
		if e.Structured || json.Valid([]byte(text)) || text == "" {
			e.Message = http.StatusText(status)
		} else {
			e.Message = clip(text, 200)
		}
	}
	return e
}

func upstreamErrorCode(v any) string {
	switch code := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(code)
	case float64:
		if code == float64(int64(code)) {
			return strconv.FormatInt(int64(code), 10)
		}
		return strconv.FormatFloat(code, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(code))
	}
}

// parseWeKnoraError keeps the historical helper shape used by provisioning.
func parseWeKnoraError(status int, raw []byte) error {
	return parseUpstreamError(status, raw)
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

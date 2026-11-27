package knowledge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	nativeDefaultTimeout    = 60 * time.Second
	nativeMaxErrorBytes     = 1 << 20
	nativeMaxResponseBytes  = 8 << 20
	nativeMaxRedirects      = 5
	nativeDefaultChunkRunes = 1200
)

// dockerEnvCheckHook allows unit tests to simulate running inside a Docker container.
var dockerEnvCheckHook = func() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// nativeReadOnlyToolDefinitions returns the canonical 4 read-only tool
// definitions shared by all native REST personal knowledge connectors.
func nativeReadOnlyToolDefinitions() []mcpclient.Tool {
	readOnlyAnn := json.RawMessage(`{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false}`)
	return []mcpclient.Tool{
		{
			Name:        WeKnoraToolListKnowledgeBases,
			Description: "List accessible knowledge bases, notebooks, spaces or root folders with id and name.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Annotations: readOnlyAnn,
		},
		{
			Name:        WeKnoraToolSearchKnowledge,
			Description: "Search across authorized knowledge bases and return matching passages with document ids and titles.",
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
			Description: "Read a document's text chunks in order by knowledge_id (also accepts path or document_id).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"knowledge_id":{"type":"string","description":"Document ID or relative path"},"path":{"type":"string","description":"Alias for knowledge_id"},"document_id":{"type":"string","description":"Alias for knowledge_id"},"offset":{"type":"integer","description":"0-based chunk offset"},"limit":{"type":"integer","description":"Chunks per page (default 20, max 50)"},"query":{"type":"string","description":"Optional case-insensitive keywords to filter chunks"}},"required":["knowledge_id"]}`),
			Annotations: readOnlyAnn,
		},
	}
}

func nativeReadOnlyToolNames() []string {
	defs := nativeReadOnlyToolDefinitions()
	out := make([]string, 0, len(defs))
	for _, t := range defs {
		out = append(out, t.Name)
	}
	return out
}

func validateNativeBaseURL(raw, defaultURL, label string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && strings.TrimSpace(defaultURL) != "" {
		raw = strings.TrimSpace(defaultURL)
	}
	if raw == "" {
		return "", apperr.New(apperr.Invalid, label+"服务地址不能为空")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", apperr.New(apperr.Invalid, label+"地址包含非法控制字符")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", apperr.New(apperr.Invalid, label+"地址无法解析，请检查格式")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", apperr.New(apperr.Invalid, label+"地址必须是 http(s)://host 形式")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", apperr.New(apperr.Invalid, label+"地址缺少主机名")
	}
	if u.User != nil {
		return "", apperr.New(apperr.Invalid, label+"地址不能内嵌账号密码")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", apperr.New(apperr.Invalid, label+"地址不能包含查询参数")
	}
	u.Fragment = ""
	s := strings.TrimRight(strings.TrimSpace(u.String()), "/")
	if s == "" {
		return "", apperr.New(apperr.Invalid, label+"服务地址不能为空")
	}
	if err := validateConnectorEndpointIP(s, true, label); err != nil {
		return "", err
	}
	return s, nil
}

var (
	// cgnatRange is RFC 6598 Shared Address Space (100.64.0.0/10), which also
	// covers Alibaba Cloud metadata service (100.100.100.200).
	_, cgnatRange, _ = net.ParseCIDR("100.64.0.0/10")
	// benchmarkRange is RFC 2544 benchmarking (198.18.0.0/15).
	_, benchmarkRange, _ = net.ParseCIDR("198.18.0.0/15")
	// nat64Range is RFC 6052 IPv6 well-known prefix (64:ff9b::/96).
	_, nat64Range, _ = net.ParseCIDR("64:ff9b::/96")
	// currentNetRange is RFC 1122 "this network" (0.0.0.0/8).
	_, currentNetRange, _ = net.ParseCIDR("0.0.0.0/8")
)

// isAlwaysBlockedIP rejects cloud metadata, link-local, CGNAT, unspecified,
// multicast, benchmarking and NAT64 prefixes for all outbound connectors.
func isAlwaysBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if (cgnatRange != nil && cgnatRange.Contains(ip)) ||
		(benchmarkRange != nil && benchmarkRange.Contains(ip)) ||
		(nat64Range != nil && nat64Range.Contains(ip)) ||
		(currentNetRange != nil && currentNetRange.Contains(ip)) {
		return true
	}
	return false
}

// isBlockedConnectorIP enforces the SSRF IP boundary. When allowLocalOrPrivate
// is false, RFC1918/ULA private ranges are blocked, and loopback is only
// permitted when tests or LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE opt in.
func isBlockedConnectorIP(ip net.IP, allowLocalOrPrivate bool) bool {
	if isAlwaysBlockedIP(ip) {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if allowLocalOrPrivate {
		return false
	}
	if ip.IsLoopback() {
		return !oauthInsecureAllowed()
	}
	return ip.IsPrivate()
}

// validateConnectorEndpointIP rejects literals in forbidden IP ranges at
// validation time so invalid metadata/link-local addresses fail fast.
func validateConnectorEndpointIP(rawURL string, allowLocalOrPrivate bool, label string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return apperr.New(apperr.Invalid, label+"地址格式无效")
	}
	host := strings.Trim(u.Hostname(), "[]")
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedConnectorIP(ip, allowLocalOrPrivate) {
			return apperr.New(apperr.Invalid, label+"地址不允许访问受保护的网络网段")
		}
	}
	return nil
}

func newSSRFGuardedDialContext(timeout time.Duration, allowLocalOrPrivate bool, label string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%s地址无效", label)
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); ip != nil {
			if isBlockedConnectorIP(ip, allowLocalOrPrivate) {
				return nil, fmt.Errorf("%s地址不允许访问", label)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("%s地址无法解析", label)
		}
		for _, candidate := range ips {
			if isBlockedConnectorIP(candidate.IP, allowLocalOrPrivate) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		}
		return nil, fmt.Errorf("%s地址不允许访问", label)
	}
}

func newGuardedHTTPTransport(allowLocalOrPrivate bool, label string) *http.Transport {
	return &http.Transport{
		// Do not use ProxyFromEnvironment: outbound connections must always pass
		// through the SSRF-guarded DialContext above.
		Proxy:                 nil,
		DialContext:           newSSRFGuardedDialContext(15*time.Second, allowLocalOrPrivate, label),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func isLocalOrPrivateHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(h); err == nil {
		h = parsedHost
	}
	h = strings.Trim(h, "[]")
	low := strings.ToLower(h)
	if low == "localhost" || low == "host.docker.internal" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil || isAlwaysBlockedIP(ip) {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func newNativeHTTPClient(timeout time.Duration, endpoint string, allowLocalInsecureTLS bool) (*http.Client, error) {
	return newNativeHTTPClientWithPolicy(timeout, endpoint, allowLocalInsecureTLS, true)
}

func newNativeHTTPClientWithPolicy(timeout time.Duration, endpoint string, allowLocalInsecureTLS, allowLocalOrPrivate bool) (*http.Client, error) {
	if timeout <= 0 {
		timeout = nativeDefaultTimeout
	}
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" {
		return nil, apperr.New(apperr.Invalid, "连接器服务地址格式无效")
	}
	if err := validateConnectorEndpointIP(endpoint, allowLocalOrPrivate, "连接器"); err != nil {
		return nil, err
	}
	transport := newGuardedHTTPTransport(allowLocalOrPrivate, "连接器")
	if allowLocalInsecureTLS {
		if !isLocalOrPrivateHost(u.Hostname()) {
			return nil, apperr.New(apperr.Invalid, "仅允许对本地或内网地址跳过自签名证书校验")
		}
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // Explicitly restricted to local/private hosts above.
			MinVersion:         tls.VersionTLS12,
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			if len(via) > nativeMaxRedirects {
				return fmt.Errorf("上游重定向次数超过 %d 次", nativeMaxRedirects)
			}
			origin := via[0].URL
			if origin == nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
				return errors.New("拒绝跨源重定向，避免凭据泄露")
			}
			return nil
		},
	}, nil
}

func isConnectionRefusedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

func isLoopbackHostname(host string) bool {
	low := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	return low == "127.0.0.1" || low == "localhost" || low == "::1"
}

func doRequestWithDockerFallback(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err == nil {
		return resp, nil
	}
	if !dockerEnvCheckHook() || req == nil || req.URL == nil || !isLoopbackHostname(req.URL.Hostname()) || !isConnectionRefusedErr(err) {
		return nil, err
	}

	retryReq := req.Clone(req.Context())
	retryURL := *req.URL
	if port := retryURL.Port(); port != "" {
		retryURL.Host = "host.docker.internal:" + port
	} else {
		retryURL.Host = "host.docker.internal"
	}
	retryReq.URL = &retryURL
	retryReq.Host = retryURL.Host
	if req.GetBody != nil {
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return nil, err
		}
		retryReq.Body = body
	}
	return client.Do(retryReq)
}

func stripTokenPrefix(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 7 && strings.EqualFold(s[:7], "Bearer ") {
		return strings.TrimSpace(s[7:])
	}
	if len(s) >= 6 && strings.EqualFold(s[:6], "Token ") {
		return strings.TrimSpace(s[6:])
	}
	return s
}

func parseBoolExtra(extra map[string]string, key string, defaultVal bool) bool {
	if len(extra) == 0 {
		return defaultVal
	}
	raw := strings.ToLower(strings.TrimSpace(extra[key]))
	switch raw {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return defaultVal
	}
}

func normalizeExtraMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractSearchKBIDs(args map[string]any) []string {
	reqIDs := argStringList(args["knowledge_base_ids"])
	if len(reqIDs) == 0 {
		reqIDs = argStringList(args["sources"])
	}
	if len(reqIDs) == 0 {
		if single := strings.TrimSpace(argString(args["knowledge_base_id"])); single != "" {
			reqIDs = []string{single}
		}
	}
	return reqIDs
}

func extractReadDocID(args map[string]any) string {
	for _, key := range []string{"knowledge_id", "document_id", "doc_id", "path", "id"} {
		if s := strings.TrimSpace(argString(args[key])); s != "" {
			return s
		}
	}
	return ""
}

func splitDocumentChunks(text string, maxChunkRunes int) []string {
	if maxChunkRunes <= 0 {
		maxChunkRunes = nativeDefaultChunkRunes
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	var cur strings.Builder
	curRunes := 0

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
		curRunes = 0
	}

	appendSegment := func(seg string) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return
		}
		r := []rune(seg)
		if len(r) <= maxChunkRunes {
			sepLen := 0
			if curRunes > 0 {
				sepLen = 2
			}
			if curRunes > 0 && curRunes+sepLen+len(r) > maxChunkRunes {
				flush()
			}
			if curRunes > 0 {
				cur.WriteString("\n\n")
				curRunes += 2
			}
			cur.WriteString(seg)
			curRunes += len(r)
			return
		}
		flush()
		// Try splitting a huge paragraph by single newlines first.
		lines := strings.Split(seg, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lr := []rune(line)
			if len(lr) > maxChunkRunes {
				flush()
				for start := 0; start < len(lr); start += maxChunkRunes {
					end := start + maxChunkRunes
					if end > len(lr) {
						end = len(lr)
					}
					if part := strings.TrimSpace(string(lr[start:end])); part != "" {
						chunks = append(chunks, part)
					}
				}
				continue
			}
			sepLen := 0
			if curRunes > 0 {
				sepLen = 1
			}
			if curRunes > 0 && curRunes+sepLen+len(lr) > maxChunkRunes {
				flush()
			}
			if curRunes > 0 {
				cur.WriteByte('\n')
				curRunes++
			}
			cur.WriteString(line)
			curRunes += len(lr)
		}
		flush()
	}

	for _, p := range paragraphs {
		appendSegment(p)
	}
	flush()
	return chunks
}

type nativeReadDocChunk struct {
	ChunkIndex int    `json:"chunk_index"`
	Content    string `json:"content"`
}

type nativeReadDocParams struct {
	ConnectionID string
	SourceID     string
	SourceName   string
	DocumentID   string
	Title        string
	FileName     string
	Content      string
	Offset       int
	Limit        int
	Query        string
	ExtraFields  map[string]any
	Started      time.Time
}

func buildReadDocumentResult(p nativeReadDocParams) (CallResult, error) {
	offset := p.Offset
	if offset < 0 {
		offset = 0
	}
	limit := p.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	allChunks := splitDocumentChunks(p.Content, nativeDefaultChunkRunes)
	total := len(allChunks)

	var collected []nativeReadDocChunk
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		for i := offset; i < end; i++ {
			collected = append(collected, nativeReadDocChunk{
				ChunkIndex: i,
				Content:    allChunks[i],
			})
		}
	}

	if q := strings.ToLower(strings.TrimSpace(p.Query)); q != "" && len(collected) > 0 {
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
		collected = []nativeReadDocChunk{}
	}

	status := domain.KnowledgeStatusHit
	if len(collected) == 0 {
		status = domain.KnowledgeStatusEmpty
	}

	title := firstNonEmpty(p.Title, p.FileName, p.DocumentID)
	fileName := strings.TrimSpace(p.FileName)

	payload := map[string]any{
		"knowledge_id": p.DocumentID,
		"total":        total,
		"offset":       offset,
		"limit":        limit,
		"chunks":       collected,
		"title":        title,
	}
	if fileName != "" {
		payload["file_name"] = fileName
	}
	if strings.TrimSpace(p.SourceID) != "" {
		payload["knowledge_base_id"] = strings.TrimSpace(p.SourceID)
	}
	if strings.TrimSpace(p.SourceName) != "" {
		payload["knowledge_base_name"] = strings.TrimSpace(p.SourceName)
	}
	for k, v := range p.ExtraFields {
		if k != "" && v != nil {
			payload[k] = v
		}
	}

	evidences := make([]domain.KnowledgeEvidence, 0, len(collected))
	var targets []string
	if len(collected) > 0 {
		for _, ch := range collected {
			evidences = append(evidences, domain.KnowledgeEvidence{
				ConnectionID: p.ConnectionID,
				SourceID:     strings.TrimSpace(p.SourceID),
				SourceName:   clipCatalogText(p.SourceName, 120),
				DocumentID:   p.DocumentID,
				Title:        clipCatalogText(title, 120),
				FileName:     clipCatalogText(fileName, 120),
				Locator:      fmt.Sprintf("chunk #%d", ch.ChunkIndex),
			})
		}
		if lbl := clipCatalogText(firstNonEmpty(fileName, title, p.DocumentID), 120); lbl != "" {
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
		LatencyMs: time.Since(p.Started).Milliseconds(),
	}, err
}

func buildListKBsResult(sources []Source, scope SourceScope, started time.Time) (CallResult, error) {
	kept := make([]Source, 0, len(sources))
	var targets []string
	for _, src := range sources {
		if !scope.Allows(src.ID) {
			continue
		}
		kept = append(kept, src)
		if len(targets) < maxKnowledgeReadTargets {
			if name := firstNonEmpty(src.Name, src.ID); name != "" {
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
		return CallResult{Status: domain.KnowledgeStatusUnavailable, LatencyMs: time.Since(started).Milliseconds()}, err
	}
	return CallResult{
		Status:    status,
		Text:      string(raw),
		Hits:      len(kept),
		Targets:   targets,
		Citeable:  false,
		LatencyMs: time.Since(started).Milliseconds(),
	}, nil
}

func paginateSlice[T any](items []T, page, pageSize int) ([]T, int, int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		return []T{}, total, page, pageSize
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return items[start:end], total, page, pageSize
}

func extractKeywordSnippet(content, query string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 240
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return clipText(content, maxRunes)
	}
	words := strings.Fields(q)
	all := []rune(content)
	bestRuneIdx := -1
	for _, w := range words {
		wRunes := []rune(w)
		if len(wRunes) == 0 {
			continue
		}
		if idx := indexRunesFold(all, wRunes); idx >= 0 {
			if bestRuneIdx < 0 || idx < bestRuneIdx {
				bestRuneIdx = idx
			}
		}
	}
	if bestRuneIdx < 0 {
		return clipText(content, maxRunes)
	}
	start := bestRuneIdx - maxRunes/4
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(all) {
		end = len(all)
	}
	snippet := strings.TrimSpace(string(all[start:end]))
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(all) {
		snippet += "…"
	}
	return snippet
}

func indexRunesFold(haystack, needle []rune) int {
	n := len(needle)
	if n == 0 || len(haystack) < n {
		return -1
	}
	limit := len(haystack) - n
	for i := 0; i <= limit; i++ {
		if strings.EqualFold(string(haystack[i:i+n]), string(needle)) {
			return i
		}
	}
	return -1
}

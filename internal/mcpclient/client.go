package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 16 * 1024 * 1024
	maxRedirects     = 5
)

// HTTPError is returned when the MCP gateway answers with a non-2xx status.
// It keeps the WWW-Authenticate challenge so callers can start an OAuth
// discovery flow, and redacts credential-looking body fragments in Error().
type HTTPError struct {
	StatusCode      int
	WWWAuthenticate string
	RetryAfter      string
	Body            string
}

// RPCError is returned when the remote MCP server returns a JSON-RPC error object.
type RPCError struct {
	Code    int
	Message string
	Label   string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp %s error [%d]: %s", e.Label, e.Code, e.Message)
}

func (e *HTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("gateway HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("gateway HTTP %d: %s", e.StatusCode, redactHTTPErrorBody(body))
}

var (
	httpErrorBearerRe = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	httpErrorParamRe  = regexp.MustCompile(`(?i)("?(?:access_token|refresh_token|id_token|client_secret)"?\s*[:=]\s*"?)([^"&\s,}]+)`)
)

func redactHTTPErrorBody(body string) string {
	body = httpErrorBearerRe.ReplaceAllString(body, "Bearer ***")
	body = httpErrorParamRe.ReplaceAllString(body, "${1}***")
	if r := []rune(body); len(r) > 512 {
		body = string(r[:512]) + "…"
	}
	return body
}

// Client is a minimal Streamable HTTP MCP client that sends JSON-RPC 2.0
// tool-call requests to an MCP gateway and parses the response.
type Client struct {
	url    string
	hdrs   map[string]string
	http   *http.Client
	mu     sync.Mutex
	initMu sync.Mutex
	nextID int64
	init   bool
	// sessionID is the Mcp-Session-Id handed out by initialize; it is echoed
	// on every follow-up request and dropped when the gateway forgets it (404).
	sessionID string
	// sessionGen increments on every reset/re-init so a late 404 can only reset
	// the generation it actually used.
	sessionGen uint64
}

func New(url string, headers map[string]string) *Client {
	return &Client{
		url:  normalizeEndpointURL(url),
		hdrs: headers,
		http: &http.Client{
			Transport:     newGuardedTransport(),
			Timeout:       60 * time.Second,
			CheckRedirect: checkSameOriginRedirect,
		},
	}
}

func checkSameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) > maxRedirects {
		return fmt.Errorf("mcp redirect limit (%d) exceeded", maxRedirects)
	}
	origin := via[0].URL
	if origin == nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
		return errors.New("mcp refuses cross-origin redirect")
	}
	return nil
}

// WithTransport sets a custom HTTP RoundTripper on this client and returns it.
func (c *Client) WithTransport(rt http.RoundTripper) *Client {
	if c != nil && rt != nil {
		c.http.Transport = rt
	}
	return c
}

// normalizeEndpointURL trims a trailing slash on the path only: query
// parameters (e.g. company_from) are part of the endpoint identity.
func normalizeEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Fragment = ""
	u.User = nil
	return u.String()
}

// SetHeaders updates the HTTP headers (e.g., auth tokens) at runtime.
func (c *Client) SetHeaders(headers map[string]string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.hdrs = headers
	c.mu.Unlock()
}

// Header returns one request header, or empty if the client is nil.
func (c *Client) Header(key string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.hdrs {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// Clone returns a client that shares the same URL and headers with an optional
// request timeout. The original client is not mutated.
func (c *Client) Clone(timeout time.Duration) *Client {
	if c == nil {
		return nil
	}
	return New(c.url, cloneHeaders(c.HeaderSnapshot())).WithTransport(c.http.Transport).WithTimeout(timeout)
}

// HeaderSnapshot returns a copy of the current request headers.
func (c *Client) HeaderSnapshot() map[string]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneHeaders(c.hdrs)
}

// WithTimeout sets the HTTP timeout on this client and returns it.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	if c != nil && timeout > 0 {
		c.http.Timeout = timeout
	}
	return c
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// CallTool invokes a named MCP tool with the given arguments and returns the
// text content of the first result block.
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	return c.CallToolContext(context.Background(), name, args)
}

// CallToolContext is CallTool with a caller-supplied deadline/cancellation.
func (c *Client) CallToolContext(ctx context.Context, name string, args map[string]any) (string, error) {
	text, structured, err := c.CallToolStructuredContext(ctx, name, args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" && len(structured) > 0 && string(structured) != "null" {
		return strings.TrimSpace(string(structured)), nil
	}
	return text, nil
}

// CallToolStructuredContext invokes a named MCP tool and returns both the
// concatenated text content blocks and any MCP 2025-06-18 structuredContent.
func (c *Client) CallToolStructuredContext(ctx context.Context, name string, args map[string]any) (string, json.RawMessage, error) {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	raw, err := c.callRPC(ctx, "tools/call", params, "tool "+name)
	if err != nil {
		return "", nil, err
	}

	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return strings.TrimSpace(string(raw)), nil, nil
	}
	if result.IsError {
		if len(result.Content) > 0 && strings.TrimSpace(result.Content[0].Text) != "" {
			return "", nil, fmt.Errorf("mcp tool %s: %s", name, result.Content[0].Text)
		}
		return "", nil, fmt.Errorf("mcp tool %s returned isError", name)
	}
	var textBlocks []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			textBlocks = append(textBlocks, block.Text)
		}
	}
	var structured json.RawMessage
	if trimmed := bytes.TrimSpace(result.StructuredContent); len(trimmed) > 0 && string(trimmed) != "null" {
		structured = trimmed
	}
	return strings.Join(textBlocks, "\n"), structured, nil
}

type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

// ListTools returns the remote MCP tool catalog after initialize.
func (c *Client) ListTools() ([]Tool, error) {
	return c.ListToolsContext(context.Background())
}

// ListToolsContext is ListTools with a caller-supplied deadline/cancellation.
func (c *Client) ListToolsContext(ctx context.Context) ([]Tool, error) {
	raw, err := c.callRPC(ctx, "tools/list", map[string]any{}, "tools/list")
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list result: %w", err)
	}
	return result.Tools, nil
}

// callRPC runs one JSON-RPC request after initialize. A 404 while a session
// id is held means the gateway dropped the session: the client re-handshakes
// once and retries so a rolling gateway restart does not fail the call.
func (c *Client) callRPC(ctx context.Context, method string, params any, label string) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureInit(ctx); err != nil {
			return nil, fmt.Errorf("mcp init: %w", err)
		}
		session, generation := c.snapshotSession()
		raw, err := c.doRPC(ctx, method, params, session)
		if err == nil {
			var rpcResp rpcResponse
			if uerr := json.Unmarshal(raw, &rpcResp); uerr != nil {
				return nil, fmt.Errorf("mcp decode %s: %w (body: %s)", label, uerr, string(raw))
			}
			if rpcResp.Error != nil {
				return nil, &RPCError{Code: rpcResp.Error.Code, Message: rpcResp.Error.Message, Label: label}
			}
			return rpcResp.Result, nil
		}
		var he *HTTPError
		if attempt == 0 && errorsAsHTTP(err, &he) && he.StatusCode == http.StatusNotFound && session != "" {
			c.resetSessionIf(session, generation)
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("mcp %s: session retry exhausted", label)
}

// doRPC posts one JSON-RPC request with the session generation captured by the
// caller and returns the result envelope bytes.
func (c *Client) doRPC(ctx context.Context, method string, params any, session string) (json.RawMessage, error) {
	id := c.nextRequestID()
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := c.newHTTPRequest(ctx, body, session)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp request %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return nil, &HTTPError{
			StatusCode:      resp.StatusCode,
			WWWAuthenticate: resp.Header.Get("WWW-Authenticate"),
			RetryAfter:      resp.Header.Get("Retry-After"),
			Body:            strings.TrimSpace(string(raw)),
		}
	}
	raw, err := c.readResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("mcp response %s: %w", method, err)
	}
	// A JSON body shaped like {"error": "..."} with neither result nor id is a
	// gateway-level error, not a JSON-RPC envelope.
	var gatewayErr struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &gatewayErr) == nil && gatewayErr.Error != "" {
		var check map[string]json.RawMessage
		if json.Unmarshal(raw, &check) == nil {
			if _, hasResult := check["result"]; !hasResult {
				if _, hasID := check["id"]; !hasID {
					return nil, fmt.Errorf("mcp gateway error: %s", gatewayErr.Error)
				}
			}
		}
	}
	return raw, nil
}

func errorsAsHTTP(err error, target **HTTPError) bool {
	if he, ok := err.(*HTTPError); ok {
		*target = he
		return true
	}
	return false
}

func (c *Client) nextRequestID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *Client) currentSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// snapshotSession returns the current session id and its generation.
func (c *Client) snapshotSession() (string, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID, c.sessionGen
}

// resetSessionIf clears the session only when it is still the generation that
// produced the 404; a peer that already re-handshook keeps its new session.
func (c *Client) resetSessionIf(session string, generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if session == "" || c.sessionID != session || c.sessionGen != generation {
		return false
	}
	c.sessionID = ""
	c.init = false
	c.sessionGen++
	return true
}

// newHTTPRequest builds a POST request and applies the current headers plus the
// caller-provided session id (never re-read from the client, to keep one
// request on one generation).
func (c *Client) newHTTPRequest(ctx context.Context, body []byte, session string) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	for k, v := range c.hdrs {
		httpReq.Header.Set(k, v)
	}
	if session != "" {
		httpReq.Header.Set("Mcp-Session-Id", session)
	}
	c.mu.Unlock()
	return httpReq, nil
}

func (c *Client) ensureInit(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.init {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	c.initMu.Lock()
	defer c.initMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.init {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	var latestSession string
	for idx, version := range []string{"2025-06-18", "2024-11-05"} {
		id := c.nextRequestID()
		req := rpcRequest{
			JSONRPC: "2.0",
			ID:      id,
			Method:  "initialize",
			Params: map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{},
				"clientInfo": map[string]any{
					"name":    "agentflow",
					"version": "1.0.0",
				},
			},
		}
		body, _ := json.Marshal(req)
		httpReq, err := c.newHTTPRequest(ctx, body, "")
		if err != nil {
			return err
		}
		resp, err := c.http.Do(httpReq)
		if err != nil {
			return err
		}
		// Authentication failures must reach the caller so an OAuth connector can
		// refresh and retry; other initialize failures stay non-fatal as before.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			return &HTTPError{
				StatusCode:      resp.StatusCode,
				WWWAuthenticate: resp.Header.Get("WWW-Authenticate"),
				RetryAfter:      resp.Header.Get("Retry-After"),
				Body:            strings.TrimSpace(string(raw)),
			}
		}
		if sid := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sid != "" {
			latestSession = sid
		}
		raw, readErr := c.readResponse(resp)
		statusCode := resp.StatusCode
		resp.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
		var rpcResp rpcResponse
		hasRPCError := json.Unmarshal(raw, &rpcResp) == nil && rpcResp.Error != nil
		// If the server rejected 2025-06-18 (HTTP 400 or JSON-RPC error), fall back once to 2024-11-05.
		if idx == 0 && (statusCode == http.StatusBadRequest || hasRPCError) {
			continue
		}
		break
	}

	c.mu.Lock()
	if latestSession != "" {
		c.sessionID = latestSession
		c.sessionGen++
	}
	c.nextID++
	session := c.sessionID
	c.init = true
	c.mu.Unlock()

	notifReq := rpcRequest{
		JSONRPC: "2.0",
		ID:      0,
		Method:  "notifications/initialized",
	}
	notifBody, _ := json.Marshal(notifReq)
	notifHTTP, _ := c.newHTTPRequest(ctx, notifBody, session)
	if notifHTTP != nil {
		if notifResp, err := c.http.Do(notifHTTP); err == nil {
			notifResp.Body.Close()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (c *Client) readResponse(resp *http.Response) ([]byte, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return c.readSSE(io.LimitReader(resp.Body, maxResponseBytes))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("mcp response exceeds %d bytes", maxResponseBytes)
	}
	return raw, nil
}

func (c *Client) readSSE(body io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				dataLines = dataLines[:0]
				var rpcResp rpcResponse
				if json.Unmarshal([]byte(data), &rpcResp) == nil && rpcResp.ID > 0 {
					if rpcResp.Result != nil {
						return json.Marshal(rpcResp)
					}
					if rpcResp.Error != nil {
						return json.Marshal(rpcResp)
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("SSE read error: %w", err)
	}
	if len(dataLines) > 0 {
		return []byte(strings.Join(dataLines, "\n")), nil
	}
	return nil, fmt.Errorf("no data in SSE stream")
}

func cloneHeaders(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

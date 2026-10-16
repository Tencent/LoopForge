package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"

	"agentflow/internal/application"
)

const (
	serverName    = "agentflow-platform"
	serverVersion = "1.0.0"
)

type ToolInput struct {
	Path  map[string]string `json:"path,omitempty"`
	Query map[string]string `json:"query,omitempty"`
	Body  any               `json:"body,omitempty"`
}

type InvokeResult struct {
	Status      int
	ContentType string
	Body        []byte
}

type Invoker interface {
	Invoke(context.Context, application.Operation, ToolInput) (InvokeResult, error)
}

// incomingAuth carries browser Cookie / Authorization from the outer /mcp
// request onto LocalInvoker synthetic API calls. Without this, login-gated
// endpoints (e.g. runs_group_invite → POST /api/runs/:id/group/members) see
// an anonymous request and return 请先登录 even when the user is signed in.
type incomingAuthKey struct{}

type incomingAuth struct {
	Cookie        string
	Authorization string
}

func withIncomingAuth(ctx context.Context, r *http.Request) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return ctx
	}
	cookie := strings.TrimSpace(r.Header.Get("Cookie"))
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if cookie == "" && authz == "" {
		return ctx
	}
	return context.WithValue(ctx, incomingAuthKey{}, incomingAuth{Cookie: cookie, Authorization: authz})
}

func applyIncomingAuth(req *http.Request, ctx context.Context) {
	if req == nil || ctx == nil {
		return
	}
	auth, ok := ctx.Value(incomingAuthKey{}).(incomingAuth)
	if !ok {
		return
	}
	if auth.Cookie != "" && req.Header.Get("Cookie") == "" {
		req.Header.Set("Cookie", auth.Cookie)
	}
	if auth.Authorization != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", auth.Authorization)
	}
}

type LocalInvoker struct {
	Handler http.Handler
}

func (i LocalInvoker) Invoke(ctx context.Context, op application.Operation, input ToolInput) (InvokeResult, error) {
	if i.Handler == nil {
		return InvokeResult{}, errors.New("AgentFlow API handler is unavailable")
	}
	path, err := buildPath(op.Path, input)
	if err != nil {
		return InvokeResult{}, err
	}
	body, err := encodeBody(input.Body)
	if err != nil {
		return InvokeResult{}, err
	}
	req := httptest.NewRequest(op.Method, path, body).WithContext(ctx)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyIncomingAuth(req, ctx)
	rec := httptest.NewRecorder()
	i.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return InvokeResult{}, err
	}
	return InvokeResult{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw}, nil
}

type RemoteInvoker struct {
	BaseURL  string
	Username string
	Password string
	Token    string
	Client   *http.Client
}

func (i RemoteInvoker) Invoke(ctx context.Context, op application.Operation, input ToolInput) (InvokeResult, error) {
	base := strings.TrimRight(strings.TrimSpace(i.BaseURL), "/")
	if base == "" {
		return InvokeResult{}, errors.New("AGENTFLOW_API_URL is required")
	}
	path, err := buildPath(op.Path, input)
	if err != nil {
		return InvokeResult{}, err
	}
	body, err := encodeBody(input.Body)
	if err != nil {
		return InvokeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, base+path, body)
	if err != nil {
		return InvokeResult{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(i.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(i.Token))
	} else if i.Username != "" || i.Password != "" {
		req.SetBasicAuth(i.Username, i.Password)
	}
	client := i.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return InvokeResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return InvokeResult{}, err
	}
	return InvokeResult{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw}, nil
}

type Server struct {
	invoker    Invoker
	operations map[string]application.Operation
}

func New(invoker Invoker) *Server {
	operations := map[string]application.Operation{}
	for _, op := range application.PublicOperations() {
		operations[op.Name] = op
	}
	return &Server{invoker: invoker, operations: operations}
}

func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", "POST, DELETE")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "stateless Streamable HTTP accepts JSON-RPC POST requests"})
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request rpcRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
		decoder.UseNumber()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcErrorResponse(nil, -32700, "parse error", err.Error()))
			return
		}
		response := s.handle(withIncomingAuth(r.Context(), r), request)
		if response == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("MCP-Protocol-Version", protocolVersion(request))
		writeJSON(w, http.StatusOK, response)
	})
}

func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(rpcErrorResponse(nil, -32700, "parse error", err.Error())); err != nil {
				return err
			}
			continue
		}
		if response := s.handle(ctx, request); response != nil {
			if err := encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func ServeRemoteStdio(ctx context.Context) error {
	return New(RemoteInvoker{
		BaseURL:  os.Getenv("AGENTFLOW_API_URL"),
		Username: os.Getenv("AGENTFLOW_API_USER"),
		Password: os.Getenv("AGENTFLOW_API_PASS"),
		Token:    os.Getenv("AGENTFLOW_API_TOKEN"),
	}).ServeStdio(ctx, os.Stdin, os.Stdout)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) handle(ctx context.Context, request rpcRequest) *rpcResponse {
	if strings.HasPrefix(request.Method, "notifications/") {
		return nil
	}
	switch request.Method {
	case "initialize":
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
			"protocolVersion": protocolVersion(request),
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
			},
			"serverInfo":   map[string]string{"name": serverName, "version": serverVersion},
			"instructions": "AgentFlow business operations. Mutating tools can start agents, change external trackers, or alter saved definitions.",
		}}
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": s.toolList()}}
	case "tools/call":
		result, err := s.callTool(ctx, request.Params)
		if err != nil {
			return rpcErrorResponse(request.ID, -32602, "invalid tool call", err.Error())
		}
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
	case "resources/list":
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"resources": []any{}}}
	case "resources/templates/list":
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"resourceTemplates": resourceTemplates()}}
	case "resources/read":
		result, err := s.readResource(ctx, request.Params)
		if err != nil {
			return rpcErrorResponse(request.ID, -32602, "resource read failed", err.Error())
		}
		return &rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
	default:
		return rpcErrorResponse(request.ID, -32601, "method not found", request.Method)
	}
}

func protocolVersion(request rpcRequest) string {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(request.Params, &params)
	switch params.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return params.ProtocolVersion
	default:
		return "2025-06-18"
	}
}

func (s *Server) toolList() []map[string]any {
	names := make([]string, 0, len(s.operations))
	for name := range s.operations {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		op := s.operations[name]
		tools = append(tools, map[string]any{
			"name":        op.Name,
			"description": op.Description + " (" + op.Method + " " + op.Path + ")",
			"inputSchema": toolInputSchema(op),
			"annotations": map[string]any{
				"readOnlyHint":    op.ReadOnly,
				"destructiveHint": !op.ReadOnly && (op.Method == "DELETE" || strings.Contains(op.Name, "reject") || strings.Contains(op.Name, "cancel")),
				"idempotentHint":  op.ReadOnly || op.Method == "PUT" || op.Method == "DELETE",
			},
		})
	}
	return tools
}

func toolInputSchema(op application.Operation) map[string]any { return ToolInputSchema(op) }

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	op, ok := s.operations[params.Name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", params.Name)
	}
	args := map[string]any{}
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return toolError("arguments must be an object", 400), nil
		}
	}
	if err := ValidateToolInput(op, args); err != nil {
		return toolError(err.Error(), 400), nil
	}
	var input ToolInput
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		decoder := json.NewDecoder(bytes.NewReader(params.Arguments))
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			return nil, err
		}
	}
	result, err := s.invoker.Invoke(ctx, op, input)
	if err != nil {
		return toolError(err.Error(), 0), nil
	}
	return resultAsTool(result), nil
}

func resultAsTool(result InvokeResult) map[string]any {
	text := string(result.Body)
	if text == "" {
		text = http.StatusText(result.Status)
	} else if !strings.HasSuffix(text, "\n") {
		// gin c.JSON omits the trailing newline that master's
		// json.Encoder.Encode writes; MCP content is the raw HTTP body.
		text += "\n"
	}
	output := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": result.Status < 200 || result.Status >= 300,
	}
	var structured any
	if json.Unmarshal(result.Body, &structured) == nil {
		output["structuredContent"] = map[string]any{"status": result.Status, "data": structured}
	}
	return output
}

func toolError(message string, status int) map[string]any {
	data := map[string]any{"error": message}
	if status != 0 {
		data["status"] = status
	}
	raw, _ := json.Marshal(data)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}, "structuredContent": data, "isError": true}
}

func resourceTemplates() []map[string]any {
	return []map[string]any{
		{"uriTemplate": "agentflow://runs/{runId}/artifacts/{artifactId}", "name": "Run artifact", "description": "Immutable artifact captured from a Run", "mimeType": "text/markdown"},
		{"uriTemplate": "agentflow://runs/{runId}/logs", "name": "Run worker logs", "description": "Complete worker log export for a Run", "mimeType": "text/plain"},
	}
}

func (s *Server) readResource(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(params.URI)
	if err != nil || parsed.Scheme != "agentflow" || parsed.Host != "runs" {
		return nil, errors.New("unsupported resource URI")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	var op application.Operation
	input := ToolInput{Path: map[string]string{}}
	switch {
	case len(parts) == 2 && parts[1] == "logs":
		op = s.operations["runs_logs_download"]
		input.Path["id"] = parts[0]
	case len(parts) == 3 && parts[1] == "artifacts":
		op = s.operations["runs_artifact_get"]
		input.Path["id"] = parts[0]
		input.Path["artifactId"] = parts[2]
	default:
		return nil, errors.New("unsupported resource URI")
	}
	result, err := s.invoker.Invoke(ctx, op, input)
	if err != nil {
		return nil, err
	}
	if result.Status < 200 || result.Status >= 300 {
		return nil, fmt.Errorf("AgentFlow API returned %d: %s", result.Status, strings.TrimSpace(string(result.Body)))
	}
	mime := strings.TrimSpace(strings.Split(result.ContentType, ";")[0])
	if mime == "" {
		mime = "text/plain"
	}
	return map[string]any{"contents": []map[string]any{{"uri": params.URI, "mimeType": mime, "text": string(result.Body)}}}, nil
}

func buildPath(pathTemplate string, input ToolInput) (string, error) {
	path := pathTemplate
	for _, name := range pathParameterNames(pathTemplate) {
		value := strings.TrimSpace(input.Path[name])
		if value == "" {
			return "", fmt.Errorf("path.%s is required", name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	if len(input.Query) > 0 {
		query := url.Values{}
		for key, value := range input.Query {
			query.Set(key, value)
		}
		path += "?" + query.Encode()
	}
	return path, nil
}

func pathParameterNames(path string) []string {
	var names []string
	for {
		start := strings.IndexByte(path, '{')
		if start < 0 {
			break
		}
		end := strings.IndexByte(path[start+1:], '}')
		if end < 0 {
			break
		}
		name := path[start+1 : start+1+end]
		if name != "" {
			names = append(names, name)
		}
		path = path[start+end+2:]
	}
	return names
}

func encodeBody(value any) (io.Reader, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(raw), nil
}

func rpcErrorResponse(id any, code int, message string, data any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

package gatemcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const Version = "1.0.0"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type finding struct {
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
	Evidence string `json:"evidence,omitempty"`
}

type gateResult struct {
	Decision       string    `json:"decision"`
	Reason         string    `json:"reason"`
	ActionRequired string    `json:"actionRequired"`
	Summary        string    `json:"summary"`
	Findings       []finding `json:"findings"`
}

var inputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"decision", "reason"},
	"properties": map[string]any{
		"decision":       map[string]any{"type": "string", "enum": []string{"pass", "rework", "fail", "human"}},
		"reason":         map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
		"actionRequired": map[string]any{"type": "string", "maxLength": 4000},
		"summary":        map[string]any{"type": "string", "maxLength": 4000},
		"findings": map[string]any{
			"type": "array", "maxItems": 50,
			"items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"title"},
				"properties": map[string]any{
					"severity": map[string]any{"type": "string", "maxLength": 64},
					"title":    map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
					"evidence": map[string]any{"type": "string", "maxLength": 4000},
				},
			},
		},
	},
}

// Run serves the small JSON-RPC MCP protocol over stdin/stdout.
func Run(stdin io.Reader, stdout io.Writer, resultPath string) error {
	resultPath = strings.TrimSpace(resultPath)
	if resultPath == "" {
		return fmt.Errorf("AGENTFLOW_GATE_RESULT_PATH is required")
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if writeErr := writeResponse(encoder, nil, nil, err.Error()); writeErr != nil {
				return writeErr
			}
			continue
		}
		if strings.HasPrefix(req.Method, "notifications/") {
			continue
		}
		if err := handleRequest(encoder, req, resultPath); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleRequest(encoder *json.Encoder, req request, resultPath string) error {
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.ProtocolVersion == "" {
			params.ProtocolVersion = "2025-06-18"
		}
		return writeResponse(encoder, req.ID, map[string]any{
			"protocolVersion": params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "agentflow-gate", "version": Version},
			"instructions":    "Call submit_gate_result exactly once to finish this node.",
		}, "")
	case "ping":
		return writeResponse(encoder, req.ID, map[string]any{}, "")
	case "tools/list":
		return writeResponse(encoder, req.ID, map[string]any{"tools": []any{map[string]any{
			"name":        "submit_gate_result",
			"description": "Submit exactly one terminal node decision. reason explains why. human also requires actionRequired to state exactly what a person must confirm.",
			"inputSchema": inputSchema,
		}}}, "")
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return writeResponse(encoder, req.ID, nil, "invalid tool parameters")
		}
		if params.Name != "submit_gate_result" {
			return writeResponse(encoder, req.ID, nil, "unknown tool")
		}
		result, problem := decodeResult(params.Arguments)
		if problem != "" {
			return writeResponse(encoder, req.ID, nil, problem)
		}
		if err := writeResultExclusive(resultPath, result); err != nil {
			if os.IsExist(err) {
				return writeResponse(encoder, req.ID, nil, "gate result already submitted")
			}
			return writeResponse(encoder, req.ID, nil, err.Error())
		}
		return writeResponse(encoder, req.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Gate result submitted. Stop now; this node has reached its terminal decision."}},
			"isError": false,
		}, "")
	default:
		return writeResponse(encoder, req.ID, nil, "method not found")
	}
}

func decodeResult(raw json.RawMessage) (gateResult, string) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return gateResult{}, "arguments must be an object"
	}
	allowed := map[string]bool{"decision": true, "reason": true, "actionRequired": true, "summary": true, "findings": true}
	var unknown []string
	for key := range fields {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return gateResult{}, "unknown fields: " + strings.Join(unknown, ", ")
	}
	if rawFindings, ok := fields["findings"]; ok {
		var items []map[string]json.RawMessage
		if string(rawFindings) == "null" || json.Unmarshal(rawFindings, &items) != nil {
			return gateResult{}, "findings must be an array with at most 50 items"
		}
		allowedFinding := map[string]bool{"severity": true, "title": true, "evidence": true}
		for _, item := range items {
			for key := range item {
				if !allowedFinding[key] {
					return gateResult{}, "finding contains unknown fields"
				}
			}
		}
	}
	var result gateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return gateResult{}, "arguments must match the submit_gate_result schema"
	}
	if result.Findings == nil {
		result.Findings = []finding{}
	}
	switch result.Decision {
	case "pass", "rework", "fail", "human":
	default:
		return gateResult{}, "decision must be pass, rework, fail, or human"
	}
	result.Reason = strings.TrimSpace(result.Reason)
	if result.Reason == "" {
		return gateResult{}, "reason is required"
	}
	if len(result.Reason) > 4000 {
		return gateResult{}, "reason is too long"
	}
	result.ActionRequired = strings.TrimSpace(result.ActionRequired)
	if len(result.ActionRequired) > 4000 {
		return gateResult{}, "actionRequired must be a string with at most 4000 characters"
	}
	if result.Decision == "human" && result.ActionRequired == "" {
		return gateResult{}, "actionRequired is required when decision is human"
	}
	result.Summary = strings.TrimSpace(result.Summary)
	if len(result.Summary) > 4000 {
		return gateResult{}, "summary must be a string with at most 4000 characters"
	}
	if len(result.Findings) > 50 {
		return gateResult{}, "findings must be an array with at most 50 items"
	}
	for i := range result.Findings {
		result.Findings[i].Title = strings.TrimSpace(result.Findings[i].Title)
		if result.Findings[i].Title == "" {
			return gateResult{}, "each finding requires a title"
		}
		if len(result.Findings[i].Title) > 500 {
			return gateResult{}, "finding title is too long"
		}
		if len(result.Findings[i].Severity) > 64 {
			return gateResult{}, "finding severity is invalid"
		}
		if len(result.Findings[i].Evidence) > 4000 {
			return gateResult{}, "finding evidence is invalid"
		}
	}
	return result, ""
}

func writeResultExclusive(path string, result gateResult) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if !ok {
			_ = os.Remove(path)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(result); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeResponse(encoder *json.Encoder, id json.RawMessage, result any, problem string) error {
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if problem != "" {
		response["error"] = map[string]any{"code": -32602, "message": problem}
	} else {
		response["result"] = result
	}
	return encoder.Encode(response)
}

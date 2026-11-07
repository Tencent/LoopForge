package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPServerHTTPAndHandleBranches(t *testing.T) {
	s := New(LocalInvoker{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	})})
	handler := s.HTTPHandler()
	for _, tc := range []struct {
		method string
		body   string
		status int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodDelete, "", http.StatusNoContent},
		{http.MethodPut, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "bad", http.StatusBadRequest},
		{http.MethodPost, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, http.StatusAccepted},
	} {
		req := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != tc.status {
			t.Fatalf("%s: %d", tc.method, rr.Code)
		}
	}
	for _, method := range []string{"ping", "resources/list", "resources/templates/list", "unknown"} {
		resp := s.handle(context.Background(), rpcRequest{JSONRPC: "2.0", ID: 1, Method: method})
		if resp == nil {
			t.Fatalf("%s returned nil", method)
		}
	}
	if got := protocolVersion(rpcRequest{Params: []byte(`{"protocolVersion":"old"}`)}); got != "2025-06-18" {
		t.Fatalf("protocol = %s", got)
	}
}

func TestSchemaPrimitiveAndCompositionBranches(t *testing.T) {
	cases := []struct {
		schema map[string]any
		value  any
		ok     bool
	}{
		{map[string]any{"type": "string"}, "x", true},
		{map[string]any{"type": "string"}, 1, false},
		{map[string]any{"type": "integer"}, json.Number("2"), true},
		{map[string]any{"type": "integer"}, float64(2.5), false},
		{map[string]any{"type": "number"}, json.Number("bad"), false},
		{map[string]any{"type": "boolean"}, true, true},
		{map[string]any{"type": "boolean"}, "true", false},
		{map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, []any{"x"}, true},
		{map[string]any{"type": "array"}, "x", false},
		{map[string]any{"type": "string", "enum": []any{"a", "b"}}, "c", false},
		{map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}}, "x", true},
		{map[string]any{"allOf": []any{map[string]any{"type": "string"}, map[string]any{"enum": []any{"x"}}}}, "x", true},
		{map[string]any{"properties": map[string]any{"x": map[string]any{"type": "string"}}, "required": []any{"x"}, "additionalProperties": false}, map[string]any{"y": "x"}, false},
	}
	for i, tc := range cases {
		err := validateInputValue(tc.schema, tc.value, "value")
		if (err == nil) != tc.ok {
			t.Fatalf("%d: err=%v", i, err)
		}
	}
}

func TestMCPServerStdioAndToolErrors(t *testing.T) {
	s := New(LocalInvoker{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failed", http.StatusInternalServerError)
	})})
	input := strings.Join([]string{
		"bad",
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"bad://x"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"missing","arguments":{}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"parse error", "resource read failed", "invalid tool call"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
	if len(resourceTemplates()) != 2 || toolError("x", 400)["isError"] != true {
		t.Fatal("resource/tool helpers")
	}
	if resultAsTool(InvokeResult{Status: http.StatusNoContent})["isError"] != false {
		t.Fatal("empty successful result")
	}
}

func TestMCPServerResourceVariants(t *testing.T) {
	s := New(LocalInvoker{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("content"))
	})})
	for _, uri := range []string{
		"agentflow://runs/run-1/logs",
		"agentflow://runs/run-1/artifacts/art-1",
	} {
		raw := []byte(`{"uri":"` + uri + `"}`)
		result, err := s.readResource(context.Background(), raw)
		if err != nil || result["contents"] == nil {
			t.Fatalf("%s = %#v, %v", uri, result, err)
		}
	}
	for _, raw := range [][]byte{
		[]byte(`{`),
		[]byte(`{"uri":"agentflow://runs/run-1/unknown"}`),
		[]byte(`{"uri":"agentflow://runs/bad/logs"}`),
	} {
		if _, err := s.readResource(context.Background(), raw); err == nil {
			t.Fatalf("resource accepted: %s", raw)
		}
	}
	if _, err := s.callTool(context.Background(), []byte(`{`)); err == nil {
		t.Fatal("invalid tool params accepted")
	}
	result, err := s.callTool(context.Background(), []byte(`{"name":"projects_list","arguments":[]}`))
	if err != nil || result["isError"] != true {
		t.Fatalf("array arguments = %#v, %v", result, err)
	}
}

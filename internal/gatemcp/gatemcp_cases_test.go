package gatemcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateMCPRunProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "result.json")
	lines := []string{
		`bad`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"pass","reason":" ok "}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"pass","reason":"again"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"unknown"}`,
	}
	var out bytes.Buffer
	if err := Run(strings.NewReader(strings.Join(lines, "\n")), &out, path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already submitted") || !strings.Contains(out.String(), "method not found") {
		t.Fatalf("output = %s", out.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"decision":"pass"`) {
		t.Fatalf("result = %s, %v", raw, err)
	}
	if err := Run(strings.NewReader(""), &out, ""); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestDecodeGateResultValidation(t *testing.T) {
	valid := `{"decision":"human","reason":"why","actionRequired":"confirm","summary":"sum","findings":[{"severity":"warn","title":"title","evidence":"e"}]}`
	if result, problem := decodeResult(json.RawMessage(valid)); problem != "" || result.Decision != "human" {
		t.Fatalf("valid = %#v, %q", result, problem)
	}
	cases := []string{
		``, `null`, `[]`, `{"unknown":1}`, `{"findings":null}`,
		`{"decision":"bad","reason":"x"}`,
		`{"decision":"pass","reason":""}`,
		`{"decision":"human","reason":"x"}`,
		`{"decision":"pass","reason":"x","findings":[{"bad":1}]}`,
		`{"decision":"pass","reason":"x","findings":[{"title":""}]}`,
		`{"decision":"pass","reason":"x","findings":[{"title":"x","severity":"` + strings.Repeat("x", 65) + `"}]}`,
	}
	for _, raw := range cases {
		if _, problem := decodeResult(json.RawMessage(raw)); problem == "" {
			t.Fatalf("accepted %s", raw)
		}
	}
	for key, max := range map[string]int{"reason": 4001, "actionRequired": 4001, "summary": 4001} {
		raw := map[string]any{"decision": "pass", "reason": "x", key: strings.Repeat("x", max)}
		body, _ := json.Marshal(raw)
		if _, problem := decodeResult(body); problem == "" {
			t.Fatalf("accepted long %s", key)
		}
	}
	findings := make([]finding, 51)
	for i := range findings {
		findings[i].Title = "x"
	}
	body, _ := json.Marshal(gateResult{Decision: "pass", Reason: "x", Findings: findings})
	if _, problem := decodeResult(body); problem == "" {
		t.Fatal("accepted too many findings")
	}
}

package gatemcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunProtocolAndValidation(t *testing.T) {
	resultPath := filepath.Join(t.TempDir(), "result.json")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"human","reason":"ambiguous"}}}`,
		`{"jsonrpc":"2.0","id":31,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"pass","reason":"ok","findings":[{"title":"bad","extra":true}]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"rework","reason":"test failed","actionRequired":"fix it","findings":[{"title":"failure","evidence":"go test ./..."}]}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Run(strings.NewReader(input), &output, resultPath); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agentflow-gate", "submit_gate_result", "actionRequired is required", "finding contains unknown fields", "Gate result submitted"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
	var result gateResult
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Decision != "rework" || result.Reason != "test failed" || len(result.Findings) != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestWriteResultExclusiveAllowsOneSubmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	wins := make(chan struct{}, writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			if err := writeResultExclusive(path, gateResult{Decision: "pass", Reason: "ok"}); err == nil {
				wins <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(wins)
	if got := len(wins); got != 1 {
		t.Fatalf("successful submissions=%d, want 1", got)
	}
}

func TestDecodeRouteValues(t *testing.T) {
	for _, raw := range []string{`{"decision":"pass","reason":"ok","route":"small"}`, `{"decision":"pass","reason":"ok","route":["small","frontend"]}`} {
		result, problem := decodeResult([]byte(raw))
		if problem != "" || len(result.Route) == 0 {
			t.Fatalf("decode %s: %q", raw, problem)
		}
	}
	for _, raw := range []string{`{"decision":"pass","reason":"ok","route":null}`, `{"decision":"pass","reason":"ok","route":[5]}`, `{"decision":"pass","reason":"ok","route":""}`} {
		if _, problem := decodeResult([]byte(raw)); problem == "" {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestRouteSchemaLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route any
		valid bool
	}{
		{"label at limit", strings.Repeat("界", maxRouteLabelLen), true},
		{"label over limit", strings.Repeat("界", maxRouteLabelLen+1), false},
		{"labels at limit", makeLabels(maxRouteLabels), true},
		{"labels over limit", makeLabels(maxRouteLabels + 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"decision": "pass", "reason": "ok", "route": tc.route})
			if err != nil {
				t.Fatal(err)
			}
			_, problem := decodeResult(raw)
			if (problem == "") != tc.valid {
				t.Fatalf("problem=%q", problem)
			}
		})
	}
}

func makeLabels(n int) []string {
	labels := make([]string, n)
	for i := range labels {
		labels[i] = "label"
	}
	return labels
}

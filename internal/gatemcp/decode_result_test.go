package gatemcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeResultProblemMessages pins the exact problem text and precedence of
// every validation branch so the decodeResult split keeps behaviour unchanged.
func TestDecodeResultProblemMessages(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	finding := func(f string) string { return `{"decision":"pass","reason":"x","findings":[` + f + `]}` }
	cases := []struct{ raw, want string }{
		{``, "arguments must be an object"},
		{`null`, "arguments must be an object"},
		{`[]`, "arguments must be an object"},
		{`{"zeta":1,"alpha":2,"decision":"bad"}`, "unknown fields: alpha, zeta"},
		{`{"findings":null}`, "findings must be an array with at most 50 items"},
		{`{"findings":{}}`, "findings must be an array with at most 50 items"},
		{finding(`{"title":"x","bad":1}`), "finding contains unknown fields"},
		{`{"decision":1}`, "arguments must match the submit_gate_result schema"},
		{`{"decision":"bad","reason":""}`, "decision must be pass, rework, fail, or human"},
		{`{"decision":"pass","reason":"  "}`, "reason is required"},
		{`{"decision":"pass","reason":"` + long(4001) + `"}`, "reason is too long"},
		{`{"decision":"pass","reason":"x","actionRequired":"` + long(4001) + `"}`, "actionRequired must be a string with at most 4000 characters"},
		{`{"decision":"human","reason":"x","actionRequired":" "}`, "actionRequired is required when decision is human"},
		{`{"decision":"pass","reason":"x","summary":"` + long(4001) + `"}`, "summary must be a string with at most 4000 characters"},
		{finding(`{"title":" "}`), "each finding requires a title"},
		{finding(`{"title":"` + long(501) + `"}`), "finding title is too long"},
		{finding(`{"title":"x","severity":"` + long(65) + `"}`), "finding severity is invalid"},
		{finding(`{"title":"x","evidence":"` + long(4001) + `"}`), "finding evidence is invalid"},
	}
	for _, tc := range cases {
		if _, problem := decodeResult(json.RawMessage(tc.raw)); problem != tc.want {
			name := tc.raw
			if len(name) > 80 {
				name = name[:80] + "..."
			}
			t.Errorf("decodeResult(%s) problem = %q, want %q", name, problem, tc.want)
		}
	}
	items := strings.TrimSuffix(strings.Repeat(`{"title":"x"},`, maxFindings+1), ",")
	if _, problem := decodeResult(json.RawMessage(finding(items))); problem != "findings must be an array with at most 50 items" {
		t.Errorf("too many findings problem = %q", problem)
	}
}

func TestDecodeResultNormalizesFields(t *testing.T) {
	raw := `{"decision":"human","reason":" why ","actionRequired":" confirm ","summary":" sum ","findings":[{"title":" t ","severity":"warn"}]}`
	result, problem := decodeResult(json.RawMessage(raw))
	if problem != "" {
		t.Fatalf("problem = %q", problem)
	}
	if result.Reason != "why" || result.ActionRequired != "confirm" || result.Summary != "sum" || result.Findings[0].Title != "t" || result.Findings[0].Severity != "warn" {
		t.Fatalf("result = %#v", result)
	}
	if result, problem := decodeResult(json.RawMessage(`{"decision":"pass","reason":"x"}`)); problem != "" || result.Findings == nil {
		t.Fatalf("findings should default to empty slice: %#v, %q", result, problem)
	}
}

func TestInputSchemaUsesSharedLimits(t *testing.T) {
	props := inputSchema["properties"].(map[string]any)
	findings := props["findings"].(map[string]any)
	itemProps := findings["items"].(map[string]any)["properties"].(map[string]any)
	checks := map[string]struct {
		got  any
		want int
	}{
		"reason":         {props["reason"].(map[string]any)["maxLength"], maxTextLen},
		"actionRequired": {props["actionRequired"].(map[string]any)["maxLength"], maxTextLen},
		"summary":        {props["summary"].(map[string]any)["maxLength"], maxTextLen},
		"findings":       {findings["maxItems"], maxFindings},
		"title":          {itemProps["title"].(map[string]any)["maxLength"], maxTitleLen},
		"severity":       {itemProps["severity"].(map[string]any)["maxLength"], maxSeverityLen},
		"evidence":       {itemProps["evidence"].(map[string]any)["maxLength"], maxTextLen},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s limit = %v, want %d", name, c.got, c.want)
		}
	}
	if maxTextLen != 4000 || maxTitleLen != 500 || maxSeverityLen != 64 || maxFindings != 50 {
		t.Fatal("schema limits changed; update the problem messages that spell them out")
	}
}

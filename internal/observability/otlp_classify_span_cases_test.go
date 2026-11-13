package observability

import (
	"strings"
	"testing"
)

// TestClassifySpanTypeAliases pins every span type alias accepted by each
// branch of classifySpan, so dropping one silently degrades it to a lifecycle
// span named after the span itself.
func TestClassifySpanTypeAliases(t *testing.T) {
	for _, tc := range []struct {
		spanType        string
		category, event string
	}{
		{"tool", "tool", "tool.call"},
		{"mcp_call", "tool", "tool.call"},
		{"Tool", "tool", "tool.call"},
		{"model_stream", "model", "model.request"},
		{"model_request", "model", "model.request"},
		{"LLM", "model", "model.request"},
		{"interaction", "lifecycle", "agent.span"},
		{"subagent", "lifecycle", "agent.span"},
		{"agent", "lifecycle", "agent.span"},
		{"workflow", "lifecycle", "custom"},
	} {
		t.Run(tc.spanType, func(t *testing.T) {
			category, event, msg := classifySpan(tc.spanType, "custom", map[string]string{})
			if category != tc.category || event != tc.event || msg != "custom" {
				t.Fatalf("classifySpan(%q) = (%q, %q, %q), want (%q, %q, %q)", tc.spanType, category, event, msg, tc.category, tc.event, "custom")
			}
		})
	}
}

// TestClassifySpanAttributePriority pins which attribute wins when several
// candidates are present, and that each detail fallback is actually consulted.
func TestClassifySpanAttributePriority(t *testing.T) {
	for _, tc := range []struct {
		name, spanType string
		attrs          map[string]string
		msg            string
	}{
		{"tool.name beats mcp tool name", "mcp_call", map[string]string{"tool.name": "Bash", "gen_ai.mcp.tool_name": "search"}, "Bash"},
		{"blank tool.name falls through", "mcp_call", map[string]string{"tool.name": "  ", "gen_ai.mcp.tool_name": "search"}, "search"},
		{"command beats file path", "tool", map[string]string{"tool.name": "Edit", "command": "ls", "file.path": "/a.go", "tool_input": "{}", "tool.parameters": "p"}, "Edit ls"},
		{"file path beats tool input", "tool", map[string]string{"tool.name": "Edit", "file.path": "/a.go", "tool_input": "{}", "tool.parameters": "p"}, "Edit /a.go"},
		{"tool input beats parameters", "tool", map[string]string{"tool.name": "Edit", "tool_input": `{"x":1}`, "tool.parameters": "p"}, `Edit {"x":1}`},
		{"parameters as last detail", "tool", map[string]string{"tool.name": "Edit", "tool.parameters": "p  q"}, "Edit p q"},
		{"model_name beats model", "llm", map[string]string{"gen_ai.model_name": "m1", "model": "m2"}, "m1"},
		{"model as fallback", "llm", map[string]string{"model": " m2 "}, "m2"},
		{"agent ignores model attrs", "agent", map[string]string{"model": "m2", "tool.name": "Bash"}, "span-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, msg := classifySpan(tc.spanType, "span-name", tc.attrs); msg != tc.msg {
				t.Fatalf("msg = %q, want %q", msg, tc.msg)
			}
		})
	}
}

// TestClassifySpanClipsLongToolDetail checks the 180-byte cap on the tool
// message, measured after whitespace in the detail has been collapsed.
func TestClassifySpanClipsLongToolDetail(t *testing.T) {
	tool := "Bash"
	exact := strings.Repeat("a", 180-len(tool)-1)
	if _, _, msg := classifySpan("tool", "x", map[string]string{"tool.name": tool, "command": exact}); msg != tool+" "+exact {
		t.Fatalf("message at the limit was clipped: %q", msg)
	}

	_, _, msg := classifySpan("tool", "x", map[string]string{"tool.name": tool, "command": exact + "b"})
	if want := tool + " " + exact + "…"; msg != want {
		t.Fatalf("message over the limit = %q, want %q", msg, want)
	}

	// Collapsing whitespace first keeps a padded command under the limit.
	words := make([]string, 50)
	for i := range words {
		words[i] = "ab"
	}
	padded := strings.Join(words, "\n\t   ")
	if _, _, msg := classifySpan("tool", "x", map[string]string{"tool.name": tool, "command": padded}); msg != tool+" "+strings.Join(words, " ") {
		t.Fatalf("whitespace should be collapsed before clipping, got %q", msg)
	}
}

// TestSpanEventLevelAndFailureMessageClip covers the non-error status path and
// the 180-byte cap on the failure message built from the span name.
func TestSpanEventLevelAndFailureMessageClip(t *testing.T) {
	span := otlpSpan{Name: "Read", EndTimeUnixNano: "3000000"}
	span.Status.Code = 1
	span.Status.Message = "ignored for ok spans"
	ev := spanEvent(span, map[string]string{"span.type": "tool"})
	if ev.Level != "info" || ev.Message != "Read" {
		t.Fatalf("ok span = level %q message %q, want info/Read", ev.Level, ev.Message)
	}

	span.Status.Code = 2
	span.Status.Message = strings.Repeat("x", 300)
	ev = spanEvent(span, map[string]string{"span.type": "tool"})
	if want := ("Read 失败: " + span.Status.Message)[:180] + "…"; ev.Level != "error" || ev.Message != want {
		t.Fatalf("failed span = level %q message %q, want error/%q", ev.Level, ev.Message, want)
	}
}

// TestSpanEventSpanTypeSources checks that the span type is read from each of
// the attribute keys spanEvent supports, in priority order.
func TestSpanEventSpanTypeSources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		attrs map[string]string
		want  string
	}{
		{"span.type", map[string]string{"span.type": "tool", "gen_ai.span.kind": "llm"}, "tool"},
		{"gen_ai.span.kind", map[string]string{"gen_ai.span.kind": "llm", "codebuddy.span_type": "tool"}, "model"},
		{"codebuddy.span_type", map[string]string{"codebuddy.span_type": "tool"}, "tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ev := spanEvent(otlpSpan{Name: "n"}, tc.attrs); ev.Category != tc.want {
				t.Fatalf("category = %q, want %q", ev.Category, tc.want)
			}
		})
	}
	if ev := spanEvent(otlpSpan{Name: "  "}, map[string]string{}); ev.Name != "span" || ev.Message != "span" {
		t.Fatalf("blank span name = %q/%q, want span/span", ev.Name, ev.Message)
	}
}

package observability

import "testing"

func TestClassifySpan(t *testing.T) {
	for _, tc := range []struct {
		name, spanType, spanName string
		attrs                    map[string]string
		category, event, msg     string
	}{
		{"tool with detail", "tool", "Bash", map[string]string{"tool.name": "Bash", "command": "go   test\n./..."}, "tool", "tool.call", "Bash go test ./..."},
		{"mcp tool without detail", "MCP_CALL", "call", map[string]string{"gen_ai.mcp.tool_name": "search"}, "tool", "tool.call", "search"},
		{"tool falls back to span name", "tool", "Read", map[string]string{}, "tool", "tool.call", "Read"},
		{"model", "llm", "chat", map[string]string{"gen_ai.model_name": "m1"}, "model", "model.request", "m1"},
		{"model falls back to span name", "model_stream", "stream", map[string]string{}, "model", "model.request", "stream"},
		{"agent", "subagent", "worker", map[string]string{}, "lifecycle", "agent.span", "worker"},
		{"unknown", "", "span", map[string]string{}, "lifecycle", "span", "span"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			category, event, msg := classifySpan(tc.spanType, tc.spanName, tc.attrs)
			if category != tc.category || event != tc.event || msg != tc.msg {
				t.Fatalf("classifySpan = (%q, %q, %q), want (%q, %q, %q)", category, event, msg, tc.category, tc.event, tc.msg)
			}
		})
	}
}

func TestSpanAttributes(t *testing.T) {
	attrs := map[string]string{"k": "v"}
	span := otlpSpan{TraceID: "t1", SpanID: "s1", StartTimeUnixNano: "1000000", EndTimeUnixNano: "3000000"}
	got := spanAttributes(span, attrs)
	if got["k"] != "v" || got["durationMs"] != "2" || got["otel.traceId"] != "t1" || got["otel.spanId"] != "s1" {
		t.Fatalf("spanAttributes = %#v", got)
	}
	if attrs["durationMs"] != "2" {
		t.Fatalf("durationMs not written back: %#v", attrs)
	}

	got = spanAttributes(otlpSpan{EndTimeUnixNano: "3000000"}, map[string]string{})
	if len(got) != 0 {
		t.Fatalf("spanAttributes without start/ids = %#v", got)
	}
}

func TestSpanEventFailureAndAssembly(t *testing.T) {
	span := otlpSpan{Name: " Bash ", ParentSpanID: "p1", StartTimeUnixNano: "1000000", EndTimeUnixNano: "3000000"}
	span.Status.Code = 2
	span.Status.Message = "exit 1"
	ev := spanEvent(span, map[string]string{"span.type": "tool"})
	if ev.Category != "tool" || ev.Name != "tool.call" || ev.Phase != "finish" || ev.Level != "error" {
		t.Fatalf("event = %#v", ev)
	}
	if ev.Message != "Bash 失败: exit 1" || ev.ParentID != "p1" || ev.Timestamp.IsZero() || ev.Attributes["durationMs"] != "2" {
		t.Fatalf("event = %#v", ev)
	}

	span.Status.Message = ""
	ev = spanEvent(span, map[string]string{"span.type": "llm", "model": "m2"})
	if ev.Level != "error" || ev.Message != "m2" {
		t.Fatalf("failed span without message = %#v", ev)
	}
}

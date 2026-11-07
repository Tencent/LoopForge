package observability

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type otlpExport struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource    `json:"resource"`
	ScopeSpans []otlpScopeSpan `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpScopeSpan struct {
	Spans []otlpSpan `json:"spans"`
}

type otlpSpan struct {
	TraceID           string   `json:"traceId"`
	SpanID            string   `json:"spanId"`
	ParentSpanID      string   `json:"parentSpanId"`
	Name              string   `json:"name"`
	Kind              int      `json:"kind"`
	StartTimeUnixNano any      `json:"startTimeUnixNano"`
	EndTimeUnixNano   any      `json:"endTimeUnixNano"`
	Attributes        []otlpKV `json:"attributes"`
	Status            struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

type otlpKV struct {
	Key   string     `json:"key"`
	Value otlpAnyVal `json:"value"`
}

type otlpAnyVal struct {
	StringValue string  `json:"stringValue"`
	IntValue    any     `json:"intValue"`
	DoubleValue float64 `json:"doubleValue"`
	BoolValue   bool    `json:"boolValue"`
}

func ParseOTLPTraces(body []byte) []domain.TelemetryEvent {
	if len(body) == 0 {
		return nil
	}
	var export otlpExport
	if json.Unmarshal(body, &export) == nil && len(export.ResourceSpans) > 0 {
		return eventsFromExport(export)
	}
	if decoded, ok := decodeOTLPProtobuf(body); ok {
		return eventsFromExport(decoded)
	}
	return nil
}

func eventsFromExport(export otlpExport) []domain.TelemetryEvent {
	var out []domain.TelemetryEvent
	for _, rs := range export.ResourceSpans {
		res := kvMap(rs.Resource.Attributes)
		runID := firstNonEmpty(res["agentflow.run_id"], res["agentflow.runId"])
		nodeID := firstNonEmpty(res["agentflow.node_id"], res["agentflow.nodeId"])
		for _, ss := range rs.ScopeSpans {
			for _, span := range ss.Spans {
				attrs := kvMap(span.Attributes)
				for k, v := range res {
					if _, ok := attrs[k]; !ok {
						attrs[k] = v
					}
				}
				event := spanEvent(span, attrs)
				if runID != "" {
					event.RunID = runID
					event.Context.RunID = runID
				}
				if nodeID != "" {
					event.NodeID = nodeID
					event.Context.NodeID = nodeID
				}
				out = append(out, event)
			}
		}
	}
	return out
}

func spanEvent(span otlpSpan, attrs map[string]string) domain.TelemetryEvent {
	spanType := firstNonEmpty(attrs["span.type"], attrs["gen_ai.span.kind"], attrs["codebuddy.span_type"])
	name := strings.TrimSpace(span.Name)
	if name == "" {
		name = "span"
	}
	category, eventName, phase := "lifecycle", name, "finish"
	msg := name
	switch strings.ToLower(spanType) {
	case "tool", "mcp_call":
		category, eventName, phase = "tool", "tool.call", "finish"
		tool := firstNonEmpty(attrs["tool.name"], attrs["gen_ai.mcp.tool_name"], name)
		detail := firstNonEmpty(attrs["command"], attrs["file.path"], attrs["tool_input"], attrs["tool.parameters"])
		if detail != "" {
			msg = clip(tool+" "+strings.Join(strings.Fields(detail), " "), 180)
		} else {
			msg = tool
		}
	case "model_stream", "model_request", "llm":
		category, eventName, phase = "model", "model.request", "finish"
		msg = firstNonEmpty(attrs["gen_ai.model_name"], attrs["model"], name)
	case "interaction", "subagent", "agent":
		category, eventName = "lifecycle", "agent.span"
	}
	if span.Status.Code == 2 {
		phase = "finish"
		if span.Status.Message != "" {
			msg = clip(name+" 失败: "+span.Status.Message, 180)
		}
	}
	start := unixNano(span.StartTimeUnixNano)
	end := unixNano(span.EndTimeUnixNano)
	if end.After(start) && !start.IsZero() {
		attrs["durationMs"] = strconv.FormatInt(end.Sub(start).Milliseconds(), 10)
	}
	attrAny := map[string]any{}
	for k, v := range attrs {
		attrAny[k] = v
	}
	if span.TraceID != "" {
		attrAny["otel.traceId"] = span.TraceID
	}
	if span.SpanID != "" {
		attrAny["otel.spanId"] = span.SpanID
	}
	level := "info"
	if span.Status.Code == 2 {
		level = "error"
	}
	return domain.TelemetryEvent{
		Category:   category,
		Name:       eventName,
		Phase:      phase,
		Level:      level,
		Message:    msg,
		Timestamp:  end,
		ParentID:   span.ParentSpanID,
		Source:     domain.TelemetrySource{Layer: "executor", Adapter: AdapterOTEL, Provider: "codebuddy", Protocol: "otlp"},
		Attributes: attrAny,
	}
}

func kvMap(kvs []otlpKV) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		if kv.Key == "" {
			continue
		}
		if kv.Value.StringValue != "" {
			out[kv.Key] = kv.Value.StringValue
			continue
		}
		if kv.Value.BoolValue {
			out[kv.Key] = "true"
			continue
		}
		if kv.Value.DoubleValue != 0 {
			out[kv.Key] = strconv.FormatFloat(kv.Value.DoubleValue, 'f', -1, 64)
			continue
		}
		if kv.Value.IntValue != nil {
			out[kv.Key] = fmt.Sprint(kv.Value.IntValue)
		}
	}
	return out
}

func unixNano(v any) time.Time {
	switch t := v.(type) {
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		if n > 0 {
			return time.Unix(0, n).UTC()
		}
	case float64:
		if t > 0 {
			return time.Unix(0, int64(t)).UTC()
		}
	case json.Number:
		n, _ := t.Int64()
		if n > 0 {
			return time.Unix(0, n).UTC()
		}
	case int64:
		if t > 0 {
			return time.Unix(0, t).UTC()
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func decodeOTLPProtobuf(body []byte) (otlpExport, bool) {
	spans := walkResourceSpans(body)
	if len(spans) == 0 {
		return otlpExport{}, false
	}
	return otlpExport{ResourceSpans: spans}, true
}

func walkResourceSpans(body []byte) []otlpResourceSpans {
	var out []otlpResourceSpans
	rest := body
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		if num == 1 && wire == 2 { // resource_spans
			out = append(out, parseResourceSpans(val))
		}
	}
	return out
}

func parseResourceSpans(msg []byte) otlpResourceSpans {
	var rs otlpResourceSpans
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		switch {
		case num == 1 && wire == 2:
			rs.Resource.Attributes = parseAttributes(val, 1)
		case num == 2 && wire == 2:
			rs.ScopeSpans = append(rs.ScopeSpans, parseScopeSpans(val))
		}
	}
	return rs
}

func parseScopeSpans(msg []byte) otlpScopeSpan {
	var ss otlpScopeSpan
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		if num == 2 && wire == 2 {
			ss.Spans = append(ss.Spans, parseSpan(val))
		}
	}
	return ss
}

func parseSpan(msg []byte) otlpSpan {
	var span otlpSpan
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		switch {
		case num == 1 && wire == 2:
			span.TraceID = hex.EncodeToString(val)
		case num == 2 && wire == 2:
			span.SpanID = hex.EncodeToString(val)
		case num == 3 && wire == 2:
			span.ParentSpanID = hex.EncodeToString(val)
		case num == 5 && wire == 2:
			span.Name = string(val)
		case num == 7 && wire == 1 && len(val) == 8:
			span.StartTimeUnixNano = strconv.FormatUint(binary.LittleEndian.Uint64(val), 10)
		case num == 8 && wire == 1 && len(val) == 8:
			span.EndTimeUnixNano = strconv.FormatUint(binary.LittleEndian.Uint64(val), 10)
		case num == 9 && wire == 2:
			span.Attributes = append(span.Attributes, parseKV(val)...)
		case num == 15 && wire == 2:
			span.Status = parseStatus(val)
		}
	}
	return span
}

func parseStatus(msg []byte) struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
} {
	var st struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		if num == 2 && wire == 2 {
			st.Message = string(val)
		}
		if num == 3 && wire == 0 {
			n, _ := binary.Uvarint(val)
			st.Code = int(n)
		}
	}
	return st
}

func parseAttributes(msg []byte, fieldNum int) []otlpKV {
	var out []otlpKV
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		if num == fieldNum && wire == 2 {
			out = append(out, parseKV(val)...)
		}
	}
	return out
}

func parseKV(msg []byte) []otlpKV {
	var kv otlpKV
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			break
		}
		rest = next
		if num == 1 && wire == 2 {
			kv.Key = string(val)
		}
		if num == 2 && wire == 2 {
			kv.Value.StringValue = anyString(val)
		}
	}
	if kv.Key == "" {
		return nil
	}
	return []otlpKV{kv}
}

func anyString(msg []byte) string {
	rest := msg
	for len(rest) > 0 {
		num, wire, val, next, ok := readProtoField(rest)
		if !ok {
			return string(msg)
		}
		rest = next
		if num == 1 && wire == 2 {
			return string(val)
		}
	}
	return ""
}

func readProtoField(b []byte) (num, wire int, val, rest []byte, ok bool) {
	if len(b) == 0 {
		return 0, 0, nil, nil, false
	}
	tag, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, 0, nil, nil, false
	}
	num, wire = int(tag>>3), int(tag&7)
	b = b[n:]
	switch wire {
	case 0:
		_, n2 := binary.Uvarint(b)
		if n2 <= 0 {
			return 0, 0, nil, nil, false
		}
		return num, wire, b[:n2], b[n2:], true
	case 1:
		if len(b) < 8 {
			return 0, 0, nil, nil, false
		}
		return num, wire, b[:8], b[8:], true
	case 2:
		ln, n2 := binary.Uvarint(b)
		if n2 <= 0 || int(ln) > len(b)-n2 {
			return 0, 0, nil, nil, false
		}
		val = b[n2 : n2+int(ln)]
		return num, wire, val, b[n2+int(ln):], true
	case 5:
		if len(b) < 4 {
			return 0, 0, nil, nil, false
		}
		return num, wire, b[:4], b[4:], true
	default:
		return 0, 0, nil, nil, false
	}
}

package runtime

import (
	"encoding/json"
	"testing"
)

// decodeAntigravityLines parses every translated stream-json line so the
// assertions below can check exact fields instead of substrings.
func decodeAntigravityLines(t *testing.T, lines []string) []map[string]any {
	t.Helper()
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid stream-json line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func antigravityTextDeltas(events []map[string]any) (starts int, deltas []string) {
	for _, event := range events {
		inner, _ := event["event"].(map[string]any)
		switch inner["type"] {
		case "content_block_start":
			starts++
		case "content_block_delta":
			delta, _ := inner["delta"].(map[string]any)
			if delta["type"] == "text_delta" {
				deltas = append(deltas, stringValue(delta["text"]))
			}
		}
	}
	return starts, deltas
}

func antigravityAssistantTexts(events []map[string]any) []string {
	var texts []string
	for _, event := range events {
		if event["type"] != "assistant" {
			continue
		}
		message, _ := event["message"].(map[string]any)
		content, _ := message["content"].([]any)
		for _, item := range content {
			block, _ := item.(map[string]any)
			if block["type"] == "text" {
				texts = append(texts, stringValue(block["text"]))
			}
		}
	}
	return texts
}

func antigravityToolResults(events []map[string]any) map[string]map[string]any {
	results := map[string]map[string]any{}
	for _, event := range events {
		if event["type"] != "user" {
			continue
		}
		message, _ := event["message"].(map[string]any)
		content, _ := message["content"].([]any)
		for _, item := range content {
			block, _ := item.(map[string]any)
			if block["type"] == "tool_result" {
				results[stringValue(block["tool_use_id"])] = block
			}
		}
	}
	return results
}

// Two agent_response steps must stream their deltas into separate blocks and
// each DONE must emit only that step's text: the accumulated buffer is reset.
func TestAntigravityResponseStepStreamsDeltasAndResetsPerStep(t *testing.T) {
	events := decodeAntigravityLines(t, translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"第一"}}`,
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"段","state":"DONE"}}`,
		`{"event":"step_update","step_update":{"step_index":1,"step_type":"agent_response","text_delta":"第二段","state":"DONE"}}`,
	))
	starts, deltas := antigravityTextDeltas(events)
	if want := []string{"第一", "段", "第二段"}; len(deltas) != len(want) || deltas[0] != want[0] || deltas[1] != want[1] || deltas[2] != want[2] {
		t.Fatalf("text deltas = %q, want %q", deltas, want)
	}
	if starts != 2 {
		t.Fatalf("content_block_start count = %d, want one per step_index (2)", starts)
	}
	texts := antigravityAssistantTexts(events)
	if len(texts) != 2 || texts[0] != "第一段" || texts[1] != "第二段" {
		t.Fatalf("assistant text blocks = %q, want [第一段 第二段]", texts)
	}

	// Deltas of a new step_index open a new block even before the previous
	// step is DONE: the block key carries the step index.
	events = decodeAntigravityLines(t, translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"x"}}`,
		`{"event":"step_update","step_update":{"step_index":1,"step_type":"agent_response","text_delta":"y"}}`,
	))
	if starts, deltas := antigravityTextDeltas(events); starts != 2 || len(deltas) != 2 {
		t.Fatalf("interleaved steps: starts=%d deltas=%q, want 2 blocks with 2 deltas", starts, deltas)
	}
}

// Usage reported on DONE agent_response steps maps every Antigravity field
// to its Claude counterpart and accumulates across steps when the final
// result carries no usage of its own.
func TestAntigravityResponseStepUsageMapsAndAccumulates(t *testing.T) {
	events := decodeAntigravityLines(t, translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"a","usage":{"input_tokens":100,"output_tokens":100}}}`,
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"b","state":"DONE","usage":{"input_tokens":3,"output_tokens":4,"cache_read_tokens":5,"thinking_tokens":6}}}`,
		`{"event":"step_update","step_update":{"step_index":1,"step_type":"agent_response","text_delta":"c","state":"DONE","usage":{"input_tokens":10,"output_tokens":20,"cache_read_tokens":30,"thinking_tokens":40}}}`,
		`{"event":"result","result":{"status":"SUCCESS","response":"abc"}}`,
	))
	last := events[len(events)-1]
	if last["type"] != "result" {
		t.Fatalf("last event = %v, want result", last)
	}
	usage, _ := last["usage"].(map[string]any)
	want := map[string]float64{
		"input_tokens":                13,
		"output_tokens":               24,
		"cache_read_input_tokens":     35,
		"cache_creation_input_tokens": 0,
		"reasoning_tokens":            46,
	}
	for key, value := range want {
		if got, _ := usage[key].(float64); got != value {
			t.Fatalf("usage[%s] = %v, want %v (usage=%v; usage on unfinished steps must be ignored)", key, usage[key], value, usage)
		}
	}
}

// Finished tool steps report is_error from the presence of tool_info.error,
// never the other way round.
func TestAntigravityToolStepResultErrorFlag(t *testing.T) {
	events := decodeAntigravityLines(t, translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":2,"conversation_id":"c-1","step_type":"tool","state":"DONE","tool_info":{"name":"read_file","parameters":{"path":"a"},"output":"file body"}}}`,
		`{"event":"step_update","step_update":{"step_index":3,"conversation_id":"c-1","step_type":"tool","state":"DONE","tool_info":{"name":"run_command","parameters":{"cmd":"ls"},"error":"permission denied"}}}`,
	))
	results := antigravityToolResults(events)
	ok, failed := results["c-1#2"], results["c-1#3"]
	if ok == nil || failed == nil {
		t.Fatalf("tool results = %v, want entries for c-1#2 and c-1#3", results)
	}
	if ok["is_error"] != false || ok["content"] != "file body" {
		t.Fatalf("successful tool result = %v, want is_error=false content=file body", ok)
	}
	if failed["is_error"] != true || failed["content"] != "permission denied" {
		t.Fatalf("failed tool result = %v, want is_error=true content=permission denied", failed)
	}
}

// An error_message step without error text must not wipe the failure that an
// earlier error_message step already recorded.
func TestAntigravityErrorStepWithoutTextKeepsEarlierFailure(t *testing.T) {
	events := decodeAntigravityLines(t, translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":1,"step_type":"error_message","error":"quota exceeded"}}`,
		`{"event":"step_update","step_update":{"step_index":2,"step_type":"error_message"}}`,
		`{"event":"result","result":{"status":"FAILED"}}`,
	))
	last := events[len(events)-1]
	if last["is_error"] != true || last["result"] != "quota exceeded" {
		t.Fatalf("result = %v, want is_error=true result=quota exceeded", last)
	}
}

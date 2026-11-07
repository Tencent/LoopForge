package observability

import (
	"strings"
	"testing"
)

func TestParseOTLPTracesJSON(t *testing.T) {
	body := []byte(`{
		"resourceSpans":[{
			"resource":{"attributes":[
				{"key":"agentflow.run_id","value":{"stringValue":"run-9"}},
				{"key":"agentflow.node_id","value":{"stringValue":"fix"}}
			]},
			"scopeSpans":[{
				"spans":[{
					"traceId":"aaa",
					"spanId":"bbb",
					"name":"Bash git status",
					"attributes":[
						{"key":"span.type","value":{"stringValue":"tool"}},
						{"key":"tool.name","value":{"stringValue":"Bash"}},
						{"key":"command","value":{"stringValue":"git status"}}
					],
					"status":{"code":1}
				}]
			}]
		}]
	}`)
	events := ParseOTLPTraces(body)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	event := events[0]
	if event.RunID != "run-9" || event.NodeID != "fix" {
		t.Fatalf("ids = %s/%s", event.RunID, event.NodeID)
	}
	if event.Source.Adapter != AdapterOTEL || event.Category != "tool" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if !strings.Contains(event.Message, "Bash") {
		t.Fatalf("message = %q", event.Message)
	}
}

package observability

import "testing"

func TestOTLPSpanContextOverridesResourceDefaults(t *testing.T) {
	body := []byte(`{"resourceSpans":[{
		"resource":{"attributes":[
			{"key":"agentflow.run_id","value":{"stringValue":"default-run"}},
			{"key":"agentflow.node_id","value":{"stringValue":"default-node"}}
		]},
		"scopeSpans":[{"spans":[
			{"name":"specific","attributes":[
				{"key":"agentflow.runId","value":{"stringValue":"span-run"}},
				{"key":"agentflow.nodeId","value":{"stringValue":"span-node"}}
			]},
			{"name":"fallback"}
		]}]
	}]}`)
	events := ParseOTLPTraces(body)
	if len(events) != 2 {
		t.Fatalf("events: %+v", events)
	}
	for i, want := range [][2]string{{"span-run", "span-node"}, {"default-run", "default-node"}} {
		got := events[i]
		if got.RunID != want[0] || got.Context.RunID != want[0] || got.NodeID != want[1] || got.Context.NodeID != want[1] {
			t.Errorf("event %d context = %+v; want %v", i, got, want)
		}
	}
}

func TestOTLPSpanContextWithoutResource(t *testing.T) {
	events := ParseOTLPTraces([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[
		{"key":"agentflow.run_id","value":{"stringValue":"run"}},
		{"key":"agentflow.node_id","value":{"stringValue":"node"}}
	]}]}]}]}`))
	if len(events) != 1 || events[0].RunID != "run" || events[0].NodeID != "node" {
		t.Fatalf("span-only context lost: %+v", events)
	}
}

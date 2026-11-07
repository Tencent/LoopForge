package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowRebuildReplayFlag(t *testing.T) {
	script := WorkflowNode{ID: "script", Type: "script", RunOnWorkspaceRebuild: true}
	ordinary := WorkflowNode{ID: "ordinary", Type: "agent"}
	for _, tt := range []struct {
		name    string
		nodes   []WorkflowNode
		edges   []WorkflowEdge
		invalid bool
	}{
		{"default off", []WorkflowNode{ordinary}, nil, false},
		{"ordinary dependencies kept", []WorkflowNode{script, ordinary}, []WorkflowEdge{{Source: "ordinary", Target: "script"}}, false},
		{"only scripts", []WorkflowNode{{ID: "agent", Type: "agent", RunOnWorkspaceRebuild: true}}, nil, true},
		{"legacy command", []WorkflowNode{{ID: "command", Type: "command", RunOnWorkspaceRebuild: true}}, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowPreparation(WorkflowDefinition{Nodes: tt.nodes, Edges: tt.edges})
			if (err != nil) != tt.invalid {
				t.Fatalf("err=%v invalid=%v", err, tt.invalid)
			}
		})
	}
}

func TestWorkflowRebuildFlagJSONCompatibility(t *testing.T) {
	for _, tt := range []struct {
		body    string
		enabled bool
	}{
		{`{"nodes":[{"id":"s","type":"script"}]}`, false},
		{`{"nodes":[{"id":"s","type":"script","runOnWorkspaceRebuild":true}]}`, true},
		{`{"nodes":[{"id":"s","type":"script","workspacePreparation":"environment"}]}`, true},
		{`{"nodes":[{"id":"s","type":"script","workspacePreparation":"code"}]}`, true},
		{`{"nodes":[{"id":"s","type":"script","workspacePreparation":"code","runOnWorkspaceRebuild":false}]}`, false},
	} {
		var wf WorkflowDefinition
		if err := json.Unmarshal([]byte(tt.body), &wf); err != nil {
			t.Fatal(err)
		}
		if len(wf.Nodes) != 1 || wf.Nodes[0].RunOnWorkspaceRebuild != tt.enabled {
			t.Fatalf("body=%s nodes=%+v", tt.body, wf.Nodes)
		}
		raw, err := json.Marshal(wf)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "workspacePreparation") {
			t.Fatalf("old classifications were written back: %s", raw)
		}
	}
	var wf WorkflowDefinition
	if err := json.Unmarshal([]byte(`{"nodes":[{"id":"s","runOnWorkspaceRebuild":"false"}]}`), &wf); err == nil {
		t.Fatal("string flag accepted as boolean")
	}
}

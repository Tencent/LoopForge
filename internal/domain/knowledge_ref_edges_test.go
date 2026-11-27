package domain

import "testing"

// TestValidateKnowledgeRefsEdges covers the direct validation rules that the
// existing TestWorkflowKnowledgeReferences only reaches indirectly.
func TestValidateKnowledgeRefsEdges(t *testing.T) {
	const team = "conn-1"
	cases := []struct {
		name  string
		refs  []KnowledgeRef
		valid bool
	}{
		{"nil refs", nil, true},
		{"empty refs", []KnowledgeRef{}, true},
		{"all no sources", []KnowledgeRef{{ConnectorID: team, Mode: "all"}}, true},
		{"another all no sources", []KnowledgeRef{{ConnectorID: "handbook", Mode: "all"}}, true},
		{"allowlist", []KnowledgeRef{{ConnectorID: team, Mode: "allowlist", Sources: []string{"dns"}}}, true},
		{"empty connector id", []KnowledgeRef{{ConnectorID: "", Mode: "all"}}, false},
		{"whitespace connector id", []KnowledgeRef{{ConnectorID: " ", Mode: "all"}}, false},
		{"leading space in connector id", []KnowledgeRef{{ConnectorID: " handbook", Mode: "all"}}, false},
		{"trailing space in connector id", []KnowledgeRef{{ConnectorID: "handbook ", Mode: "all"}}, false},
		{"duplicate connector id", []KnowledgeRef{{ConnectorID: team, Mode: "all"}, {ConnectorID: team, Mode: "all"}}, false},
		{"invalid mode", []KnowledgeRef{{ConnectorID: team, Mode: "deny"}}, false},
		{"empty mode", []KnowledgeRef{{ConnectorID: team}}, false},
		{"all with sources", []KnowledgeRef{{ConnectorID: team, Mode: "all", Sources: []string{"dns"}}}, false},
		{"allowlist without sources", []KnowledgeRef{{ConnectorID: team, Mode: "allowlist"}}, false},
		{"any connector allowlist", []KnowledgeRef{{ConnectorID: "handbook", Mode: "allowlist", Sources: []string{"dns"}}}, true},
		{"blank source", []KnowledgeRef{{ConnectorID: team, Mode: "allowlist", Sources: []string{" "}}}, false},
		{"mixed blank source", []KnowledgeRef{{ConnectorID: team, Mode: "allowlist", Sources: []string{"dns", "  "}}}, false},
		{"multiple sources", []KnowledgeRef{{ConnectorID: team, Mode: "allowlist", Sources: []string{"dns", "api"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKnowledgeRefs(tc.refs)
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateKnowledgeRefs(%+v) error = %v, want valid=%v", tc.refs, err, tc.valid)
			}
		})
	}
}

func TestValidateWorkflowKnowledgeAgentOnly(t *testing.T) {
	ref := KnowledgeRef{ConnectorID: "conn-1", Mode: "all"}
	cases := []struct {
		name  string
		node  WorkflowNode
		valid bool
	}{
		{"agent with ref", WorkflowNode{Type: "agent", KnowledgeRefs: []KnowledgeRef{ref}}, true},
		{"agent without ref", WorkflowNode{Type: "agent"}, true},
		{"script with ref", WorkflowNode{Type: "script", KnowledgeRefs: []KnowledgeRef{ref}}, false},
		{"approval with ref", WorkflowNode{Type: "approval", KnowledgeRefs: []KnowledgeRef{ref}}, false},
		{"sop with ref", WorkflowNode{Type: "sop", KnowledgeRefs: []KnowledgeRef{ref}}, false},
		{"script without ref", WorkflowNode{Type: "script"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkflowKnowledge(WorkflowDefinition{Nodes: []WorkflowNode{tc.node}})
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateWorkflowKnowledge node=%+v error = %v, want valid=%v", tc.node, err, tc.valid)
			}
		})
	}
}

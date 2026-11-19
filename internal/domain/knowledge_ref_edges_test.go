package domain

import "testing"

// TestValidateKnowledgeRefsEdges covers the direct validation rules that the
// existing TestWorkflowKnowledgeReferences only reaches indirectly.
func TestValidateKnowledgeRefsEdges(t *testing.T) {
	const team = KnowledgeMCPServerID
	cases := []struct {
		name  string
		refs  []KnowledgeRef
		valid bool
	}{
		{"nil refs", nil, true},
		{"empty refs", []KnowledgeRef{}, true},
		{"team all no sources", []KnowledgeRef{{ServerID: team, Mode: "all"}}, true},
		{"external all no sources", []KnowledgeRef{{ServerID: "handbook", Mode: "all"}}, true},
		{"team allowlist", []KnowledgeRef{{ServerID: team, Mode: "allowlist", Sources: []string{"dns"}}}, true},
		{"empty server id", []KnowledgeRef{{ServerID: "", Mode: "all"}}, false},
		{"whitespace server id", []KnowledgeRef{{ServerID: " ", Mode: "all"}}, false},
		{"leading space in server id", []KnowledgeRef{{ServerID: " handbook", Mode: "all"}}, false},
		{"trailing space in server id", []KnowledgeRef{{ServerID: "handbook ", Mode: "all"}}, false},
		{"duplicate server id", []KnowledgeRef{{ServerID: team, Mode: "all"}, {ServerID: team, Mode: "all"}}, false},
		{"invalid mode", []KnowledgeRef{{ServerID: team, Mode: "deny"}}, false},
		{"empty mode", []KnowledgeRef{{ServerID: team}}, false},
		{"all with sources", []KnowledgeRef{{ServerID: team, Mode: "all", Sources: []string{"dns"}}}, false},
		{"allowlist without sources", []KnowledgeRef{{ServerID: team, Mode: "allowlist"}}, false},
		{"external allowlist", []KnowledgeRef{{ServerID: "handbook", Mode: "allowlist", Sources: []string{"dns"}}}, false},
		{"blank source", []KnowledgeRef{{ServerID: team, Mode: "allowlist", Sources: []string{" "}}}, false},
		{"mixed blank source", []KnowledgeRef{{ServerID: team, Mode: "allowlist", Sources: []string{"dns", "  "}}}, false},
		{"multiple team sources", []KnowledgeRef{{ServerID: team, Mode: "allowlist", Sources: []string{"dns", "api"}}}, true},
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
	ref := KnowledgeRef{ServerID: KnowledgeMCPServerID, Mode: "all"}
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

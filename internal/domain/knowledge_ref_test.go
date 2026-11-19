package domain

import "testing"

func TestWorkflowKnowledgeReferences(t *testing.T) {
	team := KnowledgeRef{ServerID: KnowledgeMCPServerID, Mode: "allowlist", Sources: []string{"dns"}}
	external := KnowledgeRef{ServerID: "handbook", Mode: "all"}
	for _, tc := range []struct {
		name  string
		kind  string
		refs  []KnowledgeRef
		valid bool
	}{
		{"legacy", "agent", nil, true}, {"two services", "agent", []KnowledgeRef{team, external}, true},
		{"duplicate", "agent", []KnowledgeRef{team, team}, false},
		{"unsupported scope", "agent", []KnowledgeRef{{ServerID: "handbook", Mode: "allowlist", Sources: []string{"dns"}}}, false},
		{"empty allowlist", "agent", []KnowledgeRef{{ServerID: KnowledgeMCPServerID, Mode: "allowlist"}}, false},
		{"script", "script", []KnowledgeRef{team}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkflowKnowledge(WorkflowDefinition{Nodes: []WorkflowNode{{Type: tc.kind, KnowledgeRefs: tc.refs}}})
			if (err == nil) != tc.valid {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

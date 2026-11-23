package domain

import (
	"reflect"
	"strings"
	"testing"
)

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

func TestMarkKnowledgeReadCitedSourcesAndStripMarkers(t *testing.T) {
	read := KnowledgeRead{
		ID:       "kr-1",
		ServerID: KnowledgeMCPServerID,
		Tool:     "search_knowledge",
		Status:   KnowledgeStatusHit,
		Evidences: []KnowledgeEvidence{
			{ID: "ev_code_only", Title: "代码样例.md"},
			{ID: "ev_out", Title: "端口规范.md"},
			{ID: "ev_art", Title: "发布手册.md"},
		},
	}
	nodeOut := "真实结论 [[knowledge:ev_out]]。\n\n```markdown\n示例格式 [[knowledge:ev_code_only]]\n```\n\n~~~text\n[[knowledge:ev_code_only]]\n~~~\n\n行内代码 `` [[knowledge:ev_code_only]] `` 不算引用。"
	art := "交付物正文引用 [[knowledge:ev_out]] 和 [[knowledge:ev_art]]。"
	cited := MarkKnowledgeReadCitedSources(&read, []KnowledgeCitationSource{
		{Label: "节点输出", Content: nodeOut},
		{Label: "reports/out.md", Content: art},
	})
	if !cited || !read.Cited {
		t.Fatal("expected read to be marked cited")
	}
	if read.Evidences[0].Cited || len(read.Evidences[0].CitedIn) != 0 {
		t.Fatalf("code-only evidence must not be marked cited: %+v", read.Evidences[0])
	}
	if !read.Evidences[1].Cited || !reflect.DeepEqual(read.Evidences[1].CitedIn, []string{"节点输出", "reports/out.md"}) {
		t.Fatalf("unexpected ev_out cited state: %+v", read.Evidences[1])
	}
	if !read.Evidences[2].Cited || !reflect.DeepEqual(read.Evidences[2].CitedIn, []string{"reports/out.md"}) {
		t.Fatalf("unexpected ev_art cited state: %+v", read.Evidences[2])
	}

	stripped := StripKnowledgeCitationMarkers(nodeOut)
	if strings.Contains(stripped, "[[knowledge:ev_out]]") {
		t.Fatalf("expected prose marker stripped, got %q", stripped)
	}
	if !strings.Contains(stripped, "[[knowledge:ev_code_only]]") || !strings.Contains(stripped, "`` [[knowledge:ev_code_only]] ``") {
		t.Fatalf("expected code markers preserved in %q", stripped)
	}
}

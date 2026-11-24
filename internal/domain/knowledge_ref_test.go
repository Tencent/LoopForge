package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowKnowledgeReferences(t *testing.T) {
	team := KnowledgeRef{ConnectorID: "wk-personal-1", Mode: "allowlist", Sources: []string{"dns"}}
	external := KnowledgeRef{ConnectorID: "handbook", Mode: "all"}
	for _, tc := range []struct {
		name  string
		kind  string
		refs  []KnowledgeRef
		valid bool
	}{
		{"legacy", "agent", nil, true},
		{"two connectors", "agent", []KnowledgeRef{team, external}, true},
		{"any connector allowlist", "agent", []KnowledgeRef{{ConnectorID: "handbook", Mode: "allowlist", Sources: []string{"dns"}}}, true},
		{"duplicate", "agent", []KnowledgeRef{team, team}, false},
		{"empty allowlist", "agent", []KnowledgeRef{{ConnectorID: "wk-personal-1", Mode: "allowlist"}}, false},
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

func TestKnowledgeRefUnmarshalLegacyMCPServerID(t *testing.T) {
	var ref KnowledgeRef
	if err := json.Unmarshal([]byte(`{"mcpServerId":"legacy-conn","mode":"allowlist","sources":["kb-1"]}`), &ref); err != nil {
		t.Fatal(err)
	}
	if ref.ConnectorID != "legacy-conn" || ref.Mode != "allowlist" || !reflect.DeepEqual(ref.Sources, []string{"kb-1"}) {
		t.Fatalf("unexpected decoded ref: %+v", ref)
	}
	var modern KnowledgeRef
	if err := json.Unmarshal([]byte(`{"connectorId":"new-conn","mcpServerId":"ignored","mode":"all"}`), &modern); err != nil {
		t.Fatal(err)
	}
	if modern.ConnectorID != "new-conn" {
		t.Fatalf("expected connectorId to take precedence, got %+v", modern)
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

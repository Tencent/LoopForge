package engine

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestResolveNodeAssetsInjectsKnowledgeRuleAndCatalog(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-secret-key")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.PutMCPServer(domain.MCPServerDefinition{
		ID:          "weknora-1",
		Name:        "Engineering KB",
		Description: "VPC DNS 与排障文档",
		Scope:       "project",
		ProjectID:   "default",
		Transport:   "http",
		URL:         "https://kb.invalid/mcp",
	}); err != nil {
		t.Fatal(err)
	}

	eng := &Engine{repo: store}
	eng.BuildKnowledgeCatalog = func(ctx context.Context, projectID string, userID int64, refs []domain.KnowledgeRef) string {
		return "<bound_knowledge_bases>\n  <kb id=\"weknora-1\" name=\"Engineering KB\"><sample_documents>- vpcdns-test.md</sample_documents></kb>\n</bound_knowledge_bases>"
	}

	run := domain.Run{
		ID: "run-1",
		ExecutionContext: domain.TelemetryContext{
			ProjectID:         "default",
			ResponsibleUserID: 1,
		},
		Nodes: map[string]*domain.NodeRun{
			"n1": {NodeID: "n1", Attempt: 1},
		},
	}
	node := domain.WorkflowNode{
		ID:   "n1",
		Type: "agent",
		KnowledgeRefs: []domain.KnowledgeRef{
			{ServerID: "weknora-1", Mode: "all"},
		},
	}
	agent := domain.AgentDefinition{ID: "dev", Name: "Dev", MCPServers: []string{"weknora-1"}}

	bundle := eng.resolveNodeAssets(context.Background(), agent, run, "wf-1", node)
	if len(bundle.MCPServers) != 1 || bundle.MCPServers[0].ID != domain.KnowledgeMCPServerID+"-weknora-1" {
		t.Fatalf("unexpected MCPServers: %+v", bundle.MCPServers)
	}

	var ruleContent string
	for _, r := range bundle.Rules {
		if r.ID == domain.SOPKnowledgeUsageRuleID {
			ruleContent = r.Content
			break
		}
	}
	for _, want := range []string{domain.SOPKnowledgeLookupInstruction, "vpcdns-test.md"} {
		if !strings.Contains(ruleContent, want) {
			t.Fatalf("expected rule content containing %q, got:\n%s", want, ruleContent)
		}
	}

	// Fallback when BuildKnowledgeCatalog is nil.
	eng.BuildKnowledgeCatalog = nil
	fallbackBundle := eng.resolveNodeAssets(context.Background(), agent, run, "wf-1", node)
	var fallbackContent string
	for _, r := range fallbackBundle.Rules {
		if r.ID == domain.SOPKnowledgeUsageRuleID {
			fallbackContent = r.Content
			break
		}
	}
	if !strings.Contains(fallbackContent, `tools="mcp__agentflow-knowledge-weknora-1__*"`) {
		t.Fatalf("expected fallback catalog with mcp tool wildcard, got:\n%s", fallbackContent)
	}
}

func TestMarkKnowledgeReadsCited(t *testing.T) {
	reads := []domain.KnowledgeRead{
		{ID: "r-out", Attempt: 2, Status: "已返回"},
		{ID: "r-art", Attempt: 2, Status: "已返回"},
		{ID: "r-unused", Attempt: 2, Status: "已返回"},
		{ID: "r-nomatch", Attempt: 2, Status: "无匹配"},
		{ID: "r-old", Attempt: 1, Status: "已返回"},
	}
	artifacts := []domain.Artifact{
		{Name: "report.md", Content: "参考 [[knowledge:r-art]] 和 [[knowledge:r-nomatch]] 与 [[knowledge:r-old]]"},
	}
	markKnowledgeReadsCited(reads, 2, "结论见 [[knowledge:r-out]]", artifacts)

	if !reads[0].Cited || !reads[1].Cited {
		t.Fatalf("expected r-out and r-art to be cited: %+v", reads)
	}
	if reads[2].Cited || reads[3].Cited || reads[4].Cited {
		t.Fatalf("expected unused/nomatch/old attempt reads not to be cited: %+v", reads)
	}
}

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
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

func TestMarkKnowledgeReadsCitedEvidenceGranularity(t *testing.T) {
	evs := make([]domain.KnowledgeEvidence, 10)
	for i := range evs {
		evs[i] = domain.KnowledgeEvidence{ID: "ev_" + string(rune('1'+i)), Title: "doc"}
	}
	evs[9].ID = "ev_10"
	reads := []domain.KnowledgeRead{
		{ID: "kr_hit", Attempt: 1, Status: domain.KnowledgeStatusHit, Evidences: evs},
		{ID: "kr_partial", Attempt: 1, Status: domain.KnowledgeStatusPartial},
		{ID: "kr_unstructured", Attempt: 1, Status: domain.KnowledgeStatusUnstructured},
		{ID: "kr_empty", Attempt: 1, Status: domain.KnowledgeStatusEmpty},
		{ID: "kr_unavail", Attempt: 1, Status: domain.KnowledgeStatusUnavailable},
	}
	artifacts := []domain.Artifact{
		{Name: "notes.md", Content: "补充 [[knowledge:kr_partial]] 与 [[knowledge:kr_empty]] 与 [[knowledge:kr_unavail]]"},
	}
	markKnowledgeReadsCited(reads, 1, "核心结论见 [[knowledge:ev_2]] 和 [[knowledge:kr_unstructured]]", artifacts)

	if !reads[0].Cited {
		t.Fatal("expected kr_hit to be cited via ev_2")
	}
	for i, ev := range reads[0].Evidences {
		if ev.Cited != (i == 1) {
			t.Fatalf("evidence[%d] Cited = %v, want %v", i, ev.Cited, i == 1)
		}
	}
	if !reads[1].Cited || !reads[2].Cited {
		t.Fatalf("expected partial and unstructured reads to be cited: %+v", reads)
	}
	if reads[3].Cited || reads[4].Cited {
		t.Fatalf("expected empty and unavailable reads not to be cited: %+v", reads)
	}
}

func TestResolveNodeAssetsRoleKnowledgeBasePropagatesRunClaims(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-secret-key")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.PutKnowledgeSettings(domain.KnowledgeSettings{
		Enabled: true,
		URL:     "https://kb.invalid/mcp",
		Token:   "tok",
	}); err != nil {
		t.Fatal(err)
	}

	eng := &Engine{repo: store}
	run := domain.Run{
		ID: "run-role-kb",
		ExecutionContext: domain.TelemetryContext{
			ProjectID:         "default",
			ResponsibleUserID: 7,
		},
		Nodes: map[string]*domain.NodeRun{
			"n1": {NodeID: "n1", Attempt: 3},
		},
	}
	node := domain.WorkflowNode{ID: "n1", Type: "agent"}
	agent := domain.AgentDefinition{ID: "dev", Name: "Dev", KnowledgeBase: true}

	bundle := eng.resolveNodeAssets(context.Background(), agent, run, "wf-1", node)
	var found bool
	for _, srv := range bundle.MCPServers {
		if srv.ID == domain.KnowledgeMCPServerID {
			found = true
			tok := strings.TrimPrefix(srv.Headers["Authorization"], "Bearer ")
			if !strings.HasPrefix(tok, "afkb1.") {
				t.Fatalf("expected afkb1 proxy token header, got %+v", srv.Headers)
			}
			claims, err := knowledge.VerifyProxyToken(store.KnowledgeSettings(), tok, time.Now())
			if err != nil {
				t.Fatalf("VerifyProxyToken: %v", err)
			}
			if claims.RunID != "run-role-kb" || claims.NodeID != "n1" || claims.Attempt != 3 {
				t.Fatalf("unexpected proxy claims: %+v", claims)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s server in bundle, got %+v", domain.KnowledgeMCPServerID, bundle.MCPServers)
	}
	if !strings.Contains(domain.KnowledgeLookupInstruction, "[[knowledge:<id>]]") {
		t.Fatalf("KnowledgeLookupInstruction must instruct [[knowledge:<id>]] markers")
	}
}

func TestRecordKnowledgeReadRequiresRunningNodeAndCurrentAttempt(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	run := domain.Run{
		ID:     "run-rec-guard",
		Status: "running",
		Nodes: map[string]*domain.NodeRun{
			"n1": {NodeID: "n1", Status: "running", Attempt: 2},
			"n2": {NodeID: "n2", Status: "succeeded", Attempt: 1},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{repo: store}

	eng.RecordKnowledgeRead(run.ID, "n1", domain.KnowledgeRead{ID: "stale", Attempt: 1, Status: domain.KnowledgeStatusHit})
	eng.RecordKnowledgeRead(run.ID, "n1", domain.KnowledgeRead{ID: "current", Attempt: 2, Status: domain.KnowledgeStatusHit})
	eng.RecordKnowledgeRead(run.ID, "n2", domain.KnowledgeRead{ID: "not-running", Attempt: 1, Status: domain.KnowledgeStatusHit})

	saved, ok := store.RunState(run.ID)
	if !ok {
		t.Fatal("missing saved run")
	}
	if len(saved.Nodes["n1"].KnowledgeReads) != 1 || saved.Nodes["n1"].KnowledgeReads[0].ID != "current" {
		t.Fatalf("expected only current running attempt read recorded on n1, got %+v", saved.Nodes["n1"].KnowledgeReads)
	}
	if len(saved.Nodes["n2"].KnowledgeReads) != 0 {
		t.Fatalf("expected non-running node n2 to reject late read, got %+v", saved.Nodes["n2"].KnowledgeReads)
	}
}

func TestMarkNodeKnowledgeReadsOnFailurePreservesFrozenArtifactCitations(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	run := domain.Run{
		ID:         "run-fail-cite",
		Generation: 3,
		Status:     "running",
		Nodes: map[string]*domain.NodeRun{
			"n1": {
				NodeID:  "n1",
				Status:  "running",
				Attempt: 1,
				KnowledgeReads: []domain.KnowledgeRead{{
					ID:     "kr-1",
					Attempt: 1,
					Status: domain.KnowledgeStatusHit,
					Evidences: []domain.KnowledgeEvidence{
						{ID: "ev_1", Title: "OutDoc"},
						{ID: "ev_2", Title: "ArtDoc"},
					},
				}},
			},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{repo: store}
	frozen := []domain.Artifact{{Name: "partial.md", Path: "partial.md", Content: "局部交付物 [[knowledge:ev_2]]"}}
	eng.markNodeKnowledgeReads(run.ID, "n1", 3, 1, "失败前输出 [[knowledge:ev_1]]", frozen)

	saved, _ := store.RunState(run.ID)
	nr := saved.Nodes["n1"]
	if !nr.KnowledgeReads[0].Cited {
		t.Fatalf("expected kr-1 cited before failNode")
	}
	evs := nr.KnowledgeReads[0].Evidences
	if !evs[0].Cited || len(evs[0].CitedIn) != 1 || evs[0].CitedIn[0] != "节点输出" {
		t.Fatalf("unexpected ev_1 state: %+v", evs[0])
	}
	if !evs[1].Cited || len(evs[1].CitedIn) != 1 || evs[1].CitedIn[0] != "partial.md" {
		t.Fatalf("unexpected ev_2 state: %+v", evs[1])
	}
}

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
)

func TestResolveNodeAssetsInjectsUserConnectorCatalogAndEnforcesOwnerIsolation(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-secret-key")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	wkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/knowledge-bases":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"kb-docs","name":"Docs KB","knowledge_count":1}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/knowledge-bases/kb-docs/knowledge"):
			_, _ = w.Write([]byte(`{"success":true,"total":1,"data":[{"id":"doc-1","knowledge_base_id":"kb-docs","title":"guide-test","file_name":"guide-test.md"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer wkServer.Close()

	svc := knowledge.NewService(store, nil)
	exec := knowledge.NewExecutor(svc)
	eng := &Engine{repo: store}
	eng.ConfigureKnowledge(exec)

	connU1, err := svc.CreateConnector(context.Background(), 1, domainConnectorWeKnora("用户1知识库", wkServer.URL, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	connU2, err := svc.CreateConnector(context.Background(), 2, domainConnectorWeKnora("用户2知识库", wkServer.URL, "k2"))
	if err != nil {
		t.Fatal(err)
	}

	agent := domain.AgentDefinition{ID: "dev", Name: "Dev"}
	node := domain.WorkflowNode{
		ID:            "n1",
		Type:          "agent",
		KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: connU1.ID, Mode: "all"}},
	}

	// 1. User 1 runs the SOP: connU1 is bound and L0 catalog probes guide-test.md.
	runU1 := domain.Run{
		ID:               "run-u1",
		ExecutionContext: domain.TelemetryContext{ProjectID: "default", ResponsibleUserID: 1},
		Nodes:            map[string]*domain.NodeRun{"n1": {NodeID: "n1", Attempt: 1}},
	}
	bundle1 := eng.resolveNodeAssets(context.Background(), agent, runU1, "wf-1", node)
	wantServerID := domain.KnowledgeMCPServerID + "-" + connU1.ID
	if len(bundle1.MCPServers) != 1 || bundle1.MCPServers[0].ID != wantServerID {
		t.Fatalf("unexpected MCPServers for user 1: %+v", bundle1.MCPServers)
	}
	var ruleContent string
	for _, r := range bundle1.Rules {
		if r.ID == domain.SOPKnowledgeUsageRuleID {
			ruleContent = r.Content
			break
		}
	}
	for _, want := range []string{domain.SOPKnowledgeLookupInstruction, "guide-test.md", "mcp__" + wantServerID + "__search_knowledge"} {
		if !strings.Contains(ruleContent, want) {
			t.Fatalf("expected rule content containing %q, got:\n%s", want, ruleContent)
		}
	}

	// 2. User 2 runs the SAME workflow without overriding refs: User 1's connector MUST NOT be injected!
	runU2 := domain.Run{
		ID:               "run-u2",
		ExecutionContext: domain.TelemetryContext{ProjectID: "default", ResponsibleUserID: 2},
		Nodes:            map[string]*domain.NodeRun{"n1": {NodeID: "n1", Attempt: 1}},
	}
	bundle2 := eng.resolveNodeAssets(context.Background(), agent, runU2, "wf-1", node)
	if len(bundle2.MCPServers) != 0 {
		t.Fatalf("cross-user leakage: user 2 got user 1's MCP servers %+v", bundle2.MCPServers)
	}

	// 3. User 2 overrides refs at Run launch with their own connector: connU2 IS injected.
	override := []domain.KnowledgeRef{{ConnectorID: connU2.ID, Mode: "allowlist", Sources: []string{"kb-docs"}}}
	runU2.ExecutionContext.KnowledgeRefs = &override
	bundle2Override := eng.resolveNodeAssets(context.Background(), agent, runU2, "wf-1", node)
	if len(bundle2Override.MCPServers) != 1 || bundle2Override.MCPServers[0].ID != domain.KnowledgeMCPServerID+"-"+connU2.ID {
		t.Fatalf("expected user 2's overridden connector, got %+v", bundle2Override.MCPServers)
	}
}

func domainConnectorWeKnora(name, endpoint, tok string) knowledge.ConnectorInput {
	return knowledge.ConnectorInput{
		Name:     name,
		Type:     domain.ConnectorTypeWeKnora,
		Endpoint: endpoint,
		Secret:   domain.KnowledgeConnectorSecret{Token: tok},
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

	svc := knowledge.NewService(store, nil)
	connU7, err := svc.CreateConnector(context.Background(), 7, knowledge.ConnectorInput{
		Name:     "User7 MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: "https://kb.invalid/mcp",
		Secret:   domain.KnowledgeConnectorSecret{Token: "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Disabled connector and other user's connector must both be excluded.
	disabled := false
	if _, err := svc.CreateConnector(context.Background(), 7, knowledge.ConnectorInput{
		Name:     "User7 Disabled",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: "https://disabled.invalid/mcp",
		Enabled:  &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateConnector(context.Background(), 8, knowledge.ConnectorInput{
		Name:     "User8 MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: "https://other.invalid/mcp",
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
	wantID := domain.KnowledgeMCPServerID + "-" + connU7.ID
	if len(bundle.MCPServers) != 1 || bundle.MCPServers[0].ID != wantID {
		t.Fatalf("expected only user 7's enabled connector %s, got %+v", wantID, bundle.MCPServers)
	}
	tok := strings.TrimPrefix(bundle.MCPServers[0].Headers["Authorization"], "Bearer ")
	if !strings.HasPrefix(tok, "afkb1.") {
		t.Fatalf("expected afkb1 proxy token header, got %+v", bundle.MCPServers[0].Headers)
	}
	claims, err := knowledge.VerifyProxyToken(tok, time.Now())
	if err != nil {
		t.Fatalf("VerifyProxyToken: %v", err)
	}
	if claims.RunID != "run-role-kb" || claims.NodeID != "n1" || claims.Attempt != 3 || claims.UserID != 7 || claims.Ref == nil || claims.Ref.ConnectorID != connU7.ID {
		t.Fatalf("unexpected proxy claims: %+v", claims)
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
					ID:      "kr-1",
					Attempt: 1,
					Status:  domain.KnowledgeStatusHit,
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

func TestResolveNodeAssetsUnionOfNodeAndRoleKnowledgeRefs(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-secret-key")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := knowledge.NewService(store, nil)
	connA1, err := svc.CreateConnector(context.Background(), 42, domainConnectorWeKnora("User42 KB1", "https://kb1.invalid", "tok1"))
	if err != nil {
		t.Fatal(err)
	}
	connA2, err := svc.CreateConnector(context.Background(), 42, domainConnectorWeKnora("User42 KB2", "https://kb2.invalid", "tok2"))
	if err != nil {
		t.Fatal(err)
	}
	connForeign, err := svc.CreateConnector(context.Background(), 99, domainConnectorWeKnora("User99 Foreign", "https://foreign.invalid", "tok99"))
	if err != nil {
		t.Fatal(err)
	}

	eng := &Engine{repo: store}
	agent := domain.AgentDefinition{ID: "agent-union", Name: "UnionDev", KnowledgeBase: true}
	run := domain.Run{
		ID: "run-union-1",
		ExecutionContext: domain.TelemetryContext{
			ProjectID:         "p1",
			ResponsibleUserID: 42,
		},
		Nodes: map[string]*domain.NodeRun{
			"n1": {NodeID: "n1", Attempt: 1},
		},
	}

	// Case 1: Node narrows connA1 to allowlist ["arch"] and also references foreign connForeign.
	// Role toggle should union connA2(all) while preserving connA1(allowlist:["arch"]) and dropping connForeign.
	node := domain.WorkflowNode{
		ID:      "n1",
		Type:    "agent",
		AgentID: agent.ID,
		KnowledgeRefs: []domain.KnowledgeRef{
			{ConnectorID: connA1.ID, Mode: "allowlist", Sources: []string{"arch"}},
			{ConnectorID: connForeign.ID, Mode: "all"},
		},
	}
	bundle := eng.resolveNodeAssets(context.Background(), agent, run, "wf-1", node)
	if len(bundle.MCPServers) != 2 {
		t.Fatalf("expected 2 unioned MCP servers (connA1 + connA2), got %d: %+v", len(bundle.MCPServers), bundle.MCPServers)
	}
	claimsByConn := map[string]*knowledge.ProxyClaims{}
	for _, srv := range bundle.MCPServers {
		tok := strings.TrimPrefix(srv.Headers["Authorization"], "Bearer ")
		c, err := knowledge.VerifyProxyToken(tok, time.Now())
		if err != nil {
			t.Fatalf("VerifyProxyToken(%s): %v", srv.ID, err)
		}
		claimsByConn[c.Ref.ConnectorID] = &c
	}
	if c1 := claimsByConn[connA1.ID]; c1 == nil || c1.Ref.Mode != "allowlist" || len(c1.Ref.Sources) != 1 || c1.Ref.Sources[0] != "arch" {
		t.Fatalf("expected connA1 to preserve node allowlist [arch], got %+v", c1)
	}
	if c2 := claimsByConn[connA2.ID]; c2 == nil || c2.Ref.Mode != "all" {
		t.Fatalf("expected connA2 to be appended from role baseline with mode=all, got %+v", c2)
	}

	// Case 2: Node binds ONLY foreign connector (cross-user authoring), role toggle falls back to connA1 + connA2.
	nodeForeignOnly := domain.WorkflowNode{
		ID:      "n1",
		Type:    "agent",
		AgentID: agent.ID,
		KnowledgeRefs: []domain.KnowledgeRef{
			{ConnectorID: connForeign.ID, Mode: "all"},
		},
	}
	bundleFallback := eng.resolveNodeAssets(context.Background(), agent, run, "wf-1", nodeForeignOnly)
	if len(bundleFallback.MCPServers) != 2 {
		t.Fatalf("expected role fallback to mount connA1 and connA2 when node refs belong to another user, got %+v", bundleFallback.MCPServers)
	}

	// Case 3: Explicit run-level override exclusively overrides both node and role.
	override := []domain.KnowledgeRef{{ConnectorID: connA2.ID, Mode: "allowlist", Sources: []string{"ops"}}}
	runOverride := run
	runOverride.ExecutionContext.KnowledgeRefs = &override
	bundleOverride := eng.resolveNodeAssets(context.Background(), agent, runOverride, "wf-1", node)
	if len(bundleOverride.MCPServers) != 1 || bundleOverride.MCPServers[0].ID != domain.KnowledgeMCPServerID+"-"+connA2.ID {
		t.Fatalf("expected explicit run override to mount only connA2, got %+v", bundleOverride.MCPServers)
	}

	// Case 4: Explicit empty run-level override ("knowledgeRefs": []) survives DB round-trip
	// and suppresses both node bindings and role toggle.
	emptyOverride := make([]domain.KnowledgeRef, 0)
	runEmpty := run
	runEmpty.ID = "run-empty-override"
	runEmpty.WorkflowID = "wf-1"
	runEmpty.Status = "running"
	runEmpty.ExecutionContext.KnowledgeRefs = &emptyOverride
	if err := store.PutRun(runEmpty); err != nil {
		t.Fatalf("PutRun empty override: %v", err)
	}
	reloaded, ok := store.RunState(runEmpty.ID)
	if !ok {
		t.Fatal("RunState did not find persisted empty-override run")
	}
	if reloaded.ExecutionContext.KnowledgeRefs == nil || len(*reloaded.ExecutionContext.KnowledgeRefs) != 0 {
		t.Fatalf("expected persisted empty override non-nil empty slice, got %#v", reloaded.ExecutionContext.KnowledgeRefs)
	}
	bundleEmpty := eng.resolveNodeAssets(context.Background(), agent, reloaded, "wf-1", node)
	if len(bundleEmpty.MCPServers) != 0 {
		t.Fatalf("expected explicit empty override to mount 0 KB MCP servers, got %+v", bundleEmpty.MCPServers)
	}
	for _, r := range bundleEmpty.Rules {
		if r.ID == domain.SOPKnowledgeUsageRuleID {
			t.Fatalf("expected explicit empty override not to inject SOP KB usage rule, got %+v", r)
		}
	}
}

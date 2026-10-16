package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// workflowRowExists reports whether the physical workflows table holds the
// row (independent of the generic-template fallback in CatalogRepo.Workflow).
func workflowRowExists(t *testing.T, s *repo.Store, id string) bool {
	t.Helper()
	var n int64
	if err := s.DB.Table("workflows").Where("id = ?", id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func newGenericTemplateServer(t *testing.T) (http.Handler, *repo.Store) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := repo.SeedTestGenericTemplates(s); err != nil {
		t.Fatal(err)
	}
	for _, project := range []domain.Project{{ID: "alpha", Key: "ALPHA", Name: "Alpha"}, {ID: "beta", Key: "BETA", Name: "Beta"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	installTestTrackerMCPs(t, s, "alpha")
	installTestTrackerMCPs(t, s, "beta")
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return h, s
}

func doStatus(h http.Handler, method, path string) int {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestGenericTemplateIsListedAndReadOnly(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	listed := requestEmptyJSON[[]domain.WorkflowDefinition](t, h, http.MethodGet, "/api/workflows", http.StatusOK)
	var generic *domain.WorkflowDefinition
	for index := range listed {
		if listed[index].ID == repo.GenericBackendDeliveryWorkflowID {
			generic = &listed[index]
		}
	}
	if generic == nil {
		t.Fatalf("generic template missing from workflow list: %+v", listed)
	}
	seenIDs := map[string]bool{}
	for _, workflow := range listed {
		if workflow.Generic {
			seenIDs[workflow.ID] = true
		}
	}
	for _, id := range []string{repo.GenericBackendDeliveryWorkflowID, repo.GenericProductDeliveryWorkflowID, repo.GenericFrontendDeliveryWorkflowID} {
		if !seenIDs[id] {
			t.Fatalf("generic catalog missing %q: %#v", id, seenIDs)
		}
	}
	if !generic.Generic || generic.ProjectID != "" {
		t.Fatalf("generic template must be project independent: %+v", generic)
	}
	if len(generic.Requirements) == 0 {
		t.Fatal("generic template must declare its runtime requirements")
	}
	mrNode := (*domain.WorkflowNode)(nil)
	for index := range generic.Nodes {
		if generic.Nodes[index].ID == "mr" {
			mrNode = &generic.Nodes[index]
		}
	}
	if mrNode == nil || mrNode.Type != "agent" {
		t.Fatalf("generic template must ship an agent MR node: %+v", generic.Nodes)
	}
	// The template is not a database row, so it can never be shadowed by one.
	if workflowRowExists(t, s, repo.GenericBackendDeliveryWorkflowID) {
		t.Fatal("generic template must not be seeded into the workflows table")
	}

	detail := requestEmptyJSON[domain.WorkflowDefinition](t, h, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID, http.StatusOK)
	if detail.ID != repo.GenericBackendDeliveryWorkflowID || !detail.Generic {
		t.Fatalf("generic template detail = %+v", detail)
	}
	if code := doStatus(h, http.MethodDelete, "/api/workflows/"+repo.GenericBackendDeliveryWorkflowID); code != http.StatusConflict {
		t.Fatalf("generic template delete status = %d, want 409", code)
	}
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: repo.GenericBackendDeliveryWorkflowID, Name: "hijack", SandboxProfileID: "local",
	}, http.StatusConflict)
	if workflowRowExists(t, s, repo.GenericBackendDeliveryWorkflowID) {
		t.Fatal("generic template was written into the workflows table")
	}
	revisions := requestEmptyJSON[[]domain.DefinitionRevision](t, h, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/revisions", http.StatusOK)
	if len(revisions) != 0 {
		t.Fatalf("generic template revisions = %+v", revisions)
	}
}

func TestAdoptedTemplateRolesAreDeletableOnceTheCopyIsGone(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "alpha-backend-delivery", "name": "后端交付（复制）", "projectId": "alpha"},
		http.StatusCreated)
	if agent, ok := s.Agent(repo.GenericAgentMR); !ok || agent.ProjectID != "alpha" {
		t.Fatalf("expected alpha to own the plain template role id, got %+v ok=%v", agent, ok)
	}
	if code := doStatus(h, http.MethodDelete, "/api/agents/"+repo.GenericAgentMR); code != http.StatusConflict {
		t.Fatalf("role still used by the adopted copy: delete status = %d, want 409", code)
	}
	if code := doStatus(h, http.MethodDelete, "/api/workflows/alpha-backend-delivery"); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("delete adopted copy status = %d", code)
	}
	// The generic template itself runs its built-in role, never alpha's copy,
	// so it must not pin the project role forever.
	if code := doStatus(h, http.MethodDelete, "/api/agents/"+repo.GenericAgentMR); code != http.StatusNoContent {
		t.Fatalf("delete orphaned adopted role status = %d, want 204", code)
	}
	if _, ok := s.GenericAgentByID(repo.GenericAgentMR); !ok {
		t.Fatal("template role must survive deleting the project copy")
	}
}

func TestGenericTemplateCloneCreatesProjectRolesAndRemapsNodes(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	gongfeng := domain.MCPServerDefinition{
		ID: "mcp-61-project-alpha", Name: "Gongfeng MCP", Scope: "project", ProjectID: "alpha",
		MarketID: "61", Transport: "http", URL: "https://mcpgw.knot.example.com/gongfeng", AuthType: "taihu-pat",
	}
	if err := s.PutMCPServer(gongfeng); err != nil {
		t.Fatal(err)
	}
	clone := func(id string) domain.WorkflowDefinition {
		return requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
			"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
			map[string]any{"id": id, "name": "后端交付（复制）", "owner": "alpha-team", "projectId": "alpha"},
			http.StatusCreated)
	}
	copied := clone("alpha-backend-delivery")
	if copied.ProjectID != "alpha" || copied.Generic || copied.Shared {
		t.Fatalf("adopted workflow scope = %+v", copied)
	}
	if copied.Status != "draft" || copied.Version != 1 {
		t.Fatalf("adopted workflow must start as a fresh draft: %+v", copied)
	}
	if copied.SandboxProfileID != repo.GenericDockerSandboxID {
		t.Fatalf("adopted sandbox = %q, want %s", copied.SandboxProfileID, repo.GenericDockerSandboxID)
	}
	if len(copied.Requirements) == 0 {
		t.Fatal("adopted workflow must keep the dependency declaration")
	}
	if len(copied.Nodes) == 0 {
		t.Fatal("adopted workflow lost its nodes")
	}
	remapped := map[string]string{}
	for _, node := range copied.Nodes {
		if node.Type != "agent" || node.AgentID == "" {
			continue
		}
		agent, found := s.Agent(node.AgentID)
		if !found {
			t.Fatalf("node %s references missing agent %q", node.ID, node.AgentID)
		}
		if agent.ProjectID != "alpha" {
			t.Fatalf("node %s bound to agent from project %q", node.ID, agent.ProjectID)
		}
		remapped[node.AgentID] = node.ID
	}
	if len(remapped) == 0 {
		t.Fatal("adopted workflow has no agent nodes")
	}
	// LoopForge rules and skills must be cloned into the project alongside the
	// roles, and the role bindings must point at the cloned assets.
	for _, id := range []string{repo.GenericRuleCore, repo.GenericRuleBackendProfile} {
		rule, found := s.Rule(id)
		if !found {
			t.Fatalf("template rule %q was not cloned into the project", id)
		}
		if rule.ProjectID != "alpha" || rule.Shared || rule.Content == "" {
			t.Fatalf("cloned rule %q = %+v", id, rule)
		}
	}
	for _, id := range []string{repo.GenericSkillClarifyRequirements, repo.GenericSkillBrainstorming, repo.GenericSkillTechDesign, repo.GenericSkillWritingPlans, repo.GenericSkillKnowledgeDistill} {
		skill, found := s.Skill(id)
		if !found {
			t.Fatalf("template skill %q was not cloned into the project", id)
		}
		if skill.ProjectID != "alpha" || skill.Shared || skill.Content == "" {
			t.Fatalf("cloned skill %q = %+v", id, skill)
		}
	}
	var coreBindings int
	for _, agent := range s.Agents() {
		if agent.ProjectID != "alpha" {
			continue
		}
		for _, id := range agent.Rules {
			if id == repo.GenericRuleCore {
				coreBindings++
			}
		}
	}
	if coreBindings != 7 {
		t.Fatalf("LoopForge core rule bound by %d project roles, want 7", coreBindings)
	}
	var mrAgentID string
	for _, node := range copied.Nodes {
		if node.ID == "mr" {
			mrAgentID = node.AgentID
		}
	}
	if mrAgentID == "" {
		t.Fatal("adopted workflow lost the MR node")
	}
	mrAgent, _ := s.Agent(mrAgentID)
	foundMCP := false
	for _, id := range mrAgent.MCPServers {
		if id == gongfeng.ID {
			foundMCP = true
		}
	}
	if !foundMCP {
		t.Fatalf("MR agent MCP wiring = %#v, want %q", mrAgent.MCPServers, gongfeng.ID)
	}
	// The template role ids stay stable, so re-adopting reuses the project roles
	// instead of stacking duplicates or overwriting local edits.
	agentsBefore := len(s.Agents())
	if _, ok := s.Agent(repo.GenericAgentMR); !ok {
		t.Fatalf("expected template role id %q in the project", repo.GenericAgentMR)
	}
	clone("alpha-backend-delivery-2")
	if got := len(s.Agents()); got != agentsBefore {
		t.Fatalf("re-adopting created duplicate roles: agents %d -> %d", agentsBefore, got)
	}
	// beta did not get the plain template ids (alpha owns them), so its clones
	// use the deterministic <id>-beta suffix. Adopt now installs the default
	// Gongfeng MCP into beta and wires the MR role to that project copy.
	betaCopy := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "beta-backend-delivery", "name": "后端交付（beta）", "projectId": "beta"},
		http.StatusCreated)
	if betaCopy.ProjectID != "beta" {
		t.Fatalf("beta copy = %+v", betaCopy)
	}
	betaGongfeng, ok := s.MCPServer(mcpInstallID("61", "project", "beta"))
	if !ok || betaGongfeng.MarketID != "61" || betaGongfeng.ProjectID != "beta" {
		t.Fatalf("beta should receive the default Gongfeng MCP, got %+v ok=%v", betaGongfeng, ok)
	}
	betaMrAgentID := ""
	for _, node := range betaCopy.Nodes {
		if node.ID != "mr" {
			continue
		}
		agent, _ := s.Agent(node.AgentID)
		if len(agent.MCPServers) != 1 || agent.MCPServers[0] != betaGongfeng.ID {
			t.Fatalf("beta MR agent MCP wiring = %#v, want %q", agent.MCPServers, betaGongfeng.ID)
		}
		betaMrAgentID = node.AgentID
	}
	if betaMrAgentID != repo.GenericAgentMR+"-beta" {
		t.Fatalf("beta MR agent id = %q, want deterministic %q", betaMrAgentID, repo.GenericAgentMR+"-beta")
	}
	if _, found := s.Rule(repo.GenericRuleCore + "-beta"); !found {
		t.Fatal("beta core rule must use the deterministic project-suffixed id")
	}
	// Re-adopting in beta must reuse the deterministic clones instead of
	// stacking a fresh random-suffixed asset set on every adoption.
	betaAssetsBefore := len(s.Agents()) + len(s.Rules()) + len(s.Skills())
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "beta-backend-delivery-2", "name": "后端交付（beta 2）", "projectId": "beta"},
		http.StatusCreated)
	betaCopy2 := requestEmptyJSON[domain.WorkflowDefinition](t, h, http.MethodGet,
		"/api/workflows/beta-backend-delivery-2", http.StatusOK)
	for _, node := range betaCopy2.Nodes {
		if node.ID != "mr" {
			continue
		}
		if node.AgentID != betaMrAgentID {
			t.Fatalf("second beta adoption re-cloned the MR role: %q != %q", node.AgentID, betaMrAgentID)
		}
	}
	if got := len(s.Agents()) + len(s.Rules()) + len(s.Skills()); got != betaAssetsBefore {
		t.Fatalf("second beta adoption created duplicate assets: %d -> %d", betaAssetsBefore, got)
	}
	// Cloning a generic template requires a concrete project.
	requestJSON[map[string]string](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "orphan", "name": "orphan"}, http.StatusBadRequest)
	// ...and the project must actually exist.
	requestJSON[map[string]string](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "ghost", "name": "ghost", "projectId": "ghost"}, http.StatusBadRequest)
}

// No clone may occupy a generic template id: the store row would shadow the
// built-in template in the detail endpoint. This covers both a regular source
// SOP and the template itself.
func TestGenericTemplateIDCannotBeTakenByAnyClone(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "regular-sop", Name: "普通 SOP", SandboxProfileID: "local", ProjectID: "alpha", Nodes: []domain.WorkflowNode{}, Edges: []domain.WorkflowEdge{},
	}, http.StatusOK)
	requestJSON[map[string]string](t, h, http.MethodPost,
		"/api/workflows/regular-sop/clone",
		map[string]any{"id": repo.GenericBackendDeliveryWorkflowID, "name": "hijack", "projectId": "alpha"},
		http.StatusConflict)
	// Cloning the template onto its own id is rejected as well.
	requestJSON[map[string]string](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": repo.GenericBackendDeliveryWorkflowID, "name": "self", "projectId": "alpha"},
		http.StatusConflict)
	// The catalog template still resolves and keeps its generic identity.
	detail := requestEmptyJSON[domain.WorkflowDefinition](t, h, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID, http.StatusOK)
	if !detail.Generic {
		t.Fatalf("generic template shadowed: %+v", detail)
	}
	if workflowRowExists(t, s, repo.GenericBackendDeliveryWorkflowID) {
		t.Fatal("a workflows-table row was created for a generic template id")
	}
}

// Rows written before the reserved-id rule existed must not shadow the
// template: the catalog wins in the detail endpoint and the duplicate is
// filtered out of the list.
func TestGenericTemplateSurvivesLegacyCollidingRow(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	legacy := domain.WorkflowDefinition{
		ID: repo.GenericBackendDeliveryWorkflowID, Name: "历史同 ID SOP", SandboxProfileID: "local",
		ProjectID: "alpha", Status: "draft", Version: 1, Nodes: []domain.WorkflowNode{}, Edges: []domain.WorkflowEdge{},
	}
	if err := s.PutWorkflow(legacy); err != nil {
		t.Fatal(err)
	}
	expected, _ := s.GenericWorkflowByID(repo.GenericBackendDeliveryWorkflowID)
	detail := requestEmptyJSON[domain.WorkflowDefinition](t, h, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID, http.StatusOK)
	if !detail.Generic || detail.Name != expected.Name {
		t.Fatalf("legacy row shadowed the template: %+v", detail)
	}
	listed := requestEmptyJSON[[]domain.WorkflowDefinition](t, h, http.MethodGet, "/api/workflows", http.StatusOK)
	seen := 0
	for _, workflow := range listed {
		if workflow.ID != repo.GenericBackendDeliveryWorkflowID {
			continue
		}
		seen++
		if !workflow.Generic {
			t.Fatalf("list returned the legacy row instead of the template: %+v", workflow)
		}
	}
	if seen != 1 {
		t.Fatalf("list returned %d entries for the reserved id, want 1", seen)
	}
	// Bootstrap (the Studio first paint) must apply the same rule.
	bootstrap := requestEmptyJSON[struct {
		Workflows []domain.WorkflowDefinition `json:"workflows"`
	}](t, h, http.MethodGet, "/api/bootstrap", http.StatusOK)
	bootstrapSeen := 0
	for _, workflow := range bootstrap.Workflows {
		if workflow.ID != repo.GenericBackendDeliveryWorkflowID {
			continue
		}
		bootstrapSeen++
		if !workflow.Generic {
			t.Fatalf("bootstrap returned the legacy row instead of the template: %+v", workflow)
		}
	}
	if bootstrapSeen != 1 {
		t.Fatalf("bootstrap returned %d entries for the reserved id, want 1", bootstrapSeen)
	}
	// The legacy row is untouched in the store (no silent data deletion) and
	// the template stays read-only for writes.
	if !workflowRowExists(t, s, repo.GenericBackendDeliveryWorkflowID) {
		t.Fatal("legacy row must not be deleted by the read path")
	}
	if code := doStatus(h, http.MethodDelete, "/api/workflows/"+repo.GenericBackendDeliveryWorkflowID); code != http.StatusConflict {
		t.Fatalf("delete on a reserved id = %d, want 409", code)
	}
}

// Generic templates run by reference: the run must carry a project (sandbox,
// repositories and MCP bindings hang off it), and a legacy colliding row
// occupying the reserved id must never be executed — the catalog definition
// is what runs, matching what Studio displays.
func TestGenericTemplateRunsDirectlyByReference(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := repo.SeedTestGenericTemplates(s); err != nil {
		t.Fatal(err)
	}
	for _, project := range []domain.Project{{ID: "alpha", Key: "ALPHA", Name: "Alpha", OwnerID: 1, DefaultRepositoryID: "alpha-repo"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRepository(domain.Repository{ID: "alpha-repo", ProjectID: "alpha", Name: "Repo", Provider: "gongfeng", CloneURL: "https://example.test/repo.git", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "local", Name: "Local", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	legacy := domain.WorkflowDefinition{
		ID: repo.GenericBackendDeliveryWorkflowID, Name: "历史同 ID SOP", SandboxProfileID: "local",
		ProjectID: "alpha", Status: "draft", Version: 1, Nodes: []domain.WorkflowNode{}, Edges: []domain.WorkflowEdge{},
	}
	if err := s.PutWorkflow(legacy); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "ws")))
	h := New(s, eng, tracker.NewService(), "", "")
	run := requestJSON[domain.Run](t, h, http.MethodPost, "/api/runs",
		map[string]any{"workflowId": repo.GenericBackendDeliveryWorkflowID, "input": "x", "projectId": "alpha"},
		http.StatusCreated)
	t.Cleanup(func() {
		_ = eng.Cancel(run.ID)
		// Wait for the run to fully stop so the workspace quiesces before
		// TempDir RemoveAll — a still-running node goroutine recreates
		// files underneath the cleanup and races it.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			current, ok := s.Run(run.ID)
			if !ok || current.Status != "running" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	if run.WorkflowSnapshot.ID != repo.GenericBackendDeliveryWorkflowID || !run.WorkflowSnapshot.Generic {
		t.Fatalf("run should snapshot the catalog template, not the legacy row: %+v", run.WorkflowSnapshot)
	}
	if run.WorkflowSnapshot.SandboxProfileID != repo.GenericDockerSandboxID {
		t.Fatalf("direct generic run sandbox = %q, want %s", run.WorkflowSnapshot.SandboxProfileID, repo.GenericDockerSandboxID)
	}
	if run.ExecutionContext.ProjectID != "alpha" {
		t.Fatalf("run should belong to the requesting project: %+v", run.ExecutionContext)
	}
	if run.Repository.RepositoryID != "alpha-repo" {
		t.Fatalf("direct generic run should use the project default repository: %+v", run.Repository)
	}
}

func TestGenericTemplateDirectRunRejectsMissingDefaultRepository(t *testing.T) {
	h, _ := newGenericTemplateServer(t)
	requestJSON[map[string]string](t, h, http.MethodPost, "/api/runs",
		map[string]any{"workflowId": repo.GenericBackendDeliveryWorkflowID, "input": "x", "projectId": "alpha"},
		http.StatusConflict)
}

// First adoption binds the platform default Gongfeng MCP. Re-adopting must
// not overwrite a role that already has a binding — that may be a local edit.
func TestGenericTemplateReadoptKeepsLocalMCPBinding(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	clone := func(id string) domain.WorkflowDefinition {
		return requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
			"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
			map[string]any{"id": id, "name": id, "projectId": "alpha"}, http.StatusCreated)
	}
	clone("alpha-backend-delivery")
	mrAgent, _ := s.Agent(repo.GenericAgentMR)
	if len(mrAgent.MCPServers) != 1 || mrAgent.MCPServers[0] != mcpInstallID("61", "project", "alpha") {
		t.Fatalf("first adoption should bind the default Gongfeng MCP: %#v", mrAgent.MCPServers)
	}
	mrAgent.MCPServers = []string{"custom-mcp"}
	mrAgent.UpdatedAt = time.Now().UTC()
	if err := s.PutAgent(mrAgent); err != nil {
		t.Fatal(err)
	}
	clone("alpha-backend-delivery-2")
	mrAgent, _ = s.Agent(repo.GenericAgentMR)
	if len(mrAgent.MCPServers) != 1 || mrAgent.MCPServers[0] != "custom-mcp" {
		t.Fatalf("re-adoption overwrote a local binding: %#v", mrAgent.MCPServers)
	}
}

// A failed workflow persist keeps the cloned assets on purpose: a concurrent
// adoption may already reference them, so deleting would break a healthy SOP.
// They are pre-created assets — the retry reuses them idempotently.
func TestGenericTemplateAdoptFailureKeepsAssetsForRetry(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	// A colliding row with a higher revision makes PutWorkflow fail with a
	// revision conflict after the assets were already cloned.
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "conflicting-id", Name: "已有 SOP", SandboxProfileID: "local", ProjectID: "alpha", Revision: 5,
	}); err != nil {
		t.Fatal(err)
	}
	requestJSON[map[string]string](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "conflicting-id", "name": "x", "projectId": "alpha"}, http.StatusBadRequest)
	if _, ok := s.Agent(repo.GenericAgentMR); !ok {
		t.Fatal("MR role must survive a failed adoption")
	}
	if _, ok := s.Rule(repo.GenericRuleCore); !ok {
		t.Fatal("core rule must survive a failed adoption")
	}
	if _, ok := s.Skill(repo.GenericSkillTechDesign); !ok {
		t.Fatal("tech-design skill must survive a failed adoption")
	}
	assetsAfterFailure := len(s.Agents()) + len(s.Rules()) + len(s.Skills())
	// After the collision is gone, the retry succeeds and must reuse the
	// pre-created assets instead of stacking duplicates.
	if err := s.DeleteWorkflow("conflicting-id"); err != nil {
		t.Fatal(err)
	}
	copied := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "alpha-backend-delivery", "name": "x", "projectId": "alpha"}, http.StatusCreated)
	if got := len(s.Agents()) + len(s.Rules()) + len(s.Skills()); got != assetsAfterFailure {
		t.Fatalf("retry created duplicate assets: %d -> %d", assetsAfterFailure, got)
	}
	if _, ok := s.Rule(repo.GenericRuleCore); !ok {
		t.Fatal("retry must reuse the pre-created core rule id")
	}
	var mrAgentID string
	for _, node := range copied.Nodes {
		if node.ID == "mr" {
			mrAgentID = node.AgentID
		}
	}
	if mrAgentID != repo.GenericAgentMR {
		t.Fatalf("retry must reuse the pre-created MR role: %q", mrAgentID)
	}
}

func TestGenericTemplateCloneThenEditIsAllowed(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	copied := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/clone",
		map[string]any{"id": "alpha-backend-delivery", "name": "后端交付", "projectId": "alpha"},
		http.StatusCreated)
	copied.Description = "本地调整"
	saved := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", copied, http.StatusOK)
	if saved.Description != "本地调整" {
		t.Fatalf("adopted workflow is not editable: %+v", saved)
	}
	if _, ok := s.Workflow(copied.ID); !ok {
		t.Fatal("adopted workflow was not persisted")
	}
}

func TestPlatformAdminCanEditGenericTemplate(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "generic-admin-session-secret")
	h, s := newGenericTemplateServer(t)
	if err := s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Robert"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByLoginName("example-admin")
	if !s.IsPlatformAdmin(admin.LoginName) {
		t.Fatal("example-admin must be a platform admin")
	}
	detail := requestJSONAuthed[domain.WorkflowDefinition](t, h, admin, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID, nil, http.StatusOK)
	detail.Description = "admin patched the platform template"
	saved := requestJSONAuthed[domain.WorkflowDefinition](t, h, admin, http.MethodPut, "/api/workflows", detail, http.StatusOK)
	if saved.Description != "admin patched the platform template" || !saved.Generic {
		t.Fatalf("admin save = %+v", saved)
	}
	if workflowRowExists(t, s, repo.GenericBackendDeliveryWorkflowID) {
		t.Fatal("admin edit must stay in generic_templates, not workflows")
	}
	got, ok := s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	if !ok || got.Workflow.Description != "admin patched the platform template" {
		t.Fatalf("stored template was not updated: %+v", got.Workflow)
	}
	if err := s.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.UserByLoginName("alice")
	requestJSONAuthed[map[string]string](t, h, alice, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: repo.GenericBackendDeliveryWorkflowID, Name: "hijack", SandboxProfileID: "local",
	}, http.StatusConflict)
	published := requestJSONAuthed[domain.WorkflowDefinition](t, h, admin, http.MethodPost,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/publish", map[string]any{}, http.StatusOK)
	if published.Version < 2 {
		t.Fatalf("admin publish version = %d", published.Version)
	}
}

func TestGenericTemplateAgentsAreNotTheCurrentProjectCatalog(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "generic-admin-session-secret")
	h, s := newGenericTemplateServer(t)
	if err := s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Robert"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{
		ID: repo.GenericAgentReview, Name: "SkillHub 独立评审", ProjectID: "alpha",
		Instructions: "this is the project copy", Version: 3,
	}); err != nil {
		t.Fatal(err)
	}
	listed := requestEmptyJSON[[]domain.AgentDefinition](t, h, http.MethodGet,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", http.StatusOK)
	var review *domain.AgentDefinition
	for index := range listed {
		if listed[index].ID == repo.GenericAgentReview {
			review = &listed[index]
		}
	}
	if review == nil || review.ProjectID != "" || review.Name == "SkillHub 独立评审" || review.Instructions == "this is the project copy" {
		t.Fatalf("editor must load the bundled template role, not the project catalog: %+v", listed)
	}
	admin, _ := s.UserByLoginName("example-admin")
	review.Instructions = "platform-only review brief"
	if review.TaskPrompt != nil {
		review.TaskPrompt.Objective = "keep editing the LoopForge role"
	}
	saved := requestJSONAuthed[domain.AgentDefinition](t, h, admin, http.MethodPut,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", *review, http.StatusOK)
	if saved.Instructions != "platform-only review brief" || saved.ProjectID != "" {
		t.Fatalf("admin role save = %+v", saved)
	}
	projectCopy, _ := s.Agent(repo.GenericAgentReview)
	if projectCopy.Instructions != "this is the project copy" {
		t.Fatalf("saving the template role overwrote the project catalog: %+v", projectCopy)
	}
	if err := s.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.UserByLoginName("alice")
	requestJSONAuthed[map[string]string](t, h, alice, http.MethodPut,
		"/api/workflows/"+repo.GenericBackendDeliveryWorkflowID+"/agents", *review, http.StatusConflict)
}

func TestPlatformAdminsAPIIsConfigurable(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "generic-admin-session-secret")
	h, s := newGenericTemplateServer(t)
	if err := s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Robert"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByLoginName("example-admin")
	alice, _ := s.UserByLoginName("alice")
	requestJSONAuthed[map[string]string](t, h, alice, http.MethodGet, "/api/platform/admins", nil, http.StatusForbidden)
	listed := requestJSONAuthed[[]repo.PlatformAdminRecord](t, h, admin, http.MethodGet, "/api/platform/admins", nil, http.StatusOK)
	if len(listed) == 0 {
		t.Fatal("admin list must include builtin owners")
	}
	afterGrant := requestJSONAuthed[[]repo.PlatformAdminRecord](t, h, admin, http.MethodPut, "/api/platform/admins",
		map[string]any{"loginName": "alice", "role": "admin"}, http.StatusOK)
	found := false
	for _, rec := range afterGrant {
		if rec.LoginName == "alice" && rec.Revocable {
			found = true
		}
	}
	if !found {
		t.Fatalf("granted alice missing: %+v", afterGrant)
	}
	if !s.IsPlatformAdmin("alice") {
		t.Fatal("alice should be platform admin after grant")
	}
}

func TestPutDeletedWorkflowIsRejected(t *testing.T) {
	h, s := newGenericTemplateServer(t)
	created := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "deleted-sop", Name: "Issue修复·沙箱", ProjectID: "alpha", SandboxProfileID: "local",
	}, http.StatusOK)
	if err := s.DeleteWorkflow(created.ID); err != nil {
		t.Fatal(err)
	}
	// A stale editor tab resubmits the payload it loaded, which still carries a
	// revision. That must not recreate the deleted row.
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: created.ID, Name: created.Name, Status: "published", Version: 6, Revision: created.Revision, SandboxProfileID: "local",
	}, http.StatusConflict)
	if workflowRowExists(t, s, created.ID) {
		t.Fatal("deleted workflow was resurrected by a stale save")
	}
	recreated := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: created.ID, Name: "Recreated", ProjectID: "alpha", SandboxProfileID: "local",
	}, http.StatusOK)
	if recreated.Revision != 1 || recreated.ProjectID != "alpha" {
		t.Fatalf("recreate = %+v", recreated)
	}
}

func TestProductAndFrontendGenericTemplatesAreListed(t *testing.T) {
	h, _ := newGenericTemplateServer(t)
	for _, id := range []string{repo.GenericProductDeliveryWorkflowID, repo.GenericFrontendDeliveryWorkflowID} {
		detail := requestEmptyJSON[domain.WorkflowDefinition](t, h, http.MethodGet, "/api/workflows/"+id, http.StatusOK)
		if !detail.Generic || detail.ID != id || len(detail.Nodes) == 0 {
			t.Fatalf("template %q = %+v", id, detail)
		}
	}
}

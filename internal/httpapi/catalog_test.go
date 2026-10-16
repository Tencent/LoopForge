package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestPutAgentValidatesEffortLevel(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "effort-role", Name: "Effort", Version: 1, ProjectID: "gslb", ExecutorID: "codex", Effort: "xhigh",
	}, http.StatusOK)
	if saved.Effort != "xhigh" {
		t.Fatalf("saved effort = %q, want xhigh", saved.Effort)
	}
	errBody := requestJSON[map[string]string](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "effort-role", Name: "Effort", Version: 2, ProjectID: "gslb", ExecutorID: "codex", Effort: "ultracode",
	}, http.StatusBadRequest)
	if !strings.Contains(errBody["error"], "思考强度") {
		t.Fatalf("error = %#v", errBody)
	}
}

func TestExecutorModelsCatalogListsPerCLI(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	// The catalog is a public, non-sensitive list served without login.
	catalogs := requestEmptyJSON[map[string]executorModelCatalog](t, h, http.MethodGet, "/api/executor-models", http.StatusOK)
	cb, ok := catalogs["codebuddy"]
	if !ok || len(cb.Models) == 0 || cb.Default == "" {
		t.Fatalf("codebuddy catalog = %+v", cb)
	}
	if cb.Models[0].Credits != "" {
		t.Fatal("unconfigured public model must not claim a private price")
	}

}

func TestEnvironmentAssetsAreFilteredByProjectAndSharedScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, project := range []domain.Project{{ID: "alpha", Key: "ALPHA", Name: "Alpha"}, {ID: "beta", Key: "BETA", Name: "Beta"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []domain.DockerfileDefinition{
		{ID: "alpha-dockerfile", Name: "Alpha", ProjectID: "alpha", Content: "FROM alpine"},
		{ID: "beta-shared-dockerfile", Name: "Beta shared", ProjectID: "beta", Shared: true, Content: "FROM alpine"},
		{ID: "beta-private-dockerfile", Name: "Beta private", ProjectID: "beta", Content: "FROM alpine"},
	} {
		if err := s.PutDockerfile(item); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	items := requestEmptyJSON[[]domain.DockerfileDefinition](t, h, http.MethodGet, "/api/dockerfiles?projectId=alpha", http.StatusOK)
	visible := map[string]bool{}
	for _, item := range items {
		visible[item.ID] = true
	}
	if !visible["alpha-dockerfile"] || !visible["beta-shared-dockerfile"] || visible["beta-private-dockerfile"] {
		t.Fatalf("unexpected project catalog visibility: %#v", visible)
	}
}

func TestWorkflowAgentsAreFilteredAndValidatedByProject(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, project := range []domain.Project{{ID: "alpha", Key: "ALPHA", Name: "Alpha"}, {ID: "beta", Key: "BETA", Name: "Beta"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []domain.AgentDefinition{
		{ID: "alpha-role", Name: "Alpha role", ProjectID: "alpha", Version: 1},
		{ID: "beta-shared-role", Name: "Beta shared", ProjectID: "beta", Shared: true, Version: 1},
		{ID: "beta-private-role", Name: "Beta private", ProjectID: "beta", Version: 1},
		{ID: "legacy-role", Name: "Legacy global", Version: 1},
	} {
		if err := s.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	agents := requestEmptyJSON[[]domain.AgentDefinition](t, h, http.MethodGet, "/api/agents?projectId=alpha", http.StatusOK)
	visible := map[string]bool{}
	for _, agent := range agents {
		visible[agent.ID] = true
	}
	if !visible["alpha-role"] || !visible["beta-shared-role"] || visible["beta-private-role"] || visible["legacy-role"] {
		t.Fatalf("unexpected workflow role visibility: %#v", visible)
	}

	invalid := domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "beta-private-role"}},
	}
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", invalid, http.StatusBadRequest)
	invalid.Nodes[0].AgentID = "beta-shared-role"
	saved := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", invalid, http.StatusOK)
	if saved.Nodes[0].AgentID != "beta-shared-role" {
		t.Fatalf("shared role was not saved: %+v", saved.Nodes)
	}
	legacyPayload := map[string]any{
		"id": "legacy-node-gate", "name": "Legacy node gate", "projectId": "alpha", "sandboxProfileId": "local",
		"nodes": []any{map[string]any{
			"id": "work", "name": "Work", "type": "agent", "agentId": "alpha-role",
			"verification": []any{map[string]any{"id": "obsolete", "mode": "command", "commands": []string{"false"}}},
		}},
	}
	stored := requestJSON[map[string]any](t, h, http.MethodPut, "/api/workflows", legacyPayload, http.StatusOK)
	nodes, _ := stored["nodes"].([]any)
	node, _ := nodes[0].(map[string]any)
	if _, exists := node["verification"]; exists {
		t.Fatalf("workflow API retained removed node verification: %#v", node)
	}
}

func TestSharingSandboxCascadesDependenciesAndSharedWorkflowRequiresSharedSandbox(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDockerfile(domain.DockerfileDefinition{ID: "alpha-dockerfile", Name: "Alpha image", ProjectID: "alpha", Content: "FROM alpine"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnvironment(domain.EnvironmentDefinition{ID: "alpha-tools", Name: "Alpha tools", ProjectID: "alpha", InstallScript: "true"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/sandboxes", domain.SandboxProfile{
		ID: "alpha-container", Name: "Alpha container", ProjectID: "alpha", Shared: true, Driver: "docker", DockerfileID: "alpha-dockerfile",
	}, http.StatusOK)
	dockerfile, _ := s.Dockerfile("alpha-dockerfile")
	if !dockerfile.Shared {
		t.Fatal("sharing a sandbox did not share its Dockerfile")
	}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/sandboxes", domain.SandboxProfile{
		ID: "alpha-process", Name: "Alpha process", ProjectID: "alpha", Driver: "bwrap", EnvironmentID: "alpha-tools",
	}, http.StatusOK)
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", Shared: true, SandboxProfileID: "alpha-process",
	}, http.StatusBadRequest)
	profile, _ := s.Sandbox("alpha-process")
	environment, _ := s.Environment("alpha-tools")
	if profile.Shared || environment.Shared {
		t.Fatalf("rejected workflow unexpectedly shared its environment chain: sandbox=%+v environment=%+v", profile, environment)
	}
}

func TestCatalogAgentsKeepsProjectScope(t *testing.T) {
	source := []domain.AgentDefinition{{
		ID: "gslb-fix", Name: "Fix", Version: 2, ProjectID: "gslb", Shared: true, PlatformMCP: true,
		Description:     strings.Repeat("x", 300),
		Instructions:    "secret playbook",
		TaskPrompt:      &domain.TaskPromptSpec{Objective: "fix"},
		ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "fix-plan.md"}},
		Verification: []domain.VerificationGate{{
			ID: "plan", Name: "Plan", Mode: "command",
			Commands: []string{"test -s fix-plan.md"}, ExecutionScope: domain.ExecutionScopeRun,
		}},
	}}
	got := catalogAgents(source)
	if len(got) != 1 || got[0].ProjectID != "gslb" || !got[0].Shared || got[0].Name != "Fix" {
		t.Fatalf("project scope stripped: %+v", got)
	}
	if !got[0].PlatformMCP {
		t.Fatalf("platform MCP binding lost in catalog agent: %+v", got[0])
	}
	if got[0].Instructions != "" || got[0].TaskPrompt != nil {
		t.Fatalf("catalog agent should stay slim: %+v", got[0])
	}
	if len(got[0].ArtifactOutputs) != 1 || len(got[0].Verification) != 1 {
		t.Fatalf("catalog agent should keep validation summary: %+v", got[0])
	}
	if !strings.HasSuffix(got[0].Description, "…") || len(got[0].Description) >= len(source[0].Description) {
		t.Fatalf("description not clipped: %q", got[0].Description)
	}
	if source[0].Instructions == "" {
		t.Fatal("catalogAgents mutated source")
	}
}

func TestAgentCloneStampsProjectAndKeepsBody(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, project := range []domain.Project{{ID: "other", Key: "OTHER", Name: "Other"}, {ID: "gslb", Key: "GSLB", Name: "GSLB"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutAgent(domain.AgentDefinition{
		ID: "src-role", Name: "Source", Version: 3, ProjectID: "other", Shared: true,
		Instructions: "keep me", TaskPrompt: &domain.TaskPromptSpec{Objective: "do it"},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	copied := requestJSON[domain.AgentDefinition](t, h, http.MethodPost, "/api/agents/src-role/clone", map[string]any{
		"id": "dst-role", "name": "Copy", "projectId": "gslb",
	}, http.StatusCreated)
	if copied.ID != "dst-role" || copied.Name != "Copy" || copied.ProjectID != "gslb" || copied.Shared || copied.Instructions != "keep me" || copied.Version != 1 {
		t.Fatalf("clone = %+v", copied)
	}
	if copied.TaskPrompt == nil || copied.TaskPrompt.Objective != "do it" {
		t.Fatalf("clone dropped task book: %+v", copied.TaskPrompt)
	}
	src, ok := s.Agent("src-role")
	if !ok || !src.Shared || src.ProjectID != "other" || src.Version != 3 {
		t.Fatalf("source mutated: %+v", src)
	}
}

func TestAgentDeleteRequiresUnusedAndUnshared(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []domain.AgentDefinition{
		{ID: "free-role", Name: "Free role", ProjectID: "alpha", Version: 1},
		{ID: "referenced-role", Name: "Referenced role", ProjectID: "alpha", Version: 1},
		{ID: "shared-role", Name: "Shared role", ProjectID: "alpha", Shared: true, Version: 1},
	} {
		if err := s.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "referenced-role"}},
	}, http.StatusOK)
	if saved.Nodes[0].AgentID != "referenced-role" {
		t.Fatalf("workflow was not saved: %+v", saved)
	}

	// referenced agent must be rejected with 409
	requestEmptyJSON[map[string]any](t, h, http.MethodDelete, "/api/agents/referenced-role", http.StatusConflict)
	if _, ok := s.Agent("referenced-role"); !ok {
		t.Fatal("referenced agent was deleted despite workflow usage")
	}
	// shared agent must be rejected with 409
	requestEmptyJSON[map[string]any](t, h, http.MethodDelete, "/api/agents/shared-role", http.StatusConflict)
	if _, ok := s.Agent("shared-role"); !ok {
		t.Fatal("shared agent was deleted despite shared flag")
	}
	// unused, unshared agent is deleted with 204
	doDelete := func(path string) int {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := doDelete("/api/agents/free-role"); code != http.StatusNoContent {
		t.Fatalf("free agent delete status = %d", code)
	}
	if _, ok := s.Agent("free-role"); ok {
		t.Fatal("free agent was not deleted")
	}
	// deleting again returns 404
	if code := doDelete("/api/agents/free-role"); code != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d", code)
	}
}

func TestCatalogAssetDeleteRequiresUnusedAndUnshared(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutRule(domain.RuleDefinition{ID: "free-rule", Name: "Free rule", ProjectID: "alpha"}))
	must(s.PutRule(domain.RuleDefinition{ID: "used-rule", Name: "Used rule", ProjectID: "alpha"}))
	must(s.PutRule(domain.RuleDefinition{ID: "shared-rule", Name: "Shared rule", ProjectID: "alpha", Shared: true}))
	must(s.PutSkill(domain.SkillDefinition{ID: "free-skill", Name: "Free skill", ProjectID: "alpha"}))
	must(s.PutSkill(domain.SkillDefinition{ID: "used-skill", Name: "Used skill", ProjectID: "alpha"}))
	must(s.PutSkill(domain.SkillDefinition{ID: "shared-skill", Name: "Shared skill", ProjectID: "alpha", Shared: true}))
	must(s.PutSkillFiles("free-skill", []repo.SkillFileInput{{Path: "SKILL.md", Content: []byte("# free\n")}}))
	must(s.PutDockerfile(domain.DockerfileDefinition{ID: "free-df", Name: "Free df", ProjectID: "alpha", Content: "FROM scratch"}))
	must(s.PutDockerfile(domain.DockerfileDefinition{ID: "used-df", Name: "Used df", ProjectID: "alpha", Content: "FROM scratch"}))
	must(s.PutDockerfile(domain.DockerfileDefinition{ID: "shared-df", Name: "Shared df", ProjectID: "alpha", Shared: true, Content: "FROM scratch"}))
	must(s.PutEnvironment(domain.EnvironmentDefinition{ID: "free-env", Name: "Free env", ProjectID: "alpha"}))
	must(s.PutEnvironment(domain.EnvironmentDefinition{ID: "used-env", Name: "Used env", ProjectID: "alpha"}))
	must(s.PutEnvironment(domain.EnvironmentDefinition{ID: "shared-env", Name: "Shared env", ProjectID: "alpha", Shared: true}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "free-box", Name: "Free box", ProjectID: "alpha", Driver: "local"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "used-box", Name: "Used box", ProjectID: "alpha", Driver: "local"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "shared-box", Name: "Shared box", ProjectID: "alpha", Shared: true, Driver: "local"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "df-box", Name: "DF box", ProjectID: "alpha", Driver: "docker", DockerfileID: "used-df"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "env-box", Name: "Env box", ProjectID: "alpha", Driver: "seatbelt", EnvironmentID: "used-env"}))
	must(s.PutAgent(domain.AgentDefinition{
		ID: "asset-user", Name: "Asset user", ProjectID: "alpha", Version: 1,
		Rules: []string{"used-rule"}, Skills: []string{"used-skill"},
	}))
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "used-box",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "asset-user"}},
	}, http.StatusOK)

	doDelete := func(path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	assertConflict := func(path string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s status = %d, want 409; body %s", path, rec.Code, rec.Body.String())
		}
	}
	assertGone := func(path string, stillThere func() bool, label string) {
		t.Helper()
		if code := doDelete(path); code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, code)
		}
		if stillThere() {
			t.Fatalf("%s was not deleted", label)
		}
		if code := doDelete(path); code != http.StatusNotFound {
			t.Fatalf("repeat %s status = %d, want 404", path, code)
		}
	}

	if code := doDelete("/api/rules"); code != http.StatusBadRequest {
		t.Fatalf("missing id status = %d", code)
	}
	assertConflict("/api/rules?id=used-rule")
	assertConflict("/api/rules?id=shared-rule")
	assertGone("/api/rules?id=free-rule", func() bool { _, ok := s.Rule("free-rule"); return ok }, "free rule")

	assertConflict("/api/skills?id=used-skill")
	assertConflict("/api/skills?id=shared-skill")
	assertGone("/api/skills?id=free-skill", func() bool { _, ok := s.Skill("free-skill"); return ok }, "free skill")
	if _, ok := s.SkillFileContent("free-skill", "SKILL.md"); ok {
		t.Fatal("skill_files rows were not cascaded")
	}

	assertConflict("/api/dockerfiles?id=used-df")
	assertConflict("/api/dockerfiles?id=shared-df")
	assertGone("/api/dockerfiles?id=free-df", func() bool { _, ok := s.Dockerfile("free-df"); return ok }, "free dockerfile")

	assertConflict("/api/environments?id=used-env")
	assertConflict("/api/environments?id=shared-env")
	assertGone("/api/environments?id=free-env", func() bool { _, ok := s.Environment("free-env"); return ok }, "free environment")

	assertConflict("/api/sandboxes?id=used-box")
	assertConflict("/api/sandboxes?id=shared-box")
	assertConflict("/api/sandboxes?id=local")
	assertConflict("/api/sandboxes?id=" + repo.GenericDockerSandboxID)
	assertConflict("/api/dockerfiles?id=" + repo.GenericBackendDockerfileID)
	assertGone("/api/sandboxes?id=free-box", func() bool { _, ok := s.Sandbox("free-box"); return ok }, "free sandbox")
}

func TestSharingAgentCascadesToOwnedRulesAndSkills(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRule(domain.RuleDefinition{ID: "owned-rule", Name: "Owned rule", ProjectID: "gslb"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSkill(domain.SkillDefinition{ID: "owned-skill", Name: "Owned skill", ProjectID: "gslb"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "shared-role", Name: "Shared", Version: 1, ProjectID: "gslb", Shared: true,
		Rules: []string{"owned-rule"}, Skills: []string{"owned-skill"}, ExecutorID: "codebuddy",
	}, http.StatusOK)
	rule, _ := s.Rule("owned-rule")
	skill, _ := s.Skill("owned-skill")
	if !rule.Shared || !skill.Shared {
		t.Fatalf("dependencies were not shared: rule=%+v skill=%+v", rule, skill)
	}
}

func TestPutAgentRejectsCommandGateWithoutExecutionScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	errBody := requestJSON[map[string]string](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "scoped-role", Name: "Scoped", Version: 1, ProjectID: "gslb", ExecutorID: "codebuddy",
		Verification: []domain.VerificationGate{{ID: "plan", Name: "plan", Mode: "command", Commands: []string{`test -f fix-plan.md`}}},
	}, http.StatusBadRequest)
	if !strings.Contains(errBody["error"], "executionScope") {
		t.Fatalf("error = %#v", errBody)
	}
	saved := requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "scoped-role", Name: "Scoped", Version: 1, ProjectID: "gslb", ExecutorID: "codebuddy",
		Verification: []domain.VerificationGate{{ID: "plan", Name: "plan", Mode: "command", ExecutionScope: domain.ExecutionScopeRun, Commands: []string{`test -f fix-plan.md`}}},
	}, http.StatusOK)
	if len(saved.Verification) != 1 || saved.Verification[0].ExecutionScope != domain.ExecutionScopeRun {
		t.Fatalf("saved gates = %#v", saved.Verification)
	}
}

func TestCloneAgentMCPServersCopiesByTargetScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, server := range []domain.MCPServerDefinition{
		{ID: "project-source", Name: "Project", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/project"},
		{ID: "personal-source", Name: "Personal", Scope: "personal", OwnerUserID: 9, Transport: "http", URL: "https://example.com/personal"},
	} {
		if err := s.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{deps: &deps{repo: s}}
	server.catalog = &catalogHandlers{deps: server.deps}
	ids, err := server.catalog.cloneAgentMCPServers([]string{"project-source", "personal-source"}, "target", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("cloned ids = %#v", ids)
	}
	project, ok := s.MCPServer(ids[0])
	if !ok || project.Scope != "project" || project.ProjectID != "target" || project.OwnerUserID != 0 {
		t.Fatalf("project MCP copy = %+v", project)
	}
	personal, ok := s.MCPServer(ids[1])
	if !ok || personal.Scope != "personal" || personal.OwnerUserID != 2 || personal.ProjectID != "" {
		t.Fatalf("personal MCP copy = %+v", personal)
	}
}

func TestRunListFallsBackToIssueProject(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	if err := s.PutIssue(domain.Issue{ID: "iss-legacy", ProjectID: "gslb", Title: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{
		ID: "legacy-run", WorkflowID: "issue-fix-production", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{IssueID: "iss-legacy"},
		CreatedAt:        now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	listed := requestJSON[[]domain.Run](t, h, http.MethodGet, "/api/runs?projectId=gslb", nil, http.StatusOK)
	if len(listed) != 1 || listed[0].ID != "legacy-run" {
		t.Fatalf("gslb run list = %+v", listed)
	}
	other := requestJSON[[]domain.Run](t, h, http.MethodGet, "/api/runs?projectId=platform", nil, http.StatusOK)
	for _, item := range other {
		if item.ID == "legacy-run" {
			t.Fatal("legacy run leaked into another project list")
		}
	}
}

func TestBootstrapAgentsKeepProjectScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutAgent(domain.AgentDefinition{
		ID: "owned-role", Name: "Owned", ProjectID: "gslb", Shared: true, Instructions: "do not leak",
		ArtifactOutputs: []domain.ArtifactContract{{Name: "report", Path: "report.md", Required: true}},
		Verification:    []domain.VerificationGate{{ID: "report", Name: "report", Mode: "command", Commands: []string{"test -s report.md"}, ExecutionScope: domain.ExecutionScopeRun}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	boot := requestEmptyJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", http.StatusOK)
	raw, _ := boot["agents"].([]any)
	var found map[string]any
	for _, item := range raw {
		agent, _ := item.(map[string]any)
		if agent["id"] == "owned-role" {
			found = agent
			break
		}
	}
	if found == nil {
		t.Fatal("bootstrap missing owned-role")
	}
	if found["projectId"] != "gslb" || found["shared"] != true {
		t.Fatalf("bootstrap dropped project scope: %+v", found)
	}
	if found["instructions"] != "" {
		t.Fatalf("bootstrap leaked instructions: %+v", found)
	}
	if artifacts, ok := found["artifactOutputs"].([]any); !ok || len(artifacts) != 1 {
		t.Fatalf("bootstrap dropped role artifact contracts: %+v", found)
	}
	if gates, ok := found["verification"].([]any); !ok || len(gates) != 1 {
		t.Fatalf("bootstrap dropped role verification gates: %+v", found)
	}
}

func TestProjectDefaultRepositoryMustAlreadyExist(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "other", Key: "OTHER", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepository(domain.Repository{ID: "other-repo", ProjectID: "other", Name: "other", GongfengProjectID: "other/repo"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	missing := requestJSON[map[string]string](t, h, http.MethodPost, "/api/projects", domain.Project{
		ID: "onboard", Key: "ONB", Name: "Onboard", DefaultRepositoryID: "onboard-repo",
	}, http.StatusBadRequest)
	if missing["error"] != "default repository must belong to project" {
		t.Fatalf("missing default repo: %+v", missing)
	}

	denied := requestJSON[map[string]string](t, h, http.MethodPost, "/api/projects", domain.Project{
		ID: "stolen", Key: "STL", Name: "Stolen", DefaultRepositoryID: "other-repo",
	}, http.StatusBadRequest)
	if denied["error"] != "default repository must belong to project" {
		t.Fatalf("wrong-project default repo: %+v", denied)
	}

	created := requestJSON[domain.Project](t, h, http.MethodPost, "/api/projects", domain.Project{
		ID: "onboard", Key: "ONB", Name: "Onboard",
	}, http.StatusOK)
	if created.DefaultRepositoryID != "" {
		t.Fatalf("new project must not carry a default repo, got %q", created.DefaultRepositoryID)
	}
	repo := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"id": "onboard-repo", "projectId": "onboard", "repoUrl": "https://git.example.com/onboard/app",
	}, http.StatusOK)
	bound := requestJSON[domain.Project](t, h, http.MethodPut, "/api/projects", domain.Project{
		ID: "onboard", Name: "Onboard", DefaultRepositoryID: repo.ID,
	}, http.StatusOK)
	if bound.DefaultRepositoryID != "onboard-repo" {
		t.Fatalf("default repo after create: %q", bound.DefaultRepositoryID)
	}
}

func TestNewProjectDefaultsToGenericBackendDeliverySOP(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	created := requestJSON[domain.Project](t, h, http.MethodPost, "/api/projects", domain.Project{ID: "fresh", Key: "FRESH", Name: "Fresh"}, http.StatusOK)
	if created.WorkflowID != repo.GenericBackendDeliveryWorkflowID {
		t.Fatalf("new project should default to the LoopForge SOP, got %q", created.WorkflowID)
	}
	assertProjectHasDefaultTrackerMCPs(t, s, "fresh")
	// 显式指定或修改 SOP 不被覆盖。
	updated := requestJSON[domain.Project](t, h, http.MethodPut, "/api/projects", domain.Project{ID: "fresh", Name: "Fresh 2", WorkflowID: "custom-sop"}, http.StatusOK)
	if updated.WorkflowID != "custom-sop" {
		t.Fatalf("explicit SOP must be kept, got %q", updated.WorkflowID)
	}
}

func TestRepositoryOneLinkSetup(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[domain.Project](t, h, http.MethodPost, "/api/projects", domain.Project{ID: "linkproj", Key: "LINK", Name: "Link"}, http.StatusOK)

	// 只粘贴一个工蜂链接：自动补项目路径、仓库名、URL，主分支默认 master。
	created := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "linkproj",
		"repoUrl":   "https://git.example.com/infra/gateway/-/tree/master",
	}, http.StatusOK)
	if created.GongfengProjectID != "infra/gateway" || created.Name != "gateway" {
		t.Fatalf("unexpected parse: %+v", created)
	}
	if created.DefaultBranch != "master" {
		t.Fatalf("default branch = %q, want master", created.DefaultBranch)
	}
	if created.IntegrateBranch != "" {
		t.Fatalf("new repo must not invent an integrate branch, got %q", created.IntegrateBranch)
	}
	if created.WebURL != "https://git.example.com/infra/gateway" || created.CloneURL != "https://git.example.com/infra/gateway.git" {
		t.Fatalf("unexpected urls: %+v", created)
	}

	// scp 形式同样可解析，显式字段优先于解析结果。
	updated := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId":     "linkproj",
		"repoUrl":       "git@git.example.com:infra/storage.git",
		"name":          "存储",
		"defaultBranch": "release",
	}, http.StatusOK)
	if updated.GongfengProjectID != "infra/storage" || updated.Name != "存储" || updated.DefaultBranch != "release" {
		t.Fatalf("unexpected scp parse / field priority: %+v", updated)
	}
	if updated.CloneURL != "git@git.example.com:infra/storage.git" {
		t.Fatalf("ssh paste should keep ssh clone url, got %q", updated.CloneURL)
	}

	cloned := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "linkproj",
		"repoUrl":   "git clone git@git.example.com:infra/dns.git",
	}, http.StatusOK)
	if cloned.GongfengProjectID != "infra/dns" || cloned.CloneURL != "git@git.example.com:infra/dns.git" {
		t.Fatalf("git clone prefix was not parsed: %+v", cloned)
	}

	// 项目 ID 字段本身粘贴完整链接（内联行/弹窗都可能发生）：必须规范化为
	// group/repo 而不是把 URL 当作项目路径入库，否则同步、建分支和 MR 全挂。
	pasted := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId":         "linkproj",
		"gongfengProjectId": "https://git.example.com/infra/cache",
	}, http.StatusOK)
	if pasted.GongfengProjectID != "infra/cache" {
		t.Fatalf("full link in project id must normalize, got %q", pasted.GongfengProjectID)
	}
	if pasted.WebURL != "https://git.example.com/infra/cache" || pasted.CloneURL != "https://git.example.com/infra/cache.git" {
		t.Fatalf("urls derived from normalized id are wrong: %+v", pasted)
	}

	// 链接形态但解析失败（非工蜂域名、缺项目路径等）必须 400，绝不能把 URL 当项目路径落库。
	for _, bad := range []string{
		"https://gitlab.invalid/infra/cache",
		"git@gitlab.invalid:infra/cache.git",
		"https://git.example.com",
		"https://",
		"infra/../etc",
		"infra/repo name",
		`infra\repo`,
	} {
		resp := requestJSON[map[string]string](t, h, http.MethodPost, "/api/repositories", map[string]any{
			"projectId":         "linkproj",
			"gongfengProjectId": bad,
		}, http.StatusBadRequest)
		if resp["error"] == "" {
			t.Fatalf("expected error message for %q", bad)
		}
	}

	// 普通项目路径与数字 ID 保持兼容。
	for _, good := range []string{"tencent/infra/gateway", "12345", "legacy-project_id"} {
		stored := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
			"projectId":         "linkproj",
			"gongfengProjectId": good,
		}, http.StatusOK)
		if stored.GongfengProjectID != good {
			t.Fatalf("plain path %q must be stored as-is, got %q", good, stored.GongfengProjectID)
		}
	}
}

func TestGlobalRuleWorkflowBinding(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "beta", Key: "BETA", Name: "Beta"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "alpha-sop", Name: "Alpha SOP", ProjectID: "alpha", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "dev", Name: "实现", Type: "agent", AgentID: "developer"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "beta-private", Name: "Beta private", ProjectID: "beta", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "dev", Name: "实现", Type: "agent", AgentID: "developer"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守", Priority: 50,
		Global: true, WorkflowIDs: []string{" alpha-sop ", "alpha-sop", ""},
	}, http.StatusOK)
	saved, ok := s.Rule("alpha-global")
	if !ok || !saved.Global || len(saved.WorkflowIDs) != 1 || saved.WorkflowIDs[0] != "alpha-sop" {
		t.Fatalf("saved rule = %#v", saved)
	}
	missing := requestJSON[map[string]string](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守",
		Global: true, WorkflowIDs: []string{"missing-sop"},
	}, http.StatusBadRequest)
	if missing["error"] == "" {
		t.Fatal("expected missing SOP error")
	}
	foreign := requestJSON[map[string]string](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守",
		Global: true, WorkflowIDs: []string{"beta-private"},
	}, http.StatusBadRequest)
	if foreign["error"] == "" {
		t.Fatal("expected foreign SOP error")
	}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守",
		Global: false, WorkflowIDs: []string{"alpha-sop"},
	}, http.StatusOK)
	cleared, _ := s.Rule("alpha-global")
	if cleared.Global || len(cleared.WorkflowIDs) != 0 {
		t.Fatalf("turning the switch off must drop SOP bindings: %#v", cleared)
	}
}

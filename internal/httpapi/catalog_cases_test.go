package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCatalogRepositoryAgentAndAssetBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, p := range []domain.Project{
		{ID: "cvb-cat", Key: "CVBC", Name: "CVB Catalog", OwnerID: f.user.ID},
		{ID: "cvb-other", Key: "CVBO", Name: "CVB Other", OwnerID: f.user.ID},
	} {
		if err := f.s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	requests := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodDelete, "/api/repositories", nil, 400},
		{http.MethodPost, "/api/repositories", map[string]any{"projectId": "cvb-cat", "repoUrl": "https://bitbucket.org/nope/x"}, 400},
		{http.MethodPost, "/api/repositories", map[string]any{"projectId": "cvb-cat", "repoUrl": "https://git.example.com/a/one", "url": "https://git.example.com/a/two"}, 400},
		{http.MethodPost, "/api/repositories", map[string]any{"projectId": "missing", "name": "x", "gongfengProjectId": "a/x"}, 400},
		{http.MethodPost, "/api/repositories", map[string]any{"projectId": "cvb-cat", "name": "x", "gongfengProjectId": "a/x", "provider": "gitlab"}, 400},
		{http.MethodPost, "/api/repositories", map[string]any{"id": "cvb-repo", "projectId": "cvb-cat", "repoUrl": "https://git.example.com/a/x"}, 200},
		{http.MethodGet, "/api/repositories/cvb-repo", nil, 200},

		{http.MethodPut, "/api/agents", map[string]any{"id": "cvb-agent"}, 400},
		{http.MethodPut, "/api/agents", map[string]any{"id": "cvb-agent", "projectId": "missing"}, 404},
		{http.MethodPut, "/api/agents", map[string]any{"id": "cvb-agent", "projectId": "cvb-cat", "executorId": "bad"}, 400},
		{http.MethodPut, "/api/agents", map[string]any{"id": "cvb-agent", "name": "Agent", "projectId": "cvb-cat"}, 200},
		{http.MethodPut, "/api/agents", map[string]any{"id": "cvb-agent", "name": "Agent", "projectId": "cvb-other"}, 409},
		{http.MethodGet, "/api/agents?projectId=cvb-cat", nil, 200},
		{http.MethodGet, "/api/agents/cvb-agent/revisions", nil, 200},
		{http.MethodGet, "/api/agents/cvb-agent/revisions/missing", nil, 404},
		{http.MethodPost, "/api/agents/cvb-agent/clone", map[string]any{}, 400},
		{http.MethodPost, "/api/agents/cvb-agent/clone", map[string]any{"id": "clone", "name": "Clone", "projectId": "missing"}, 404},
		{http.MethodPost, "/api/agents/cvb-agent/clone", map[string]any{"id": "cvb-agent-clone", "name": "Clone", "projectId": "cvb-other"}, 201},
		{http.MethodDelete, "/api/agents/cvb-agent-clone", nil, 204},

		{http.MethodPut, "/api/rules", map[string]any{"id": "cvb-rule", "name": "Rule", "projectId": "missing"}, 404},
		{http.MethodPut, "/api/rules", map[string]any{"id": "cvb-rule", "name": "Rule", "projectId": "cvb-cat", "global": true, "workflowIds": []string{"missing"}}, 400},
		{http.MethodPut, "/api/rules", map[string]any{"id": "cvb-rule", "name": "Rule", "projectId": "cvb-cat"}, 200},
		{http.MethodGet, "/api/rules?projectId=cvb-cat", nil, 200},
		{http.MethodPut, "/api/skills", map[string]any{"id": "cvb-skill", "name": "Skill", "projectId": "missing"}, 404},
		{http.MethodPut, "/api/skills", map[string]any{"id": "cvb-skill", "name": "Skill", "projectId": "cvb-cat"}, 200},
		{http.MethodGet, "/api/skills?projectId=cvb-cat", nil, 200},

		{http.MethodPut, "/api/dockerfiles", map[string]any{"id": "cvb-df", "name": "DF", "projectId": "cvb-cat", "content": "FROM scratch"}, 200},
		{http.MethodPut, "/api/environments", map[string]any{"id": "cvb-env", "name": "Env", "projectId": "cvb-cat", "installScript": "true"}, 200},
		{http.MethodPut, "/api/sandboxes", map[string]any{"id": "cvb-box-bad", "name": "Box", "projectId": "cvb-cat", "dockerfileId": "missing"}, 400},
		{http.MethodPut, "/api/sandboxes", map[string]any{"id": "cvb-box", "name": "Box", "projectId": "cvb-cat", "driver": "docker", "dockerfileId": "cvb-df", "environmentId": "cvb-env"}, 200},
		{http.MethodGet, "/api/dockerfiles?projectId=cvb-cat", nil, 200},
		{http.MethodGet, "/api/environments?projectId=cvb-cat", nil, 200},
		{http.MethodGet, "/api/sandboxes?projectId=cvb-cat", nil, 200},
		{http.MethodDelete, "/api/dockerfiles?id=cvb-df", nil, 409},
		{http.MethodDelete, "/api/environments?id=cvb-env", nil, 204},
	}
	for i, tc := range requests {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s %s = %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	// Referenced/shared assets exercise delete guards and shared-reference checks.
	agent, _ := f.s.Agent("cvb-agent")
	agent.Rules = []string{"cvb-rule"}
	agent.Skills = []string{"cvb-skill"}
	agent.Shared = true
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/rules?id=cvb-rule", "/api/skills?id=cvb-skill"} {
		if rec := cvBRequest(t, f, http.MethodDelete, path, nil, true); rec.Code != 409 {
			t.Errorf("guarded delete %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/rules", map[string]any{"id": "cvb-rule", "name": "Rule", "projectId": "cvb-cat", "shared": true}, true); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/rules", map[string]any{"id": "cvb-rule", "name": "Rule", "projectId": "cvb-cat", "shared": false}, true); rec.Code != 409 {
		t.Errorf("unshare referenced rule = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogWorkflowBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, p := range []domain.Project{{ID: "cvb-wfp", Key: "WFP", Name: "Workflow", OwnerID: f.user.ID}, {ID: "cvb-wfo", Key: "WFO", Name: "Other", OwnerID: f.user.ID}} {
		if err := f.s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-private-box", Name: "Private", ProjectID: "cvb-wfo", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-box", Name: "Box", ProjectID: "cvb-wfp", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-private-agent", Name: "Private", ProjectID: "cvb-wfo", Version: 1}); err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"id": "cvb-flow", "name": "Flow", "projectId": "cvb-wfp", "sandboxProfileId": "cvb-box"}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPut, "/api/workflows", map[string]any{"id": "wf-no-box", "name": "No box", "projectId": "cvb-wfp"}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "wf-missing-box", "name": "Missing", "projectId": "cvb-wfp", "sandboxProfileId": "missing"}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "wf-foreign-box", "name": "Foreign", "projectId": "cvb-wfp", "sandboxProfileId": "cvb-private-box"}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "wf-agent", "name": "Agent", "projectId": "cvb-wfp", "sandboxProfileId": "cvb-box", "nodes": []any{map[string]any{"id": "n", "type": "agent", "agentId": "missing"}}}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "wf-agent2", "name": "Agent", "projectId": "cvb-wfp", "sandboxProfileId": "cvb-box", "nodes": []any{map[string]any{"id": "n", "type": "agent", "agentId": "cvb-private-agent"}}}, 400},
		{http.MethodPut, "/api/workflows", base, 200},
		{http.MethodGet, "/api/workflows?q=flow&projectId=cvb-wfp", nil, 200},
		{http.MethodGet, "/api/workflows/cvb-flow", nil, 200},
		{http.MethodGet, "/api/workflows/cvb-flow/revisions", nil, 200},
		{http.MethodGet, "/api/workflows/cvb-flow/revisions/missing", nil, 404},
		{http.MethodPost, "/api/workflows/cvb-flow/clone", map[string]any{}, 400},
		{http.MethodPost, "/api/workflows/cvb-flow/clone", map[string]any{"id": "cvb-flow-clone", "name": "Clone", "projectId": "cvb-wfp"}, 201},
		{http.MethodPost, "/api/workflows/cvb-flow-clone/publish", nil, 200},
		{http.MethodDelete, "/api/workflows/cvb-flow-clone", nil, 204},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "deleted-stale", "name": "Stale", "projectId": "cvb-wfp", "sandboxProfileId": "cvb-box", "revision": 2}, 409},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%d %s %s = %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	// Deletion conflicts: active run, then bound issue.
	if err := f.s.PutRun(domain.Run{ID: "cvb-active", WorkflowID: "cvb-flow", Status: "running", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/workflows/cvb-flow", nil, true); rec.Code != 409 {
		t.Errorf("active workflow delete = %d: %s", rec.Code, rec.Body.String())
	}
	run, _ := f.s.Run("cvb-active")
	run.Status = "succeeded"
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutIssue(domain.Issue{ID: "cvb-bound", ProjectID: "cvb-wfp", Title: "Bound", WorkflowID: "cvb-flow", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/workflows/cvb-flow", nil, true); rec.Code != 409 || !strings.Contains(rec.Body.String(), "Issue") {
		t.Errorf("bound workflow delete = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogDirectSharingAndCloneHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, project := range []domain.Project{
		{ID: "cvb-direct-a", Key: "CDA", Name: "Direct A"},
		{ID: "cvb-direct-b", Key: "CDB", Name: "Direct B"},
	} {
		if err := f.s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	h := &catalogHandlers{deps: &deps{repo: f.s}}

	for _, rule := range []domain.RuleDefinition{
		{ID: "cvb-share-rule", Name: "Share", ProjectID: "cvb-direct-a"},
		{ID: "cvb-foreign-rule", Name: "Foreign", ProjectID: "cvb-direct-b"},
		{ID: "cvb-already-rule", Name: "Already", ProjectID: "cvb-direct-a", Shared: true},
	} {
		if err := f.s.PutRule(rule); err != nil {
			t.Fatal(err)
		}
	}
	for _, skill := range []domain.SkillDefinition{
		{ID: "cvb-share-skill", Name: "Share", ProjectID: "cvb-direct-a"},
		{ID: "cvb-foreign-skill", Name: "Foreign", ProjectID: "cvb-direct-b"},
		{ID: "cvb-already-skill", Name: "Already", ProjectID: "cvb-direct-a", Shared: true},
	} {
		if err := f.s.PutSkill(skill); err != nil {
			t.Fatal(err)
		}
	}
	agent := domain.AgentDefinition{
		ID: "cvb-share-agent", Name: "Share agent", ProjectID: "cvb-direct-a",
		Rules:  []string{"missing", "cvb-foreign-rule", "cvb-already-rule", "cvb-share-rule"},
		Skills: []string{"missing", "cvb-foreign-skill", "cvb-already-skill", "cvb-share-skill"},
	}
	if err := h.shareAgentAssets(agent); err != nil {
		t.Fatal(err)
	}
	if item, _ := f.s.Rule("cvb-share-rule"); !item.Shared {
		t.Fatal("same-project rule was not shared")
	}
	if item, _ := f.s.Skill("cvb-share-skill"); !item.Shared {
		t.Fatal("same-project skill was not shared")
	}
	if item, _ := f.s.Rule("cvb-foreign-rule"); item.Shared {
		t.Fatal("foreign rule was shared")
	}

	servers := []domain.MCPServerDefinition{
		{ID: "cvb-empty", Name: "Legacy", Command: "true"},
		{ID: "cvb-project-same", Name: "Same", Command: "true", Scope: "project", ProjectID: "cvb-direct-b"},
		{ID: "cvb-project-market", Name: "Market source", Command: "true", Scope: "project", ProjectID: "cvb-direct-a", MarketID: "market-x"},
		{ID: "cvb-project-installed", Name: "Installed", Command: "true", Scope: "project", ProjectID: "cvb-direct-b", MarketID: "market-x"},
		{ID: "cvb-project-copy", Name: "Copy", Command: "true", Scope: "project", ProjectID: "cvb-direct-a"},
		{ID: "cvb-personal-own", Name: "Own", Command: "true", Scope: "personal", OwnerUserID: f.user.ID},
		{ID: "cvb-personal-other", Name: "Other", Command: "true", Scope: "personal", OwnerUserID: f.user.ID + 100},
	}
	for _, server := range servers {
		if err := f.s.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.cloneAgentMCPServers([]string{"cvb-personal-other"}, "cvb-direct-b", 0); !errors.Is(err, errPersonalMCPCloneRequiresLogin) {
		t.Fatalf("personal clone without login error = %v", err)
	}
	got, err := h.cloneAgentMCPServers([]string{
		"missing", "cvb-empty", "cvb-project-same", "cvb-project-market",
		"cvb-project-copy", "cvb-personal-own", "cvb-personal-other",
	}, "cvb-direct-b", f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("cloned MCP ids = %#v", got)
	}
	if got[0] != "cvb-project-same" || got[1] != "cvb-project-installed" || got[3] != "cvb-personal-own" {
		t.Fatalf("unexpected reused MCP ids: %#v", got)
	}
	for _, id := range []string{got[2], got[4]} {
		if _, ok := f.s.MCPServer(id); !ok {
			t.Fatalf("cloned MCP %q was not stored", id)
		}
	}
	again, err := h.cloneAgentMCPServers([]string{"cvb-project-copy"}, "cvb-direct-b", f.user.ID)
	if err != nil || len(again) != 1 || again[0] != got[2] {
		t.Fatalf("idempotent clone = %#v, %v", again, err)
	}
}

func TestCatalogDirectEnvironmentBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, project := range []domain.Project{
		{ID: "cvb-env-a", Key: "CEA", Name: "Environment A"},
		{ID: "cvb-env-b", Key: "CEB", Name: "Environment B"},
	} {
		if err := f.s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	h := &catalogHandlers{deps: &deps{repo: f.s}}
	if err := h.validateEnvironmentAsset("", "name", "cvb-env-a", "asset"); err == nil {
		t.Fatal("missing id accepted")
	}
	if err := h.validateEnvironmentAsset("id", "name", "missing", "asset"); err == nil {
		t.Fatal("missing project accepted")
	}
	if err := h.validateEnvironmentAsset("id", "name", "cvb-env-a", "asset"); err != nil {
		t.Fatal(err)
	}

	if err := h.shareSandboxDependencies(domain.SandboxProfile{ProjectID: "cvb-env-a", DockerfileID: "missing"}); err == nil {
		t.Fatal("missing Dockerfile accepted")
	}
	if err := h.shareSandboxDependencies(domain.SandboxProfile{ProjectID: "cvb-env-a", EnvironmentID: "missing"}); err == nil {
		t.Fatal("missing environment accepted")
	}
	if err := f.s.PutDockerfile(domain.DockerfileDefinition{ID: "cvb-foreign-df", Name: "Foreign DF", ProjectID: "cvb-env-b", Content: "FROM scratch"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutEnvironment(domain.EnvironmentDefinition{ID: "cvb-foreign-env", Name: "Foreign env", ProjectID: "cvb-env-b"}); err != nil {
		t.Fatal(err)
	}
	if err := h.shareSandboxDependencies(domain.SandboxProfile{ProjectID: "cvb-env-a", DockerfileID: "cvb-foreign-df"}); err == nil {
		t.Fatal("private foreign Dockerfile accepted")
	}
	if err := h.shareSandboxDependencies(domain.SandboxProfile{ProjectID: "cvb-env-a", EnvironmentID: "cvb-foreign-env"}); err == nil {
		t.Fatal("private foreign environment accepted")
	}
	if err := f.s.PutDockerfile(domain.DockerfileDefinition{ID: "cvb-own-df", Name: "Own DF", ProjectID: "cvb-env-a", Content: "FROM scratch"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutEnvironment(domain.EnvironmentDefinition{ID: "cvb-own-env", Name: "Own env", ProjectID: "cvb-env-a"}); err != nil {
		t.Fatal(err)
	}
	box := domain.SandboxProfile{
		ID: "cvb-shared-box", Name: "Shared box", ProjectID: "cvb-env-a", Shared: true,
		Driver: "docker", DockerfileID: "cvb-own-df", EnvironmentID: "cvb-own-env",
	}
	if err := h.shareSandboxDependencies(box); err != nil {
		t.Fatal(err)
	}
	df, _ := f.s.Dockerfile("cvb-own-df")
	env, _ := f.s.Environment("cvb-own-env")
	if !df.Shared || !env.Shared {
		t.Fatalf("dependencies not shared: df=%v env=%v", df.Shared, env.Shared)
	}

	if err := f.s.PutSandbox(box); err != nil {
		t.Fatal(err)
	}
	envBox := domain.SandboxProfile{
		ID: "cvb-shared-env-box", Name: "Shared env box", ProjectID: "cvb-env-a",
		Shared: true, Driver: "bwrap", EnvironmentID: "cvb-own-env",
	}
	if err := f.s.PutSandbox(envBox); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "cvb-shared-wf", Name: "Shared workflow", Shared: true, SandboxProfileID: box.ID}); err != nil {
		t.Fatal(err)
	}
	if !h.sharedEnvironmentReference("sandbox", box.ID) || h.sharedEnvironmentReference("sandbox", "missing") {
		t.Fatal("shared workflow sandbox reference lookup is wrong")
	}
	if !h.sharedEnvironmentReference("dockerfile", df.ID) || !h.sharedEnvironmentReference("environment", env.ID) {
		t.Fatal("shared sandbox dependency reference lookup is wrong")
	}
	if h.sharedEnvironmentReference("other", df.ID) {
		t.Fatal("unknown dependency kind matched")
	}
}

func TestCatalogMoreHTTPBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, project := range []domain.Project{
		{ID: "cvb-http-a", Key: "CHA", Name: "HTTP A", DefaultRepositoryID: "cvb-delete-repo"},
		{ID: "cvb-http-b", Key: "CHB", Name: "HTTP B"},
	} {
		if err := f.s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.PutRepository(domain.Repository{ID: "cvb-delete-repo", ProjectID: "cvb-http-a", Name: "Delete", Provider: "gongfeng", GongfengProjectID: "a/delete"}); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/repositories?id=cvb-delete-repo", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("delete repository = %d: %s", rec.Code, rec.Body.String())
	}
	if project, _ := f.s.Project("cvb-http-a"); project.DefaultRepositoryID != "" {
		t.Fatalf("default repository was not cleared: %+v", project)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/repositories/cvb-delete-repo", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted repository lookup = %d", rec.Code)
	}

	if err := f.s.PutRule(domain.RuleDefinition{ID: "cvb-http-rule", Name: "Rule", ProjectID: "cvb-http-a"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSkill(domain.SkillDefinition{ID: "cvb-http-skill", Name: "Skill", ProjectID: "cvb-http-a"}); err != nil {
		t.Fatal(err)
	}
	agent := domain.AgentDefinition{
		ID: "cvb-http-agent", Name: "Agent", ProjectID: "cvb-http-a", Shared: true,
		Rules: []string{"cvb-http-rule"}, Skills: []string{"cvb-http-skill"}, Version: 1,
	}
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/agents/cvb-http-agent", "/api/rules?id=cvb-http-rule", "/api/skills?id=cvb-http-skill"} {
		rec := cvBRequest(t, f, http.MethodDelete, path, nil, true)
		if rec.Code != http.StatusConflict {
			t.Errorf("shared delete %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	agent.Shared = false
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-http-box", Name: "Box", ProjectID: "cvb-http-a", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{
		ID: "cvb-agent-ref-flow", Name: "Agent ref", ProjectID: "cvb-http-a", SandboxProfileID: "cvb-http-box",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: agent.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/agents/cvb-http-agent", nil, true); rec.Code != http.StatusConflict {
		t.Fatalf("referenced agent delete = %d: %s", rec.Code, rec.Body.String())
	}

	cases := []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodPut, "/api/rules", map[string]any{"id": "x"}, http.StatusBadRequest},
		{http.MethodPut, "/api/skills", map[string]any{"id": "x"}, http.StatusBadRequest},
		{http.MethodDelete, "/api/rules?id=missing", nil, http.StatusNotFound},
		{http.MethodDelete, "/api/skills", nil, http.StatusBadRequest},
		{http.MethodPut, "/api/rules", map[string]any{"id": "cvb-http-rule", "name": "Moved", "projectId": "cvb-http-b"}, http.StatusConflict},
		{http.MethodPut, "/api/skills", map[string]any{"id": "cvb-http-skill", "name": "Moved", "projectId": "cvb-http-b"}, http.StatusConflict},
		{http.MethodPut, "/api/dockerfiles", map[string]any{"id": "", "name": "Bad", "projectId": "cvb-http-a"}, http.StatusBadRequest},
		{http.MethodPut, "/api/environments", map[string]any{"id": "bad", "name": "Bad", "projectId": "missing"}, http.StatusBadRequest},
		{http.MethodPut, "/api/sandboxes", map[string]any{"id": "bad", "name": "Bad", "projectId": "missing"}, http.StatusBadRequest},
		{http.MethodDelete, "/api/dockerfiles?id=" + repo.GenericBackendDockerfileID, nil, http.StatusConflict},
		{http.MethodDelete, "/api/sandboxes?id=local", nil, http.StatusConflict},
		{http.MethodGet, "/api/executors", nil, http.StatusOK},
	}
	for _, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestCatalogWorkflowMoreBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "cvb-wf-more", Key: "CWM", Name: "Workflow More"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-wf-more-box", Name: "Box", ProjectID: "cvb-wf-more", Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	for _, workflow := range []domain.WorkflowDefinition{
		{ID: "cvb-search-hit", Name: "Needle Alpha", Description: "plain", ProjectID: "cvb-wf-more", SandboxProfileID: "cvb-wf-more-box"},
		{ID: "cvb-search-miss", Name: "Other", Description: "plain", ProjectID: "cvb-wf-more", SandboxProfileID: "cvb-wf-more-box"},
		{ID: "cvb-other-project", Name: "Needle Other", ProjectID: "elsewhere", SandboxProfileID: "cvb-wf-more-box"},
	} {
		if err := f.s.PutWorkflow(workflow); err != nil {
			t.Fatal(err)
		}
	}
	rec := cvBRequest(t, f, http.MethodGet, "/api/workflows?projectId=cvb-wf-more&q=needle", nil, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cvb-search-hit") ||
		strings.Contains(rec.Body.String(), "cvb-search-miss") || strings.Contains(rec.Body.String(), "cvb-other-project") {
		t.Fatalf("filtered workflows = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", map[string]any{
		"id": "cvb-bad-stage", "name": "Bad stage", "projectId": "cvb-wf-more", "sandboxProfileId": "cvb-wf-more-box",
		"nodes": []any{map[string]any{"id": "n", "name": "N", "type": "script", "metricStage": "typo"}},
	}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad metric stage = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/workflows/missing", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing workflow = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/workflows/cvb-search-hit/agents", nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("nongeneric agents = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/workflows/cvb-search-hit/clone", map[string]any{
		"id": "cvb-search-clone", "name": "Clone", "owner": "owner",
	}, true); rec.Code != http.StatusCreated {
		t.Fatalf("workflow clone default project = %d: %s", rec.Code, rec.Body.String())
	}
	cloned, ok := f.s.Workflow("cvb-search-clone")
	if !ok || cloned.ProjectID != "cvb-wf-more" || cloned.Status != "draft" || cloned.Version != 1 {
		t.Fatalf("cloned workflow = %+v, %v", cloned, ok)
	}
}

func TestCatalogSmallDirectHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "cvb-helper", Key: "CVH", Name: "Helpers"}); err != nil {
		t.Fatal(err)
	}
	h := &catalogHandlers{deps: &deps{repo: f.s}}
	if err := normalizeGlobalRule(f.s, nil); err != nil {
		t.Fatal(err)
	}
	rule := domain.RuleDefinition{Global: false, WorkflowIDs: []string{"discard"}}
	if err := normalizeGlobalRule(f.s, &rule); err != nil || rule.WorkflowIDs != nil {
		t.Fatalf("disabled global rule = %+v, %v", rule, err)
	}
	if err := h.validateWorkflowAgentScopes(domain.WorkflowDefinition{}); err != nil {
		t.Fatal(err)
	}
	if err := h.validateWorkflowAgentScopes(domain.WorkflowDefinition{
		ProjectID: "cvb-helper",
		Nodes: []domain.WorkflowNode{
			{ID: "script", Type: "script"},
			{ID: "empty", Type: "agent"},
			{ID: "preview", Type: "agent", AgentID: domain.PreviewBuildAgentID},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if workflowHasNotifications(domain.WorkflowDefinition{}) {
		t.Fatal("empty workflow reported notifications")
	}
	if !workflowHasNotifications(domain.WorkflowDefinition{Notifications: []domain.NotificationBinding{{}}}) {
		t.Fatal("workflow notification was missed")
	}
	if !workflowHasNotifications(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{Notifications: []domain.NodeNotification{{}}}}}) {
		t.Fatal("node notification was missed")
	}

	if err := f.s.PutAgent(domain.AgentDefinition{
		ID: "cvb-helper-agent", Name: "Helper agent", ProjectID: "cvb-helper",
		Rules: []string{"r", "r"}, Skills: []string{"s"},
	}); err != nil {
		t.Fatal(err)
	}
	if names := h.agentNamesReferencing("rule", "r"); len(names) != 1 || names[0] != "Helper agent" {
		t.Fatalf("rule references = %#v", names)
	}
	if names := h.agentNamesReferencing("skill", "s"); len(names) != 1 {
		t.Fatalf("skill references = %#v", names)
	}
	if err := f.s.PutDockerfile(domain.DockerfileDefinition{ID: "cvb-helper-df", Name: "DF", ProjectID: "cvb-helper", Content: "FROM scratch"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutEnvironment(domain.EnvironmentDefinition{ID: "cvb-helper-env", Name: "Env", ProjectID: "cvb-helper"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{
		ID: "cvb-helper-docker", Name: "Docker", ProjectID: "cvb-helper",
		Driver: "docker", DockerfileID: "cvb-helper-df",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{
		ID: "cvb-helper-bwrap", Name: "Bwrap", ProjectID: "cvb-helper",
		Driver: "bwrap", EnvironmentID: "cvb-helper-env",
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.sandboxNamesReferencing("dockerfile", "cvb-helper-df")) != 1 ||
		len(h.sandboxNamesReferencing("environment", "cvb-helper-env")) != 1 {
		t.Fatal("sandbox dependency names were not found")
	}
	if len(h.workflowNamesReferencingSandbox("missing")) != 0 {
		t.Fatal("missing sandbox unexpectedly referenced")
	}
}

func TestCatalogRevisionRestoreAndValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project := domain.Project{ID: "cvb-r2-catalog", Key: "R2C", Name: "Catalog", OwnerID: f.user.ID}
	other := domain.Project{ID: "cvb-r2-catalog-other", Key: "R2O", Name: "Other", OwnerID: f.user.ID}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutProject(other); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-box", Name: "Box", ProjectID: project.ID, Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: "cvb-r2-private-box", Name: "Private", ProjectID: other.ID, Driver: "bwrap"}); err != nil {
		t.Fatal(err)
	}
	agent := domain.AgentDefinition{ID: "cvb-r2-agent", Name: "Agent", ProjectID: project.ID, ExecutorID: "codebuddy", Version: 1}
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	agent.Name = "Agent v2"
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{ID: "cvb-r2-workflow", Name: "Workflow", ProjectID: project.ID,
		SandboxProfileID: "cvb-r2-box", Version: 1, Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: agent.ID}}}
	if err := f.s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	workflow, _ = f.s.Workflow(workflow.ID)
	workflow.Name = "Workflow v2"
	if err := f.s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPut, "/api/workflows", map[string]any{"id": "cvb-r2-bad-stage", "name": "Bad", "projectId": project.ID, "sandboxProfileId": "cvb-r2-box",
			"nodes": []any{map[string]any{"id": "n", "type": "script", "metricStage": "invalid"}}}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "cvb-r2-shared-private", "name": "Bad", "projectId": other.ID,
			"sandboxProfileId": "cvb-r2-private-box", "shared": true}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{"id": "cvb-r2-foreign-box", "name": "Bad", "projectId": project.ID,
			"sandboxProfileId": "cvb-r2-private-box"}, 400},
		{http.MethodGet, "/api/workflows?q=workflow&projectId=" + project.ID, nil, 200},
		{http.MethodGet, "/api/workflows/" + workflow.ID + "/revisions", nil, 200},
		{http.MethodGet, "/api/agents/" + agent.ID + "/revisions", nil, 200},
		{http.MethodPost, "/api/workflows/" + workflow.ID + "/revisions/missing/restore", nil, 404},
		{http.MethodPost, "/api/agents/" + agent.ID + "/revisions/missing/restore", nil, 400},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	restoreFirstRevision := func(kind, id string) {
		rec := cvBRequest(t, f, http.MethodGet, "/api/"+kind+"/"+id+"/revisions", nil, true)
		var revisions []domain.DefinitionRevision
		if err := json.Unmarshal(rec.Body.Bytes(), &revisions); err != nil || len(revisions) == 0 {
			t.Fatalf("%s revisions: %v %s", kind, err, rec.Body.String())
		}
		path := "/api/" + kind + "/" + id + "/revisions/" + revisions[0].ID + "/restore"
		if restored := cvBRequest(t, f, http.MethodPost, path, nil, true); restored.Code != http.StatusOK {
			t.Errorf("restore %s = %d: %s", kind, restored.Code, restored.Body.String())
		}
	}
	restoreFirstRevision("workflows", workflow.ID)
	restoreFirstRevision("agents", agent.ID)
}

func TestCatalogCrossProjectAssetConflicts(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, project := range []domain.Project{
		{ID: "cvb-r2-assets-a", Key: "R2A", Name: "A", OwnerID: f.user.ID},
		{ID: "cvb-r2-assets-b", Key: "R2B", Name: "B", OwnerID: f.user.ID},
	} {
		if err := f.s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.PutRule(domain.RuleDefinition{ID: "cvb-r2-cross-rule", Name: "Rule", ProjectID: "cvb-r2-assets-a"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSkill(domain.SkillDefinition{ID: "cvb-r2-cross-skill", Name: "Skill", ProjectID: "cvb-r2-assets-a"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/rules", map[string]any{"id": "cvb-r2-cross-rule", "name": "Rule", "projectId": "cvb-r2-assets-b"}},
		{"/api/skills", map[string]any{"id": "cvb-r2-cross-skill", "name": "Skill", "projectId": "cvb-r2-assets-b"}},
	} {
		rec := cvBRequest(t, f, http.MethodPut, tc.path, tc.body, true)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s conflict = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}
}

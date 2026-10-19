package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestMain(m *testing.M) {
	if runtime.RunMCPCallCLI(os.Args[1:]) {
		return
	}
	os.Exit(m.Run())
}

func putGateTestAgent(t *testing.T, s *repo.Store, id string, gates []domain.VerificationGate) {
	t.Helper()
	if err := s.PutAgent(domain.AgentDefinition{ID: id, Name: id, ExecutorID: "mock", Verification: gates}); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedWorkerChangesRemainInFrozenPatch(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", source, "add", ".").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	if output, err := exec.Command("git", "-C", source, "commit", "-m", "baseline").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := prepareRepository(context.Background(), workspace, domain.RepositoryContext{RepositoryID: "repo", LocalPath: source, BaseBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "value.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", workspace, "add", ".").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	if output, err := exec.Command("git", "-C", workspace, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "fix").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	run := domain.Run{ID: "run", Repository: domain.RepositoryContext{RepositoryID: "repo"}, Nodes: map[string]*domain.NodeRun{"fix": {NodeID: "fix", Attempt: 1}}}
	artifacts, err := collectArtifacts(workspace, run, domain.WorkflowNode{ID: "fix"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Name == "repository-patch" && strings.Contains(artifact.Content, "value.txt") {
			return
		}
	}
	t.Fatalf("committed change was not preserved: %#v", artifacts)
}

func TestResolveAssetsLoadsSkillBinaryFiles(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutSkill(domain.SkillDefinition{ID: "bin-skill", Name: "Bin", ProjectID: "p1", Content: "# bin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSkillFiles("bin-skill", []repo.SkillFileInput{
		{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755},
	}); err != nil {
		t.Fatal(err)
	}
	assets := (&Engine{repo: st}).resolveAssets(domain.AgentDefinition{Skills: []string{"bin-skill"}}, "p1", "", 1)
	if len(assets.Skills) != 1 || len(assets.Skills[0].FileRefs) != 1 {
		t.Fatalf("skill manifest not resolved: %#v", assets.Skills)
	}
	content := assets.SkillFiles["bin-skill"]["scripts/run.sh"]
	if string(content) != "#!/bin/sh\n" {
		t.Fatalf("binary content not loaded into bundle: %q", content)
	}
}

func TestResolveAssetsEnforcesProjectAndPersonalScope(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	servers := []domain.MCPServerDefinition{
		{ID: "legacy", Name: "Legacy", Transport: "http", URL: "https://example.com/legacy"},
		{ID: "p1-mcp", Name: "P1", Scope: "project", ProjectID: "p1", Transport: "http", URL: "https://example.com/p1"},
		{ID: "p2-mcp", Name: "P2", Scope: "project", ProjectID: "p2", Transport: "http", URL: "https://example.com/p2"},
		{ID: "mine", Name: "Mine", Scope: "personal", OwnerUserID: 2, Transport: "http", URL: "https://example.com/mine"},
		{ID: "other", Name: "Other", Scope: "personal", OwnerUserID: 3, Transport: "http", URL: "https://example.com/other"},
	}
	for _, server := range servers {
		if err := st.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	for _, rule := range []domain.RuleDefinition{
		{ID: "legacy-rule", Name: "Legacy rule"},
		{ID: "p1-rule", Name: "P1 rule", ProjectID: "p1"},
		{ID: "p2-rule", Name: "P2 rule", ProjectID: "p2"},
		{ID: "shared-rule", Name: "Shared rule", ProjectID: "p2", Shared: true},
	} {
		if err := st.PutRule(rule); err != nil {
			t.Fatal(err)
		}
	}
	for _, skill := range []domain.SkillDefinition{
		{ID: "legacy-skill", Name: "Legacy skill"},
		{ID: "p1-skill", Name: "P1 skill", ProjectID: "p1"},
		{ID: "p2-skill", Name: "P2 skill", ProjectID: "p2"},
		{ID: "shared-skill", Name: "Shared skill", ProjectID: "p2", Shared: true},
	} {
		if err := st.PutSkill(skill); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{repo: st}
	agent := domain.AgentDefinition{
		Rules:      []string{"legacy-rule", "p1-rule", "p2-rule", "shared-rule"},
		Skills:     []string{"legacy-skill", "p1-skill", "p2-skill", "shared-skill"},
		MCPServers: []string{"legacy", "p1-mcp", "p2-mcp", "mine", "other"},
	}
	assets := e.resolveAssets(agent, "p1", "", 2)
	if len(assets.Rules) != 2 || assets.Rules[0].ID != "p1-rule" || assets.Rules[1].ID != "shared-rule" {
		t.Fatalf("resolved rules = %#v", assets.Rules)
	}
	if len(assets.Skills) != 2 || assets.Skills[0].ID != "p1-skill" || assets.Skills[1].ID != "shared-skill" {
		t.Fatalf("resolved skills = %#v", assets.Skills)
	}
	if len(assets.MCPServers) != 2 || assets.MCPServers[0].ID != "p1-mcp" || assets.MCPServers[1].ID != "mine" {
		t.Fatalf("resolved MCPs = %#v", assets.MCPServers)
	}
	script := e.resolveScriptMCPAssets("p1", 2)
	got := map[string]bool{}
	for _, server := range script.MCPServers {
		got[server.ID] = true
	}
	if len(got) != 2 || !got["p1-mcp"] || !got["mine"] {
		t.Fatalf("script MCPs = %#v", script.MCPServers)
	}
}

func TestResolveAssetsBindsPlatformMCP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &Engine{repo: st}
	agent := domain.AgentDefinition{PlatformMCP: true}
	// 未配置平台基地址（例如未设置地址的 worker）时只带路径：执行机器改写到自己的
	// server 地址，云端执行丢弃并告警。
	assets := e.resolveAssets(agent, "p1", "", 1)
	if len(assets.MCPServers) != 1 || assets.MCPServers[0].URL != "/mcp" {
		t.Fatalf("platform MCP without a base should be path-only: %#v", assets.MCPServers)
	}
	if cloud, warnings, _ := runtime.PrepareMCPServers(assets, runtime.MCPTarget{}); len(cloud.MCPServers) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "AGENTFLOW_PLATFORM_MCP_URL") {
		t.Fatalf("cloud run kept an unreachable platform MCP: %#v %v", cloud.MCPServers, warnings)
	}
	e.ConfigurePlatformMCP("http://127.0.0.1:8080/", "secret-token")
	assets = e.resolveAssets(agent, "p1", "", 1)
	if len(assets.MCPServers) != 1 {
		t.Fatalf("platform MCP not bound: %#v", assets.MCPServers)
	}
	server := assets.MCPServers[0]
	if server.ID != domain.PlatformMCPServerID || server.Transport != "http" || server.URL != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("unexpected platform MCP definition: %#v", server)
	}
	if server.Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("platform MCP auth header = %#v", server.Headers)
	}
	// 未开启开关的角色不注入。
	assets = e.resolveAssets(domain.AgentDefinition{}, "p1", "", 1)
	if len(assets.MCPServers) != 0 {
		t.Fatalf("platform MCP bound for a role that did not opt in: %#v", assets.MCPServers)
	}
}

func TestResolveAssetsBindsKnowledgeMCP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &Engine{repo: st}
	e.ConfigurePlatformMCP("http://agentflow.local/", "")
	agent := domain.AgentDefinition{ID: "dev", KnowledgeBase: true}
	// 未接入知识库时开关退化为无操作，不影响运行。
	if assets := e.resolveAssets(agent, "p1", "", 3); len(assets.MCPServers) != 0 || len(assets.Rules) != 0 {
		t.Fatalf("knowledge bound before the knowledge base is connected: %#v", assets)
	}
	settings := domain.KnowledgeSettings{Enabled: true, URL: "http://kb.local/mcp", Token: "upstream-secret"}
	if err := st.PutKnowledgeSettings(settings); err != nil {
		t.Fatal(err)
	}
	assets := e.resolveAssets(agent, "p1", "", 3)
	if len(assets.MCPServers) != 1 || len(assets.Rules) != 1 || assets.Rules[0].ID != domain.KnowledgeUsageRuleID {
		t.Fatalf("knowledge MCP/rule not bound: %#v", assets)
	}
	server := assets.MCPServers[0]
	if server.ID != domain.KnowledgeMCPServerID || server.Transport != "http" || server.URL != "http://agentflow.local"+knowledge.ProxyPath {
		t.Fatalf("unexpected knowledge MCP definition: %#v", server)
	}
	auth := server.Headers["Authorization"]
	if strings.Contains(auth, "upstream-secret") {
		t.Fatal("upstream token leaked into the run asset bundle")
	}
	claims, err := knowledge.VerifyProxyToken(settings, strings.TrimPrefix(auth, "Bearer "), time.Now())
	if err != nil || claims.ProjectID != "p1" || claims.UserID != 3 || claims.AgentID != "dev" {
		t.Fatalf("proxy token claims = %#v, %v", claims, err)
	}
	if assets := e.resolveAssets(domain.AgentDefinition{ID: "other"}, "p1", "", 3); len(assets.MCPServers) != 0 {
		t.Fatalf("knowledge MCP bound for a role that did not opt in: %#v", assets.MCPServers)
	}
	if warning := knowledgeSkippedWarning(agent, assets); warning != "" {
		t.Fatalf("warned about a bound knowledge MCP: %s", warning)
	}
	if cloud, warnings, _ := runtime.PrepareMCPServers(assets, runtime.MCPTarget{}); len(cloud.MCPServers) != 1 || len(warnings) != 0 {
		t.Fatalf("cloud run dropped a reachable knowledge MCP: %#v %v", cloud.MCPServers, warnings)
	}
}

func TestKnowledgeMCPWithoutPlatformBase(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &Engine{repo: st}
	agent := domain.AgentDefinition{ID: "dev", KnowledgeBase: true}
	if warning := knowledgeSkippedWarning(agent, e.resolveAssets(agent, "p1", "", 3)); !strings.Contains(warning, "知识中心未启用") {
		t.Fatalf("knowledge not connected should explain itself, got %q", warning)
	}
	if err := st.PutKnowledgeSettings(domain.KnowledgeSettings{Enabled: true, URL: "http://kb.local/mcp", Token: "upstream-secret"}); err != nil {
		t.Fatal(err)
	}
	// 独立 worker 未配置平台地址：执行机器仍能改写使用，云端执行丢弃并说明原因。
	assets := e.resolveAssets(agent, "p1", "", 3)
	if len(assets.MCPServers) != 1 || assets.MCPServers[0].URL != knowledge.ProxyPath {
		t.Fatalf("knowledge MCP without a base should be path-only: %#v", assets.MCPServers)
	}
	rebased, _, _ := runtime.PrepareMCPServers(assets, runtime.MCPTarget{ServerURL: "https://runner.example/"})
	if rebased.MCPServers[0].URL != "https://runner.example"+knowledge.ProxyPath || assets.MCPServers[0].URL != knowledge.ProxyPath {
		t.Fatalf("rebase = %q (original %q)", rebased.MCPServers[0].URL, assets.MCPServers[0].URL)
	}
	if cloud, warnings, _ := runtime.PrepareMCPServers(assets, runtime.MCPTarget{}); len(cloud.MCPServers) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], domain.KnowledgeMCPServerID) {
		t.Fatalf("cloud = %#v, warnings = %v", cloud.MCPServers, warnings)
	}
}

func TestGlobalRuleInjectsIntoEveryNodeOfBoundWorkflow(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, rule := range []domain.RuleDefinition{
		{ID: "bound", Name: "Bound", ProjectID: "p1", Content: "already on the role", Priority: 10},
		{ID: "global-sop", Name: "Global SOP", ProjectID: "p1", Content: "applies to every node", Priority: 80, Global: true, WorkflowIDs: []string{"sop-a", " sop-a "}},
		{ID: "global-other", Name: "Other SOP", ProjectID: "p1", Content: "other", Global: true, WorkflowIDs: []string{"sop-b"}},
		{ID: "plain", Name: "Plain", ProjectID: "p1", Content: "not global", WorkflowIDs: []string{"sop-a"}},
		{ID: "foreign", Name: "Foreign", ProjectID: "p2", Content: "foreign", Global: true, WorkflowIDs: []string{"sop-a"}},
		{ID: "shared-global", Name: "Shared global", ProjectID: "p2", Content: "shared", Shared: true, Global: true, WorkflowIDs: []string{"sop-a"}},
	} {
		if err := st.PutRule(rule); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{repo: st}
	agent := domain.AgentDefinition{Rules: []string{"bound"}}
	assets := e.resolveAssets(agent, "p1", "sop-a", 1)
	got := map[string]int{}
	for _, rule := range assets.Rules {
		got[rule.ID]++
	}
	if got["bound"] != 1 || got["global-sop"] != 1 || got["shared-global"] != 1 {
		t.Fatalf("sop-a rules = %#v", assets.Rules)
	}
	if got["global-other"] != 0 || got["plain"] != 0 || got["foreign"] != 0 {
		t.Fatalf("sop-a included rules outside the binding: %#v", assets.Rules)
	}
	again := e.resolveAssets(domain.AgentDefinition{Rules: []string{"global-sop"}}, "p1", "sop-a", 1)
	count := 0
	for _, rule := range again.Rules {
		if rule.ID == "global-sop" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("role-bound global rule duplicated: %#v", again.Rules)
	}
	other := e.resolveAssets(agent, "p1", "sop-b", 1)
	ids := map[string]bool{}
	for _, rule := range other.Rules {
		ids[rule.ID] = true
	}
	if !ids["bound"] || !ids["global-other"] || ids["global-sop"] || ids["shared-global"] {
		t.Fatalf("sop-b rules = %#v", other.Rules)
	}
	none := e.resolveAssets(domain.AgentDefinition{}, "p1", "", 1)
	if len(none.Rules) != 0 {
		t.Fatalf("empty workflow must not inject global rules: %#v", none.Rules)
	}
}

func TestGenericMRRoleResolvesGongfengMarketAlias(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := repo.SeedTestGenericTemplates(st); err != nil {
		t.Fatal(err)
	}
	gongfeng := domain.MCPServerDefinition{
		ID: "mcp-61-project-alpha", Name: "Gongfeng MCP", Scope: "project", ProjectID: "alpha",
		MarketID: "61", Transport: "http", URL: "https://mcpgw.knot.example.com/gongfeng", AuthType: "taihu-pat",
	}
	tapd := domain.MCPServerDefinition{
		ID: "mcp-70-project-alpha", Name: "TAPD MCP", Scope: "project", ProjectID: "alpha",
		MarketID: "70", Transport: "http", URL: "https://mcpgw.knot.example.com/tapd", AuthType: "taihu-pat",
	}
	other := domain.MCPServerDefinition{
		ID: "mcp-61-project-beta", Name: "Gongfeng MCP", Scope: "project", ProjectID: "beta",
		MarketID: "61", Transport: "http", URL: "https://mcpgw.knot.example.com/gongfeng", AuthType: "taihu-pat",
	}
	for _, server := range []domain.MCPServerDefinition{gongfeng, tapd, other} {
		if err := st.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	agent, ok := st.GenericAgentByID(repo.GenericAgentMR)
	if !ok {
		t.Fatal("generic MR role missing")
	}
	if len(agent.MCPServers) != 1 || agent.MCPServers[0] != "gongfeng" {
		t.Fatalf("generic MR role should bind the gongfeng alias, got %#v", agent.MCPServers)
	}
	assets := (&Engine{repo: st}).resolveAssets(agent, "alpha", "", 1)
	if len(assets.MCPServers) != 1 || assets.MCPServers[0].ID != gongfeng.ID {
		t.Fatalf("generic MR node MCP = %#v, want project Gongfeng install %q", assets.MCPServers, gongfeng.ID)
	}
	byCatalog := (&Engine{repo: st}).resolveAssets(domain.AgentDefinition{MCPServers: []string{"61"}}, "alpha", "", 1)
	if len(byCatalog.MCPServers) != 1 || byCatalog.MCPServers[0].ID != gongfeng.ID {
		t.Fatalf("catalog id 61 should still resolve: %#v", byCatalog.MCPServers)
	}
	wrong := (&Engine{repo: st}).resolveAssets(domain.AgentDefinition{MCPServers: []string{"gongfeng"}}, "missing", "", 1)
	if len(wrong.MCPServers) != 0 {
		t.Fatalf("gongfeng alias must stay inside the run project, got %#v", wrong.MCPServers)
	}
}

func TestGenericCatalogAssetsIgnoreStoredCrossProjectCollisions(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := repo.SeedTestGenericTemplates(st); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: repo.GenericAgentRequirement, Name: "Foreign edit", ProjectID: "other", ExecutorID: "missing-executor"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRule(domain.RuleDefinition{ID: repo.GenericRuleCore, Name: "Foreign rule", ProjectID: "other", Content: "wrong"}); err != nil {
		t.Fatal(err)
	}
	wf, ok := st.GenericWorkflowByID(repo.GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("generic backend SOP missing")
	}
	agent, ok := workflowAgent(st, wf, repo.GenericAgentRequirement)
	if !ok || agent.Name == "Foreign edit" {
		t.Fatalf("generic workflow resolved stored role collision: %#v", agent)
	}
	if _, err := workflowSandboxRequirements(st, wf); err != nil {
		t.Fatalf("generic catalog role was poisoned by stored collision: %v", err)
	}
	assets := (&Engine{repo: st}).resolveAssets(agent, "project", "", 1)
	for _, rule := range assets.Rules {
		if rule.ID == repo.GenericRuleCore && rule.Content == "wrong" {
			t.Fatalf("generic catalog rule was shadowed: %#v", rule)
		}
	}
}

func TestProjectSandboxNeverCrossesProjectBoundary(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, profile := range []domain.SandboxProfile{
		{ID: "local", Name: "Other local", ProjectID: "other", Driver: "local"},
		{ID: "mine", Name: "Mine", ProjectID: "project", Driver: "local"},
	} {
		if err := st.PutSandbox(profile); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := projectSandbox(st, "local", "project")
	if !ok || got.ID != "mine" {
		t.Fatalf("resolved sandbox=%#v ok=%v, want project-owned mine", got, ok)
	}
}

func TestShellAndPythonScriptNodesExecuteInOrder(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{
		ID: "script-languages", Name: "Script languages", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "shell", Name: "Shell", Type: "script", ScriptLanguage: "shell", Script: "printf shell > value.txt", MaxAttempts: 1},
			{ID: "python", Name: "Python", Type: "script", ScriptLanguage: "python", Script: "from pathlib import Path\nassert Path('value.txt').read_text() == 'shell'\nPath('python.txt').write_text('ok')", MaxAttempts: 1, ArtifactOutputs: []domain.ArtifactContract{{Name: "python", Path: "python.txt", Required: true}}},
		},
		Edges: []domain.WorkflowEdge{{ID: "shell-python", Source: "shell", Target: "python", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	finished, _ := s.Run(run.ID)
	if len(finished.Artifacts) == 0 || finished.Nodes["python"].Status != "succeeded" {
		t.Fatalf("script run did not produce artifact: %+v", finished)
	}
	for _, nodeID := range []string{"shell", "python"} {
		output := finished.Nodes[nodeID].Output
		if !strings.Contains(output, "# 脚本执行结果") || !strings.Contains(output, "标准输出：无") {
			t.Fatalf("script %s did not retain an execution summary: %q", nodeID, output)
		}
	}
}

func TestScriptNodeCanCallProjectMCP(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutProject(domain.Project{ID: "dns", Name: "DNS"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{ID: 9, LoginName: "owner", TaihuPAT: "tai_pat_secret"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMCPServer(domain.MCPServerDefinition{
		ID: "mcp-2214-project-dns", Name: "七彩石（Rainbow） MCP", MarketID: "2214",
		Scope: "project", ProjectID: "dns", Transport: "http", URL: "https://mcpgw.knot.example.com/rainbow", AuthType: runtime.MCPAuthTaihuPAT,
	}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "mcp-script", Name: "MCP script", SandboxProfileID: "local", ProjectID: "dns",
		Nodes: []domain.WorkflowNode{{
			ID: "list", Name: "List", Type: "script", ScriptLanguage: "shell",
			Script: "agentflow mcp list > mcp.json", MaxAttempts: 1,
			ArtifactOutputs: []domain.ArtifactContract{{Name: "mcp", Path: "mcp.json", Required: true}},
		}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "dns", ResponsibleUserID: 9})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	finished, _ := s.Run(run.ID)
	body := ""
	for _, artifact := range finished.Artifacts {
		if artifact.Name == "mcp" {
			body = artifact.Content
		}
	}
	if !strings.Contains(body, "2214") || !strings.Contains(body, "rainbow") {
		t.Fatalf("mcp list artifact = %q", body)
	}
	if strings.Contains(body, "tai_pat_secret") || strings.Contains(strings.ToLower(body), "bearer") {
		t.Fatalf("mcp list leaked credentials: %q", body)
	}
}

func TestVerificationGateCanCallProjectMCP(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutProject(domain.Project{ID: "dns", Name: "DNS"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{ID: 9, LoginName: "owner", TaihuPAT: "tai_pat_secret"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMCPServer(domain.MCPServerDefinition{
		ID: "mcp-2214-project-dns", Name: "七彩石（Rainbow） MCP", MarketID: "2214",
		Scope: "project", ProjectID: "dns", Transport: "http", URL: "https://mcpgw.knot.example.com/rainbow", AuthType: runtime.MCPAuthTaihuPAT,
	}); err != nil {
		t.Fatal(err)
	}
	putGateTestAgent(t, s, "mcp-validator", []domain.VerificationGate{{ID: "mcp", Name: "mcp", Mode: "command", Required: true, ExecutionScope: domain.ExecutionScopeRun, Commands: []string{"agentflow mcp list > gate-mcp.json"}}})
	wf := domain.WorkflowDefinition{
		ID: "mcp-gate", Name: "MCP gate", SandboxProfileID: "local", ProjectID: "dns",
		Nodes: []domain.WorkflowNode{
			{ID: "prep", Name: "Prep", Type: "command", Command: "printf ready > evidence.txt", MaxAttempts: 1},
			{ID: "validate", Name: "Validate", Type: "agent", AgentID: "mcp-validator", ExecutorID: "mock", MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{
					{Name: "evidence", Path: "evidence.txt", Required: true},
					{Name: "mcp", Path: "gate-mcp.json", Required: false},
				},
			},
		},
		Edges: []domain.WorkflowEdge{{ID: "prep-validate", Source: "prep", Target: "validate", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "dns", ResponsibleUserID: 9})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	finished, _ := s.Run(run.ID)
	body := ""
	for _, artifact := range finished.Artifacts {
		if artifact.Name == "mcp" {
			body = artifact.Content
		}
	}
	if !strings.Contains(body, "2214") || !strings.Contains(body, "rainbow") {
		t.Fatalf("gate mcp list artifact = %q status=%s node=%+v", body, finished.Status, finished.Nodes["prep"])
	}
}

func TestCollectRepositoryPatchIncludesUntrackedE2EAndOmitsDeliverables(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", source, "add", ".").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	if output, err := exec.Command("git", "-C", source, "commit", "-m", "baseline").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := prepareRepository(context.Background(), workspace, domain.RepositoryContext{RepositoryID: "repo", LocalPath: source, BaseBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	e2eDir := filepath.Join(workspace, "tests", "integration")
	if err := os.MkdirAll(e2eDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e2eDir, "skills_count_cache_e2e_test.go"), []byte("package integration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "e2e-cases.md"), []byte("# cases\nSKIP_INCREMENTAL_E2E\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run", Nodes: map[string]*domain.NodeRun{"test": {NodeID: "test", Attempt: 1}}}
	artifacts, err := collectArtifacts(workspace, run, domain.WorkflowNode{ID: "test"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	var patch string
	for _, artifact := range artifacts {
		if artifact.Name == "repository-patch" {
			patch = artifact.Content
		}
	}
	if !strings.Contains(patch, "skills_count_cache_e2e_test.go") {
		t.Fatalf("untracked e2e file missing from patch: %s", patch)
	}
	if strings.Contains(patch, "e2e-cases.md") {
		t.Fatalf("deliverable markdown leaked into repository patch: %s", patch)
	}
}

func TestMaterializeInputsMountsAncestorE2ECases(t *testing.T) {
	dir := t.TempDir()
	wf := domain.WorkflowDefinition{ID: "wf", Edges: []domain.WorkflowEdge{
		{Source: "test", Target: "mr"},
		{Source: "mr", Target: "checkers"},
		{Source: "checkers", Target: "verify"},
	}}
	run := domain.Run{Artifacts: []domain.Artifact{
		{NodeID: "test", Attempt: 1, Name: "e2e-cases", Path: "e2e-cases.md", MediaType: "text/markdown", SHA256: "abc", Content: "SKIP_INCREMENTAL_E2E\n原因：仅文档"},
		{NodeID: "checkers", Attempt: 1, Name: "checkers-report", Path: "checkers-report.md", MediaType: "text/markdown", SHA256: "def", Content: "all green"},
	}}
	manifest, err := materializeInputs(dir, wf, domain.WorkflowNode{ID: "verify"}, run)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".agentflow", "inputs", "test", "e2e-cases.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "SKIP_INCREMENTAL_E2E") {
		t.Fatalf("mounted e2e-cases = %q", got)
	}
	if !strings.Contains(manifest, "test / e2e-cases") {
		t.Fatalf("manifest missing test e2e-cases: %s", manifest)
	}
	if strings.Contains(manifest, "SKIP_INCREMENTAL_E2E") {
		t.Fatalf("prompt catalog must not inline artifact bodies: %s", manifest)
	}
}

func TestMaterializeInputsRejectsSymlinkedControlDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".agentflow", "inputs")); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "build", Target: "review"}}}
	run := domain.Run{Artifacts: []domain.Artifact{{NodeID: "build", Attempt: 1, Name: "report", MediaType: "text/markdown", Content: "do not escape"}}}
	if _, err := materializeInputs(dir, wf, domain.WorkflowNode{ID: "review"}, run); err == nil {
		t.Fatal("materializeInputs accepted a symlinked .agentflow/inputs directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "build", "report.md")); !os.IsNotExist(err) {
		t.Fatalf("artifact escaped the workspace through a symlink: %v", err)
	}
}

func TestMaterializeInputsMountsCloseDeliveryArtifacts(t *testing.T) {
	dir := t.TempDir()
	wf := domain.WorkflowDefinition{ID: "wf", Edges: []domain.WorkflowEdge{
		{Source: "test", Target: "mr"},
		{Source: "mr", Target: "checkers"},
		{Source: "checkers", Target: "verify"},
		{Source: "verify", Target: "close"},
	}}
	run := domain.Run{Artifacts: []domain.Artifact{
		{NodeID: "test", Attempt: 1, Name: "test-report", Path: "test-report.md", MediaType: "text/markdown", SHA256: "a", Content: "coverage 81%"},
		{NodeID: "test", Attempt: 1, Name: "e2e-cases", Path: "e2e-cases.md", MediaType: "text/markdown", SHA256: "b", Content: "go test ./tests/integration"},
		{NodeID: "mr", Attempt: 1, Name: "mr-body", Path: "mr-body.md", MediaType: "text/markdown", SHA256: "c", Content: "https://git.example.com/x/y/merge_requests/1"},
		{NodeID: "verify", Attempt: 1, Name: "e2e-report", Path: "e2e-report.md", MediaType: "text/markdown", SHA256: "d", Content: "pipeline https://zhiyan.example.com/qci/1"},
		{NodeID: "checkers", Attempt: 1, Name: "checkers-report", Path: "checkers-report.md", MediaType: "text/markdown", SHA256: "e", Content: "all green"},
	}}
	manifest, err := materializeInputs(dir, wf, domain.WorkflowNode{ID: "close"}, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join(".agentflow", "inputs", "test", "test-report.md"),
		filepath.Join(".agentflow", "inputs", "verify", "e2e-report.md"),
		filepath.Join(".agentflow", "inputs", "mr", "mr-body.md"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing mounted %s: %v", rel, err)
		}
	}
	if !strings.Contains(manifest, "verify / e2e-report") || !strings.Contains(manifest, "test / test-report") {
		t.Fatalf("manifest missing close delivery artifacts: %s", manifest)
	}
	if strings.Contains(manifest, "coverage 81%") || strings.Contains(manifest, "pipeline https://zhiyan.example.com") {
		t.Fatalf("prompt catalog must not inline artifact bodies: %s", manifest)
	}
}

func TestMergeArtifactContractsLetsNodeRefineAgentContract(t *testing.T) {
	agent := []domain.ArtifactContract{{Name: "report", Path: "result.md", MediaType: "text/markdown", Required: true}, {Name: "patch", Path: "changes.patch", Required: false}}
	node := []domain.ArtifactContract{{Name: "report", Path: "review.md", MediaType: "text/markdown", Required: true}}
	merged := mergeArtifactContracts(agent, node)
	if len(merged) != 2 || merged[0].Path != "review.md" || merged[1].Name != "patch" {
		t.Fatalf("unexpected merged contracts: %+v", merged)
	}
}

func TestPrepareRepositoryRefreshesBaseThenCreatesWorkBranch(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "latest"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
	ctx := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "agentflow/issue-22-run"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	branch, err := exec.Command("git", "-C", workspace, "branch", "--show-current").Output()
	if err != nil || strings.TrimSpace(string(branch)) != ctx.WorkBranch {
		t.Fatalf("work branch = %q, err=%v", branch, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != "latest\n" {
		t.Fatalf("unexpected base content %q, err=%v", content, err)
	}
}

func TestPrepareRepositoryAppliesResponsibleUserGitIdentityOnCloneAndResume(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "init", "-b", "master")
	mustGit(t, source, "config", "user.name", "Source User")
	mustGit(t, source, "config", "user.email", "source@example.com")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "add", ".")
	mustGit(t, source, "commit", "-m", "base")
	mustGit(t, "", "clone", "--bare", source, remote)

	repository := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/identity"}
	ctx := withGitIdentity(context.Background(), "example-admin", "example-admin@users.noreply.loopforge.invalid")
	if err := prepareRepository(ctx, workspace, repository); err != nil {
		t.Fatal(err)
	}
	gitOutput := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if got := gitOutput("config", "--local", "user.name"); got != "example-admin" {
		t.Fatalf("user.name = %q", got)
	}
	if got := gitOutput("config", "--local", "user.email"); got != "example-admin@users.noreply.loopforge.invalid" {
		t.Fatalf("user.email = %q", got)
	}

	mustGit(t, workspace, "config", "--local", "user.name", "Stale User")
	mustGit(t, workspace, "config", "--local", "user.email", "stale@example.com")
	if err := prepareRepository(ctx, workspace, repository); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, workspace, "add", "fix.txt")
	mustGit(t, workspace, "commit", "-m", "fix")
	if got := gitOutput("log", "-1", "--format=%an <%ae>"); got != "example-admin <example-admin@users.noreply.loopforge.invalid>" {
		t.Fatalf("commit author = %q", got)
	}
}

func TestPrepareRepositoryIgnoresMissingLocalPathWhenCloneURLExists(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "ok.txt"), []byte("from-remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "ok"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
	ctx := domain.RepositoryContext{
		RepositoryID: "skillhub",
		CloneURL:     remote,
		LocalPath:    filepath.Join(root, "missing-host-checkout"),
		BaseBranch:   "master",
		WorkBranch:   "feature/fix-issue-18",
	}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatalf("missing localPath should fall back to cloneUrl: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, "ok.txt"))
	if err != nil || string(got) != "from-remote\n" {
		t.Fatalf("workspace content = %q err=%v", got, err)
	}
}

func TestPrepareRepositoryResumeKeepsLocalCommits(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
	ctx := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-issue-9"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("local work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "local work"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	branch, err := exec.Command("git", "-C", workspace, "branch", "--show-current").Output()
	if err != nil || strings.TrimSpace(string(branch)) != ctx.WorkBranch {
		t.Fatalf("resumed branch = %q, err=%v", branch, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "fix.txt"))
	if err != nil || string(content) != "local work\n" {
		t.Fatalf("resume reset local commits: %q err=%v", content, err)
	}
}

func TestPrepareRepositoryChecksOutExistingRemoteWorkBranch(t *testing.T) {
	root := t.TempDir()
	source, remote, workspace := filepath.Join(root, "source"), filepath.Join(root, "remote.git"), filepath.Join(root, "workspace")
	writeGitRepoWithWorkBranch(t, source, remote, "feature/fix-issue-18")
	ctx := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-issue-18"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	assertCheckedOutWorkBranch(t, workspace, ctx.WorkBranch, "fix\n")
}

func TestPrepareRepositoryResumeResetsNamesakeWorkBranch(t *testing.T) {
	root := t.TempDir()
	source, remote, workspace := filepath.Join(root, "source"), filepath.Join(root, "remote.git"), filepath.Join(root, "workspace")
	writeGitRepoWithWorkBranch(t, source, remote, "feature/fix-issue-18")
	mustGit(t, "", "clone", "--single-branch", "--branch", "master", remote, workspace)
	mustGit(t, workspace, "checkout", "--force", "-B", "agentflow-base", "origin/master")
	if output, err := exec.Command("git", "-C", workspace, "fetch", "origin", "feature/fix-issue-18").CombinedOutput(); err != nil {
		t.Fatalf("legacy fetch: %v: %s", err, output)
	}
	if _, err := exec.Command("git", "-C", workspace, "rev-parse", "--verify", "origin/feature/fix-issue-18^{commit}").CombinedOutput(); err == nil {
		t.Fatal("setup expected origin/feature/fix-issue-18 to be missing after single-branch clone")
	}
	mustGit(t, workspace, "checkout", "-B", "feature/fix-issue-18", "agentflow-base")
	got, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(got) != "base\n" {
		t.Fatalf("setup namesake content = %q err=%v", got, err)
	}
	ctx := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-issue-18"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	assertCheckedOutWorkBranch(t, workspace, ctx.WorkBranch, "fix\n")
}

func TestPrepareRepositoryResumeKeepsLocalCommitsWhenRemoteWorkBranchExists(t *testing.T) {
	root := t.TempDir()
	source, remote, workspace := filepath.Join(root, "source"), filepath.Join(root, "remote.git"), filepath.Join(root, "workspace")
	writeGitRepoWithWorkBranch(t, source, remote, "feature/fix-issue-18")
	mustGit(t, "", "clone", "--single-branch", "--branch", "master", remote, workspace)
	mustGit(t, workspace, "checkout", "-B", "feature/fix-issue-18", "origin/master")
	if err := os.WriteFile(filepath.Join(workspace, "local.txt"), []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, workspace, "config", "user.email", "test@example.com")
	mustGit(t, workspace, "config", "user.name", "Test")
	mustGit(t, workspace, "add", ".")
	mustGit(t, workspace, "commit", "-m", "local work")
	ctx := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-issue-18"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	branch, err := exec.Command("git", "-C", workspace, "branch", "--show-current").Output()
	if err != nil || strings.TrimSpace(string(branch)) != ctx.WorkBranch {
		t.Fatalf("resumed branch = %q, err=%v", branch, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "local.txt"))
	if err != nil || string(content) != "keep me\n" {
		t.Fatalf("resume reset unique local commits: %q err=%v", content, err)
	}
	base, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(base) != "base\n" {
		t.Fatalf("unique local history should stay on master lineage, got %q err=%v", base, err)
	}
}

func writeGitRepoWithWorkBranch(t *testing.T, source, remote, workBranch string) {
	t.Helper()
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "init", "-b", "master")
	mustGit(t, source, "config", "user.email", "test@example.com")
	mustGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "add", ".")
	mustGit(t, source, "commit", "-m", "base")
	mustGit(t, source, "checkout", "-b", workBranch)
	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "add", ".")
	mustGit(t, source, "commit", "-m", "fix")
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	if output, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func assertCheckedOutWorkBranch(t *testing.T, workspace, workBranch, wantContent string) {
	t.Helper()
	branch, err := exec.Command("git", "-C", workspace, "branch", "--show-current").Output()
	if err != nil || strings.TrimSpace(string(branch)) != workBranch {
		t.Fatalf("work branch = %q, err=%v", branch, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != wantContent {
		t.Fatalf("checked out content = %q, err=%v", content, err)
	}
	origin, err := exec.Command("git", "-C", workspace, "rev-parse", "--verify", "origin/"+workBranch+"^{commit}").Output()
	if err != nil {
		t.Fatalf("origin/%s missing: %v", workBranch, err)
	}
	head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != strings.TrimSpace(string(origin)) {
		t.Fatalf("HEAD %q != origin/%s %q", strings.TrimSpace(string(head)), workBranch, strings.TrimSpace(string(origin)))
	}
}

func TestStartUsesFreshWorkBranchPerSOP(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	if err := s.PutRun(domain.Run{ID: "old", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: "gf-5", IssueNumber: 5}, Repository: domain.RepositoryContext{RepositoryID: "skillhub", WorkBranch: "feature/old-sop-branch"}, Nodes: map[string]*domain.NodeRun{}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	wf := domain.WorkflowDefinition{ID: "branch-test", Name: "Branch test", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "skillhub"}, domain.TelemetryContext{IssueID: "gf-5", IssueNumber: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(run.ID) })
	if run.Repository.WorkBranch == "" || run.Repository.WorkBranch == "feature/old-sop-branch" {
		t.Fatalf("new SOP reused previous branch: %q", run.Repository.WorkBranch)
	}
	if persistentSandboxID(run) != run.ID {
		t.Fatalf("sandbox should be per SOP run, got %q", persistentSandboxID(run))
	}
}

func TestFinishSucceededDeletesPersistentSandboxImmediately(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m := runtime.NewManager(filepath.Join(dir, "workspaces"))
	e := New(s, m)
	runID := "run-sandbox-reclaim"
	if err := s.PutRun(domain.Run{ID: runID, Status: "running", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	sb, err := m.AcquirePersistent(context.Background(), runID, domain.SandboxProfile{ID: "local", Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	path := sb.Workspace()
	if err := os.WriteFile(filepath.Join(path, "clone.txt"), []byte("repo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.finish(runID, "succeeded", "done")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("succeeded sandbox survived immediate cleanup: %v", err)
	}
	finished, ok := s.Run(runID)
	if !ok || finished.WorkspaceCleanedAt.IsZero() {
		t.Fatal("workspace cleanup was not recorded")
	}
}

func TestFinishFailedKeepsPersistentSandbox(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m := runtime.NewManager(filepath.Join(dir, "workspaces"))
	e := New(s, m)
	runID := "run-keep-on-fail"
	if err := s.PutRun(domain.Run{ID: runID, Status: "running", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	sb, err := m.AcquirePersistent(context.Background(), runID, domain.SandboxProfile{ID: "local", Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	path := sb.Workspace()
	if err := os.WriteFile(filepath.Join(path, "clone.txt"), []byte("repo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.finish(runID, "failed", "test failed")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed run wiped persist workspace: %v", err)
	}
}

func TestDefaultWorkBranchIncludesHourMinute(t *testing.T) {
	now := time.Date(2026, 8, 17, 22, 54, 0, 0, time.Local)
	got := domain.DefaultWorkBranch(domain.Issue{ID: "gf-5", Source: "gongfeng", Kind: "issue", Number: 5}, now)
	if got != "feature/fix-issue-5-20260817-2254" {
		t.Fatalf("work branch = %q", got)
	}
}

func TestPrepareRepositoryKeepsExistingWorkspaceDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "ok.txt"), []byte("from-clone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "ok"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agentflow", "docker", "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareRepository(context.Background(), workspace, domain.RepositoryContext{RepositoryID: "repo", LocalPath: source, BaseBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("workspace directory was replaced; docker bind-mount would break")
	}
	got, err := os.ReadFile(filepath.Join(workspace, "ok.txt"))
	if err != nil || string(got) != "from-clone\n" {
		t.Fatalf("workspace content = %q err=%v", got, err)
	}
}

func TestResetCloneDestinationCreatesMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if err := resetCloneDestination(missing); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(missing)
	if err != nil || !info.IsDir() {
		t.Fatalf("workspace not created: %v", err)
	}
}

func TestPrepareRepositoryRetryKeepsExistingWorkspaceDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	badRef := filepath.Join(root, "bad-ref")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "ok.txt"), []byte("from-remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "ok"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "clone", remote, badRef).CombinedOutput(); err != nil {
		t.Fatalf("create reference: %v: %s", err, output)
	}
	if err := os.RemoveAll(filepath.Join(badRef, ".git", "objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.RepositoryContext{RepositoryID: "repo", LocalPath: badRef, CloneURL: remote, BaseBranch: "master"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("workspace directory was replaced on clone retry; docker bind-mount would break")
	}
	got, err := os.ReadFile(filepath.Join(workspace, "ok.txt"))
	if err != nil || string(got) != "from-remote\n" {
		t.Fatalf("workspace content = %q err=%v", got, err)
	}
}

func TestPrepareRepositoryUsesRemoteMasterNotStaleLocalPath(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	local := filepath.Join(root, "local")
	workspace := filepath.Join(root, "workspace")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "stale"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "clone", remote, local).CombinedOutput(); err != nil {
		t.Fatalf("create local checkout: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "latest"}, {"remote", "add", "origin", remote}, {"push", "origin", "master"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := domain.RepositoryContext{RepositoryID: "skillhub", LocalPath: local, CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-issue-5-20260817-2254"}
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != "latest\n" {
		t.Fatalf("wanted remote master, got %q err=%v", content, err)
	}
	origin, err := exec.Command("git", "-C", workspace, "remote", "get-url", "origin").Output()
	if err != nil || strings.TrimSpace(string(origin)) != remote {
		t.Fatalf("origin = %q, err=%v", origin, err)
	}
}

func TestPrepareRepositoryWithTokenKeepsCleanOrigin(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "ok.txt"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "ok"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v: %s", err, output)
	}
	repo := domain.RepositoryContext{RepositoryID: "skillhub", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-issue-18"}
	ctx := withGitToken(context.Background(), "tok_secret")
	if err := prepareRepository(ctx, workspace, repo); err != nil {
		t.Fatal(err)
	}
	origin, err := exec.Command("git", "-C", workspace, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(origin))
	if got != remote {
		t.Fatalf("origin = %q, want clean cloneUrl %q", got, remote)
	}
	if strings.Contains(got, "tok_secret") {
		t.Fatal("token leaked into origin url")
	}
	for _, name := range []string{gitAuthIncludeFile, gitAskPassFile, gitAskPassSecretFile} {
		if _, err := os.Stat(filepath.Join(workspace, ".git", name)); !os.IsNotExist(err) {
			t.Fatalf("workspace persisted git credential file %s: %v", name, err)
		}
	}
}

func TestRunWaitsForGateAndCompletes(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putTestDeliveryWorkflow(t, s)
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start("delivery", "build a small feature")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	if current.Nodes["review"].Status != "waiting_approval" {
		t.Fatalf("review status = %s", current.Nodes["review"].Status)
	}
	if err := e.Approve(r.ID, "review", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
}

func TestHumanGateFailureWaitsForApprove(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "human-gate-agent", []domain.VerificationGate{{ID: "strict", Name: "strict", Mode: "command", Required: true, Commands: []string{"false"}, OnFailure: "human", ExecutionScope: domain.ExecutionScopeRun}})
	wf := domain.WorkflowDefinition{ID: "human-gate-lab", Name: "human gate lab", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "write", Name: "write", Type: "command", Command: "printf 'ok\\n' > out.md", ArtifactOutputs: []domain.ArtifactContract{{Name: "out", Path: "out.md", Required: true}}, MaxAttempts: 1},
		{ID: "check", Name: "check", Type: "agent", AgentID: "human-gate-agent", ExecutorID: "mock", RejectTo: "write", OnFailure: "human", MaxAttempts: 1, MaxReworks: 3},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "write", Target: "check", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	if current.Nodes["check"].Status != "waiting_approval" {
		t.Fatalf("check status = %s error=%s", current.Nodes["check"].Status, current.Nodes["check"].Error)
	}
	if err := e.Approve(r.ID, "check", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
}

func TestSubmitReworkWithoutRejectToDegradesToFail(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutAgent(domain.AgentDefinition{ID: "rework-no-target-agent", Name: "rework-no-target-agent", ExecutorID: "mock", Instructions: "[[mock:gate=rework]]"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "rework-no-target", Name: "rework no target", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "solo", Name: "solo", Type: "agent", AgentID: "rework-no-target-agent", ExecutorID: "mock", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	current, _ := s.Run(run.ID)
	node := current.Nodes["solo"]
	if node.Status != "failed" {
		t.Fatalf("solo status = %s", node.Status)
	}
	if !strings.Contains(node.Error, "no rework target") {
		t.Fatalf("error should explain the degraded rework: %q", node.Error)
	}
	if node.GateResult == nil || node.GateResult.Decision != "rework" {
		t.Fatalf("gate result should be preserved for audit: %+v", node.GateResult)
	}
}

func TestSubmitReworkRejectsUpstreamInsteadOfRetrying(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// Attempt 1 submits rework; if the node (wrongly) retries locally, attempt
	// 2 would pass and the upstream node would never be reworked.
	if err := s.PutAgent(domain.AgentDefinition{ID: "rework-upstream-agent", Name: "rework-upstream-agent", ExecutorID: "mock", Instructions: "[[mock:gate=rework,pass]]"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "rework-upstream", Name: "rework upstream", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "repair", Name: "repair", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "review", Name: "review", Type: "agent", AgentID: "rework-upstream-agent", ExecutorID: "mock", MaxAttempts: 3, MaxReworks: 3, RejectTo: "repair"},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "repair", Target: "review", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	current, _ := s.Run(run.ID)
	if repair := current.Nodes["repair"]; repair.ReworkCount != 1 {
		t.Fatalf("rework decision must reject upstream instead of retrying the node locally: repair reworkCount=%d", repair.ReworkCount)
	}
	rejected := false
	for _, event := range current.Telemetry {
		if event.Name == "gate.rejected" {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("rework decision must emit gate.rejected")
	}
}

func TestSubmitReworkBudgetExhaustedWaitsForHuman(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// The agent keeps asking for rework; MaxReworks=1 allows a single reject.
	// The second rework verdict must park the node on a human gate instead of
	// terminating the run as failed: approve accepts the upstream output.
	if err := s.PutAgent(domain.AgentDefinition{ID: "rework-budget-agent", Name: "rework-budget-agent", ExecutorID: "mock", Instructions: "[[mock:gate=rework]]"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "rework-budget", Name: "rework budget", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "repair", Name: "repair", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "review", Name: "review", Type: "agent", AgentID: "rework-budget-agent", ExecutorID: "mock", MaxAttempts: 3, MaxReworks: 1, RejectTo: "repair"},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "repair", Target: "review", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "waiting")
	current, _ := s.Run(run.ID)
	if repair := current.Nodes["repair"]; repair.ReworkCount != 1 {
		t.Fatalf("exactly one upstream rework must happen before the budget is spent: repair reworkCount=%d", repair.ReworkCount)
	}
	review := current.Nodes["review"]
	if review.Status != "waiting_approval" {
		t.Fatalf("review status = %s", review.Status)
	}
	if !strings.Contains(review.Error, "返工次数已用尽") || !strings.Contains(review.Error, "MaxReworks=1") {
		t.Fatalf("budget-exhausted rework must explain itself: %q", review.Error)
	}
	// A human approval accepts the upstream output and completes the run.
	if err := e.Approve(run.ID, "review", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
}

func TestSubmitTerminalDecisionSkipsGatesTelemetry(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putGateTestAgent(t, s, "rework-with-gates-agent", []domain.VerificationGate{{ID: "objective", Name: "objective", Mode: "command", Required: true, Commands: []string{"true"}, OnFailure: "fail", ExecutionScope: domain.ExecutionScopeRun}})
	// The mock agent defaults to pass; rework is forced via instructions.
	if agent, ok := s.Agent("rework-with-gates-agent"); ok {
		agent.Instructions = "[[mock:gate=rework]]"
		if err := s.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	wf := domain.WorkflowDefinition{ID: "rework-with-gates", Name: "rework with gates", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "repair", Name: "repair", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "review", Name: "review", Type: "agent", AgentID: "rework-with-gates-agent", ExecutorID: "mock", MaxAttempts: 1, MaxReworks: 1, RejectTo: "repair"},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "repair", Target: "review", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Always-rework agent with MaxReworks=1: first verdict rejects upstream, the
	// second exhausts the budget and parks the node on a human gate.
	waitFor(t, s, run.ID, "waiting")
	current, _ := s.Run(run.ID)
	found := false
	for _, event := range current.Telemetry {
		if event.Name == "gate.terminal" {
			found = true
			if event.Attributes["decision"] != "rework" {
				t.Fatalf("gate.terminal decision=%v", event.Attributes["decision"])
			}
			if !strings.Contains(event.Message, "decision=rework") || !strings.Contains(event.Message, "skipped by terminal decision") {
				t.Fatalf("gate.terminal message=%q", event.Message)
			}
			break
		}
	}
	if !found {
		t.Fatal("terminal rework decision must record that objective gates were skipped")
	}
}

func TestSubmitGateResumesSessionWhenResultIsMissing(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putGateTestAgent(t, s, "submit-gate-agent", nil)
	wf := domain.WorkflowDefinition{
		ID: "submit-gate-lab", Name: "submit gate lab", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{
			ID: "review", Name: "review", Type: "agent", AgentID: "submit-gate-agent", ExecutorID: "mock", MaxAttempts: 1,
		}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	current, _ := s.Run(run.ID)
	node := current.Nodes["review"]
	if node.GateResult == nil || node.GateResult.Decision != "pass" {
		t.Fatalf("gate result=%+v", node.GateResult)
	}
	if node.SessionID != "mock-submit-gate-agent" {
		t.Fatalf("session=%q", node.SessionID)
	}
	foundMissing := false
	for _, event := range current.Telemetry {
		if event.Name == "gate.submit.missing" {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Fatal("missing submit result did not trigger a same-session resume")
	}
}

func TestHumanGateFailureRetryRerunsNode(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "human-retry-agent", []domain.VerificationGate{{ID: "strict", Name: "strict", Mode: "command", Required: true, Commands: []string{"false"}, OnFailure: "human", ExecutionScope: domain.ExecutionScopeRun}})
	wf := domain.WorkflowDefinition{ID: "human-retry-lab", Name: "human retry lab", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "check", Name: "check", Type: "agent", AgentID: "human-retry-agent", ExecutorID: "mock", OnFailure: "human", MaxAttempts: 1},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	if err := e.Retry(r.ID, "check", "人工确认重试本节点", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	if current.Nodes["check"].Status != "waiting_approval" {
		t.Fatalf("after retry check status = %s", current.Nodes["check"].Status)
	}
}

func TestCancelMarksActiveNodeCancelled(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "cancel-active", Name: "cancel active", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "echo started; sleep 5; echo finished", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		current, _ := s.Run(r.ID)
		if current.Nodes["wait"].Status == "running" {
			running = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !running {
		t.Fatal("node did not start before cancellation")
	}
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "cancelled")
	current, _ := s.Run(r.ID)
	if current.Nodes["wait"].Status != "cancelled" || current.Nodes["wait"].Error != "" {
		t.Fatalf("cancelled node = %+v", current.Nodes["wait"])
	}
}

func TestRetryCancelledRunFromNodeAfterImmediateCleanup(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "resume-cancelled", Name: "resume cancelled", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "analyze", Name: "analyze", Type: "command", Command: "printf 'plan\\n' > fix-plan.md", ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "fix-plan.md", Required: true}}, MaxAttempts: 1},
		{ID: "fix", Name: "fix", Type: "command", Command: "sleep 2; test -f .agentflow/inputs/analyze/plan.md", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "analyze-fix", Source: "analyze", Target: "fix", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.Run(r.ID)
		if current.Nodes["analyze"].Status == "succeeded" && current.Nodes["fix"].Status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := s.Run(r.ID)
	if current.Nodes["analyze"].Status != "succeeded" || current.Nodes["fix"].Status != "running" {
		t.Fatalf("expected analyze succeeded and fix running before cancel, got analyze=%+v fix=%+v", current.Nodes["analyze"], current.Nodes["fix"])
	}
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "cancelled")
	current = waitForWorkspaceCleanup(t, s, r.ID)
	if err := e.Retry(r.ID, "", "", ""); err == nil || !strings.Contains(err.Error(), "workspace has been cleaned") {
		t.Fatalf("retry error = %v", err)
	}
	if err := e.Retry(r.ID, "fix", "从指定节点重跑", "确认上游方案后继续"); err != nil {
		t.Fatalf("retry cancelled run from chosen node: %v", err)
	}
	waitFor(t, s, r.ID, "succeeded")
	replayed, _ := s.Run(r.ID)
	if replayed.Nodes["analyze"].Status != "succeeded" || replayed.Nodes["fix"].Status != "succeeded" || strings.Contains(replayed.Nodes["fix"].Feedback, "确认上游方案后继续") || strings.Contains(replayed.HumanContext, "确认上游方案后继续") {
		t.Fatalf("replayed run = %+v", replayed)
	}
}

func TestRetrySucceededRunFromNodeAfterCleanup(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "replay-succeeded", Name: "replay succeeded", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "first", Name: "first", Type: "command", Command: "printf one > first.txt", ArtifactOutputs: []domain.ArtifactContract{{Name: "first", Path: "first.txt", Required: true}}, MaxAttempts: 1},
		{ID: "second", Name: "second", Type: "command", Command: "printf two > second.txt", ArtifactOutputs: []domain.ArtifactContract{{Name: "second", Path: "second.txt", Required: true}}, MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "first-second", Source: "first", Target: "second", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	cleaned := waitForWorkspaceCleanup(t, s, r.ID)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		t.Fatal("expected succeeded run workspace to be cleaned before replay")
	}
	if err := e.Retry(r.ID, "second", "结果不符合预期", "请按补充说明重做第二步"); err != nil {
		t.Fatalf("retry succeeded run: %v", err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Nodes["first"].Status != "succeeded" {
		t.Fatalf("upstream node should stay succeeded, got %+v", current.Nodes["first"])
	}
	if current.Nodes["second"].Status != "succeeded" {
		t.Fatalf("replayed node = %+v", current.Nodes["second"])
	}
	if strings.Contains(current.Nodes["second"].Feedback, "请按补充说明重做第二步") || strings.Contains(current.HumanContext, "请按补充说明重做第二步") {
		t.Fatalf("script replay saved human context: feedback=%q humanContext=%q", current.Nodes["second"].Feedback, current.HumanContext)
	}
}

func TestRetryLegacySucceededRunAfterMainBotMissingSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "replay-succeeded", Name: "replay succeeded", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "first", Name: "first", Type: "command", Command: "printf one > first.txt", ArtifactOutputs: []domain.ArtifactContract{{Name: "first", Path: "first.txt", MediaType: "text/plain", Required: true}}, MaxAttempts: 1},
		{ID: "second", Name: "second", Type: "command", Command: "test -s .agentflow/inputs/first/first.txt && printf two > second.txt", ArtifactOutputs: []domain.ArtifactContract{{Name: "second", Path: "second.txt", Required: true}}, MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "first-second", Source: "first", Target: "second", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	cleaned := waitForWorkspaceCleanup(t, s, r.ID)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		t.Fatal("expected succeeded run workspace to be cleaned before replay")
	}
	// Simulate a pre-upgrade run whose directory was deleted before snapshots
	// existed, followed by a Main Bot attempt that creates lease-only metadata.
	token, err := e.beginWorkspaceUseAfterCleanup(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", r.ID).Updates(map[string]any{"archive": nil, "sha256": "", "saved_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), r.ID, token); err == nil {
		t.Fatal("missing snapshot was reported restored")
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), r.ID, token); err != nil {
		t.Fatal(err)
	}
	if err = e.RestoreConversationWorkspaceIfNeeded(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(r.ID, "second", "结果不符合预期", "请按补充说明重做第二步"); err != nil {
		t.Fatalf("retry succeeded run: %v", err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Nodes["first"].Status != "succeeded" || current.Nodes["first"].Attempt != 1 || current.Nodes["second"].Attempt != 2 {
		t.Fatalf("upstream node should stay succeeded, got %+v", current.Nodes["first"])
	}
	if current.Nodes["second"].Status != "succeeded" {
		t.Fatalf("replayed node = %+v", current.Nodes["second"])
	}
	if strings.Contains(current.Nodes["second"].Feedback, "请按补充说明重做第二步") || strings.Contains(current.HumanContext, "请按补充说明重做第二步") {
		t.Fatalf("script replay saved human context: feedback=%q humanContext=%q", current.Nodes["second"].Feedback, current.HumanContext)
	}
}

func TestRejectResetsTargetAndDescendants(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putTestDeliveryWorkflow(t, s)
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start("delivery", "first pass")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	if err := e.Reject(r.ID, domain.RejectRequest{NodeID: "review", TargetID: "implement", Reason: "needs changes"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	if current.Nodes["implement"].ReworkCount != 1 {
		t.Fatalf("rework count = %d", current.Nodes["implement"].ReworkCount)
	}
	if current.Nodes["implement"].Attempt != 2 {
		t.Fatalf("attempt = %d", current.Nodes["implement"].Attempt)
	}
	if current.Nodes["implement"].Feedback != "needs changes" {
		t.Fatalf("feedback = %q", current.Nodes["implement"].Feedback)
	}
}

func TestRenderPromptAppendsIssueIdentityFromStore(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	issue := domain.Issue{
		ID: "tapd-old-run", ProjectID: "platform", Source: "tapd",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1010000000100001887", ExternalType: "story", ExternalURL: "https://tapd.example.com/story/1010000000100001887"},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "product-tapd", Name: "TAPD", TaskPrompt: &domain.TaskPromptSpec{Inputs: []string{"任务输入：{{input}}"}}}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "generic-product-delivery", Nodes: []domain.WorkflowNode{{ID: "tapd", AgentID: "product-tapd"}}}
	node := domain.WorkflowNode{ID: "tapd", AgentID: "product-tapd"}
	run := domain.Run{Input: "在soul广场页面新增一个banner", ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}}
	got := renderPromptWithStore(s, wf, node, run)
	if !strings.Contains(got, "外部 ID：1010000000100001887") || strings.Contains(got, "回写") {
		t.Fatalf("old run prompt must receive live TAPD identity:\n%s", got)
	}
	dup := renderPromptWithStore(s, wf, node, domain.Run{Input: issue.IdentityBlock(), ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}})
	if strings.Count(dup, "需求身份（平台权威，禁止忽略）") != 1 {
		t.Fatalf("identity block must not be appended twice:\n%s", dup)
	}
}

func TestRenderPromptIncludesInputUpstreamAndFeedback(t *testing.T) {
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "plan", Target: "build"}}}
	node := domain.WorkflowNode{ID: "build"}
	run := domain.Run{Input: "request", Nodes: map[string]*domain.NodeRun{"plan": {NodeID: "plan", Output: "approved plan"}, "build": {NodeID: "build", Feedback: "add tests"}}}
	got := renderPromptSpec(wf, node, domain.TaskPromptSpec{Objective: "input={{input}}\nfeedback={{feedback}}\nupstream={{upstream}}\nplan={{node.plan.output}}"}, run)
	for _, want := range []string{"input=request", "feedback=add tests", "[plan]\napproved plan", "plan=approved plan"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q does not contain %q", got, want)
		}
	}
}

func TestGenericRequirementPlaybookIncludesIssueInput(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := repo.SeedTestGenericTemplates(s); err != nil {
		t.Fatal(err)
	}
	wf, ok := s.GenericWorkflowByID(repo.GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("generic backend SOP missing")
	}
	var node domain.WorkflowNode
	for _, item := range wf.Nodes {
		if item.ID == "requirement" {
			node = item
			break
		}
	}
	if node.ID == "" {
		t.Fatal("requirement node missing")
	}
	got := renderPromptWithStore(s, wf, node, domain.Run{Input: "超时告警\n\n连接池打满后 droplet 无响应"})
	if !strings.Contains(got, "超时告警") || !strings.Contains(got, "连接池打满后 droplet 无响应") {
		t.Fatalf("generic requirement playbook dropped issue body:\n%s", got)
	}
}

func TestRenderPromptSubstitutesRejectToAndSubject(t *testing.T) {
	wf := domain.WorkflowDefinition{ID: "issue-fix-production"}
	node := domain.WorkflowNode{ID: "review", RejectTo: "fix"}
	got := renderPromptSpec(wf, node, domain.TaskPromptSpec{Objective: "失败打回 {{rejectTo}}；主题={{subject}}"}, domain.Run{})
	if !strings.Contains(got, "失败打回 fix") || !strings.Contains(got, "主题=本次 Issue") {
		t.Fatalf("placeholders not substituted: %s", got)
	}
	req := renderPromptSpec(domain.WorkflowDefinition{ID: "requirement-development-production"}, domain.WorkflowNode{RejectTo: "code"}, domain.TaskPromptSpec{Objective: "打回 {{rejectTo}} {{subject}}"}, domain.Run{})
	if !strings.Contains(req, "打回 code") || !strings.Contains(req, "本次需求") {
		t.Fatalf("requirement placeholders not substituted: %s", req)
	}
}

func TestCompileTaskPromptFillsDeliverablesFromNodeContracts(t *testing.T) {
	node := domain.WorkflowNode{
		ID:              "fix",
		ArtifactOutputs: []domain.ArtifactContract{{Name: "fix-report", Path: "fix-report.md"}},
	}
	spec := domain.TaskPromptSpec{Role: "修复工程师", Objective: "最小修复"}
	got := compileTaskPrompt(node, spec, []domain.VerificationGate{{Name: "修复证据门禁"}})
	if !strings.Contains(got, "fix-report.md") || !strings.Contains(got, "修复证据门禁") {
		t.Fatalf("compiled taskbook missing node contracts: %s", got)
	}
}

func TestParallelNodesDoNotOverwriteEachOther(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := testDeliveryWorkflow()
	wf.ID = "parallel"
	wf.Name = "parallel"
	wf.Nodes = append(wf.Nodes[:0], wf.Nodes[0], wf.Nodes[0], wf.Nodes[2])
	wf.Nodes[0].ID, wf.Nodes[0].Name = "left", "left"
	wf.Nodes[1].ID, wf.Nodes[1].Name = "right", "right"
	wf.Nodes[2].ID, wf.Nodes[2].Name = "join", "join"
	wf.Nodes[2].RejectTo = ""
	wf.Nodes[2].OnFailure = "fail"
	wf.Edges = []domain.WorkflowEdge{{ID: "l", Source: "left", Target: "join", When: "success"}, {ID: "r", Source: "right", Target: "join", When: "success"}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start("parallel", "parallel work")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current := waitForWorkspaceCleanup(t, s, r.ID)
	if current.SandboxID != "" || current.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("succeeded run sandbox was not cleaned: sandbox=%q cleaned=%v", current.SandboxID, current.WorkspaceCleanedAt)
	}
	sharedSandboxID := current.Nodes["left"].SandboxID
	if sharedSandboxID == "" {
		t.Fatal("nodes did not record their shared sandbox")
	}
	for _, id := range []string{"left", "right", "join"} {
		if current.Nodes[id].Status != "succeeded" {
			t.Fatalf("%s status = %s", id, current.Nodes[id].Status)
		}
		if current.Nodes[id].SandboxID != sharedSandboxID {
			t.Fatalf("%s sandbox = %q, shared sandbox = %q", id, current.Nodes[id].SandboxID, sharedSandboxID)
		}
	}
}

func testDeliveryWorkflow() domain.WorkflowDefinition {
	return domain.WorkflowDefinition{
		ID: "delivery", Name: "Delivery", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "implement", Name: "Implement", Type: "agent", AgentID: "developer", ExecutorID: "mock", MaxAttempts: 2, MaxReworks: 3},
			{ID: "verify", Name: "Verify", Type: "command", Command: "printf 'verification passed\\n'", RejectTo: "implement", MaxAttempts: 1, MaxReworks: 3},
			{ID: "review", Name: "Review", Type: "agent", AgentID: "reviewer", ExecutorID: "mock", RejectTo: "implement", MaxAttempts: 1, MaxReworks: 3},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "implement", Target: "verify", When: "success"}, {ID: "e2", Source: "verify", Target: "review", When: "success"}},
	}
}

func putTestDeliveryWorkflow(t *testing.T, s *repo.Store) {
	t.Helper()
	putGateTestAgent(t, s, "reviewer", []domain.VerificationGate{{ID: "human", Name: "Human", Mode: "manual", Required: true}})
	developer, _ := s.Agent("developer")
	developer.ExecutorID = "mock"
	developer.TaskPrompt = &domain.TaskPromptSpec{Objective: "{{input}}"}
	if err := s.PutAgent(developer); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(testDeliveryWorkflow()); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredTaskPromptCompilesTaskbook(t *testing.T) {
	node := domain.WorkflowNode{ID: "build", RetryStrategy: "targeted_fix", CarryOver: []string{"failed_checks", "last_error"}}
	spec := domain.TaskPromptSpec{
		Role: "开发工程师", Objective: "实现登录限流", Context: "仓库：{{repository}}",
		Inputs: []string{"需求：{{input}}", "证据：{{artifacts}}"}, Actions: []string{"修改实现", "运行测试"},
		Deliverables: []string{"change-report.json"}, AcceptanceCriteria: []string{"测试通过"}, Constraints: []string{"不得自动合并"}, RetryInstructions: "仅处理失败项",
	}
	run := domain.Run{Input: "每分钟最多 10 次", Repository: domain.RepositoryContext{RepositoryID: "repo", Name: "service", GongfengProjectID: "team/service", BaseBranch: "main"}, Nodes: map[string]*domain.NodeRun{"build": {Feedback: "补充边界用例"}}}
	got := renderPromptSpec(domain.WorkflowDefinition{}, node, spec, run)
	for _, want := range []string{"# 节点任务书", "开发工程师", "实现登录限流", "每分钟最多 10 次", "service (team/service), 最新远端基线 main, 本次工作分支", "修改实现", "change-report.json", "仅修复失败项", "补充边界用例"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compiled taskbook does not contain %q:\n%s", want, got)
		}
	}
}

func TestNodeFailureAutomaticallyRejectsToConfiguredTarget(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "auto-reject", Name: "auto reject", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "repair", Name: "repair", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "verify", Name: "verify", Type: "script", ScriptLanguage: "python", Script: "raise SystemExit(1)", MaxAttempts: 1, MaxReworks: 1, OnFailure: "reject", RejectTo: "repair"},
	}, Edges: []domain.WorkflowEdge{{ID: "next", Source: "repair", Target: "verify", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "failed")
	current, _ := s.Run(r.ID)
	if current.Nodes["repair"].ReworkCount != 1 || current.Nodes["repair"].Attempt != 2 {
		t.Fatalf("repair was not reworked once: %+v", current.Nodes["repair"])
	}
	found := false
	for _, event := range current.Events {
		if event.Type == "gate.rejected" && strings.Contains(event.Message, "节点失败自动打回") {
			found = true
		}
	}
	if !found {
		t.Fatalf("automatic rejection event missing: %+v", current.Events)
	}
}

func TestPassFailedScriptContinuesDownstream(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "pass-fail-stop", Name: "pass fail stop", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "hard", Name: "hard", Type: "script", ScriptLanguage: "shell", Script: "exit 1", OnFailure: "fail", MaxAttempts: 1},
		{ID: "next", Name: "next", Type: "script", ScriptLanguage: "shell", Script: "printf 'ok\\n'", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "hard", Target: "next", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "failed")
	before, _ := s.Run(r.ID)
	if before.Nodes["hard"].Status != "failed" {
		t.Fatalf("hard status = %s", before.Nodes["hard"].Status)
	}
	attempt := before.Nodes["hard"].Attempt
	if err := e.Pass(r.ID, "hard", "人工确认脚本误报"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Nodes["hard"].Status != "succeeded" {
		t.Fatalf("hard after pass = %+v", current.Nodes["hard"])
	}
	if current.Nodes["hard"].Attempt != attempt {
		t.Fatalf("pass must not re-execute the failed node: attempt %d -> %d", attempt, current.Nodes["hard"].Attempt)
	}
	if current.Nodes["next"].Status != "succeeded" {
		t.Fatalf("downstream should continue, next=%+v", current.Nodes["next"])
	}
	if current.HumanContext != "人工确认脚本误报" {
		t.Fatalf("humanContext = %q", current.HumanContext)
	}
}

func TestPassAfterReworkLimitReached(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "pass-rework-limit", Name: "pass rework limit", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "repair", Name: "repair", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "verify", Name: "verify", Type: "script", ScriptLanguage: "shell", Script: "exit 1", MaxAttempts: 1, MaxReworks: 1, OnFailure: "reject", RejectTo: "repair"},
		{ID: "done", Name: "done", Type: "script", ScriptLanguage: "shell", Script: "printf 'done\\n'", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{
		{ID: "e1", Source: "repair", Target: "verify", When: "success"},
		{ID: "e2", Source: "verify", Target: "done", When: "success"},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "failed")
	current, _ := s.Run(r.ID)
	if current.Nodes["verify"].Status != "failed" {
		t.Fatalf("verify status = %s error=%s", current.Nodes["verify"].Status, current.Nodes["verify"].Error)
	}
	if err := e.Pass(r.ID, "verify", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	final, _ := s.Run(r.ID)
	if final.Nodes["done"].Status != "succeeded" {
		t.Fatalf("done = %+v", final.Nodes["done"])
	}
}

func TestPassRejectsAgentNode(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-pass-agent", WorkflowID: "wf", Status: "failed", CreatedAt: now, UpdatedAt: now,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:    "wf",
			Nodes: []domain.WorkflowNode{{ID: "agent", Name: "提交工蜂 MR", Type: "agent"}},
		},
		Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "failed"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	if err := e.Pass(run.ID, "agent", "补充信息"); err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("Pass error = %v, want script-only", err)
	}
}

func TestPassRejectsActiveRun(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	run := domain.Run{ID: "run-pass-active", WorkflowID: "wf", Status: "waiting", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	if err := e.Pass(run.ID, "gate", ""); err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("Pass error = %v, want still active", err)
	}
}

func TestArtifactsAreVersionedAndMaterializedForDownstreamNode(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "artifacts", Name: "artifact handoff", Version: 1, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "produce", Name: "produce", Type: "command", Command: "mkdir -p docs && printf '# Plan\\n\\naccepted\\n' > docs/plan.md", ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "docs/plan.md", MediaType: "text/markdown", Required: true}}, MaxAttempts: 1},
			{ID: "consume", Name: "consume", Type: "command", Command: "cat .agentflow/inputs/produce/plan.md", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "handoff", Source: "produce", Target: "consume", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if len(current.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(current.Artifacts))
	}
	if current.Artifacts[0].Name != "plan" || !strings.Contains(current.Artifacts[0].Content, "accepted") {
		t.Fatalf("unexpected producer artifact: %#v", current.Artifacts[0])
	}
	if current.Artifacts[1].Name != "result" || !strings.Contains(current.Artifacts[1].Content, "accepted") {
		t.Fatalf("downstream did not consume artifact: %#v", current.Artifacts[1])
	}
}

func TestRepositoryPatchIsFrozenAndAppliedToDownstreamSandbox(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "repository-handoff", Name: "repository handoff", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "change", Name: "change", Type: "command", Command: "printf changed > repo/app.txt; printf '{}\\n' > report.json; mkdir -p repo/config; printf 'Bearer secret-token\\n' > repo/config/mcporter.json", ArtifactOutputs: []domain.ArtifactContract{{Name: "report", Path: "report.json", MediaType: "application/json", Required: true}}, MaxAttempts: 1},
		{ID: "verify", Name: "verify", Type: "command", Command: "test \"$(cat repo/app.txt)\" = changed", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "handoff", Source: "change", Target: "verify", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.StartWithContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "repo", Name: "repo", LocalPath: source, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Nodes["verify"].Status != "succeeded" {
		t.Fatalf("downstream did not receive repository patch: %+v", current.Nodes["verify"])
	}
	found := false
	for _, artifact := range current.Artifacts {
		if artifact.NodeID == "change" && artifact.Name == "repository-patch" && strings.Contains(artifact.Content, "app.txt") {
			found = true
			if strings.Contains(artifact.Content, "secret-token") || strings.Contains(artifact.Content, "mcporter.json") {
				t.Fatalf("executor MCP config leaked into repository patch: %s", artifact.Content)
			}
		}
	}
	if !found {
		t.Fatal("repository patch was not frozen as an immutable artifact")
	}
}

func TestSingleRepositoryKeepsDeliverablesAtRunRoot(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "single-layout", Name: "single layout", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "analyze", Name: "analyze", Type: "command", Command: "printf '# plan\\nroot cause\\n' > fix-plan.md; printf changed\\n > repo/app.txt; test -f fix-plan.md && test ! -f repo/fix-plan.md && test ! -e .git && test -d repo/.git", ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "fix-plan.md", Required: true}}, MaxAttempts: 1},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.StartWithContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "repo", Name: "repo", LocalPath: source, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Repository.WorkspaceDir != "repo" {
		t.Fatalf("WorkspaceDir = %q, want repo", current.Repository.WorkspaceDir)
	}
	foundPlan, foundPatch := false, false
	for _, artifact := range current.Artifacts {
		if artifact.Name == "plan" {
			foundPlan = true
			if artifact.RepositoryDir != "" {
				t.Fatalf("deliverable must be collected from the run root, RepositoryDir=%q", artifact.RepositoryDir)
			}
			if !strings.Contains(artifact.Content, "root cause") {
				t.Fatalf("plan artifact = %q", artifact.Content)
			}
		}
		if artifact.Name == "repository-patch" {
			foundPatch = true
			if artifact.RepositoryDir != "repo" {
				t.Fatalf("patch RepositoryDir = %q, want repo", artifact.RepositoryDir)
			}
			if !strings.Contains(artifact.Content, "app.txt") {
				t.Fatalf("repository patch missing code change: %s", artifact.Content)
			}
			if strings.Contains(artifact.Content, "fix-plan") {
				t.Fatalf("deliverable leaked into repository patch: %s", artifact.Content)
			}
		}
	}
	if !foundPlan {
		t.Fatal("fix-plan.md was not collected as a run-level artifact")
	}
	if !foundPatch {
		t.Fatal("repository patch was not collected from the checkout subdirectory")
	}
}

func TestAutomaticGatesAndArtifactsCompleteBeforeManualApproval(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "artifact-validator", []domain.VerificationGate{
		{ID: "human-first", Name: "Human", Mode: "manual", Required: true},
		{ID: "automatic-after", Name: "Automatic", Mode: "command", Commands: []string{"test -s evidence.md"}, Required: true, ExecutionScope: domain.ExecutionScopeRun},
	})
	wf := domain.WorkflowDefinition{ID: "gated-artifact", Name: "gated artifact", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "prepare", Name: "prepare", Type: "command", Command: "printf '# Evidence\\n' > evidence.md", MaxAttempts: 1},
		{ID: "deliver", Name: "deliver", Type: "agent", AgentID: "artifact-validator", ExecutorID: "mock",
			ArtifactOutputs: []domain.ArtifactContract{{Name: "evidence", Path: "evidence.md", Required: true}},
			MaxAttempts:     1,
		},
	}, Edges: []domain.WorkflowEdge{{ID: "prepare-deliver", Source: "prepare", Target: "deliver", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	evidenceFrozen := false
	for _, artifact := range current.Artifacts {
		if artifact.NodeID == "deliver" && artifact.Name == "evidence" {
			evidenceFrozen = true
			break
		}
	}
	if !evidenceFrozen {
		t.Fatalf("artifact was not frozen before approval: %#v", current.Artifacts)
	}
	var artifactValidatedSequence, gateStartedSequence int64
	for _, event := range current.Telemetry {
		if event.Category == "artifact" && event.Name == "artifact.contract.validated" {
			artifactValidatedSequence = event.Sequence
		}
		if event.Category == "verification" && event.Name == "process.started" {
			gateStartedSequence = event.Sequence
		}
	}
	if artifactValidatedSequence == 0 || gateStartedSequence == 0 || artifactValidatedSequence >= gateStartedSequence {
		t.Fatalf("artifact validation must precede the automatic gate: artifact=%d gate=%d", artifactValidatedSequence, gateStartedSequence)
	}
	if err := e.Approve(r.ID, "deliver", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
}

func TestArtifactContractIsValidatedBeforeAutomaticGate(t *testing.T) {
	dir := t.TempDir()
	gateMarker := filepath.Join(dir, "gate-ran")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "missing-artifact-validator", []domain.VerificationGate{{
		ID: "marker", Name: "marker", Mode: "command", Commands: []string{"touch " + shellSingleQuote(gateMarker)}, Required: true, ExecutionScope: domain.ExecutionScopeRun,
	}})
	wf := domain.WorkflowDefinition{ID: "artifact-before-gate", Name: "artifact before gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "deliver", Name: "deliver", Type: "agent", AgentID: "missing-artifact-validator", ExecutorID: "mock",
		ArtifactOutputs: []domain.ArtifactContract{{Name: "evidence", Path: "evidence.md", Required: true}},
		MaxAttempts:     1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "failed")
	if _, err := os.Stat(gateMarker); !os.IsNotExist(err) {
		t.Fatalf("automatic gate ran before artifact validation: %v", err)
	}
	current, _ := s.Run(r.ID)
	if node := current.Nodes["deliver"]; node == nil || !strings.Contains(node.Error, "artifact contract") {
		t.Fatalf("node did not fail on its artifact contract: %+v", node)
	}
}

// TestVerificationGateRunsMultiLineCommandsAsOneScript guards against a
// regression where each entry in a gate's Commands slice was executed as an
// independent `sh -lc` invocation. The web UI splits a gate's script editor
// content on "\n" into that slice (see applyGateField in web/app.js), so a
// single multi-line construct such as this if/else block is represented as
// five separate array entries. Running each line on its own used to fail
// with a confusing shell syntax error (e.g. "unexpected end of file" for the
// "if ...; then" line on its own); the gate must instead join them back into
// one script before executing.
func TestVerificationGateRunsMultiLineCommandsAsOneScript(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "multiline-validator", []domain.VerificationGate{{
		ID: "multiline", Name: "Multiline", Mode: "command", Required: true,
		ExecutionScope: domain.ExecutionScopeRun,
		Commands: []string{
			"if [ -s evidence.md ]; then",
			"  echo evidence-ok",
			"else",
			"  exit 1",
			"fi",
		},
	}})
	wf := domain.WorkflowDefinition{ID: "multiline-gate", Name: "multiline gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "prepare", Name: "prepare", Type: "command", Command: "printf '# Evidence\\n' > evidence.md", MaxAttempts: 1},
		{ID: "deliver", Name: "deliver", Type: "agent", AgentID: "multiline-validator", ExecutorID: "mock", ArtifactOutputs: []domain.ArtifactContract{{Name: "evidence", Path: "evidence.md", Required: true}}, MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "prepare-deliver", Source: "prepare", Target: "deliver", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	for _, event := range current.Telemetry {
		if event.Category == "verification" && event.Level == "error" {
			t.Fatalf("multi-line gate command failed instead of running as one script: %s", event.Message)
		}
	}
}

func TestRunUsesFrozenWorkflowSnapshotDuringConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "snapshot", Name: "Snapshot", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "approve", Name: "Approve", Type: "approval", MaxAttempts: 1},
		{ID: "original", Name: "Original", Type: "command", Command: "printf original", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "next", Source: "approve", Target: "original", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	edited, _ := s.Workflow(wf.ID)
	edited.Nodes = edited.Nodes[:1]
	edited.Edges = nil
	if err := s.PutWorkflow(edited); err != nil {
		t.Fatal(err)
	}
	if err := e.Approve(r.ID, "approve", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.WorkflowSnapshot.ID != wf.ID || current.Nodes["original"].Output != "original\n" {
		t.Fatalf("run was changed by concurrent workflow edit: %#v", current)
	}
}

func TestRetryLegacyRunDefaultsEmptySandboxProfileToLocal(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutSandbox(domain.SandboxProfile{ID: "profile-added-later", Name: "Profile added later", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "legacy-sandbox-retry", Name: "Legacy sandbox retry", SandboxProfileID: "profile-added-later",
		Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", Type: "command", Command: "printf resumed", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-legacy-sandbox-retry", WorkflowID: wf.ID,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: wf.ID, Name: wf.Name, Nodes: append([]domain.WorkflowNode(nil), wf.Nodes...),
		},
		Status: "failed",
		Nodes: map[string]*domain.NodeRun{
			"build": {NodeID: "build", Status: "failed", Attempt: 1, Error: "historical failure"},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if got := workflowForRun(s, run).SandboxProfileID; got != "local" {
		t.Fatalf("legacy run sandbox profile = %q, want local", got)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	if err := e.Retry(run.ID, "build", "兼容历史运行", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	current, _ := s.Run(run.ID)
	if got := current.Nodes["build"].Output; got != "resumed\n" {
		t.Fatalf("legacy retry output = %q, want resumed", got)
	}
}

func TestStartDefaultsEmptySandboxProfileToLocal(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{
		ID: "default-local-sandbox", Name: "Default local sandbox",
		Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", Type: "command", Command: "printf local", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	current, _ := s.Run(run.ID)
	if current.WorkflowSnapshot.SandboxProfileID != "local" {
		t.Fatalf("run sandbox profile = %q, want local", current.WorkflowSnapshot.SandboxProfileID)
	}
}

func TestParallelProgressTelemetryCoalescesPerNode(t *testing.T) {
	base := time.Now().UTC()
	events := []domain.TelemetryEvent{}
	for sequence := int64(1); sequence <= 100; sequence++ {
		nodeID := "left"
		if sequence%2 == 0 {
			nodeID = "right"
		}
		observation := domain.TelemetryEvent{NodeID: nodeID, Sequence: sequence, Timestamp: base.Add(time.Duration(sequence) * time.Millisecond), Category: "reasoning", Name: "agent.reasoning", Phase: "progress", Level: "info", Message: "x"}
		if !coalesceProgressTelemetry(&events, observation) {
			events = append(events, observation)
		}
	}
	if len(events) != 2 {
		t.Fatalf("parallel token streams produced %d events, want 2", len(events))
	}
	for _, event := range events {
		if len(event.Message) != 50 {
			t.Fatalf("node %s message length = %d, want 50", event.NodeID, len(event.Message))
		}
	}
}

func waitFor(t *testing.T, s *repo.Store, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := s.Run(id)
		if r.Status == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	r, _ := s.Run(id)
	details := map[string]string{}
	for nodeID, node := range r.Nodes {
		details[nodeID] = node.Status + ": " + node.Error
	}
	t.Fatalf("run status = %s, want %s; nodes=%+v", r.Status, want, details)
}

func waitForWorkspaceCleanup(t *testing.T, s *repo.Store, id string) domain.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := s.Run(id)
		if !r.WorkspaceCleanedAt.IsZero() {
			return r
		}
		time.Sleep(25 * time.Millisecond)
	}
	r, _ := s.Run(id)
	t.Fatalf("run %s workspace cleanup was not recorded", id)
	return r
}

func TestRecoverMisplacedArtifactsCopiesHostLeakIntoSandbox(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "skillhub")
	workspace := filepath.Join(root, "sandbox")
	if err := os.MkdirAll(host, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", host}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	body := strings.Repeat("plan line\n", 20)
	if err := os.WriteFile(filepath.Join(host, "fix-plan.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ArtifactOutputs: []domain.ArtifactContract{{Name: "fix-plan", Path: "fix-plan.md", Required: true}}}
	got := recoverMisplacedArtifacts(workspace, host, node)
	if len(got) != 1 {
		t.Fatalf("recovered = %v, want one entry", got)
	}
	copied, err := os.ReadFile(filepath.Join(workspace, "fix-plan.md"))
	if err != nil || string(copied) != body {
		t.Fatalf("sandbox artifact = %q err=%v", copied, err)
	}
	if _, err := os.Stat(filepath.Join(host, "fix-plan.md")); !os.IsNotExist(err) {
		t.Fatalf("untracked host leak was not removed: %v", err)
	}
}

func TestResolveNodeExecutorPrefersAgentModel(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agent := domain.AgentDefinition{ID: "analyst", Name: "Analyst", ExecutorID: "codebuddy", Model: "agent-model"}
	got, err := resolveNodeExecutor(s, domain.WorkflowNode{AgentID: "analyst", ExecutorID: "mock"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "codebuddy" {
		t.Fatalf("executor = %s, want codebuddy from the agent", got.ID)
	}
	if got.Model != "agent-model" {
		t.Fatalf("model = %s, want agent override", got.Model)
	}
	agentEffort := domain.AgentDefinition{ID: "analyst", Name: "Analyst", ExecutorID: "codex", Effort: "xhigh"}
	withEffort, err := resolveNodeExecutor(s, domain.WorkflowNode{AgentID: "analyst"}, agentEffort)
	if err != nil {
		t.Fatal(err)
	}
	if withEffort.Effort != "xhigh" {
		t.Fatalf("effort = %q, want agent effort propagated", withEffort.Effort)
	}
	if err := s.PutExecutor(domain.ExecutorDefinition{ID: "legacy-codex", Name: "Legacy Codex", Kind: "codex", Binary: "custom-codex", Args: []string{"unsafe"}, Model: "legacy-model"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := resolveNodeExecutor(s, domain.WorkflowNode{ExecutorID: "legacy-codex"}, domain.AgentDefinition{ID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ID != "codex" || legacy.Binary != "codex" || legacy.Model != "legacy-model" {
		t.Fatalf("legacy executor was not safely mapped to the built-in CLI: %+v", legacy)
	}
	_, err = resolveNodeExecutor(s, domain.WorkflowNode{ExecutorID: "custom"}, domain.AgentDefinition{ID: "legacy"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("custom executor must be rejected: %v", err)
	}
}

func TestResolveSandboxResourcesRequiresDockerfileForDocker(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := resolveSandboxResources(s, domain.SandboxProfile{ID: "container", Driver: "docker"}); err == nil {
		t.Fatal("docker profile without dockerfile should fail")
	}
	df := domain.DockerfileDefinition{ID: "runtime", Name: "Runtime", Content: "FROM debian:bookworm-slim"}
	if err := s.PutDockerfile(df); err != nil {
		t.Fatal(err)
	}
	resources, err := resolveSandboxResources(s, domain.SandboxProfile{ID: "container", Driver: "docker", DockerfileID: df.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resources.Dockerfile.Content) == "" || resources.Dockerfile.ID != df.ID {
		t.Fatalf("expected catalog dockerfile, got %+v", resources.Dockerfile)
	}
	env := domain.EnvironmentDefinition{
		ID:            "jdk",
		Name:          "JDK",
		Variables:     map[string]string{"JAVA_HOME": "${PREFIX}/jdk"},
		PathEntries:   []string{"bin", "jdk/bin"},
		InstallScript: "mkdir -p \"$PREFIX/bin\"",
	}
	if err := s.PutEnvironment(env); err != nil {
		t.Fatal(err)
	}
	resources, err = resolveSandboxResources(s, domain.SandboxProfile{ID: "mac", Driver: "seatbelt", EnvironmentID: env.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resources.Environment.ID != env.ID || resources.Environment.Variables["JAVA_HOME"] == "" || strings.TrimSpace(resources.Environment.InstallScript) == "" {
		t.Fatalf("expected catalog environment, got %+v", resources.Environment)
	}
}

func TestOverlayLatestNodeBindingsRefreshesArtifactContracts(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	current := domain.WorkflowDefinition{
		ID: "issue-fix-production", Name: "Issue fix", SandboxProfileID: "local", Version: 2,
		Nodes: []domain.WorkflowNode{{ID: "checkers", Name: "Checkers", Type: "command", ArtifactOutputs: []domain.ArtifactContract{{Name: "strict", Path: "checkers-report.md", Required: true}}}},
	}
	if err := s.PutWorkflow(current); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{WorkflowID: "issue-fix-production", WorkflowSnapshot: domain.WorkflowDefinition{
		ID: "issue-fix-production", Version: 1,
		Nodes: []domain.WorkflowNode{{ID: "checkers", ArtifactOutputs: []domain.ArtifactContract{{Name: "legacy", Path: "legacy-report.md"}}}},
	}}
	overlayLatestNodeBindings(s, &run, map[string]bool{"checkers": true})
	path := ""
	for _, n := range run.WorkflowSnapshot.Nodes {
		if n.ID == "checkers" && len(n.ArtifactOutputs) > 0 {
			path = n.ArtifactOutputs[0].Path
		}
	}
	if path != "checkers-report.md" {
		t.Fatalf("retry overlay did not refresh artifact contract: %s", path)
	}
}

func TestOverlayLatestNodeBindingsReadsGenericTemplate(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := repo.SeedTestGenericTemplates(s); err != nil {
		t.Fatal(err)
	}
	template, ok := s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	if !ok {
		t.Fatal("generic template missing")
	}
	template.Workflow.Nodes = append([]domain.WorkflowNode(nil), template.Workflow.Nodes...)
	for i := range template.Workflow.Nodes {
		if template.Workflow.Nodes[i].ID == "implement" {
			template.Workflow.Nodes[i].RejectTo = "design"
		}
	}
	if err := s.PutGenericTemplate(template); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		WorkflowID: repo.GenericBackendDeliveryWorkflowID,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: repo.GenericBackendDeliveryWorkflowID, Generic: true,
			Nodes: []domain.WorkflowNode{{ID: "implement", RejectTo: ""}},
		},
	}
	overlayLatestNodeBindings(s, &run, map[string]bool{"implement": true})
	if run.WorkflowSnapshot.Nodes[0].RejectTo != "design" {
		t.Fatalf("generic overlay missed stored template: %+v", run.WorkflowSnapshot.Nodes[0])
	}
}

func TestShouldRecordLogSkipsModelGeneration(t *testing.T) {
	if shouldRecordLog(domain.TelemetryEvent{Category: "model", Name: "model.request", Message: "model generation started"}) {
		t.Fatal("model generation should stay in telemetry, not worker logs")
	}
	if shouldRecordLog(domain.TelemetryEvent{Category: "model", Name: "model.request", Message: "model generation finished"}) {
		t.Fatal("model generation finished should not be a worker log")
	}
	if !shouldRecordLog(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "start", Message: "Write fix-plan.md"}) {
		t.Fatal("tool starts should remain in worker logs")
	}
	if shouldRecordLog(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Message: "Read 完成"}) {
		t.Fatal("successful tool finishes are heartbeat noise")
	}
	if shouldRecordLog(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Message: "工具完成"}) {
		t.Fatal("generic tool-complete lines are heartbeat noise")
	}
	if !shouldRecordLog(domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Level: "error", Message: "Bash 失败: signal: killed"}) {
		t.Fatal("failed tool finishes should stay in worker logs")
	}
}

func TestDockerImageBuildNotice(t *testing.T) {
	if dockerImageBuildLabel(nil) != "沙箱镜像" {
		t.Fatalf("empty snap label = %q", dockerImageBuildLabel(nil))
	}
	snap := &domain.RunnerSandboxSpec{Dockerfile: &domain.DockerfileDefinition{ID: "skillhub-backend", Name: "Skillhub Backend", Content: "FROM debian"}}
	if got := dockerImageBuildLabel(snap); got != "Skillhub Backend" {
		t.Fatalf("named label = %q", got)
	}
	if !dockerImageNeedsBuild(context.Background(), nil) {
		t.Fatal("missing snapshot must look like a first build")
	}
	if !dockerImageNeedsBuild(context.Background(), snap) {
		t.Fatal("unknown image tag must look like a first build")
	}
	if formatElapsedCN(12*time.Second) != "12 秒" || formatElapsedCN(2*time.Minute) != "2 分钟" || formatElapsedCN(2*time.Minute+15*time.Second) != "2 分 15 秒" {
		t.Fatalf("elapsed = %q / %q / %q", formatElapsedCN(12*time.Second), formatElapsedCN(2*time.Minute), formatElapsedCN(2*time.Minute+15*time.Second))
	}
	if !shouldRecordLog(domain.TelemetryEvent{Category: "sandbox", Name: "sandbox.image.building", Message: "首次运行：正在构建沙箱镜像（Skillhub Backend），通常需要数分钟，完成后才会开始执行"}) {
		t.Fatal("image-build notice must appear in realtime logs")
	}
}

func TestRepositoryPatchHandoffIsFilenameSummary(t *testing.T) {
	diff := []byte("diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n@@ -1,2 +1,3 @@\n package app\n+func Added() {}\n")
	summary := summarizeRepositoryPatch(diff)
	if !strings.Contains(summary, "app.go") || !strings.Contains(summary, "files 1") {
		t.Fatalf("summary = %q", summary)
	}
	if strings.Contains(summary, "func Added") || strings.Contains(summary, "@@") {
		t.Fatalf("summary inlined hunk body: %q", summary)
	}
	art := domain.Artifact{Name: "repository-patch", Content: summary}
	if got := artifactPromptBody(art); got != summary {
		t.Fatalf("prompt body = %q", got)
	}
	legacy := domain.Artifact{Name: "repository-patch", Content: string(diff)}
	if got := artifactPromptBody(legacy); strings.Contains(got, "func Added") {
		t.Fatalf("legacy prompt still inlined hunk: %q", got)
	}
	prompt := renderPromptSpec(domain.WorkflowDefinition{}, domain.WorkflowNode{}, domain.TaskPromptSpec{Objective: "see {{artifact.code.repository-patch}}"}, domain.Run{
		Artifacts: []domain.Artifact{{NodeID: "code", Name: "repository-patch", Content: string(diff)}},
	})
	if strings.Contains(prompt, "func Added") || strings.Contains(prompt, "@@") {
		t.Fatalf("rendered prompt inlined full patch: %s", prompt)
	}
	if !strings.Contains(prompt, "app.go") {
		t.Fatalf("rendered prompt missing filename: %s", prompt)
	}
}

func TestCollectArtifactsBestEffortKeepsWrittenReport(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "comments-report.md"), []byte("# comments\nUNRESOLVED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run", Nodes: map[string]*domain.NodeRun{"comments": {NodeID: "comments", Attempt: 3}}}
	node := domain.WorkflowNode{ID: "comments", ArtifactOutputs: []domain.ArtifactContract{{Name: "comments-report", Path: "comments-report.md", MediaType: "text/markdown", Required: true}}}
	got, err := collectArtifactsBestEffort(workspace, run, node, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "comments-report" {
		t.Fatalf("best-effort artifacts = %#v", got)
	}
}

func TestCollectRepositoryPatchSkipsQuotedAgentflowModuleCache(t *testing.T) {
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "app.go"}, {"commit", "-m", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "agentflow-baseline"), bytes.TrimSpace(head), 0o600); err != nil {
		t.Fatal(err)
	}
	modDoc := filepath.Join(workspace, ".agentflow", "private-home", "codebuddy", "go", "pkg", "mod", "github.com", "tjfoc", "gmsm@v1.4.1", "API使用说明.md")
	if err := os.MkdirAll(filepath.Dir(modDoc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modDoc, []byte("# gmsm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e2e := filepath.Join(workspace, "handler_skill_frontmatter_e2e_test.go")
	if err := os.WriteFile(e2e, []byte("package httpapi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := collectRepositoryPatch(workspace)
	if err != nil {
		t.Fatalf("collectRepositoryPatch: %v", err)
	}
	text := string(patch)
	if !strings.Contains(text, "handler_skill_frontmatter_e2e_test.go") {
		t.Fatalf("patch missing untracked test file:\n%s", text)
	}
	if strings.Contains(text, "使用说明") || strings.Contains(text, "gmsm") || strings.Contains(text, ".agentflow") {
		t.Fatalf("module cache leaked into repository patch:\n%s", text)
	}
}

func TestCollectRepositoryPatchUsesNearerUpstreamAfterMerge(t *testing.T) {
	root := t.TempDir()
	source, remote, work := filepath.Join(root, "source"), filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "app.go"}, {"commit", "-m", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "-C", source, "branch", "agentflow/integration").CombinedOutput(); err != nil {
		t.Fatalf("branch integrate: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "clone", remote, work).CombinedOutput(); err != nil {
		t.Fatalf("clone work: %v: %s", err, output)
	}
	for _, args := range [][]string{{"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	oldBase, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".git", "agentflow-baseline"), bytes.TrimSpace(oldBase), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(source, "upstream.go"), []byte("package app\nfunc Upstream() {}\n"+strings.Repeat("// padding\n", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", source, "checkout", "agentflow/integration").CombinedOutput(); err != nil {
		t.Fatalf("checkout integrate: %v: %s", err, output)
	}
	for _, args := range [][]string{{"add", "upstream.go"}, {"commit", "-m", "large integrate update"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "-C", source, "push", remote, "agentflow/integration").CombinedOutput(); err != nil {
		t.Fatalf("push integrate: %v: %s", err, output)
	}

	if output, err := exec.Command("git", "-C", work, "checkout", "-b", "feature/issue").CombinedOutput(); err != nil {
		t.Fatalf("work branch: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(work, "fix.go"), []byte("package app\nfunc Fix() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "fix.go"}, {"commit", "-m", "issue fix"}} {
		if output, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "-C", work, "fetch", "origin", "+refs/heads/agentflow/integration:refs/remotes/origin/agentflow/integration").CombinedOutput(); err != nil {
		t.Fatalf("fetch integrate: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", work, "merge", "--no-edit", "-m", "merge integrate", "origin/agentflow/integration").CombinedOutput(); err != nil {
		t.Fatalf("merge integrate: %v: %s", err, output)
	}

	patch, err := collectRepositoryPatch(work)
	if err != nil {
		t.Fatalf("collectRepositoryPatch: %v", err)
	}
	text := string(patch)
	if !strings.Contains(text, "fix.go") {
		t.Fatalf("patch missing issue change:\n%s", text)
	}
	if strings.Contains(text, "func Upstream") || strings.Contains(text, "padding") {
		t.Fatalf("patch included already-merged integrate history:\n%s", text)
	}
}

func TestUnquoteGitLsFilesPathDecodesOctalChineseName(t *testing.T) {
	want := ".agentflow/private-home/codebuddy/go/pkg/mod/github.com/tjfoc/gmsm@v1.4.1/API使用说明.md"
	raw := quoteGitCStyle(want)
	got := gitwork.UnquoteLsFilesPath(raw)
	if got != want {
		t.Fatalf("unquote(%q) = %q, want %q", raw, got, want)
	}
	if !gitwork.Excluded(raw) {
		t.Fatal("quoted module-cache path must still be excluded from the patch")
	}
}

func TestExcludeFromRepositoryPatchDependencies(t *testing.T) {
	excluded := []string{
		// Go
		"vendor/github.com/gin-gonic/gin/context.go",
		"backend/vendor/modules.txt",
		"pkg/mod/github.com/foo@v1.0.0/foo.go",
		// Frontend
		"node_modules/express/index.js",
		"web/node_modules/vue/package.json",
		"web/node-modules/vue/package.json",
		"frontend/node-modules/react/index.js",
		".pnpm-store/v3/files/xx/yy",
		".yarn/cache/foo.zip",
		"bower_components/jquery/jquery.js",
		".turbo/cache/hash.tar.zst",
		".next/cache/webpack.json",
		".nuxt/dist/server/server.mjs",
		".output/server/index.mjs",
		// PHP
		"vendor/composer/autoload_real.php",
		"vendor/laravel/framework/src/Illuminate/Foundation/Application.php",
		// Python
		"venv/lib/python3.10/site-packages/pip/__init__.py",
		".venv/bin/activate",
		"env/lib/python3.9/site-packages/requests/__init__.py",
		"site-packages/urllib3/__init__.py",
		"dist-packages/six.py",
		"__pycache__/app.cpython-310.pyc",
		"pkg/__pycache__/utils.cpython-311.pyc",
		"app.pyc",
		"pkg.egg-info/PKG-INFO",
		".pytest_cache/v/cache/nodeids",
		".mypy_cache/3.10/builtins.data.json",
		".ruff_cache/content/hash",
		".tox/py310/bin/python",
		// Java & JVM
		"target/classes/com/example/Main.class",
		"target/dependency/commons-lang3-3.12.0.jar",
		"submodule/target/app-1.0.0.jar",
		".gradle/caches/modules-2/files-2.1/hash.jar",
		".m2/repository/org/springframework/spring-core/5.3.0/spring-core.jar",
		"app.class",
		"app.jar",
		"app.war",
		// Rust
		"target/debug/deps/libfoo.rlib",
		"target/release/build/app/build.rs",
		"crates/core/target/debug/deps/libcore.rlib",
		"target/debug/deps/libbar.rmeta",
		// C / C++
		"vcpkg_installed/x64-linux/lib/libboost.a",
		".conan/data/pkg/1.0.0/user/channel/package/hash/lib/libpkg.a",
		"CMakeFiles/3.20.0/CMakeCCompiler.cmake",
		"cmake-build-debug/main.o",
		"cmake-build-release/app.exe",
		".deps/foo.Plo",
		".libs/libfoo.so",
		"main.o",
		"main.obj",
		"libfoo.so",
		"libfoo.dylib",
		"app.dll",
		"app.exe",
		"app.pdb",
		"header.pch",
		"header.gch",
		// Swift / iOS / macOS
		"Pods/AFNetworking/AFNetworking.m",
		"Carthage/Build/iOS/Alamofire.framework/Alamofire",
		"DerivedData/App/Build/Products/Debug/App.app",
		".build/x86_64-apple-macosx/debug/App",
		".swiftpm/configuration/registries.json",
		"MyLib.framework/MyLib",
		"MyLib.xcframework/ios-arm64/MyLib.framework/MyLib",
		"App.app.dSYM/Contents/Info.plist",
		// Android
		".cxx/Debug/hash/arm64-v8a/android_gradle_build.json",
		"app.aar",
		"app.apk",
		"app.aab",
		"classes.dex",
		// .NET / C#
		"obj/Debug/net8.0/app.dll",
		"obj/project.assets.json",
		".nuget/packages/newtonsoft.json/13.0.1/newtonsoft.json.nupkg",
		"app.nupkg",
		// Ruby
		".bundle/config",
		"app.gem",
		// Elixir / Erlang
		"deps/phoenix/mix.exs",
		"_build/dev/lib/phoenix/ebin/phoenix.beam",
		".rebar3/rebar.lock",
		"app.beam",
		// Dart / Flutter
		".dart_tool/package_config.json",
		// Terraform / IaC
		".terraform/providers/registry.terraform.io/hashicorp/aws/5.0.0/terraform-provider-aws",
		".terragrunt-cache/hash/main.tf",
		// Internal & SOP reports
		".agentflow/temp.txt",
		".codebuddy/rules.json",
		"fix-plan.md",
		"test-report.md",
	}
	for _, p := range excluded {
		if !gitwork.Excluded(p) {
			t.Errorf("expected %q to be excluded from repository patch", p)
		}
	}

	included := []string{
		"vendor.go",
		"src/vendor.go",
		"pkg/vendor_helper.go",
		"node_modules_test.js",
		"web/node_modules_spec.ts",
		"main.go",
		"package.json",
		"go.mod",
		"go.sum",
		"scripts/build.sh",
		"Main.java",
		"src/main/java/com/example/App.java",
		"index.php",
		"app.py",
		"lib.rs",
		"Cargo.toml",
		"pom.xml",
		"build.gradle",
		"composer.json",
		"main.cpp",
		"main.h",
		"CMakeLists.txt",
		"Program.cs",
		"AppDelegate.swift",
		"app.rb",
		"mix.exs",
		"main.tf",
	}
	for _, p := range included {
		if gitwork.Excluded(p) {
			t.Errorf("expected %q NOT to be excluded from repository patch", p)
		}
	}
}

func TestCollectRepositoryPatchExcludesMultiLanguageDependencies(t *testing.T) {
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	// Initial real source files across Go, Java, PHP, Python, Rust, Frontend, C/C++, C#, Swift, Ruby
	sources := map[string]string{
		"main.go":                     "package main\n",
		"Main.java":                   "public class Main {}\n",
		"index.php":                   "<?php echo 'hello';\n",
		"app.py":                      "print('hello')\n",
		"lib.rs":                      "pub fn hello() {}\n",
		"web/index.ts":                "console.log('hello');\n",
		"main.cpp":                    "#include <iostream>\n",
		"Program.cs":                  "class Program {}\n",
		"AppDelegate.swift":           "import UIKit\n",
		"app.rb":                      "puts 'hello'\n",
		"vendor/dep/dep.go":           "package dep\n",
		"web/node_modules/dep/dep.js": "module.exports={};\n",
		"target/classes/Main.class":   "classbytes",
		"coverage_clover.xml":         "<xml>old coverage</xml>\n",
		".phpunit.result.cache":       "old cache",
	}
	for rel, content := range sources {
		full := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "agentflow-baseline"), bytes.TrimSpace(head), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Modify real source files
	realMods := map[string]string{
		"main.go":           "package main\n// modified go\n",
		"Main.java":         "public class Main { int x; }\n",
		"index.php":         "<?php echo 'modified php';\n",
		"app.py":            "print('modified python')\n",
		"lib.rs":            "pub fn hello() { let _ = 1; }\n",
		"web/index.ts":      "console.log('modified ts');\n",
		"main.cpp":          "#include <iostream>\n// modified cpp\n",
		"Program.cs":        "class Program { int a; }\n",
		"AppDelegate.swift": "import UIKit\n// modified swift\n",
		"app.rb":            "puts 'modified ruby'\n",
	}
	for rel, content := range realMods {
		if err := os.WriteFile(filepath.Join(workspace, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 2. Modify tracked & add untracked dependency files across ecosystems
	depMods := map[string]string{
		// Go & PHP & Rust vendor
		"vendor/dep/dep.go":             "package dep\n// modified vendor\n",
		"vendor/dep/untracked.go":       "package dep\n",
		"pkg/mod/github.com/foo/foo.go": "package foo\n",
		// Frontend
		"web/node_modules/dep/dep.js": "// modified node_modules\n",
		"web/node_modules/new/new.js": "// untracked node_modules\n",
		"sub/node-modules/pkg/pkg.js": "// untracked node-modules\n",
		".pnpm-store/v3/file":         "pnpm\n",
		".yarn/cache/a.zip":           "yarn\n",
		".next/cache/bundle.json":     "next\n",
		// Java & Rust
		"target/classes/Main.class":     "modifiedclassbytes",
		"target/debug/deps/libfoo.rlib": "rlibbytes",
		"target/dependency/app.jar":     "jarbytes",
		".gradle/caches/dep.jar":        "gradlejar",
		// Python
		"venv/lib/site-packages/mod.py": "pythonvenv",
		"__pycache__/app.pyc":           "pycbytes",
		"app.egg-info/PKG-INFO":         "egginfo",
		// C / C++
		"CMakeFiles/3.20/CMakeC.cmake": "cmakecache",
		"vcpkg_installed/lib/boost.a":  "boostarchive",
		"cmake-build-debug/main.o":     "mainobject",
		// Swift / iOS
		"Pods/AFNetworking/AF.m": "objc",
		"DerivedData/App/out":    "xcode",
		// .NET / C#
		"obj/Debug/net8.0/app.dll": "dotnetdll",
		"obj/project.assets.json":  "dotnetassets",
		// Ruby
		".bundle/config": "bundleconfig",
		// Elixir / Erlang
		"deps/phoenix/mix.exs": "phoenixdeps",
		"_build/dev/app.beam":  "beambytes",
		// Terraform
		".terraform/providers/aws": "terraformaws",
		// Test artifacts & reports & caches
		"coverage_clover.xml":         "<xml>massive updated coverage clover</xml>\n",
		".phpunit.result.cache":       "new cache data\n",
		"junit_unit_test.xml":         "<xml>huge junit test results</xml>\n",
		"tests/coverage/lcov.info":    "TN:\nSF:main.go\n",
		".phpunit.cache/test-results": "new cache\n",
	}
	for rel, content := range depMods {
		full := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	patch, err := collectRepositoryPatch(workspace)
	if err != nil {
		t.Fatalf("collectRepositoryPatch: %v", err)
	}
	text := string(patch)

	// Verify all real source modifications are preserved
	for _, expectedFile := range []string{"main.go", "Main.java", "index.php", "app.py", "lib.rs", "index.ts", "main.cpp", "Program.cs", "AppDelegate.swift", "app.rb"} {
		if !strings.Contains(text, expectedFile) {
			t.Fatalf("patch missing real change for %s:\n%s", expectedFile, text)
		}
	}

	// Verify all dependency modifications, test artifacts and untracked files are excluded
	forbiddenSnippets := []string{
		"vendor", "node_modules", "node-modules", "pnpm-store",
		"target", ".gradle", "venv", "__pycache__", "egg-info",
		"CMakeFiles", "vcpkg", "cmake-build", "Pods", "DerivedData",
		"obj/Debug", "project.assets.json", ".bundle", "phoenix",
		"terraform", ".rlib", ".jar", ".class",
		"coverage_clover.xml", "junit_unit_test.xml", ".phpunit.result.cache",
		"lcov.info", "massive updated coverage", "huge junit test",
	}
	for _, snippet := range forbiddenSnippets {
		if strings.Contains(text, snippet) {
			t.Fatalf("patch leaked %q:\n%s", snippet, text)
		}
	}
}

func TestSOPRunHandoffE2EWithExcludedDependencies(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	// 1. Workspace 1: Node "impl" executes
	ws1 := filepath.Join(t.TempDir(), "ws1")
	rc := domain.RepositoryContext{RepositoryID: "repo1", LocalPath: source, BaseBranch: "main"}
	if err := prepareRepository(ctx, ws1, rc); err != nil {
		t.Fatal(err)
	}

	// Modify business code and add an untracked source file
	if err := os.WriteFile(filepath.Join(ws1, "main.go"), []byte("package main\n\nfunc main() {\n\t// implemented\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws1, "handler_e2e_test.go"), []byte("package main\n// new e2e test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate heavy dependencies generated by agent or build tools
	depDirs := []string{
		filepath.Join(ws1, "vendor", "github.com", "pkg"),
		filepath.Join(ws1, "node_modules", "vue"),
		filepath.Join(ws1, "target", "classes"),
		filepath.Join(ws1, "venv", "lib"),
		filepath.Join(ws1, "CMakeFiles", "3.20"),
		filepath.Join(ws1, "obj", "Debug"),
		filepath.Join(ws1, "Pods", "AF"),
	}
	for _, d := range depDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "dep.dat"), []byte("heavy dependency data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Simulate test artifacts, reports and caches generated by test suites (e.g. PHPUnit, coverage)
	testArtifactFiles := []string{
		filepath.Join(ws1, "coverage_clover.xml"),
		filepath.Join(ws1, "junit_unit_test.xml"),
		filepath.Join(ws1, ".phpunit.result.cache"),
		filepath.Join(ws1, "coverage", "lcov.info"),
		filepath.Join(ws1, "tests", "_output", "report.html"),
	}
	for _, f := range testArtifactFiles {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("<xml>massive test output</xml>\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := domain.Run{
		ID:         "run-sop-e2e",
		WorkflowID: "wf-dev",
		Repository: rc,
		Nodes: map[string]*domain.NodeRun{
			"impl":   {NodeID: "impl", Attempt: 1, Status: "succeeded"},
			"verify": {NodeID: "verify", Attempt: 1, Status: "running"},
		},
	}
	nodeImpl := domain.WorkflowNode{ID: "impl"}
	artifacts, err := collectArtifacts(ws1, run, nodeImpl, "impl finished")
	if err != nil {
		t.Fatalf("collectArtifacts on ws1: %v", err)
	}
	run.Artifacts = artifacts

	// Find the repository-patch artifact
	var patchArtifact *domain.Artifact
	for i := range run.Artifacts {
		if run.Artifacts[i].Name == "repository-patch" {
			patchArtifact = &run.Artifacts[i]
			break
		}
	}
	if patchArtifact == nil {
		t.Fatal("expected repository-patch artifact to be produced by node impl")
	}

	// Verify patch contains only business changes, zero dependency leakage
	if strings.Contains(patchArtifact.Content, "implemented") || strings.Contains(patchArtifact.Content, "@@") {
		t.Fatalf("handoff stored a full diff:\n%s", patchArtifact.Content)
	}
	if !strings.Contains(patchArtifact.Content, "main.go") {
		t.Fatalf("patch missing main.go change:\n%s", patchArtifact.Content)
	}
	if !strings.Contains(patchArtifact.Content, "handler_e2e_test.go") {
		t.Fatalf("patch missing untracked handler_e2e_test.go:\n%s", patchArtifact.Content)
	}
	for _, forbidden := range []string{"vendor", "node_modules", "target", "venv", "CMakeFiles", "obj", "Pods", "coverage_clover.xml", "junit_unit_test.xml", ".phpunit.result.cache", "lcov.info", "report.html"} {
		if strings.Contains(patchArtifact.Content, forbidden) {
			t.Fatalf("patch leaked %s:\n%s", forbidden, patchArtifact.Content)
		}
	}

	wf := domain.WorkflowDefinition{
		ID: "wf-dev",
		Nodes: []domain.WorkflowNode{
			nodeImpl,
			{ID: "verify"},
		},
		Edges: []domain.WorkflowEdge{
			{Source: "impl", Target: "verify"},
		},
	}

	shared, err := materializeInputs(ws1, wf, domain.WorkflowNode{ID: "verify"}, run)
	if err != nil {
		t.Fatalf("materializeInputs in shared workspace: %v", err)
	}
	if !strings.Contains(shared, "Skipped applying") || !strings.Contains(shared, "main.go") {
		t.Fatalf("shared handoff should list files and skip apply:\n%s", shared)
	}

	// A clean clone is not a production path (the run owns one workspace).
	// Confirm we no longer paste a full diff onto it.
	ws2 := filepath.Join(t.TempDir(), "ws2")
	if err := prepareRepository(ctx, ws2, rc); err != nil {
		t.Fatal(err)
	}
	manifest, err := materializeInputs(ws2, wf, domain.WorkflowNode{ID: "verify"}, run)
	if err != nil {
		t.Fatalf("materializeInputs in ws2 failed: %v", err)
	}
	if strings.Contains(manifest, "Applied a legacy") {
		t.Fatalf("new summary must not apply onto a clean clone:\n%s", manifest)
	}
	mainContent, err := os.ReadFile(filepath.Join(ws2, "main.go"))
	if err != nil || strings.Contains(string(mainContent), "implemented") {
		t.Fatalf("ws2 should stay at baseline, got %q err %v", mainContent, err)
	}
	if _, err := os.Stat(filepath.Join(ws2, "handler_e2e_test.go")); !os.IsNotExist(err) {
		t.Fatal("ws2 should not receive the untracked e2e file from a summary")
	}

	// Delivery completeness still sees the new file from the summary.
	engine := &Engine{}
	if warnings := engine.verifyDeliveryCompleteness(run, ws2); len(warnings) == 0 {
		t.Fatal("expected a missing-file warning on the clean clone")
	}

	// 3. After the real workspace commits the new file, the warning clears.
	for _, args := range [][]string{{"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", ws1}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v in ws1: %v: %s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "-C", ws1, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add in ws1: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", ws1, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "delivered").CombinedOutput(); err != nil {
		t.Fatalf("git commit in ws1: %v: %s", err, output)
	}
	if warnings := engine.verifyDeliveryCompleteness(run, ws1); len(warnings) > 0 {
		t.Fatalf("verifyDeliveryCompleteness reported unexpected warnings: %v", warnings)
	}
}

func TestIsTestArtifactOrCachePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// PHPUnit caches & reports
		{path: "coverage_clover.xml", want: true},
		{path: "tests/coverage_clover.xml", want: true},
		{path: "junit_unit_test.xml", want: true},
		{path: "tests/junit_unit_test.xml", want: true},
		{path: ".phpunit.result.cache", want: true},
		{path: "sub/.phpunit.result.cache", want: true},
		{path: "phpunit.cache/result.cache", want: false}, // not a dot prefix directory
		{path: ".phpunit.cache/result.cache", want: true},
		{path: ".pest.cache/test", want: true},
		{path: ".php-cs-fixer.cache", want: true},
		{path: ".php_cs.cache", want: true},
		{path: "custom.phpunit.result.cache", want: true},

		// Test reports
		{path: "junit.xml", want: true},
		{path: "reports/junit.xml", want: true},
		{path: "junit-report.xml", want: true},
		{path: "test-results.xml", want: true},
		{path: "test-report.xml", want: true},
		{path: "test-results/report.html", want: true},
		{path: "tests/_output/report.html", want: true},

		// Coverage data & reports
		{path: "clover.xml", want: true},
		{path: "cobertura.xml", want: true},
		{path: "jacoco.xml", want: true},
		{path: "jacoco.exec", want: true},
		{path: "coverage.xml", want: true},
		{path: "coverage.json", want: true},
		{path: "coverage.lcov", want: true},
		{path: "coverage.out", want: true},
		{path: "coverage.txt", want: true},
		{path: "profile.out", want: true},
		{path: "lcov.info", want: true},
		{path: "lcov.dat", want: true},
		{path: ".coverage", want: true},
		{path: ".coverage.worker1", want: true},
		{path: "coverage/index.html", want: true},
		{path: "coverage-report/summary.txt", want: true},
		{path: "htmlcov/index.html", want: true},
		{path: ".nyc_output/12345.json", want: true},

		// Compiler coverage counters
		{path: "main.gcda", want: true},
		{path: "sub/main.gcno", want: true},
		{path: "main.gcov", want: true},

		// Protected configs & test source codes - MUST NOT BE EXCLUDED
		{path: "phpunit.xml", want: false},
		{path: "app/phpunit.xml.dist", want: false},
		{path: "tests/UserTest.php", want: false},
		{path: "tests/junit_test.go", want: false},
		{path: "tests/test_coverage.py", want: false},
		{path: "pkg/coverage/calculator.go", want: false},
		{path: "jest.config.js", want: false},
		{path: "package.json", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := gitwork.IsTestArtifactOrCachePath(tt.path)
			if got != tt.want {
				t.Errorf("gitwork.IsTestArtifactOrCachePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
			// Verify excludeFromRepositoryPatch propagates this
			excluded := gitwork.Excluded(tt.path)
			if excluded != tt.want {
				t.Errorf("gitwork.Excluded(%q) = %v, want %v", tt.path, excluded, tt.want)
			}
		})
	}
}

func quoteGitCStyle(rel string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(rel); i++ {
		c := rel[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			fmt.Fprintf(&b, "\\%03o", c)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestBuildLoopRetroDoesNotBlameMaxTurnsOnMissingResult(t *testing.T) {
	body := buildLoopRetro(domain.Run{}, "comments", 1, fmt.Errorf("executor exited without a terminal result"), "")
	if strings.Contains(body, "检查 `--max-turns`") {
		t.Fatalf("loop retro still blamed max-turns:\n%s", body)
	}
	if !strings.Contains(body, "type:result") {
		t.Fatalf("loop retro missing protocol diagnosis:\n%s", body)
	}
}

func TestBindGrepToArtifacts(t *testing.T) {
	arts := []domain.ArtifactContract{{Name: "checkers-report", Path: "checkers-report.md", Required: true}}
	bare := `grep -Eqi 'ALL_GREEN[[:space:]]*=[[:space:]]*true'`
	got := bindGrepToArtifacts(bare, arts)
	if got != bare+" checkers-report.md" {
		t.Fatalf("bare grep should bind report file, got %q", got)
	}
	already := `grep -Eqi 'ALL_GREEN[[:space:]]*=[[:space:]]*true' checkers-report.md`
	if bindGrepToArtifacts(already, arts) != already {
		t.Fatalf("grep with filename must stay unchanged")
	}
	piped := `git ls-files | grep -Eq '_e2e_test\.go$'`
	if bindGrepToArtifacts(piped, arts) != piped {
		t.Fatalf("piped grep must stay unchanged, got %q", bindGrepToArtifacts(piped, arts))
	}
}

func TestBindGitDiffQuietExcludesDeliverables(t *testing.T) {
	arts := []domain.ArtifactContract{{Name: "fix-plan", Path: "fix-plan.md", Required: true}}
	got := bindVerificationCommand("git diff --quiet", arts)
	if !strings.Contains(got, ":(exclude)fix-plan.md") || !strings.Contains(got, ":(exclude)checkers-report.md") {
		t.Fatalf("cleanliness gate must ignore SOP deliverables, got %q", got)
	}
	if !strings.HasPrefix(got, "git diff --quiet -- .") {
		t.Fatalf("cleanliness gate must keep pathspec separator, got %q", got)
	}
	already := "git diff --quiet -- . ':(exclude)fix-plan.md'"
	if bindVerificationCommand(already, arts) != already {
		t.Fatalf("explicit exclude must stay unchanged")
	}
	unrelated := "git status --porcelain"
	if bindVerificationCommand(unrelated, arts) != unrelated {
		t.Fatalf("non-diff commands must stay unchanged")
	}
}

func TestRecordHumanContextPersistsForRetryAndPrompt(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "human-context-agent", []domain.VerificationGate{{ID: "strict", Name: "strict", Mode: "command", Required: true, Commands: []string{"false"}, OnFailure: "human", ExecutionScope: domain.ExecutionScopeRun}})
	wf := domain.WorkflowDefinition{ID: "human-context-lab", Name: "human context lab", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "check", Name: "check", Type: "agent", AgentID: "human-context-agent", ExecutorID: "mock", OnFailure: "human", MaxAttempts: 1},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	if err := e.Retry(r.ID, "check", "人工确认重试本节点", "QTA 失败用例 TestCanonicalPayloadMarshal"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	if !strings.Contains(current.HumanContext, "TestCanonicalPayloadMarshal") {
		t.Fatalf("run humanContext = %q", current.HumanContext)
	}
	if !strings.Contains(current.Nodes["check"].Feedback, "TestCanonicalPayloadMarshal") {
		t.Fatalf("node feedback = %q", current.Nodes["check"].Feedback)
	}
	found := false
	for _, a := range current.Artifacts {
		if a.Name == "human-context" && strings.Contains(a.Content, "TestCanonicalPayloadMarshal") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing human-context artifact")
	}
	prompt := renderPromptSpec(wf, domain.WorkflowNode{ID: "check"}, domain.TaskPromptSpec{Objective: "do work {{feedback}}"}, current)
	if !strings.Contains(prompt, "人工补充上下文") || !strings.Contains(prompt, "TestCanonicalPayloadMarshal") {
		t.Fatalf("prompt missing human context: %s", prompt)
	}
	if err := writeHumanContextFile(dir, current.HumanContext); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "human-context.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "TestCanonicalPayloadMarshal") {
		t.Fatalf("workspace file = %q", body)
	}
}

func TestApproveWithHumanContextReachesDownstream(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "human-approve-agent", []domain.VerificationGate{{ID: "strict", Name: "strict", Mode: "command", Required: true, Commands: []string{"false"}, OnFailure: "human", ExecutionScope: domain.ExecutionScopeRun}})
	wf := domain.WorkflowDefinition{ID: "human-approve-context", Name: "approve context", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "gate", Name: "gate", Type: "agent", AgentID: "human-approve-agent", ExecutorID: "mock", OnFailure: "human", MaxAttempts: 1},
		{ID: "next", Name: "next", Type: "command", Command: "test -f human-context.md && grep -q TestFoo human-context.md", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "gate", Target: "next", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	if err := e.Approve(r.ID, "gate", "QTA 失败用例 TestFoo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Nodes["next"].Status != "succeeded" {
		t.Fatalf("downstream should see human-context.md, next=%+v", current.Nodes["next"])
	}
	if current.HumanContext != "QTA 失败用例 TestFoo" {
		t.Fatalf("humanContext = %q", current.HumanContext)
	}
}

func TestGateOnFailureIsConfiguredNotInferred(t *testing.T) {
	if resolveGateOnFailure("loop", "reject") != "loop" {
		t.Fatal("gate onFailure must win over node default")
	}
	if resolveGateOnFailure("", "reject") != "reject" {
		t.Fatal("empty gate onFailure should inherit node/policy default")
	}
	if resolveGateOnFailure("bogus", "fail") != "fail" {
		t.Fatal("invalid gate onFailure should fall back")
	}
	if !shouldSelfCorrect("loop") {
		t.Fatal("loop gates should self-correct")
	}
	if shouldSelfCorrect("reject") || shouldSelfCorrect("fail") || shouldSelfCorrect("human") {
		t.Fatal("non-loop gates must not self-correct")
	}
	node := domain.WorkflowNode{MaxAttempts: 3, RejectTo: "fix"}
	verdict := fmt.Errorf(`verification 独立评审门禁 command "grep -Eqi 'blocking[^0-9]*0' review-report.md": exit status 1`)
	if shouldRetryFailedAttempt(node, 1, verdict) {
		t.Fatal("verification failures are handled by gate onFailure, not extra node attempts")
	}
	if !shouldRetryFailedAttempt(node, 1, fmt.Errorf("executor exited without a terminal result")) {
		t.Fatal("protocol errors may still retry the same node")
	}
	if shouldRetryFailedAttempt(node, 1, runtime.ErrMaxTurns) {
		t.Fatal("max turns must not auto-consume extra attempts")
	}
	if shouldRetryFailedAttempt(node, 1, fmt.Errorf("current working directory is outside of container mount namespace: exit status 126")) {
		t.Fatal("cwd-mount 126 must not retry the node and re-clone the bind")
	}
	if shouldRetryFailedAttempt(node, 1, fmt.Errorf("exit status 126")) {
		t.Fatal("bare exit status 126 must not retry the node")
	}
	if allowRejectOnFailure(domain.WorkflowNode{RejectTo: "fix", OnFailure: "reject"}, runtime.ErrMaxTurns, false) {
		t.Fatal("max turns must not auto-reject to fix")
	}
}

func TestApplyUsageAccumulatesIndependentSessions(t *testing.T) {
	run := domain.Run{}
	nr := domain.NodeRun{}
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 80})
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 120, OutputTokens: 12, CacheReadTokens: 90})
	applyUsage(&run, &nr, "s2", domain.TokenUsage{InputTokens: 50, OutputTokens: 5, CacheReadTokens: 40})
	if run.Usage.InputTokens != 170 || run.Usage.OutputTokens != 17 || run.Usage.CacheReadTokens != 130 {
		t.Fatalf("run usage = %+v", run.Usage)
	}
	if nr.Usage.InputTokens != 170 || billedTokens(run.Usage) != 187 {
		t.Fatalf("node usage = %+v total=%d", nr.Usage, billedTokens(run.Usage))
	}
}

func TestApplyUsagePropagatesCostStatus(t *testing.T) {
	run := domain.Run{}
	nr := domain.NodeRun{}
	// 真实零成本：适配器显式上报 0 且标记 reported，不应被当成"未识别"。
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 10, OutputTokens: 2, CostUSD: 0, CostStatus: "reported"})
	if run.Usage.CostStatus != "reported" || nr.Usage.CostStatus != "reported" {
		t.Fatalf("cost status = run:%q node:%q, want reported", run.Usage.CostStatus, nr.Usage.CostStatus)
	}
	// 状态跟随最近一次快照。
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 20, OutputTokens: 4, CostUSD: 0.01, CostStatus: "estimated"})
	if run.Usage.CostStatus != "estimated" {
		t.Fatalf("cost status = %q, want estimated", run.Usage.CostStatus)
	}
	// 未带状态的快照不覆盖既有识别状态（成本保持累计口径）。
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 30, OutputTokens: 6, CostUSD: 0.01})
	if run.Usage.CostStatus != "estimated" {
		t.Fatalf("cost status = %q, want estimated to persist", run.Usage.CostStatus)
	}
	if run.Usage.InputTokens != 30 || run.Usage.OutputTokens != 6 || run.Usage.CostUSD != 0.01 {
		t.Fatalf("run usage = %+v", run.Usage)
	}
	// 只有状态、无数值变化的快照也应更新识别状态。
	applyUsage(&run, &nr, "s1", domain.TokenUsage{CostStatus: "reported"})
	if run.Usage.InputTokens != 30 || run.Usage.CostStatus != "reported" {
		t.Fatalf("status-only update: usage=%+v status=%q", run.Usage, run.Usage.CostStatus)
	}
}

func TestExistingMRBlurbDoesNotReusePreviousSOP(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	old := domain.Run{ID: "run-old", WorkflowID: "issue-fix-production", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: "gf-5"}, Artifacts: []domain.Artifact{{NodeID: "mr", Name: "mr-body", Content: "MR URL: https://git.example.com/example/team-project/-/merge_requests/138\nHEAD abc", Attempt: 1, CreatedAt: time.Now().UTC()}}, UpdatedAt: time.Now().UTC()}
	if err := s.PutRun(old); err != nil {
		t.Fatal(err)
	}
	got := existingMRBlurb(s, domain.Run{ID: "run-new", ExecutionContext: domain.TelemetryContext{IssueID: "gf-5"}})
	if strings.Contains(got, "merge_requests/138") || strings.Contains(got, "必须在此 MR") {
		t.Fatalf("new SOP still bound to previous MR: %s", got)
	}
}

func TestAlignOnlyApprovalDoesNotWait(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: BaselineAlignWorkflowID, Name: "align auto", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "work", Name: "work", Type: "command", Command: "printf aligned\\n", MaxAttempts: 1},
		{ID: "confirm", Name: "confirm", Type: "approval", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "work", Target: "confirm"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if current.Nodes["confirm"].Status != "succeeded" {
		t.Fatalf("align-only confirm = %+v, want auto-succeeded", current.Nodes["confirm"])
	}
}

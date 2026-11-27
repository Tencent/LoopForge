package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestProjectAssetBundleRoundTrip(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	store := f.s
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(store.PutProject(domain.Project{ID: "bundle-source", Name: "Source", OwnerID: f.user.ID}))
	save(store.PutProject(domain.Project{ID: "bundle-target", Name: "Target", OwnerID: f.user.ID}))
	markdown := []byte("---\nname: original\n---\n# Exact bytes\n")
	binary := []byte{0, 0xff, '\n'}
	save(store.PutSkillSnapshot(domain.SkillDefinition{
		ID: "company-skill", Name: "Company skill", ProjectID: "bundle-source",
	}, []repo.SkillFileInput{
		{Path: "SKILL.md", Content: markdown, Mode: 0644},
		{Path: "scripts/tool.bin", Content: binary, Mode: 0755},
	}, true))
	save(store.PutMCPServer(domain.MCPServerDefinition{
		ID: "company-mcp", Name: "Example MCP", ProjectID: "bundle-source", Scope: "project",
		Transport: "stdio", Command: "node", Args: []string{"server.js"}, Environment: map[string]string{"API_KEY": "binding-test-secret"},
	}))
	save(store.PutMCPFiles("company-mcp", []repo.SkillFileInput{{Path: "server.js", Content: []byte("console.log('adapter')")}}))
	save(store.PutAgent(domain.AgentDefinition{
		ID: "company-role", Name: "Reviewer", ProjectID: "bundle-source", ExecutorID: "codebuddy",
		Skills: []string{"company-skill"}, MCPServers: []string{"company-mcp"}, Rules: []string{"company-rule"},
		Prompt:      "Read .agentflow/company-role/skills/company-skill/SKILL.md",
		PlatformMCP: true,
	}))
	save(store.PutRule(domain.RuleDefinition{
		ID: "company-rule", Name: "Rule", ProjectID: "bundle-source", Global: true, WorkflowIDs: []string{"company-sop"},
		Content: "Cite source evidence.",
	}))
	save(store.PutSandbox(domain.SandboxProfile{
		ID: "company-sandbox", Name: "Sandbox", ProjectID: "bundle-source", Driver: "docker",
		Dockerfile: &domain.SandboxDockerfile{
			Content: "FROM python:3.12-slim", Files: map[string]string{"config/defaults.txt": "mode=review\n"},
		},
		Environment: map[string]string{"TOKEN": "sandbox-test-secret"},
	}))
	save(store.PutWorkflow(domain.WorkflowDefinition{
		ID: "company-sop", Name: "Review", ProjectID: "bundle-source", SandboxProfileID: "company-sandbox", Status: "published",
		ProjectCredentialIDs: []string{"source-connection-id"},
		Nodes: []domain.WorkflowNode{
			{ID: "review", Name: "Review", Type: "agent", AgentID: "company-role",
				KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: "source-knowledge-id", Mode: "all"}}},
			{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "python",
				Script: "CODE_SKILL = \"company-skill\"\nprint(CODE_SKILL)", RejectTo: "review", OnFailure: "reject"},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "review", Target: "verify", When: "success"}},
	}))

	exportPath := "/api/projects/bundle-source/asset-bundles/export"
	selection := map[string]any{"assets": []domain.BundleAssetRef{
		{Kind: "sop", ID: "company-sop"}, {Kind: "skill", ID: "company-skill"},
		{Kind: "mcp", ID: "company-mcp"}, {Kind: "agent", ID: "company-role"},
	}}
	res := cvBRequest(t, f, "POST", exportPath, selection, true)
	if res.Code != http.StatusOK {
		t.Fatalf("export: %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "test-secret") || strings.Contains(res.Body.String(), "source-connection-id") || strings.Contains(res.Body.String(), "source-knowledge-id") {
		t.Fatal("the bundle exported a credential")
	}
	var bundle domain.ProjectAssetBundle
	if err := json.Unmarshal(res.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Skills) != 1 || len(bundle.MCPServers) != 1 || len(bundle.Workflows) != 1 ||
		len(bundle.Sandboxes) != 1 || len(bundle.Bindings) < 3 {
		t.Fatalf("bundle failed to include and deduplicate dependencies: %+v", bundle)
	}
	target := "/api/projects/bundle-target/asset-bundles/"
	res = cvBRequest(t, f, "POST", target+"preview", bundle, true)
	if res.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", res.Code, res.Body.String())
	}
	for _, skill := range store.Skills() {
		if skill.ProjectID == "bundle-target" {
			t.Fatal("preview persisted a skill")
		}
	}
	res = cvBRequest(t, f, "POST", target+"import", bundle, true)
	if res.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	var result struct{ Assets []domain.BundleAssetSummary }
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for _, item := range result.Assets {
		ids[item.Kind] = item.TargetID
		if item.ID == item.TargetID {
			t.Fatal("import reused source ID")
		}
	}
	role, _ := store.Agent(ids["agent"])
	skill, _ := store.Skill(ids["skill"])
	workflow, _ := store.Workflow(ids["sop"])
	server, _ := store.MCPServer(ids["mcp"])
	sandbox, _ := store.Sandbox(ids["sandbox"])
	rule, _ := store.Rule(ids["rule"])
	if role.ProjectID != "bundle-target" || role.Shared || role.PlatformMCP ||
		len(role.Skills) != 1 || role.Skills[0] != ids["skill"] || len(role.MCPServers) != 1 || role.MCPServers[0] != ids["mcp"] ||
		len(role.Rules) != 1 || role.Rules[0] != ids["rule"] ||
		workflow.ProjectID != "bundle-target" || workflow.Status != "draft" || workflow.Shared || workflow.Nodes[0].AgentID != role.ID ||
		workflow.Nodes[1].RejectTo != "review" || !strings.Contains(workflow.Nodes[1].Script, ids["skill"]) ||
		workflow.SandboxProfileID != sandbox.ID || sandbox.Dockerfile == nil || sandbox.Dockerfile.Content != "FROM python:3.12-slim" ||
		sandbox.Dockerfile.Files["config/defaults.txt"] != "mode=review\n" ||
		len(workflow.ProjectCredentialIDs) != 0 || len(workflow.Nodes[0].KnowledgeRefs) != 0 ||
		len(rule.WorkflowIDs) != 1 || rule.WorkflowIDs[0] != workflow.ID ||
		!strings.Contains(role.Prompt, ".agentflow/"+role.ID+"/skills/"+skill.ID+"/SKILL.md") ||
		len(server.Environment) != 0 || server.Scope != "project" || len(sandbox.Environment) != 0 {
		t.Fatal("references or project visibility were not safely remapped")
	}
	for path, expected := range map[string][]byte{"SKILL.md": markdown, "scripts/tool.bin": binary} {
		raw, ok := store.SkillFileContent(skill.ID, path)
		if !ok || !bytes.Equal(raw, expected) {
			t.Fatalf("file %q changed during round trip", path)
		}
	}
	if len(skill.FileRefs) != 2 || skill.FileRefs[1].Mode != 0755 {
		t.Fatalf("executable mode not preserved: %+v", skill.FileRefs)
	}
	if raw, ok := store.MCPFileContent(server.ID, "server.js"); !ok || string(raw) != "console.log('adapter')" {
		t.Fatal("missing MCP package bytes")
	}
}

func TestProjectAssetBundleRejectsBadFilesAndRollsBack(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "bundle-rollback", Name: "Rollback", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	tgt := "/api/projects/bundle-rollback/asset-bundles/"
	content := []byte("regular asset")
	bundle := domain.ProjectAssetBundle{
		Format: domain.ProjectAssetBundleFormat,
		Skills: []domain.BundleSkill{{
			SkillDefinition: domain.SkillDefinition{ID: "a-skill", Name: "Test skill"},
			Attachments: []domain.BundleAssetFile{{
				Path: "../outside", Content: content, SHA256: fmt.Sprintf("%x", sha256.Sum256(content)),
			}},
		}},
	}
	res := cvBRequest(t, f, "POST", tgt+"import", bundle, true)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unsafe path: %d %s", res.Code, res.Body.String())
	}
	bundle.Skills[0].Attachments[0].Path = "SKILL.md"
	bundle.Sandboxes = []domain.SandboxProfile{{ID: "wrong-driver", Name: "Malconfigured", Driver: "invalid"}}
	res = cvBRequest(t, f, "POST", tgt+"preview", bundle, true)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("preview accepted an unsupported sandbox: %d %s", res.Code, res.Body.String())
	}
	bundle.Sandboxes[0].Driver = "native"
	if err := f.s.DB.Exec("CREATE TRIGGER bundle_reject_sandbox BEFORE INSERT ON sandboxes BEGIN SELECT RAISE(FAIL, 'forced sandbox insert'); END").Error; err != nil {
		t.Fatal(err)
	}
	res = cvBRequest(t, f, "POST", tgt+"import", bundle, true)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "forced sandbox insert") {
		t.Fatalf("expected final write validation failure: %d %s", res.Code, res.Body.String())
	}
	for _, skill := range f.s.Skills() {
		if skill.ProjectID == "bundle-rollback" {
			t.Fatal("partial write survived failed import")
		}
	}
}

func TestProjectAssetBundleScope(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	otherID := f.user.ID + 99
	if err := f.s.PutProject(domain.Project{ID: "bundle-acl", Name: "ACL", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSkill(domain.SkillDefinition{ID: "unrelated-private", Name: "Other", ProjectID: "other-project"}); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/bundle-acl/asset-bundles/export"
	input := map[string]any{"assets": []domain.BundleAssetRef{{Kind: "skill", ID: "unrelated-private"}}}
	request := cvBRequest(t, f, "POST", path, input, false)
	if request.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous export: %d %s", request.Code, request.Body.String())
	}
	request = cvBMiscRequestAs(t, f, domain.User{ID: otherID, LoginName: "bundle-outsider"}, "POST", path, input)
	if request.Code != http.StatusForbidden {
		t.Fatalf("outsider export: %d %s", request.Code, request.Body.String())
	}
	request = cvBRequest(t, f, "POST", path, input, true)
	if request.Code != http.StatusBadRequest {
		t.Fatalf("cross-project export: %d %s", request.Code, request.Body.String())
	}
}

func TestProjectAssetBundlePreservesAutomaticExecutionSettings(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "bundle-auto", Name: "Auto", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	bundle := domain.ProjectAssetBundle{
		Format: domain.ProjectAssetBundleFormat,
		Agents: []domain.AgentDefinition{{ID: "reviewer", Name: "Reviewer"}},
		Workflows: []domain.WorkflowDefinition{{
			ID: "review", Name: "Review LOOP", AutoSelectSandbox: true,
			Nodes: []domain.WorkflowNode{{ID: "review", Name: "Review", Type: "agent", AgentID: "reviewer"}},
		}},
	}
	res := cvBRequest(t, f, "POST", "/api/projects/bundle-auto/asset-bundles/import", bundle, true)
	if res.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	var result struct{ Assets []domain.BundleAssetSummary }
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, asset := range result.Assets {
		switch asset.Kind {
		case "agent":
			v, ok := f.s.Agent(asset.TargetID)
			if !ok || v.ExecutorID != "" {
				t.Fatal("the imported role must follow the user's executor")
			}
		case "sop":
			v, ok := f.s.Workflow(asset.TargetID)
			if !ok || !v.AutoSelectSandbox || v.SandboxProfileID != "" {
				t.Fatal("the imported LOOP must select its sandbox at run time")
			}
			if v.Edges == nil || len(v.Edges) != 0 {
				t.Fatalf("a LOOP without edges must keep an empty array, got %#v", v.Edges)
			}
		}
	}
}

func TestProjectAssetBundleExportIgnoresOtherProjectsGlobalRules(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(f.s.PutProject(domain.Project{ID: "bundle-shared-src", Name: "Source", OwnerID: f.user.ID}))
	save(f.s.PutProject(domain.Project{ID: "bundle-other", Name: "Other", OwnerID: f.user.ID + 99}))
	save(f.s.PutAgent(domain.AgentDefinition{ID: "shared-src-role", Name: "Reviewer", ProjectID: "bundle-shared-src"}))
	save(f.s.PutWorkflow(domain.WorkflowDefinition{
		ID: "shared-src-loop", Name: "Shared LOOP", ProjectID: "bundle-shared-src", Shared: true, AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "review", Name: "Review", Type: "agent", AgentID: "shared-src-role"}},
	}))
	save(f.s.PutRule(domain.RuleDefinition{
		ID: "shared-src-own-rule", Name: "Own", ProjectID: "bundle-shared-src", Global: true,
		WorkflowIDs: []string{"shared-src-loop"}, Content: "Own rule.",
	}))
	// Another project binds its own global rule to the shared LOOP. The source
	// project cannot see or remove it, so it must not block or join the export.
	save(f.s.PutRule(domain.RuleDefinition{
		ID: "other-global-rule", Name: "Other", ProjectID: "bundle-other", Global: true,
		WorkflowIDs: []string{"shared-src-loop"}, Content: "Other project rule.",
	}))
	res := cvBRequest(t, f, "POST", "/api/projects/bundle-shared-src/asset-bundles/export",
		map[string]any{"assets": []domain.BundleAssetRef{{Kind: "sop", ID: "shared-src-loop"}}}, true)
	if res.Code != http.StatusOK {
		t.Fatalf("export: %d %s", res.Code, res.Body.String())
	}
	var bundle domain.ProjectAssetBundle
	if err := json.Unmarshal(res.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Rules) != 1 || bundle.Rules[0].ID != "shared-src-own-rule" || strings.Contains(res.Body.String(), "other-global-rule") {
		t.Fatalf("export must carry only this project's global rules: %+v", bundle.Rules)
	}
}

func TestProjectAssetBundleExplainsForeignDependencies(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(f.s.PutProject(domain.Project{ID: "bundle-borrower", Name: "Borrower", OwnerID: f.user.ID}))
	save(f.s.PutAgent(domain.AgentDefinition{ID: "lender-role", Name: "Shared role", ProjectID: "bundle-lender", Shared: true}))
	save(f.s.PutWorkflow(domain.WorkflowDefinition{
		ID: "borrower-loop", Name: "Borrower LOOP", ProjectID: "bundle-borrower", AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "review", Name: "Review", Type: "agent", AgentID: "lender-role"}},
	}))
	path := "/api/projects/bundle-borrower/asset-bundles/export"
	res := cvBRequest(t, f, "POST", path, map[string]any{"assets": []domain.BundleAssetRef{{Kind: "sop", ID: "borrower-loop"}}}, true)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "agent/lender-role") ||
		!strings.Contains(res.Body.String(), "请先复制到当前项目") {
		t.Fatalf("a shared role from another project needs a copy hint: %d %s", res.Code, res.Body.String())
	}
	// A private asset of another project must read exactly like a missing one.
	save(f.s.PutSkill(domain.SkillDefinition{ID: "lender-private", Name: "Private", ProjectID: "bundle-lender"}))
	errorFor := func(id string) string {
		t.Helper()
		res := cvBRequest(t, f, "POST", path, map[string]any{"assets": []domain.BundleAssetRef{{Kind: "skill", ID: id}}}, true)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("export skill %s: %d %s", id, res.Code, res.Body.String())
		}
		return strings.ReplaceAll(res.Body.String(), id, "<id>")
	}
	if hidden, missing := errorFor("lender-private"), errorFor("missing-skill"); hidden != missing || strings.Contains(hidden, "复制") {
		t.Fatalf("a private foreign asset is distinguishable from a missing one: %s vs %s", hidden, missing)
	}
}

func TestProjectAssetBundlePreviewRunsTheSave(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "bundle-dry-run", Name: "Dry run", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	tgt := "/api/projects/bundle-dry-run/asset-bundles/"
	bundle := func(role domain.AgentDefinition) domain.ProjectAssetBundle {
		return domain.ProjectAssetBundle{
			Format: domain.ProjectAssetBundleFormat,
			Agents: []domain.AgentDefinition{role},
			Workflows: []domain.WorkflowDefinition{{
				ID: "dry-loop", Name: "Dry LOOP", AutoSelectSandbox: true,
				Nodes: []domain.WorkflowNode{{ID: "review", Name: "Review", Type: "agent", AgentID: role.ID}},
			}},
			Bindings: []domain.BundleBinding{{Kind: "loop-integrations", AssetID: "dry-loop"}},
		}
	}
	for name, role := range map[string]domain.AgentDefinition{
		"hook language": {ID: "dry-role", Name: "Role", Hooks: []domain.AgentHook{{ID: "h1", Name: "bad", Event: "Stop", Language: "ruby", Script: "puts 1"}}},
		"artifact path": {ID: "dry-role", Name: "Role", ArtifactOutputs: []domain.ArtifactContract{{Name: "x", Path: "../escape"}}},
	} {
		res := cvBRequest(t, f, "POST", tgt+"preview", bundle(role), true)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("preview accepted an invalid %s: %d %s", name, res.Code, res.Body.String())
		}
	}
	valid := bundle(domain.AgentDefinition{ID: "dry-role", Name: "Role"})
	res := cvBRequest(t, f, "POST", tgt+"preview", valid, true)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "targetId") {
		t.Fatalf("preview: %d %s", res.Code, res.Body.String())
	}
	for _, workflow := range f.s.Workflows() {
		if workflow.ProjectID == "bundle-dry-run" {
			t.Fatal("preview persisted a LOOP")
		}
	}
	for _, role := range f.s.Agents() {
		if role.ProjectID == "bundle-dry-run" {
			t.Fatal("preview persisted a role")
		}
	}
	res = cvBRequest(t, f, "POST", tgt+"import", valid, true)
	if res.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	var result struct {
		Assets   []domain.BundleAssetSummary
		Bindings []domain.BundleBinding
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	loopID := ""
	for _, asset := range result.Assets {
		if asset.Kind == "sop" {
			loopID = asset.TargetID
		}
	}
	if loopID == "" || len(result.Bindings) != 1 || result.Bindings[0].AssetID != loopID {
		t.Fatalf("import bindings must point at the imported copy: %+v", result)
	}
	if workflow, ok := f.s.Workflow(loopID); !ok || workflow.ProjectID != "bundle-dry-run" {
		t.Fatal("import did not save the LOOP")
	}
}

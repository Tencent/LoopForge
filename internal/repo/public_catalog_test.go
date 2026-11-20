package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func publicCatalogTemplates(t *testing.T) map[string]GenericTemplate {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/seed/001_public_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^INSERT IGNORE INTO generic_templates .*VALUES \('([^']*)','[^']*','(.*)',UTC_TIMESTAMP\(\)\);$`)
	templates := map[string]GenericTemplate{}
	for _, line := range strings.Split(string(raw), "\n") {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		var template GenericTemplate
		payload := strings.NewReplacer(`\\`, `\`, `''`, `'`).Replace(match[2])
		if err := json.Unmarshal([]byte(payload), &template); err != nil {
			t.Fatal(err)
		}
		templates[match[1]] = template
	}
	if len(templates) != 4 {
		t.Fatalf("expected four public templates, got %d", len(templates))
	}
	return templates
}

// The production catalog must pass the user's task to every executable role.
// Each public role stores that brief in prompt, including {{input}}.
func TestPublicCatalogPassesRunInputToEveryRole(t *testing.T) {
	for id, template := range publicCatalogTemplates(t) {
		if err := domain.ValidateWorkflowPreparation(template.Workflow); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		if err := domain.ValidateWorkflowMetricStages(template.Workflow); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		for _, agent := range template.Agents {
			if !strings.Contains(agent.Prompt, "{{input}}") {
				t.Errorf("%s role %s drops the user's run input", id, agent.ID)
			}
			if err := domain.ValidateVerificationGates(agent.Verification); err != nil {
				t.Errorf("%s role %s: %v", id, agent.ID, err)
			}
		}
	}
}

// A code SOP must not succeed with its changes only in the reclaimed
// workspace: the MR node runs after acceptance and before the summary, and
// its command gate blocks while a checkout has unpushed work.
func TestPublicCodeSOPsOpenMRBeforeDeliver(t *testing.T) {
	templates := publicCatalogTemplates(t)
	for _, id := range []string{GenericBackendDeliveryWorkflowID, GenericFrontendDeliveryWorkflowID, "issue-fix-production"} {
		template, ok := templates[id]
		if !ok {
			t.Fatalf("%s missing", id)
		}
		next := map[string][]string{}
		for _, edge := range template.Workflow.Edges {
			next[edge.Source] = append(next[edge.Source], edge.Target)
		}
		if got := strings.Join(next["accept"], ","); got != "mr" {
			t.Errorf("%s: accept leads to %q, want mr", id, got)
		}
		if got := strings.Join(next["mr"], ","); got != "deliver" {
			t.Errorf("%s: mr leads to %q, want deliver", id, got)
		}
		var mrAgentID string
		for _, node := range template.Workflow.Nodes {
			if node.ID != "mr" {
				continue
			}
			mrAgentID = node.AgentID
			if len(node.ArtifactOutputs) != 1 || node.ArtifactOutputs[0].Name != "mr-body" || node.ArtifactOutputs[0].Path != "mr-body.md" {
				t.Errorf("%s: mr node must publish mr-body.md, got %+v", id, node.ArtifactOutputs)
			}
		}
		found := false
		for _, agent := range template.Agents {
			if agent.ID != mrAgentID {
				continue
			}
			found = true
			if len(agent.Verification) != 2 || agent.Verification[0].Mode != "command" || agent.Verification[0].ExecutionScope != "run" || len(agent.Verification[0].Commands) != 1 || agent.Verification[1].Mode != domain.VerificationModeMR || len(agent.Verification[1].Commands) != 0 {
				t.Errorf("%s: mr role needs a run-scoped push gate followed by an MR gate, got %+v", id, agent.Verification)
			}
			if !agent.Loop.Enabled || agent.Loop.OnExhausted != "human" {
				t.Errorf("%s: mr role must hand an unresolved push to a person, got %+v", id, agent.Loop)
			}
			if !strings.Contains(agent.Prompt, "{{existingMR}}") {
				t.Errorf("%s: mr role drops the existing MR context", id)
			}
			if !strings.Contains(agent.Prompt, "git credential fill") || strings.Contains(agent.Prompt, "$AGENTFLOW_GIT_TOKEN") {
				t.Errorf("%s: mr role must use per-host Git credentials", id)
			}
		}
		if !found {
			t.Errorf("%s: mr node references missing role %q", id, mrAgentID)
		}
	}
	for _, node := range templates[GenericProductDeliveryWorkflowID].Workflow.Nodes {
		if node.ID == "mr" {
			t.Errorf("product SOP delivers documents only and must not open an MR")
		}
	}
}

func TestPublicMRGateRequiresTheRunWorkBranchPushed(t *testing.T) {
	var gate string
	for _, agent := range publicCatalogTemplates(t)[GenericBackendDeliveryWorkflowID].Agents {
		if agent.ID == GenericBackendDeliveryWorkflowID+"-mr" {
			gate = agent.Verification[0].Commands[0]
		}
	}
	if gate == "" {
		t.Fatal("mr gate missing")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	workspace := t.TempDir()
	passes := func(manifest ...string) bool {
		t.Helper()
		cmd := exec.Command("sh", "-lc", gate)
		cmd.Dir = workspace
		cmd.Env = append(os.Environ(), "AGENTFLOW_WORK_BRANCHES="+strings.Join(manifest, "\n"))
		return cmd.Run() == nil
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	git(root, "init", "-q", "--bare", remote)
	seed := filepath.Join(root, "seed")
	git(root, "clone", "-q", remote, seed)
	write(filepath.Join(seed, "a.txt"), "a\n")
	git(seed, "add", ".")
	git(seed, "commit", "-qm", "init")
	git(seed, "push", "-q", "origin", "HEAD:main")
	write(filepath.Join(seed, "r.txt"), "release\n")
	git(seed, "add", ".")
	git(seed, "commit", "-qm", "release")
	git(seed, "push", "-q", "origin", "HEAD:release")

	write(filepath.Join(workspace, "requirement.md"), "doc\n")
	if !passes() {
		t.Fatal("a run without repositories must pass")
	}
	repo := filepath.Join(workspace, "app")
	git(workspace, "clone", "-q", remote, repo)
	git(repo, "checkout", "-qb", "work")
	app := "app\twork\tmain"
	write(filepath.Join(repo, ".agentflow", "state"), "x\n")
	write(filepath.Join(repo, "node_modules", "dep.js"), "x\n")
	if !passes(app) {
		t.Fatal("a repository the run left unchanged must pass; .agentflow and node_modules are ignored")
	}
	write(filepath.Join(repo, "a.txt"), "changed\n")
	write(filepath.Join(repo, "new.txt"), "new\n")
	if passes(app) {
		t.Fatal("modified and untracked files must block")
	}
	git(repo, "add", "a.txt", "new.txt")
	git(repo, "commit", "-qm", "change")
	if passes(app) {
		t.Fatal("an unpushed commit must block")
	}
	git(repo, "push", "-q", "origin", "HEAD:other")
	if passes(app) {
		t.Fatal("HEAD pushed to another branch must block while the work branch is not pushed")
	}
	git(repo, "push", "-q", "-u", "origin", "work")
	if !passes(app) {
		t.Fatal("a pushed work branch must pass")
	}
	write(filepath.Join(repo, "later.txt"), "later\n")
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "later")
	if passes(app) {
		t.Fatal("a commit after the push must block")
	}
	git(repo, "push", "-q", "origin", "work")
	git(repo, "checkout", "-q", "-b", "elsewhere", "origin/main")
	if passes(app) {
		t.Fatal("a checkout moved off the work branch that holds the commits must block")
	}
	git(repo, "checkout", "-q", "work")
	if !passes(app) {
		t.Fatal("back on the pushed work branch must pass")
	}
	if passes("app\twork\tmissing") {
		t.Fatal("a target branch missing from origin must block")
	}

	release := filepath.Join(workspace, "svc")
	git(workspace, "clone", "-q", "-b", "release", remote, release)
	git(release, "checkout", "-qb", "fix")
	if !passes(app, "svc\tfix\trelease") {
		t.Fatal("a work branch with nothing beyond its frozen release target must pass")
	}
	if passes(app, "svc\tfix\tmain") {
		t.Fatal("measured against main the release commit is unpushed work and must block")
	}
}

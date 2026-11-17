package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func prepareBindingRoot(t *testing.T, workspace string) string {
	t.Helper()
	for _, dir := range []string{"rules", "mcp", "bindings"} {
		if _, err := EnsureControlDirectory(workspace, dir); err != nil {
			t.Fatal(err)
		}
	}
	bindingRoot, err := resetControlDirectory(workspace, "bindings", "fixer")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"rules", "mcp"} {
		if err := os.Mkdir(filepath.Join(bindingRoot, kind), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return bindingRoot
}

func TestMaterializeAgentRulesSortsWritesAndLinks(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot := prepareBindingRoot(t, workspace)
	rules := []domain.RuleDefinition{
		{ID: "low", Name: "Low", Content: "low body", Priority: 1},
		{ID: "high", Name: "High", Content: "high body", Priority: 9},
	}
	lines, err := materializeAgentRules(workspace, bindingRoot, rules)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"", "### High", "high body", "", "### Low", "low body"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("rule lines = %q, want %q", lines, want)
	}
	if rules[0].ID != "high" {
		t.Fatalf("rules should be sorted in place by priority, got %q first", rules[0].ID)
	}
	for _, rule := range rules {
		content, err := os.ReadFile(filepath.Join(bindingRoot, "rules", rule.ID+".md"))
		if err != nil || string(content) != rule.Content {
			t.Fatalf("rule %s not reachable through binding: %q %v", rule.ID, content, err)
		}
		target, err := os.Readlink(filepath.Join(bindingRoot, "rules", rule.ID+".md"))
		if err != nil || target != filepath.Join("..", "..", "..", "rules", rule.ID+".md") {
			t.Fatalf("rule %s link target = %q %v", rule.ID, target, err)
		}
	}
}

func TestMaterializeAgentRulesRequiresID(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot := prepareBindingRoot(t, workspace)
	_, err := materializeAgentRules(workspace, bindingRoot, []domain.RuleDefinition{{ID: "", Name: "Nameless"}})
	if err == nil || err.Error() != "rule id is required" {
		t.Fatalf("expected rule id error, got %v", err)
	}
}

func TestMaterializeAgentRulesEmpty(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot := prepareBindingRoot(t, workspace)
	lines, err := materializeAgentRules(workspace, bindingRoot, nil)
	if err != nil || len(lines) != 0 {
		t.Fatalf("empty rules = %q %v", lines, err)
	}
}

func TestMaterializeMCPDefinitionsWritesAndLinks(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot := prepareBindingRoot(t, workspace)
	servers := []domain.MCPServerDefinition{{ID: "docs", Name: "Docs"}}
	if err := materializeMCPDefinitions(workspace, bindingRoot, servers); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bindingRoot, "mcp", "docs.json")
	target, err := os.Readlink(link)
	if err != nil || target != filepath.Join("..", "..", "..", "mcp", "docs.json") {
		t.Fatalf("mcp link target = %q %v", target, err)
	}
	raw, err := os.ReadFile(link)
	if err != nil || !strings.Contains(string(raw), `"docs"`) {
		t.Fatalf("mcp definition not reachable through binding: %s %v", raw, err)
	}
}

func TestMaterializeMCPDefinitionsRequiresID(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot := prepareBindingRoot(t, workspace)
	err := materializeMCPDefinitions(workspace, bindingRoot, []domain.MCPServerDefinition{{ID: "  "}})
	if err == nil || err.Error() != "MCP server id is required" {
		t.Fatalf("expected MCP server id error, got %v", err)
	}
}

package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func prepareBindingRoot(t *testing.T, workspace string) (string, func(path, target string) error) {
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
	return bindingRoot, func(path, target string) error {
		return materializeManagedBinding(workspace, path, target)
	}
}

func assertMaterializedFileBinding(t *testing.T, path, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("binding must be an ordinary file snapshot: %s %v", path, err)
		}
		return
	}
	got, err := os.Readlink(path)
	if err != nil || got != target {
		t.Fatalf("binding target = %q %v, want %q", got, err, target)
	}
}

func TestMaterializeAgentRulesSortsWritesAndLinks(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot, bind := prepareBindingRoot(t, workspace)
	rules := []domain.RuleDefinition{
		{ID: "low", Name: "Low", Content: "low body", Priority: 1},
		{ID: "high", Name: "High", Content: "high body", Priority: 9},
	}
	lines, err := materializeAgentRules(workspace, bindingRoot, rules, bind)
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
		assertMaterializedFileBinding(t, filepath.Join(bindingRoot, "rules", rule.ID+".md"), filepath.Join("..", "..", "..", "rules", rule.ID+".md"))
	}
}

func TestMaterializeAgentRulesRequiresID(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot, bind := prepareBindingRoot(t, workspace)
	_, err := materializeAgentRules(workspace, bindingRoot, []domain.RuleDefinition{{ID: "", Name: "Nameless"}}, bind)
	if err == nil || err.Error() != "rule id is required" {
		t.Fatalf("expected rule id error, got %v", err)
	}
}

func TestMaterializeAgentRulesEmpty(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot, bind := prepareBindingRoot(t, workspace)
	lines, err := materializeAgentRules(workspace, bindingRoot, nil, bind)
	if err != nil || len(lines) != 0 {
		t.Fatalf("empty rules = %q %v", lines, err)
	}
}

func TestMaterializeMCPDefinitionsWritesAndLinks(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot, bind := prepareBindingRoot(t, workspace)
	servers := []domain.MCPServerDefinition{{ID: "docs", Name: "Docs"}}
	if err := materializeMCPDefinitions(workspace, bindingRoot, servers, bind); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bindingRoot, "mcp", "docs.json")
	assertMaterializedFileBinding(t, link, filepath.Join("..", "..", "..", "mcp", "docs.json"))
	raw, err := os.ReadFile(link)
	if err != nil || !strings.Contains(string(raw), `"docs"`) {
		t.Fatalf("mcp definition not reachable through binding: %s %v", raw, err)
	}
}

func TestMaterializeMCPDefinitionsRequiresID(t *testing.T) {
	workspace := t.TempDir()
	bindingRoot, bind := prepareBindingRoot(t, workspace)
	err := materializeMCPDefinitions(workspace, bindingRoot, []domain.MCPServerDefinition{{ID: "  "}}, bind)
	if err == nil || err.Error() != "MCP server id is required" {
		t.Fatalf("expected MCP server id error, got %v", err)
	}
}

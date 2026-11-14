package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestWindowsManagedBindingCopiesSkillsAndRefreshesSnapshots(t *testing.T) {
	workspace := t.TempDir()
	agent := domain.AgentDefinition{ID: "frontend", Name: "Frontend"}
	assets := domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{{ID: "fe-code", Name: "Frontend code", Description: "frontend standards", Content: "# first revision"}},
		Rules:  []domain.RuleDefinition{{ID: "frontend-rule", Name: "Frontend rule", Content: "follow frontend standards"}},
	}
	for _, revision := range []string{"first", "second"} {
		assets.Skills[0].Content = "# " + revision + " revision"
		if _, err := materialize(workspace, "snapshot-"+revision, domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{
			"skills/fe-code/SKILL.md",
			"bindings/frontend/skills/fe-code/SKILL.md",
			"frontend/skills/fe-code/SKILL.md",
		} {
			path := filepath.Join(workspace, ".agentflow", filepath.FromSlash(rel))
			raw, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(raw), "# "+revision+" revision") {
				t.Fatalf("skill snapshot %s is not current: %q %v", rel, raw, err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := rejectManagedBindingReparseInfo(path, info); err != nil {
				t.Fatalf("skill view must contain ordinary files: %v", err)
			}
		}
		if raw, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "frontend", "rules", "frontend-rule.md")); err != nil || string(raw) != "follow frontend standards" {
			t.Fatalf("agent rule snapshot missing: %q %v", raw, err)
		}
		if err := os.WriteFile(filepath.Join(workspace, ".agentflow", "skills", "fe-code", "SKILL.md"), []byte("changed after snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "frontend", "skills", "fe-code", "SKILL.md"))
		if err != nil || !strings.Contains(string(raw), "# "+revision+" revision") {
			t.Fatalf("agent snapshot changed with its source: %q %v", raw, err)
		}
	}
}

func TestWindowsManagedBindingAllowsRepeatedSkillAndRuleReferences(t *testing.T) {
	workspace := t.TempDir()
	skill := domain.SkillDefinition{ID: "frontend-skill", Name: "Frontend skill", Description: "Frontend conventions", Content: "# Frontend conventions"}
	rule := domain.RuleDefinition{ID: "frontend-rule", Name: "Frontend rule", Content: "follow frontend standards"}
	assets := domain.AgentAssetBundle{Skills: []domain.SkillDefinition{skill, skill}, Rules: []domain.RuleDefinition{rule, rule}}
	agent := domain.AgentDefinition{ID: "frontend", Name: "Frontend"}
	if _, err := materialize(workspace, "repeated", domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err != nil {
		t.Fatal(err)
	}
	for rel, expected := range map[string]string{"skills/frontend-skill/SKILL.md": skill.Content, "rules/frontend-rule.md": rule.Content} {
		raw, err := os.ReadFile(filepath.Join(workspace, ".agentflow", agent.ID, filepath.FromSlash(rel)))
		if err != nil || !strings.Contains(string(raw), expected) {
			t.Fatalf("repeated asset %s missing: %v", rel, err)
		}
	}
}

func TestWindowsManagedBindingRejectsConflictingSources(t *testing.T) {
	workspace := t.TempDir()
	control := filepath.Join(workspace, ".agentflow")
	if err := os.Mkdir(control, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(control, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(control, "view.txt")
	bindings := []managedBinding{{path: destination, target: "first.txt"}, {path: destination, target: "second.txt"}}
	if err := finishManagedBindings(workspace, bindings); err == nil {
		t.Fatal("conflicting binding sources were accepted")
	}
}

func TestWindowsManagedBindingRejectsEscapesAndPreservesExistingFiles(t *testing.T) {
	workspace := t.TempDir()
	control := filepath.Join(workspace, ".agentflow")
	if err := os.MkdirAll(filepath.Join(control, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "source.txt"), []byte("managed asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(workspace, "outside.txt")
	if err := os.WriteFile(outside, []byte("preserve outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(control, "agent", "existing.txt")
	if err := os.WriteFile(existing, []byte("preserve existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, target string }{
		{"source outside control", filepath.Join(control, "agent", "escaped.txt"), filepath.Join("..", "..", "outside.txt")},
		{"destination outside control", filepath.Join(workspace, "new.txt"), filepath.Join(".agentflow", "source.txt")},
		{"absolute source", filepath.Join(control, "agent", "absolute.txt"), outside},
		{"drive-relative source", filepath.Join(control, "agent", "drive.txt"), "C:outside.txt"},
		{"existing destination", existing, filepath.Join("..", "source.txt")},
		{"destination below source", filepath.Join(control, "agent", "recursive"), ".."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := materializeManagedBinding(workspace, tc.path, tc.target); err == nil {
				t.Fatal("unsafe binding was accepted")
			}
		})
	}
	for path, want := range map[string]string{outside: "preserve outside", existing: "preserve existing"} {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
			t.Fatalf("existing file changed: %s %q %v", path, raw, err)
		}
	}
}

func TestWindowsManagedBindingRejectsJunctions(t *testing.T) {
	for _, mode := range []string{"control root", "source parent", "source descendant", "destination parent"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "private.txt"), []byte("private outside content"), 0o600); err != nil {
				t.Fatal(err)
			}
			control := filepath.Join(workspace, ".agentflow")
			if mode == "control root" {
				createManagedBindingTestJunction(t, control, outside)
				if err := materializeManagedBinding(workspace, filepath.Join(control, "copy.txt"), "private.txt"); err == nil {
					t.Fatal("junction control root was accepted")
				}
				return
			}
			if err := os.MkdirAll(filepath.Join(control, "source"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(control, "source", "regular.txt"), []byte("asset"), 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "destination parent" {
				createManagedBindingTestJunction(t, filepath.Join(control, "agent"), outside)
				if err := materializeManagedBinding(workspace, filepath.Join(control, "agent", "copy.txt"), filepath.Join("..", "source", "regular.txt")); err == nil {
					t.Fatal("junction destination parent was accepted")
				}
				if _, err := os.Stat(filepath.Join(outside, "copy.txt")); !os.IsNotExist(err) {
					t.Fatalf("binding wrote through destination junction: %v", err)
				}
				return
			}
			if err := os.Mkdir(filepath.Join(control, "agent"), 0o700); err != nil {
				t.Fatal(err)
			}
			junction := filepath.Join(control, "source", "junction")
			createManagedBindingTestJunction(t, junction, outside)
			target := filepath.Join("..", "source")
			if mode == "source parent" {
				target = filepath.Join(target, "junction", "private.txt")
			}
			if err := materializeManagedBinding(workspace, filepath.Join(control, "agent", "copy"), target); err == nil {
				t.Fatal("junction source was accepted")
			}
		})
	}
}

func createManagedBindingTestJunction(t *testing.T, path, target string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/d", "/c", "mklink", "/J", path, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

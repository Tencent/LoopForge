package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestMaterializeSkillBinaryFiles(t *testing.T) {
	workspace := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}
	assets := domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{{
			ID: "bin", Name: "Binary", Description: "bin", Content: "# Binary",
			FileRefs: []domain.SkillFileRef{
				{Path: "assets/logo.png", Size: int64(len(png)), Mode: 0o644},
				{Path: "scripts/check.sh", Size: 12, Mode: 0o755},
			},
		}},
		SkillFiles: map[string]map[string][]byte{
			"bin": {"assets/logo.png": png, "scripts/check.sh": []byte("#!/bin/sh\n")},
		},
	}
	agent := domain.AgentDefinition{ID: "fixer", Name: "Fixer"}
	if _, err := materialize(workspace, "invoke-bin", domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(workspace, ".agentflow", "skills", "bin")

	content, err := os.ReadFile(filepath.Join(base, "assets", "logo.png"))
	if err != nil || string(content) != string(png) {
		t.Fatalf("binary skill file not materialized: %v %v", content, err)
	}
	info, err := os.Stat(filepath.Join(base, "scripts", "check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("executable bit not preserved: %v", info.Mode().Perm())
	}

	// run-context.json must not embed binary payloads.
	raw, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "runtime", "invoke-bin", "run-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "iVBORw0") || strings.Contains(string(raw), `"skillFiles"`) {
		t.Fatalf("run-context.json leaked binary payloads: %s", raw[:min(200, len(raw))])
	}
}

func TestMaterializeUploadedSkillMdOverridesPayloadContent(t *testing.T) {
	workspace := t.TempDir()
	assets := domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{{
			ID: "mixed", Name: "Mixed", Description: "payload", Content: "this should NOT be used",
			FileRefs: []domain.SkillFileRef{{Path: "SKILL.md", Size: 12, Mode: 0o644}},
		}},
		SkillFiles: map[string]map[string][]byte{"mixed": {"SKILL.md": []byte("---\nname: custom\n---\n\n# from upload\n")}},
	}
	agent := domain.AgentDefinition{ID: "fixer", Name: "Fixer"}
	if _, err := materialize(workspace, "invoke-md", domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "skills", "mixed", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "# from upload") {
		t.Fatalf("uploaded SKILL.md body was not used: %q", text)
	}
	if !strings.Contains(text, "name: mixed\n") || strings.Contains(text, "name: custom\n") {
		t.Fatalf("mismatched frontmatter name was not aligned to catalog id: %q", text)
	}
}

func TestMaterializeUploadedSkillMdGetsFrontmatterWhenMissing(t *testing.T) {
	workspace := t.TempDir()
	assets := domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{{
			ID: "sop", Name: "SOP", Description: "Issue 修复 SOP", Content: "payload should not win",
			FileRefs: []domain.SkillFileRef{{Path: "SKILL.md", Size: 12, Mode: 0o644}},
		}},
		SkillFiles: map[string]map[string][]byte{"sop": {"SKILL.md": []byte("# DNSPod Issue 修复\n")}},
	}
	agent := domain.AgentDefinition{ID: "tester", Name: "Tester"}
	if _, err := materialize(workspace, "invoke-sop", domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "skills", "sop", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.HasPrefix(strings.TrimSpace(text), "---") || !strings.Contains(text, "name: sop") || !strings.Contains(text, "# DNSPod Issue 修复") {
		t.Fatalf("missing frontmatter on uploaded SKILL.md: %q", text)
	}
}

func TestMaterializeSelectedSkillsListsInvocationNameAndRoot(t *testing.T) {
	workspace := t.TempDir()
	assets := domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{
			{ID: "unit-test-gen", Name: "单元测试生成", Description: "单元测试生成", Content: "unused"},
			{ID: "dnspod-issue-fix", Name: "Issue 修复", Description: "SOP overview", Content: "# SOP\n"},
		},
		SkillFiles: map[string]map[string][]byte{
			"unit-test-gen": {"SKILL.md": []byte("---\nname: unit-test\ndescription: 单测治理总线（迁移增强版）\n---\n\n# Unit Test\n")},
		},
	}
	agent := domain.AgentDefinition{ID: "dnspod-unit-test-engineer", Name: "DNSPod 单元测试工程师", Instructions: "写增量单测。"}
	if _, err := materialize(workspace, "invoke-skills", domain.ExecutorDefinition{Kind: "codebuddy"}, agent, assets); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "dnspod-unit-test-engineer", "agent-system.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, needle := range []string{
		"If this CLI exposes a native Skill tool or slash command",
		"Otherwise read SKILL.md, then follow it using the skill root",
		"name: `unit-test-gen` (id: unit-test-gen)",
		"description: 单测治理总线（迁移增强版）",
		"root: `.agentflow/dnspod-unit-test-engineer/skills/unit-test-gen`",
		"name: `dnspod-issue-fix` (id: dnspod-issue-fix)",
		"root: `.agentflow/dnspod-unit-test-engineer/skills/dnspod-issue-fix`",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("agent-system.md missing %q:\n%s", needle, text)
		}
	}
	if strings.Contains(text, "name: `unit-test`") {
		t.Fatalf("stale frontmatter name leaked into listing:\n%s", text)
	}
	if strings.Contains(text, "Read the referenced SKILL.md before applying one") {
		t.Fatalf("legacy Read-only skill hint is still present:\n%s", text)
	}
	skillMD, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "skills", "unit-test-gen", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skillMD), "name: unit-test-gen\n") || strings.Contains(string(skillMD), "name: unit-test\n") {
		t.Fatalf("workspace SKILL.md was not aligned: %q", skillMD)
	}
}

func TestMaterializeSkillBinaryPathTraversalRejected(t *testing.T) {
	workspace := t.TempDir()
	assets := domain.AgentAssetBundle{
		Skills: []domain.SkillDefinition{{
			ID: "evil", Name: "Evil", Description: "evil", Content: "# evil",
			FileRefs: []domain.SkillFileRef{{Path: "../escape.txt", Size: 5, Mode: 0o644}},
		}},
		SkillFiles: map[string]map[string][]byte{"evil": {"../escape.txt": []byte("evil")}},
	}
	agent := domain.AgentDefinition{ID: "probe", Name: "Probe"}
	if _, err := materialize(workspace, "invoke-evil", domain.ExecutorDefinition{Kind: "codex"}, agent, assets); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

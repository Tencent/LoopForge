package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestEnsureSkillFrontmatterPreservesMatchingName(t *testing.T) {
	raw := []byte("---\nname: mixed\ndescription: payload\ncompatibility: php\n---\n\n# from upload\n")
	got := ensureSkillFrontmatter(raw, "mixed", "payload")
	if string(got) != string(raw) {
		t.Fatalf("matching frontmatter was rewritten: %q", got)
	}
}

func TestEnsureSkillFrontmatterFillsMissingDescription(t *testing.T) {
	raw := []byte("---\nname: mixed\n---\n\n# body\n")
	got := string(ensureSkillFrontmatter(raw, "mixed", "catalog desc"))
	if !strings.Contains(got, "name: mixed\n") {
		t.Fatalf("name lost: %q", got)
	}
	if !strings.Contains(got, `description: "catalog desc"`) && !strings.Contains(got, "description: catalog desc") {
		t.Fatalf("description was not filled: %q", got)
	}
}

func TestEnsureSkillFrontmatterAddsNameAndDescription(t *testing.T) {
	got := string(ensureSkillFrontmatter([]byte("# DNSPod Issue 修复\n"), "dnspod-issue-fix", "openclaw-universal SMALL"))
	if !strings.HasPrefix(got, "---\nname: dnspod-issue-fix\n") {
		t.Fatalf("missing name frontmatter: %q", got)
	}
	if !strings.Contains(got, `description: "openclaw-universal SMALL"`) && !strings.Contains(got, "description: openclaw-universal SMALL") {
		t.Fatalf("missing description: %q", got)
	}
	if !strings.Contains(got, "# DNSPod Issue 修复") {
		t.Fatalf("body was dropped: %q", got)
	}
}

func TestEnsureSkillFrontmatterAlignsMismatchedName(t *testing.T) {
	raw := []byte("---\nname: unit-test\ndescription: 单测治理总线\ncompatibility: php\n---\n\n# Unit Test\n")
	got := string(ensureSkillFrontmatter(raw, "unit-test-gen", "ignored catalog desc"))
	if !strings.Contains(got, "name: unit-test-gen\n") {
		t.Fatalf("name was not aligned to catalog id: %q", got)
	}
	if strings.Contains(got, "name: unit-test\n") {
		t.Fatalf("stale frontmatter name kept: %q", got)
	}
	if !strings.Contains(got, "description: 单测治理总线") {
		t.Fatalf("original description was dropped: %q", got)
	}
	if !strings.Contains(got, "compatibility: php") {
		t.Fatalf("extra frontmatter keys were dropped: %q", got)
	}
	if !strings.Contains(got, "# Unit Test") {
		t.Fatalf("body was dropped: %q", got)
	}
}

func TestParseSkillFrontmatterPrefersUploadedName(t *testing.T) {
	name, desc := parseSkillFrontmatter([]byte("---\nname: unit-test\ndescription: \"单测治理总线\"\n---\n\n# Unit Test\n"))
	if name != "unit-test" || desc != "单测治理总线" {
		t.Fatalf("parsed %q %q", name, desc)
	}
}

func TestCompileSelectedSkillAlignsNameToCatalogID(t *testing.T) {
	item, err := compileSelectedSkill(".agentflow/tester", domain.SkillDefinition{
		ID: "unit-test-gen", Description: "单元测试生成", Content: "ignored",
	}, map[string][]byte{"SKILL.md": []byte("---\nname: unit-test\ndescription: 单测治理总线\n---\n\n# Unit Test\n")})
	if err != nil {
		t.Fatal(err)
	}
	if item.name != "unit-test-gen" || item.id != "unit-test-gen" {
		t.Fatalf("name/id = %q %q", item.name, item.id)
	}
	if item.desc != "单测治理总线" {
		t.Fatalf("desc = %q", item.desc)
	}
	if !strings.Contains(string(item.body), "name: unit-test-gen\n") || strings.Contains(string(item.body), "name: unit-test\n") {
		t.Fatalf("workspace SKILL.md was not aligned: %q", item.body)
	}
	if item.root != ".agentflow/tester/skills/unit-test-gen" || item.path != ".agentflow/tester/skills/unit-test-gen/SKILL.md" {
		t.Fatalf("paths = %q %q", item.root, item.path)
	}
}

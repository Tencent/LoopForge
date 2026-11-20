package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"github.com/goccy/go-yaml"
)

func TestSkillMetadataReadsYAMLScalars(t *testing.T) {
	for _, tc := range []struct{ name, description, want string }{
		{"folded", ">-\n  Review changes\n  before publishing", "Review changes before publishing"},
		{"literal", "|-\n  First line\n  Second line", "First line\nSecond line"},
		{"single quote", "'Review a user''s changes'", "Review a user's changes"},
		{"comment", "Review changes # metadata comment", "Review changes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "---\nname: review\ndescription: " + tc.description + "\n---\n\n# Instructions\n"
			item, err := compileSelectedSkill(".agentflow/agent", domain.SkillDefinition{ID: "review"}, map[string][]byte{"SKILL.md": []byte(raw)})
			if err != nil || item.desc != tc.want {
				t.Fatalf("description=%q err=%v; want %q", item.desc, err, tc.want)
			}
			if string(item.body) != raw {
				t.Fatal("matching frontmatter should retain the uploaded document")
			}
		})
	}
}

func TestSkillNameReplacementKeepsYAMLValid(t *testing.T) {
	raw := []byte("---\nname: >-\n  old-name\ndescription: Keep the instructions\nmetadata:\n  name: nested-metadata\n---\n\n# Instructions\n")
	got := ensureSkillFrontmatter(raw, "review", "")
	header, _, ok := splitFrontmatter(got)
	var metadata map[string]any
	if !ok || yaml.Unmarshal([]byte(header), &metadata) != nil || metadata["name"] != "review" {
		t.Fatalf("replacement damaged YAML: %s", got)
	}
	if !strings.Contains(string(got), "nested-metadata") || !strings.Contains(string(got), "# Instructions") {
		t.Fatalf("replacement lost metadata or body: %s", got)
	}
}

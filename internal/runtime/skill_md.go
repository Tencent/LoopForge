package runtime

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

type selectedSkill struct {
	skill domain.SkillDefinition
	id    string
	name  string
	desc  string
	root  string
	path  string
	body  []byte
}

func compileSelectedSkill(agentRel string, skill domain.SkillDefinition, files map[string][]byte) (selectedSkill, error) {
	id := safeID(skill.ID)
	if id == "" {
		return selectedSkill{}, fmt.Errorf("skill id is required")
	}
	raw := []byte(skill.Content)
	if data, ok := files["SKILL.md"]; ok {
		raw = data
	}
	// Workspace-only: never persist these patches back to the catalog.
	body := ensureSkillFrontmatter(raw, id, skill.Description)
	name, desc := parseSkillFrontmatter(body)
	if name != id {
		name = id
	}
	if desc == "" {
		desc = strings.TrimSpace(skill.Description)
	}
	root := filepath.ToSlash(filepath.Join(agentRel, "skills", id))
	return selectedSkill{
		skill: skill,
		id:    id,
		name:  name,
		desc:  desc,
		root:  root,
		path:  root + "/SKILL.md",
		body:  body,
	}, nil
}

func ensureSkillFrontmatter(raw []byte, id, description string) []byte {
	if !hasClosedFrontmatter(raw) {
		return prependSkillFrontmatter(raw, id, description)
	}
	body := raw
	name, desc := parseSkillFrontmatter(body)
	if name != id {
		body = setFrontmatterField(body, "name", id)
	}
	if strings.TrimSpace(desc) == "" && strings.TrimSpace(description) != "" {
		body = setFrontmatterField(body, "description", description)
	}
	return body
}

func prependSkillFrontmatter(raw []byte, id, description string) []byte {
	body := strings.TrimSpace(string(raw))
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(yamlFrontmatterScalar(id))
	b.WriteString("\ndescription: ")
	b.WriteString(yamlFrontmatterScalar(description))
	b.WriteString("\n---\n")
	if body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	return []byte(b.String())
}

func hasClosedFrontmatter(raw []byte) bool {
	_, _, ok := splitFrontmatter(raw)
	return ok
}

func setFrontmatterField(raw []byte, key, value string) []byte {
	header, closing, ok := splitFrontmatter(raw)
	if !ok {
		return raw
	}
	file, err := parser.ParseBytes([]byte(header), parser.ParseComments)
	if err != nil {
		return raw
	}
	root, err := yaml.PathString("$")
	if err != nil {
		return raw
	}
	// Merge the top-level key as YAML so replacing a block scalar removes
	// its continuation lines and never changes a nested metadata key.
	if err := root.MergeFromReader(file, strings.NewReader(key+": "+yamlFrontmatterScalar(value))); err != nil {
		return raw
	}
	header = file.String()
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(header)
	if !strings.HasSuffix(header, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(closing)
	if !strings.HasSuffix(closing, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func splitFrontmatter(raw []byte) (header, closing string, ok bool) {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "---") {
		return "", "", false
	}
	rest := strings.TrimLeft(trimmed[3:], "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", false
	}
	return rest[:end], rest[end+1:], true
}

func parseSkillFrontmatter(body []byte) (name, description string) {
	header, _, ok := splitFrontmatter(body)
	if !ok {
		return "", ""
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(header), &metadata); err != nil {
		return "", ""
	}
	return metadata.Name, metadata.Description
}

var yamlPlainScalar = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func yamlFrontmatterScalar(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return `""`
	}
	if yamlPlainScalar.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

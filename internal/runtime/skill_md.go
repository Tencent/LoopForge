package runtime

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"agentflow/internal/domain"
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
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "---") {
		return false
	}
	rest := strings.TrimLeft(trimmed[3:], "\r\n")
	return strings.Contains(rest, "\n---")
}

func setFrontmatterField(raw []byte, key, value string) []byte {
	header, closing, ok := splitFrontmatter(raw)
	if !ok {
		return raw
	}
	line := key + ": " + yamlFrontmatterScalar(value)
	lines := strings.Split(header, "\n")
	found := false
	for i, existing := range lines {
		k, _, cut := strings.Cut(strings.TrimRight(existing, "\r"), ":")
		if cut && strings.TrimSpace(k) == key {
			lines[i] = line
			found = true
			break
		}
	}
	if !found {
		if strings.TrimSpace(header) == "" {
			lines = []string{line}
		} else {
			lines = append([]string{line}, lines...)
		}
	}
	header = strings.Join(lines, "\n")
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
	for _, line := range strings.Split(header, "\n") {
		key, val, cut := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if !cut {
			continue
		}
		key = strings.TrimSpace(key)
		val = unquoteFrontmatterValue(strings.TrimSpace(val))
		switch key {
		case "name":
			name = val
		case "description":
			description = val
		}
	}
	return name, description
}

func unquoteFrontmatterValue(val string) string {
	if val == "" {
		return ""
	}
	if strings.HasPrefix(val, `"`) {
		if unquoted, err := strconv.Unquote(val); err == nil {
			return unquoted
		}
	}
	if len(val) >= 2 && strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") {
		return val[1 : len(val)-1]
	}
	return val
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

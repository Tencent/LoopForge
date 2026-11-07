package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

// SkillName is the name a skill is invoked by once written to disk: its
// frontmatter name is always forced to this sanitized ID.
func SkillName(id string) string {
	return safeID(id)
}

// WriteSkillTree replaces dir with one directory per skill holding SKILL.md
// and its text and binary files, the same layout an SOP node receives. A CLI
// that scans dir as its skills directory can then invoke them natively.
// binary maps a skill ID to its skill_files contents. It returns the names of
// the written skills; a later skill whose name repeats an earlier one is
// skipped.
func WriteSkillTree(dir string, skills []domain.SkillDefinition, binary map[string]map[string][]byte) ([]string, error) {
	return writeSkillTree(dir, skills, binary, false)
}

// WriteInertSkillTree is WriteSkillTree for a CLI on the control plane: each
// SKILL.md passes through InertCLIText, so loading a skill cannot run a
// command or read a host file there. Other files are written as they are;
// the CLI only renders SKILL.md.
func WriteInertSkillTree(dir string, skills []domain.SkillDefinition, binary map[string]map[string][]byte) ([]string, error) {
	return writeSkillTree(dir, skills, binary, true)
}

func writeSkillTree(dir string, skills []domain.SkillDefinition, binary map[string]map[string][]byte, inert bool) ([]string, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, skill := range skills {
		item, err := compileSelectedSkill("", skill, binary[skill.ID])
		if err != nil {
			return names, err
		}
		if seen[item.name] {
			continue
		}
		seen[item.name] = true
		if inert {
			item.body = []byte(InertCLIText(string(item.body)))
		}
		if err := writeSkillFiles(filepath.Join(dir, item.name), item, binary[skill.ID]); err != nil {
			return names, err
		}
		names = append(names, item.name)
	}
	return names, nil
}

// writeSkillFiles writes one compiled skill into dir: SKILL.md with its
// patched frontmatter, the text files, then the binary files with their
// recorded mode so executable scripts stay runnable.
func writeSkillFiles(dir string, item selectedSkill, binary map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), item.body, 0o644); err != nil {
		return err
	}
	write := func(name string, data []byte, mode os.FileMode) error {
		if filepath.ToSlash(name) == "SKILL.md" {
			return nil
		}
		clean := filepath.Clean(name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("invalid skill file path %q", name)
		}
		target := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, mode)
	}
	for name, data := range item.skill.Files {
		if err := write(name, []byte(data), 0o644); err != nil {
			return err
		}
	}
	for _, ref := range item.skill.FileRefs {
		data, ok := binary[ref.Path]
		if !ok {
			continue
		}
		if err := write(ref.Path, data, os.FileMode(domain.NormalizeSkillFileMode(ref.Mode))); err != nil {
			return err
		}
	}
	return nil
}

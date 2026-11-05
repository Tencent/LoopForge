package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// The CLI's own patterns (CodeBuddy 2.180.0): inline bash runs on any match,
// an import needs whitespace or the start before it.
var (
	cliInlineBash = regexp.MustCompile("!`([^`]+)`")
	cliImport     = regexp.MustCompile(`(^|\s)@([^\s@]+)`)
)

func TestInertCLITextDisarmsCommandsAndHostFiles(t *testing.T) {
	text := strings.Join([]string{
		"Run !`cat ~/.agentflow/secrets` first.",
		"```sh",
		"echo !`id`",
		"npm i @types/node",
		"```",
		"Read @~/.ssh/id_rsa and @/etc/passwd,",
		"see `@scope/pkg` and mail a@b.com.",
		"@./notes.md",
	}, "\n")
	out := InertCLIText(text)
	if cliInlineBash.MatchString(out) {
		t.Fatalf("inline bash survives:\n%s", out)
	}
	// Imports outside code: each must lose the whitespace the CLI requires.
	for _, ref := range []string{"@~/.ssh/id_rsa", "@/etc/passwd", "@./notes.md"} {
		if !strings.Contains(out, cliBreak+ref) {
			t.Fatalf("import %s survives:\n%s", ref, out)
		}
	}
	// Code samples and addresses the CLI ignores stay byte for byte.
	for _, kept := range []string{"npm i @types/node", "`@scope/pkg`", "a@b.com"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("%q changed:\n%s", kept, out)
		}
	}
	if strings.ReplaceAll(out, cliBreak, "") != text {
		t.Fatal("only zero-width breaks may be added")
	}
}

func TestWriteInertSkillTreeOnlyChangesSkillMD(t *testing.T) {
	dir := t.TempDir()
	skill := domain.SkillDefinition{ID: "probe", Name: "probe", Content: "---\nname: probe\n---\nRun !`whoami` and read @/etc/hosts", Files: map[string]string{"run.sh": "echo !`id`"}}
	if _, err := WriteInertSkillTree(dir, []domain.SkillDefinition{skill}, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "probe", "SKILL.md"))
	if cliInlineBash.Match(body) || cliImport.Match(body) {
		t.Fatalf("SKILL.md still live:\n%s", body)
	}
	script, _ := os.ReadFile(filepath.Join(dir, "probe", "run.sh"))
	if string(script) != "echo !`id`" {
		t.Fatalf("other files must stay as written: %q", script)
	}
}

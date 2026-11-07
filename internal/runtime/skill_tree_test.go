package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestWriteSkillTreeReplacesSkills(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(dir, "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	skills := []domain.SkillDefinition{
		{ID: "Gen_Tests", Description: "生成测试", Content: "# Gen", Files: map[string]string{"ref/a.md": "A"}, FileRefs: []domain.SkillFileRef{{Path: "scripts/run.sh", Mode: 0o755}}},
		{ID: "GEN_tests", Content: "重名"},
	}
	names, err := WriteSkillTree(dir, skills, map[string]map[string][]byte{"Gen_Tests": {"scripts/run.sh": []byte("echo hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "gentests" {
		t.Fatalf("names=%v", names)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "gentests", "SKILL.md"))
	if !strings.Contains(string(body), "name: gentests") || !strings.Contains(string(body), "# Gen") {
		t.Fatalf("SKILL.md=%s", body)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "gentests", "ref", "a.md")); string(data) != "A" {
		t.Fatal("text file missing")
	}
	if info, err := os.Stat(filepath.Join(dir, "gentests", "scripts", "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binary script not executable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); !os.IsNotExist(err) {
		t.Fatal("stale skill kept")
	}
	if _, err := WriteSkillTree(dir, []domain.SkillDefinition{{ID: "bad", Files: map[string]string{"../x": "x"}}}, nil); err == nil {
		t.Fatal("path escape accepted")
	}
}

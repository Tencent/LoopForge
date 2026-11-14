package httpapi

import (
	"path/filepath"
	"slices"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestConversationAssetHomeFollowsEngine(t *testing.T) {
	root := "/conv"
	cases := map[string]string{
		assistantEngineCodeBuddy:    filepath.Join(root, "home", ".codebuddy"),
		assistantEngineCodex:        filepath.Join(root, "home", ".codex"),
		assistantEngineClaude:       filepath.Join(root, "home", ".claude"),
		"":                          filepath.Join(root, "home", ".codebuddy"),
		runtime.AgentCLIKimi:        filepath.Join(root, "home", ".agents"),
		runtime.AgentCLIOpenCode:    filepath.Join(root, "home", ".agents"),
		runtime.AgentCLIPi:          filepath.Join(root, "home", ".pi", "agent"),
		runtime.AgentCLIAntigravity: filepath.Join(root, "home", ".gemini", "config"),
	}
	for engine, want := range cases {
		if got := conversationAssetHome(root, engine); got != want {
			t.Errorf("engine %q: home=%q, want %q", engine, got, want)
		}
	}
}

func TestConversationSkillsMergesSourcesFirstWins(t *testing.T) {
	h, _ := workbenchFixture(t)
	for _, skill := range []domain.SkillDefinition{
		{ID: "lint", Name: "代码检查", ProjectID: "workbench", Content: "项目版"},
		{ID: "asset", Name: "带文件", ProjectID: "elsewhere", Content: "a"},
		{ID: "plain", Name: "无清单", ProjectID: "elsewhere", Content: "p"},
	} {
		if err := h.repo.PutSkill(skill); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"asset", "plain"} {
		if err := h.repo.PutSkillFiles(id, []repo.SkillFileInput{{Path: "data.bin", Content: []byte(id)}}); err != nil {
			t.Fatal(err)
		}
	}
	req := assistantRequest{
		ProjectID: " workbench ",
		roles: []roleSubagent{{
			Skills: []domain.SkillDefinition{
				{ID: "lint", Content: "角色版"},
				{ID: "role-only", Content: "r"},
			},
			SkillFiles: map[string]map[string][]byte{"role-only": {"x.txt": []byte("x")}},
		}},
		skills: []domain.SkillDefinition{
			{ID: "role-only", Content: "提到版"},
			{ID: "asset", FileRefs: []domain.SkillFileRef{{Path: "data.bin"}}},
			{ID: "plain"},
		},
	}
	skills, files := h.conversationSkills(req)
	var ids []string
	for _, skill := range skills {
		ids = append(ids, skill.ID)
	}
	if want := []string{"lint", "role-only", "asset", "plain"}; !slices.Equal(ids, want) {
		t.Fatalf("skills=%v, want %v", ids, want)
	}
	if skills[0].Content != "项目版" || skills[1].Content != "r" {
		t.Fatalf("first occurrence must win: %+v", skills[:2])
	}
	if string(files["role-only"]["x.txt"]) != "x" {
		t.Fatalf("role skill files missing: %v", files)
	}
	if string(files["asset"]["data.bin"]) != "asset" {
		t.Fatalf("skill with FileRefs must load its files: %v", files)
	}
	if _, ok := files["plain"]; ok {
		t.Fatalf("skill without FileRefs must not load files: %v", files)
	}
	if _, ok := files["lint"]; ok {
		t.Fatalf("project skill without files gets no entry: %v", files)
	}
}

func TestConversationSkillsWithoutProjectSkipsCatalog(t *testing.T) {
	h, _ := workbenchFixture(t)
	if err := h.repo.PutSkill(domain.SkillDefinition{ID: "lint", Name: "代码检查", ProjectID: "workbench", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	skills, files := h.conversationSkills(assistantRequest{ProjectID: "  "})
	if len(skills) != 0 || len(files) != 0 {
		t.Fatalf("no project means no catalog skills: %v %v", skills, files)
	}
}

package runtime

import (
	"slices"
	"testing"

	"agentflow/internal/domain"
)

func TestCursorModelTable(t *testing.T) {
	levels := domain.ExecutorEffortLevels[AgentCLICursor]
	ids, raws := map[string]bool{}, map[string]bool{}
	for _, model := range cursorModels {
		if ids[model.ID] || model.Name == "" {
			t.Fatalf("duplicate or unnamed family %+v", model)
		}
		ids[model.ID] = true
		for effort, raw := range model.Variants {
			if (effort != "" && !slices.Contains(levels, effort)) || raws[raw] {
				t.Fatalf("%s: bad variant %q=%q", model.ID, effort, raw)
			}
			raws[raw] = true
		}
		if _, ok := model.Variants[model.Default]; !ok || (model.Default == "") != (len(model.Variants) == 1 && model.Variants[""] != "") {
			t.Fatalf("%s: default %q outside %v", model.ID, model.Default, model.Variants)
		}
	}
}

func TestCursorModelID(t *testing.T) {
	cases := []struct{ id, effort, want string }{
		{"cursor-grok-4.6", "", "cursor-grok-4.6-high"},
		{"cursor-grok-4.6", "low", "cursor-grok-4.6-low"},
		{"gpt-5.5", "xhigh", "gpt-5.5-extra-high"},
		{"gpt-5.3-codex", "medium", "gpt-5.3-codex"},
		{"gpt-5.6-sol-fast", "none", "gpt-5.6-sol-none-fast"},
		{"claude-4.6-opus-thinking", "max", "claude-4.6-opus-max-thinking"},
		{"claude-opus-5-5", "", "claude-opus-5-5-medium"},
		// Missing efforts take the closest one, the stronger on a tie.
		{"kimi-k3", "medium", "kimi-k3-high"},
		{"glm-5.2", "none", "glm-5.2-high"},
		{"gemini-4-argon", "max", "gemini-4-argon-high"},
		{"composer-2.5", "high", "composer-2.5"},
		{"auto", "", "auto"},
		// Raw IDs and unknown models pass through.
		{"claude-opus-5-high", "low", "claude-opus-5-high"},
		{"some-new-model", "high", "some-new-model"},
	}
	for _, c := range cases {
		if got := CursorModelID(c.id, c.effort); got != c.want {
			t.Errorf("CursorModelID(%q, %q) = %q, want %q", c.id, c.effort, got, c.want)
		}
	}
	if got := CursorModelEfforts("kimi-k3"); !slices.Equal(got, []string{"low", "high", "max"}) {
		t.Errorf("kimi-k3 efforts: %v", got)
	}
	if got := CursorModelEfforts("composer-2.5"); got != nil {
		t.Errorf("composer efforts: %v", got)
	}
	if family, ok := CursorModelFamily("gpt-5.6-terra-xhigh-fast"); !ok || family.ID != "gpt-5.6-terra-fast" {
		t.Errorf("family of a fast variant: %+v %v", family, ok)
	}
	argv := AgentCLICommand(AgentCLICursor, AgentCLICommandOptions{Model: "claude-opus-4-8-thinking", Effort: "xhigh", Prompt: "p"}).Argv
	if i := slices.Index(argv, "--model"); i < 0 || argv[i+1] != "claude-opus-4-8-thinking-xhigh" {
		t.Errorf("cursor argv: %q", argv)
	}
}

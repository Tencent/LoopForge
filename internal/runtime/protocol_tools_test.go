package runtime

import "testing"

func TestSummarizeSkillToolCallIncludesSkillName(t *testing.T) {
	got := summarizeToolCall("Skill", map[string]any{"skill": "unit-test"})
	if got != "Skill unit-test" {
		t.Fatalf("summarize = %q", got)
	}
	attrs := slimToolInput(map[string]any{"skill": "unit-test", "args": "ignored"})
	input, _ := attrs["input"].(map[string]string)
	if input["skill"] != "unit-test" {
		t.Fatalf("slim attributes = %#v", attrs)
	}
}

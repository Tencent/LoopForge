package httpapi

import "testing"

// A duplicated enum value makes strict validators reject the whole tool
// definition, which takes every confirm-mode draft submission with it. The
// schema must therefore list each writable tool exactly once, in every
// execution mode.
func TestSubmitAssistantReplySchemaEnumHasNoDuplicates(t *testing.T) {
	modes := map[string]assistantExecMode{
		"confirm":           {},
		"auto":              {AutoWrite: true},
		"auto-confirm-risk": {AutoWrite: true, ConfirmDangerous: true},
		"assisted":          {AutoWrite: true, ReviewWrites: true},
	}
	for name, mode := range modes {
		schema := submitAssistantReplySchema(mode)
		drafts, ok := schema["properties"].(map[string]any)["drafts"].(map[string]any)
		if !ok {
			t.Fatalf("%s: drafts is missing", name)
		}
		items, ok := drafts["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s: drafts.items is missing", name)
		}
		props, ok := items["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: drafts.items.properties is missing", name)
		}
		toolSchema, ok := props["tool"].(map[string]any)
		if !ok {
			t.Fatalf("%s: drafts.items.properties.tool is missing", name)
		}
		rawEnum, ok := toolSchema["enum"].([]any)
		if !ok || len(rawEnum) == 0 {
			t.Fatalf("%s: tool enum is missing or empty", name)
		}
		seen := make(map[string]struct{}, len(rawEnum))
		for _, entry := range rawEnum {
			value, ok := entry.(string)
			if !ok || value == "" {
				t.Fatalf("%s: tool enum holds a non-string or empty entry: %#v", name, entry)
			}
			if _, dup := seen[value]; dup {
				t.Fatalf("%s: tool enum repeats %q", name, value)
			}
			seen[value] = struct{}{}
		}
		if _, ok := seen["assistant_tasks_save"]; !ok {
			t.Fatalf("%s: assistant_tasks_save must stay a draft target", name)
		}
	}
}

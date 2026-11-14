package runtime

import (
	"encoding/json"
	"testing"
)

func zcodePreToolUseCommand(t *testing.T, document map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	configured, ok := document["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks missing: %#v", document)
	}
	events, ok := configured["events"].(map[string]any)
	if !ok {
		t.Fatalf("hooks.events missing: %#v", configured)
	}
	entries, ok := events["PreToolUse"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("PreToolUse = %#v", events["PreToolUse"])
	}
	hooks := entries[0].(map[string]any)["hooks"].([]any)
	if len(hooks) != 1 {
		t.Fatalf("PreToolUse hooks = %#v", hooks)
	}
	return configured, hooks[0].(map[string]any)
}

func TestMergeZCodeApprovalHookEmptyDocument(t *testing.T) {
	document := map[string]any{}
	mergeZCodeApprovalHook(document, "python3 '/tmp/hook.py'")
	configured, hook := zcodePreToolUseCommand(t, document)
	if configured["enabled"] != true {
		t.Fatalf("hooks.enabled = %#v", configured["enabled"])
	}
	if hook["type"] != "command" || hook["command"] != "python3 '/tmp/hook.py'" || hook["onError"] != "deny" || hook["timeout"] != conversationApprovalHookWatchdog {
		t.Fatalf("hook = %#v", hook)
	}
}

func TestMergeZCodeApprovalHookKeepsOtherEntries(t *testing.T) {
	var document map[string]any
	raw := `{"mcpServers":{"agentflow_session":{"type":"http"}},"hooks":{"enabled":false,"events":{"SessionStart":[{"hooks":[{"type":"command","command":"cleanup"}]}],"PreToolUse":[{"matcher":"old"}]}}}`
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	mergeZCodeApprovalHook(document, "launch")
	if _, ok := document["mcpServers"].(map[string]any)["agentflow_session"]; !ok {
		t.Fatalf("mcpServers dropped: %#v", document)
	}
	configured, hook := zcodePreToolUseCommand(t, document)
	if configured["enabled"] != true {
		t.Fatalf("hooks.enabled = %#v", configured["enabled"])
	}
	if _, ok := configured["events"].(map[string]any)["SessionStart"]; !ok {
		t.Fatalf("SessionStart dropped: %#v", configured)
	}
	if hook["command"] != "launch" {
		t.Fatalf("old PreToolUse not replaced: %#v", hook)
	}
}

func TestMergeZCodeApprovalHookReplacesMalformedSections(t *testing.T) {
	document := map[string]any{"hooks": "invalid"}
	mergeZCodeApprovalHook(document, "launch")
	configured, _ := zcodePreToolUseCommand(t, document)
	if configured["enabled"] != true {
		t.Fatalf("hooks.enabled = %#v", configured["enabled"])
	}

	document = map[string]any{"hooks": map[string]any{"events": []any{}}}
	mergeZCodeApprovalHook(document, "launch")
	zcodePreToolUseCommand(t, document)
}

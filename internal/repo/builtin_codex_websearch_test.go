package repo

import (
	"slices"
	"testing"
)

// Issue #1313: the Codex CLI always advertises its builtin web_search tool
// in /v1/responses requests. OpenAI-compatible gateways fronting non-OpenAI
// providers reject the whole request with 400 responses_feature_not_supported,
// so every SOP node behind such a gateway fails deterministically. The chat
// path already disables the builtin (-c web_search="disabled") and runs allow
// MCP as their only tool surface, so the builtin must stay disabled here too.
func TestBuiltinCodexExecutorDisablesBuiltinWebSearch(t *testing.T) {
	executor, ok := BuiltInExecutor("codex")
	if !ok {
		t.Fatal("builtin codex executor missing")
	}
	idx := slices.Index(executor.Args, `web_search="disabled"`)
	if idx < 0 {
		t.Fatalf("codex executor must pass -c web_search=\"disabled\": %q", executor.Args)
	}
	if idx == 0 || executor.Args[idx-1] != "-c" {
		t.Fatalf(`web_search="disabled" must follow a -c flag: %q`, executor.Args)
	}
}

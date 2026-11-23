package runtime

import (
	"net/url"
	"strings"
)

// BindClaudeCodeEndpoint configures the Anthropic Messages endpoint and its
// provider-specific protocol options for both local and runner invocations.
func BindClaudeCodeEndpoint(env map[string]string, baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	env["ANTHROPIC_BASE_URL"] = baseURL
	// DeepSeek's Anthropic endpoint rejects inline beta tool definitions.
	// Prevent new deferred tool searches and strip beta fields when Claude
	// Code replays tool-search results already stored in older sessions.
	if endpoint, err := url.Parse(baseURL); err == nil && strings.EqualFold(endpoint.Hostname(), "api.deepseek.com") {
		env["ENABLE_TOOL_SEARCH"] = "false"
		env["CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS"] = "1"
	}
}

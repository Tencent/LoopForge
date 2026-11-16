package domain

import "strings"

const ExecutorKindDeepSeek = "deepseek"

// BinaryProvider returns the CLI runtime provider for an executor kind.
// Keep the original kind for credential binding and Runner capabilities.
func BinaryProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if strings.EqualFold(provider, ExecutorKindDeepSeek) {
		return "codex"
	}
	return provider
}

const DeepSeekDefaultModel = "deepseek-flash"
const DeepSeekDefaultBaseURL = "https://api.deepseek.com"

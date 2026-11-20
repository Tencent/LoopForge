package httpapi

// Codex last is one model call; total is the thread's cumulative snapshot.
// Start with last, then use changes in total to deduplicate updates without
// charging a resumed thread's previous turns to this invocation.
type codexTokenUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningTokens       int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

func (s *codexAppServer) collectUsage(last codexTokenUsage, total *codexTokenUsage) []string {
	delta := last
	if total != nil && s.usageTotal != nil {
		prev := *s.usageTotal
		if total.InputTokens < prev.InputTokens || total.OutputTokens < prev.OutputTokens || total.CachedInputTokens < prev.CachedInputTokens || total.CacheWriteInputTokens < prev.CacheWriteInputTokens || total.TotalTokens < prev.TotalTokens {
			return nil
		}
		delta = codexTokenUsage{
			InputTokens: total.InputTokens - prev.InputTokens, CachedInputTokens: total.CachedInputTokens - prev.CachedInputTokens,
			CacheWriteInputTokens: total.CacheWriteInputTokens - prev.CacheWriteInputTokens,
			OutputTokens:          total.OutputTokens - prev.OutputTokens, TotalTokens: total.TotalTokens - prev.TotalTokens,
			ReasoningTokens: max(0, total.ReasoningTokens-prev.ReasoningTokens),
		}
	}
	if total != nil {
		copy := *total
		s.usageTotal = &copy
	}
	if delta.TotalTokens == 0 {
		delta.TotalTokens = delta.InputTokens + delta.OutputTokens
	}
	s.usageSum.InputTokens += delta.InputTokens
	s.usageSum.CachedInputTokens += delta.CachedInputTokens
	s.usageSum.CacheWriteInputTokens += delta.CacheWriteInputTokens
	s.usageSum.OutputTokens += delta.OutputTokens
	s.usageSum.ReasoningTokens += delta.ReasoningTokens
	s.usageSum.TotalTokens += delta.TotalTokens
	u := s.usageSum
	s.usage = map[string]any{
		"input_tokens": u.InputTokens, "cached_input_tokens": u.CachedInputTokens,
		"cache_creation_input_tokens": u.CacheWriteInputTokens,
		"output_tokens":               u.OutputTokens, "reasoning_tokens": u.ReasoningTokens, "total_tokens": u.TotalTokens,
	}
	return []string{codexLine(map[string]any{"type": "usage", "usage": s.usage})}
}

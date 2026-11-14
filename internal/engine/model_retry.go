package engine

import (
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func mergeModelRetryPolicy(agent, node *domain.ModelRetryPolicy) *domain.ModelRetryPolicy {
	if agent == nil && node == nil {
		return nil
	}
	merged := domain.ModelRetryPolicy{}
	if agent != nil {
		merged = *agent
	}
	if node != nil {
		if node.InitialSeconds != nil {
			merged.InitialSeconds = node.InitialSeconds
		}
		if node.MaxSeconds != nil {
			merged.MaxSeconds = node.MaxSeconds
		}
		if node.MaxRetries != nil {
			merged.MaxRetries = node.MaxRetries
		}
	}
	return &merged
}

func uniqueFallbackModels(primary string, models []string) []string {
	seen := map[string]bool{strings.TrimSpace(primary): true}
	var result []string
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			result = append(result, model)
		}
	}
	return result
}

func modelRetrySettings(nr *domain.NodeRun) (time.Duration, time.Duration, int) {
	initial, maximum, retries := 15, 240, 5
	if p := nr.ModelRetry; p != nil {
		if p.InitialSeconds != nil {
			initial = max(1, min(*p.InitialSeconds, 3600))
		}
		if p.MaxSeconds != nil {
			maximum = max(1, min(*p.MaxSeconds, 3600))
		}
		if p.MaxRetries != nil {
			retries = max(0, min(*p.MaxRetries, 20))
		}
	}
	return time.Duration(min(initial, maximum)) * time.Second, time.Duration(maximum) * time.Second, retries
}

func modelRetryBase(initial, maximum time.Duration, retry int) time.Duration {
	base := initial
	for i := 1; i < retry && base < maximum; i++ {
		base = min(base*2, maximum)
	}
	return base
}

// advanceFallbackModel only switches among models explicitly configured for
// the same executor. Starting a fresh CLI session avoids carrying an oversized
// or provider-specific conversation into the replacement model.
func (e *Engine) advanceFallbackModel(r *domain.Run, nodeID string, nr *domain.NodeRun, reason runtime.ModelErrorClass) bool {
	for nr.FallbackModelIndex < len(nr.FallbackModels) {
		next := strings.TrimSpace(nr.FallbackModels[nr.FallbackModelIndex])
		nr.FallbackModelIndex++
		if next == "" || next == nr.ActiveModel {
			continue
		}
		previous := nr.ActiveModel
		nr.ActiveModel = next
		nr.ModelTransientErrors = 0
		nr.ResumeSessionID = ""
		nr.SessionID = ""
		nr.Activity = "切换备选模型，使用已有工作区重新执行"
		e.event(r, nodeID, "model.fallback", fmt.Sprintf("模型 %s → %s（%s）；使用新会话", previous, next, reason))
		r.Events[len(r.Events)-1].Attributes = map[string]any{"class": string(reason), "fromModel": previous, "toModel": next}
		return true
	}
	return false
}

func hasModelRetry(r domain.Run) bool {
	for _, n := range r.Nodes {
		if n != nil && n.Status == "waiting_retry" {
			return true
		}
	}
	return false
}

func modelRetryDue(r domain.Run, now time.Time) bool {
	for _, n := range r.Nodes {
		if n != nil && n.Status == "waiting_retry" && !n.NextRetryAt.IsZero() && !n.NextRetryAt.After(now) {
			return true
		}
	}
	return false
}

func promoteDueModelRetries(r *domain.Run, now time.Time) bool {
	changed := false
	for _, n := range r.Nodes {
		if n != nil && n.Status == "waiting_retry" && !n.NextRetryAt.IsZero() && !n.NextRetryAt.After(now) {
			n.Status = "pending"
			n.Activity = "模型服务重试已就绪"
			changed = true
		}
	}
	return changed
}

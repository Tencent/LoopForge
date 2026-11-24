package repo

import (
	"sort"
	"strings"

	"agentflow/internal/domain"
)

type analyticsTokenUsageKey struct {
	date, userID, model string
}

// Use the same execution total and day as UserUsage. Node/model observations
// attribute that total; they never add another copy of the run's tokens.
func accumulateAnalyticsTokenUsage(values map[analyticsTokenUsageKey]int64, run domain.Run, userID, day string, external []domain.InsightUsage) {
	if values == nil || run.Usage.TotalTokens <= 0 {
		return
	}
	if userID == "" {
		userID = "unassigned"
	}
	models := map[string]int64{}
	if external != nil {
		for _, usage := range external {
			models[strings.TrimSpace(usage.Model)] += usage.InputTokens + usage.OutputTokens
		}
	} else {
		for _, node := range run.Nodes {
			if node != nil && node.Usage.TotalTokens > 0 {
				models[strings.TrimSpace(node.Model)] += node.Usage.TotalTokens
			}
		}
	}
	var attributed int64
	for _, tokens := range models {
		attributed += tokens
	}
	if attributed > run.Usage.TotalTokens {
		// Conflicting historical observations do not justify inventing a model
		// allocation. Keep the authoritative total visible as unattributed.
		models = map[string]int64{"": run.Usage.TotalTokens}
	} else {
		models[""] += run.Usage.TotalTokens - attributed
	}
	for model, tokens := range models {
		if tokens > 0 {
			values[analyticsTokenUsageKey{date: day, userID: userID, model: model}] += tokens
		}
	}
}

func finalizeAnalyticsTokenUsage(values map[analyticsTokenUsageKey]int64) []domain.AnalyticsTokenUsagePoint {
	var out []domain.AnalyticsTokenUsagePoint
	for key, tokens := range values {
		out = append(out, domain.AnalyticsTokenUsagePoint{Date: key.date, UserID: key.userID, Model: key.model, Tokens: tokens})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

package runtime

import (
	"slices"
	"strings"

	"agentflow/internal/domain"
)

// SplitAgyModelEffort separates an Antigravity model ID's recognized effort suffix.
func SplitAgyModelEffort(model string) (base, effort string) {
	for _, level := range domain.ExecutorEffortLevels[AgentCLIAntigravity] {
		if base, ok := strings.CutSuffix(model, "-"+level); ok {
			return base, level
		}
	}
	return model, ""
}

func agyClaudeFamily(model string) string {
	base, _ := SplitAgyModelEffort(model)
	switch base {
	case "claude-opus-5-5", "claude-sonnet-5-5":
		return base
	}
	return ""
}

// AgyAccountModelEfforts includes the Claude variants exposed by account login.
func AgyAccountModelEfforts(model string) []string {
	if agyClaudeFamily(model) != "" {
		return []string{"low", "medium", "high", "max"}
	}
	return AgyModelEfforts(model)
}

// FoldAgyAccountModels keeps an advertised model ID for the default and
// carries the actual variant IDs and levels returned by this account.
func FoldAgyAccountModels(models []domain.CLIModel) []domain.CLIModel {
	out := make([]domain.CLIModel, 0, len(models))
	groups := make(map[string]int)
	for _, model := range models {
		base, effort := SplitAgyModelEffort(model.ID)
		levels := AgyAccountModelEfforts(base)
		if !slices.Contains(levels, effort) {
			out = append(out, model)
			continue
		}
		index, exists := groups[base]
		if !exists {
			index = len(out)
			groups[base] = index
			name := model.Name
			if suffix := " (" + effort + ")"; strings.HasSuffix(strings.ToLower(name), suffix) {
				name = name[:len(name)-len(suffix)]
			}
			if name == "" {
				name = base
			}
			out = append(out, domain.CLIModel{
				ID: model.ID, Name: name, DefaultEffort: effort,
				Efforts: make(map[string]string), ReasoningEfforts: model.ReasoningEfforts,
			})
		}
		group := &out[index]
		group.Efforts[effort] = model.ID
		for _, level := range levels {
			if id := model.Efforts[level]; id == base+"-"+level {
				group.Efforts[level] = id
			}
		}
		if agyClaudeFamily(base) != "" && effort == "medium" {
			group.ReasoningEfforts = model.ReasoningEfforts
		}
	}
	for base, index := range groups {
		row := &out[index]
		if agyClaudeFamily(base) != "" && row.Efforts["medium"] != "" {
			row.ID, row.DefaultEffort = row.Efforts["medium"], "medium"
		}
		levels := []string{}
		for _, level := range AgyAccountModelEfforts(base) {
			if row.Efforts[level] != "" {
				levels = append(levels, level)
			}
		}
		row.DiscoveredReasoningEfforts = &levels
	}
	return out
}

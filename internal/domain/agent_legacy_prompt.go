package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Roles saved before the single-prompt model kept their brief in two fields:
// free-form `instructions` and a structured `taskPrompt`. Stored agents,
// generic templates, definition revisions and older API clients can still
// carry only those fields. Decoding folds them into Prompt so such a role never
// runs with an empty brief; a stored non-empty prompt always wins.
type legacyAgentBrief struct {
	Instructions string          `json:"instructions"`
	TaskPrompt   json.RawMessage `json:"taskPrompt"`
}

type legacyTaskPromptSpec struct {
	Role               string   `json:"role"`
	Objective          string   `json:"objective"`
	Context            string   `json:"context"`
	Inputs             []string `json:"inputs"`
	Actions            []string `json:"actions"`
	Deliverables       []string `json:"deliverables"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Constraints        []string `json:"constraints"`
	RetryInstructions  string   `json:"retryInstructions"`
}

type agentDefinitionJSON AgentDefinition

func (a *AgentDefinition) UnmarshalJSON(data []byte) error {
	decoded := agentDefinitionJSON(*a)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*a = AgentDefinition(decoded)
	if strings.TrimSpace(a.Prompt) != "" {
		return nil
	}
	var legacy legacyAgentBrief
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil
	}
	var spec legacyTaskPromptSpec
	raw := strings.TrimSpace(string(legacy.TaskPrompt))
	if raw != "" && raw != "null" && json.Unmarshal(legacy.TaskPrompt, &spec) != nil {
		// A plain-text task prompt is kept as the objective.
		var text string
		if json.Unmarshal(legacy.TaskPrompt, &text) == nil {
			spec = legacyTaskPromptSpec{Objective: text}
		}
	}
	a.Prompt = legacyAgentPrompt(a.Name, legacy.Instructions, spec)
	return nil
}

// legacyAgentPrompt uses the same layout as the manual migration of existing
// roles: role line, instructions, objective, then one section per field.
func legacyAgentPrompt(name, instructions string, spec legacyTaskPromptSpec) string {
	instructions = strings.TrimSpace(instructions)
	var parts []string
	if role := strings.TrimSpace(spec.Role); role != "" && role != strings.TrimSpace(name) && !strings.HasPrefix(instructions, "你是") {
		parts = append(parts, "你是"+role+"。")
	}
	if instructions != "" {
		parts = append(parts, instructions)
	}
	if objective := strings.TrimSpace(spec.Objective); objective != "" {
		parts = append(parts, objective)
	}
	section := func(title, body string) {
		if body != "" {
			parts = append(parts, "### "+title+"\n\n"+body)
		}
	}
	bullets := func(values []string, skip func(string) bool) string {
		var lines []string
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" && (skip == nil || !skip(value)) {
				lines = append(lines, "- "+value)
			}
		}
		return strings.Join(lines, "\n")
	}
	var steps []string
	for _, action := range spec.Actions {
		if action = strings.TrimSpace(action); action != "" {
			steps = append(steps, fmt.Sprintf("%d. %s", len(steps)+1, action))
		}
	}
	section("工作上下文", strings.TrimSpace(spec.Context))
	section("输入", bullets(spec.Inputs, nil))
	section("执行步骤", strings.Join(steps, "\n"))
	section("交付物要求", bullets(spec.Deliverables, nil))
	section("完成标准", bullets(spec.AcceptanceCriteria, nil))
	section("禁止", bullets(spec.Constraints, func(value string) bool { return strings.Contains(instructions, value) }))
	section("失败或被打回时", strings.TrimSpace(spec.RetryInstructions))
	return strings.Join(parts, "\n\n")
}

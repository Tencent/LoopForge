package domain

import (
	"encoding/json"
	"strings"
)

// WorkflowNotifications selects one SOP Bot, with compatibility for legacy node bindings.
// Explicit SOP configuration takes precedence over all node configuration.
func WorkflowNotifications(w WorkflowDefinition) []NotificationBinding {
	var selected *NotificationBinding
	if len(w.Notifications) > 0 {
		b := w.Notifications[0]
		selected = &b
	}
	// Prefer legacy SOP configuration to individual node settings.
	if selected == nil {
		for _, node := range w.Nodes {
			for _, b := range node.Notifications {
				if b.Scope == "run" {
					copy := b
					selected = &copy
					break
				}
			}
			if selected != nil {
				break
			}
		}
	}
	if selected == nil {
		for _, node := range w.Nodes {
			if len(node.Notifications) > 0 {
				b := node.Notifications[0]
				b.ContentMode = "ai"
				b.Template = ""
				b.AIInstruction = ""
				selected = &b
				break
			}
		}
	}
	if selected == nil || selected.BotID == "" {
		return nil
	}
	selected.Scope = "run"
	selected.Events = []string{"succeeded"}
	selected.ContentMode = "ai"
	return []NotificationBinding{*selected}
}

// Node reminders always use platform status and never reuse an AI completion report.
func NodeReminderBindings(bindings []NotificationBinding) []NotificationBinding {
	out := []NotificationBinding{}
	for _, b := range bindings {
		events := []string{"waiting", "failed"}
		out = append(out, NotificationBinding{BotID: b.BotID, Events: events, Scope: "node", WaitingPolicy: "actionable", Template: "### {{status}}\n需求：{{issue.title}}\nSOP：{{workflow}}\n节点：{{node}}\n{{error}}\n查看详情：{{url}}"})
	}
	return out
}

// BuildWorkflowNotificationResult combines contributions from every node in graph order.
// No model call occurs inside a database transaction or during notification retries.
func BuildWorkflowNotificationResult(run Run) string {
	sections := []string{}
	generatedAny, simulated := false, false
	budget := 1200
	if count := len(run.WorkflowSnapshot.Nodes); count > 0 {
		budget = 6000 / count
		if budget > 1800 {
			budget = 1800
		}
	}
	for _, node := range run.WorkflowSnapshot.Nodes {
		status, result, errorText := "未执行", "", ""
		if nr := run.Nodes[node.ID]; nr != nil {
			status = nr.Status
			if label, ok := map[string]string{"succeeded": "已完成", "failed": "失败", "skipped": "已跳过", "pending": "未执行", "running": "执行中", "waiting_approval": "待人工处理"}[status]; ok {
				status = label
			}
			result = nr.Output
			errorText = nr.Error
		}
		ai, plain, generated := ParseNotificationResult(result)
		if generated {
			generatedAny = true
			simulated = simulated || ai.Simulated
			plain = ai.Body
			if plain == "" {
				plain = ai.Summary
			}
		}
		section := "### " + NotificationText(node.Name, 160) + "（" + status + "）"
		if text := NotificationText(plain, budget); text != "" {
			section += "\n" + text
		}
		if errorText != "" {
			section += "\n失败原因：" + NotificationText(errorText, 600)
		}
		sections = append(sections, section)
	}
	body := strings.Join(sections, "\n\n")
	if !generatedAny {
		return body
	}
	content := AINotificationContent{Title: run.WorkflowSnapshot.Name + " · SOP 已完成", Summary: body, Body: body, Simulated: simulated}
	raw, _ := json.Marshal(content)
	return body + "\n\n```notification\n" + string(raw) + "\n```"
}

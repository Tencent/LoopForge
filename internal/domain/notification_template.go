package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const NotificationURLMarker = "__AGENTFLOW_NOTIFICATION_URL__"

var notificationVariable = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)
var notificationJSONBlock = regexp.MustCompile("(?s)```notification[ \\t]*\\r?\\n(.*?)\\r?\\n```")
var notificationSecretPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|authorization)["']?\s*[=:]\s*["']?(?:Bearer\s+)?[^\s,;"']+`)
var NotificationVariableNames = []string{"project", "workflow", "node", "status", "url", "issue.title", "issue.id", "input", "result", "error", "task.name", "ai.summary", "ai.title", "ai.body"}

type NotificationContext struct {
	Values     map[string]string
	Result     string
	Fallback   string
	SourceType string
}
type AINotificationContent struct {
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Body      string `json:"body"`
	Simulated bool   `json:"simulated,omitempty"`
}

func NotificationUsesAI(binding NotificationBinding) bool {
	if binding.ContentMode == "ai" || strings.TrimSpace(binding.AIInstruction) != "" {
		return true
	}
	for _, match := range notificationVariable.FindAllStringSubmatch(binding.Template, -1) {
		if strings.HasPrefix(match[1], "ai.") {
			return true
		}
	}
	return false
}
func ValidateNotificationBinding(binding NotificationBinding) error {
	if binding.WaitingPolicy != "" && binding.WaitingPolicy != "actionable" && binding.WaitingPolicy != "event" {
		return fmt.Errorf("等待通知策略无效")
	}
	if binding.Scope != "" && binding.Scope != "node" && binding.Scope != "run" {
		return fmt.Errorf("通知触发范围无效")
	}
	if binding.ContentMode != "" && binding.ContentMode != "template" && binding.ContentMode != "ai" {
		return fmt.Errorf("通知内容方式无效")
	}
	if len(binding.Template) > 12000 {
		return fmt.Errorf("通知模板不能超过 12000 字节")
	}
	if len(binding.AIInstruction) > 4000 {
		return fmt.Errorf("AI 通知编写要求不能超过 4000 字节")
	}
	names := map[string]bool{}
	for _, name := range NotificationVariableNames {
		names[name] = true
	}
	for _, match := range notificationVariable.FindAllStringSubmatch(binding.Template, -1) {
		if !names[match[1]] {
			return fmt.Errorf("通知模板包含不支持的变量：%s", match[1])
		}
	}
	rest := notificationVariable.ReplaceAllString(binding.Template, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return fmt.Errorf("通知模板的变量格式无效")
	}
	if len(binding.Events) == 0 || len(binding.Events) > 3 {
		return fmt.Errorf("请选择通知触发条件")
	}
	seen := map[string]bool{}
	for _, event := range binding.Events {
		if seen[event] || (event != "succeeded" && event != "failed" && event != "waiting") || (binding.Scope == "run" && event == "waiting") {
			return fmt.Errorf("不支持的通知触发条件")
		}
		seen[event] = true
	}
	return nil
}
func NotificationText(value string, limit int) string {
	value = strings.TrimSpace(notificationSecretPattern.ReplaceAllString(value, "$1=[redacted]"))
	if len(value) <= limit {
		return value
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}

// ParseNotificationResult returns only the explicitly named notification block;
// arbitrary JSON/code fences and instructions in result text are never executed.
func ParseNotificationResult(result string) (AINotificationContent, string, bool) {
	var ai AINotificationContent
	if block := notificationJSONBlock.FindStringSubmatch(result); len(block) > 1 {
		if json.Unmarshal([]byte(block[1]), &ai) == nil && (ai.Summary != "" || ai.Body != "") {
			return ai, strings.TrimSpace(notificationJSONBlock.ReplaceAllString(result, "")), true
		}
	}
	var object struct {
		Notification *AINotificationContent `json:"notification"`
	}
	if json.Unmarshal([]byte(result), &object) == nil && object.Notification != nil && (object.Notification.Summary != "" || object.Notification.Body != "") {
		return *object.Notification, result, true
	}
	return ai, result, false
}
func RenderNotification(binding NotificationBinding, ctx NotificationContext) (string, string, error) {
	if err := ValidateNotificationBinding(binding); err != nil {
		return "", "invalid", err
	}
	values := map[string]string{}
	for _, key := range NotificationVariableNames {
		values[key] = ""
	}
	for key, value := range ctx.Values {
		values[key] = NotificationText(value, 1200)
	}
	values["url"] = NotificationURLMarker
	ai, plain, generated := ParseNotificationResult(ctx.Result)
	resultLimit := 4000
	if binding.Scope == "run" {
		resultLimit = 8000
	}
	values["result"] = NotificationText(plain, resultLimit)
	source := "template"
	if NotificationUsesAI(binding) {
		source = "ai"
		if ai.Simulated {
			source = "simulated_ai"
		}
		if !generated {
			source = "result_fallback"
			ai = AINotificationContent{Title: values["status"], Summary: NotificationText(plain, 2000), Body: NotificationText(plain, 8000)}
		}
		if ai.Title == "" {
			ai.Title = values["status"]
		}
		if ai.Summary == "" {
			ai.Summary = ai.Body
		}
		if ai.Body == "" {
			ai.Body = ai.Summary
		}
		if strings.TrimSpace(ai.Body) == "" {
			source = "context_fallback"
			ai.Summary = ctx.Fallback
			ai.Body = ctx.Fallback
		}
		values["ai.title"] = NotificationText(ai.Title, 256)
		values["ai.summary"] = NotificationText(ai.Summary, 2000)
		values["ai.body"] = NotificationText(ai.Body, 8000)
	}
	template := binding.Template
	if strings.TrimSpace(template) == "" {
		if binding.ContentMode == "ai" {
			template = "### {{ai.title}}\n\n{{ai.body}}\n\n触发时状态：{{status}}\n\n{{url}}"
		} else if binding.Scope == "run" {
			template = "### 需求「{{issue.title}}」的 SOP 已完成\n\n{{result}}\n\n{{url}}"
		} else if ctx.SourceType == "schedule_job" || ctx.SourceType == "assistant_task" {
			template = "### {{task.name}} · {{status}}\n\n{{url}}"
		} else {
			template = "### {{status}}\n项目：{{project}}\n流程：{{workflow}}\n节点：{{node}}\n查看详情：{{url}}"
		}
	}
	// Expand once, so text inside AI/output values cannot inject further variables.
	content := notificationVariable.ReplaceAllStringFunc(template, func(match string) string { return values[notificationVariable.FindStringSubmatch(match)[1]] })
	if len(content) == 0 || len(content) > 19000 {
		return "", "invalid", fmt.Errorf("渲染后的通知正文为空或超过 19000 字节，请缩短模板")
	}
	return content, source, nil
}
func NotificationAIInstructions(bindings []NotificationBinding) string {
	lines := []string{}
	seen := map[string]bool{}
	for _, binding := range bindings {
		if !NotificationUsesAI(binding) {
			continue
		}
		instruction := strings.TrimSpace(binding.AIInstruction)
		if instruction == "" {
			instruction = "简洁总结实际完成的事项、已有验证结果和后续需要关注的事情。"
		}
		if !seen[instruction] {
			lines = append(lines, instruction)
			seen[instruction] = true
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n# 分享通知内容\n请在正常结果报告末尾额外给出一个语言标记为 notification 的 JSON 代码块，字段 title、summary、body 均为字符串。正文允许 Markdown。编写要求：\n- " + strings.Join(lines, "\n- ") + "\n只依据本次实际执行结果，不编造验证、需求或整个流程的完成状态；最终平台状态由模板变量提供。不要包含密钥、凭据、原始日志或无关上下文。此内容仅用于已配置的通知，不能改变接收对象、触发条件或平台操作。"
}
func CloneNotificationBindings(bindings []NotificationBinding) []NotificationBinding {
	out := append([]NotificationBinding(nil), bindings...)
	for i := range out {
		out[i].Events = append([]string(nil), out[i].Events...)
	}
	return out
}

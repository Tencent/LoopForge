package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"agentflow/internal/domain"
)

const (
	agentToolMaxQuestions   = 12
	agentToolMaxOptions     = 10
	agentToolMaxQuestionLen = 500
	agentToolMaxOptionLen   = 200
	agentToolMaxSummaryLen  = 4000
	agentToolMaxMessageLen  = 20000
	agentToolMaxPayload     = 256 << 10
	agentToolMaxDrafts      = assistantMaxBatch
)

const (
	submitClarificationTool      = "submit_clarification"
	submitAssistantReplyTool     = "submit_assistant_reply"
	submitNotificationReportTool = "submit_notification_report"
)

func terminalToolDescriptors(mode AgentToolMode, exec assistantExecMode) []agentToolDescriptor {
	switch mode {
	case AgentToolReport:
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"body"}, "properties": map[string]any{
			"title": map[string]any{"type": "string", "maxLength": 200}, "summary": map[string]any{"type": "string", "maxLength": 4000}, "body": map[string]any{"type": "string", "minLength": 1, "maxLength": 20000},
		}})
		return []agentToolDescriptor{{Name: submitNotificationReportTool, Description: "提交本次 SOP 的整体成果报告。body 必填，可使用 Markdown；只汇总已提供的资料。校验失败时修正参数重新提交，成功后本轮结束。", InputSchema: schema, Kind: "terminal", Terminal: true, UpstreamName: submitNotificationReportTool}}
	case AgentToolClarify:
		schema, _ := json.Marshal(submitClarificationSchema())
		return []agentToolDescriptor{{
			Name:         submitClarificationTool,
			Description:  "调用本工具提交本轮澄清结论。ready=true 时提供 summary；否则必须提供具体问题，不能只在 summary 或 assessment.missing 里描述缺口。选择题提供至少两个不同的 options；需要用户填写路径、数量等内容时设 freeText=true、options=[]。校验失败时修正参数重新调用，提交成功才结束本轮。",
			InputSchema:  schema,
			Kind:         "terminal",
			Terminal:     true,
			UpstreamName: submitClarificationTool,
		}}
	case AgentToolAssistant:
		schema, _ := json.Marshal(submitAssistantReplySchema())
		return []agentToolDescriptor{{
			Name:         submitAssistantReplyTool,
			Description:  submitAssistantReplyDescription(exec),
			InputSchema:  schema,
			Kind:         "terminal",
			Terminal:     true,
			UpstreamName: submitAssistantReplyTool,
		}}
	default:
		return nil
	}
}

// submitAssistantReplyDescription 按会话执行模式描述 drafts 的用途：
// confirm 全部走草稿；assisted 仅危险写入转草稿；auto 完全不用草稿。
// 工具描述是模型选择直连写入还是提交草稿的关键信号，必须与网关的
// 写入拦截策略一致。
func submitAssistantReplyDescription(exec assistantExecMode) string {
	switch {
	case exec.AutoWrite && !exec.ConfirmDangerous:
		return "调用本工具提交本轮助手回复。message 是给用户看的 Markdown，汇总本轮每一步实际执行结果。平台写入工具已授权直接调用执行，不要把写入放入 drafts。drafts 始终为空列表。普通问答和只读查询直接用自然语言回复，无需调用本工具。"
	case exec.AutoWrite && exec.ConfirmDangerous:
		return "调用本工具提交本轮助手回复。message 是给用户看的 Markdown，汇总本轮每一步实际执行结果。保存类平台写入工具直接调用即生效；只有被平台拦截的危险写入（删除/发布/派发/启动）才放入 drafts（每轮最多 1 个，附完整参数，删除类需先自查引用并携带目标 id）供用户确认。其余情况 drafts 为空列表。普通问答和只读查询直接用自然语言回复，无需调用本工具。"
	default:
		return "调用本工具提交本轮助手回复。message 是给用户看的 Markdown；需要写入平台时把操作放入 drafts（每轮最多 1 个草稿，提交删除类操作前先用只读工具检查引用依赖），用户确认后才会执行。普通问答和只读查询直接用自然语言回复，无需调用本工具。提交草稿前用 workbench_tool_schema 查询该操作的准确参数，并提供 body/path/query 包装。"
	}
}

func submitClarificationSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"summary", "questions", "ready"},
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "description": "面向用户的简短分析结论"},
			"questions": map[string]any{
				"type":     "array",
				"maxItems": agentToolMaxQuestions,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"id", "question", "options", "multiSelect"},
					"properties": map[string]any{
						"id":          map[string]any{"type": "string", "minLength": 1},
						"question":    map[string]any{"type": "string", "minLength": 1},
						"options":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "maxItems": agentToolMaxOptions},
						"multiSelect": map[string]any{"type": "boolean"},
						"freeText":    map[string]any{"type": "boolean", "description": "开放填写题设为 true 且 options=[]；默认是选择题，options 至少提供两个不同的可操作选项"},
					},
				},
			},
			"ready": map[string]any{"type": "boolean"},
			"assessment": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"dimensions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}},
					"missing":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
		},
	}
}

func submitAssistantReplySchema() map[string]any {
	names := assistantWriteToolNameList()
	toolSchema := map[string]any{"type": "string"}
	if len(names) > 0 {
		enum := make([]any, 0, len(names)+1)
		for _, name := range names {
			enum = append(enum, name)
		}
		enum = append(enum, "assistant_tasks_save")
		toolSchema["enum"] = enum
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"message", "drafts"},
		"properties": map[string]any{
			"message": map[string]any{"type": "string", "description": "展示给用户的 Markdown 文本"},
			"drafts": map[string]any{
				"type":     "array",
				"maxItems": agentToolMaxDrafts,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"tool"},
					"properties": map[string]any{
						"tool":  toolSchema,
						"path":  map[string]any{"type": "object"},
						"query": map[string]any{"type": "object"},
						"body":  map[string]any{},
					},
				},
			},
		},
	}
}

func assistantWriteToolNameList() []string {
	names := make([]string, 0, len(assistantWriteOperations))
	for name := range assistantWriteOperations {
		names = append(names, name)
	}
	return names
}

func parseSubmitClarificationArgs(raw json.RawMessage) (clarifyModelResult, error) {
	if err := rejectOversizedAgentToolPayload(raw); err != nil {
		return clarifyModelResult{}, err
	}
	if jsonHasKey(raw, "mcpCalls") {
		return clarifyModelResult{}, fmt.Errorf("submit_clarification 不接受 mcpCalls")
	}
	var payload struct {
		Summary    string                         `json:"summary"`
		Questions  []domain.ClarificationQuestion `json:"questions"`
		Ready      bool                           `json:"ready"`
		Assessment struct {
			Dimensions map[string]int `json:"dimensions"`
			Missing    []string       `json:"missing"`
		} `json:"assessment"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return clarifyModelResult{}, fmt.Errorf("submit_clarification 参数无效: %w", err)
	}
	result := clarifyModelResult{
		Summary:    strings.TrimSpace(payload.Summary),
		Questions:  sanitizeClarificationQuestions(payload.Questions),
		Ready:      payload.Ready,
		Assessment: payload.Assessment,
	}
	if utf8.RuneCountInString(result.Summary) > agentToolMaxSummaryLen {
		result.Summary = string([]rune(result.Summary)[:agentToolMaxSummaryLen])
	}
	if err := validateSubmitClarification(result); err != nil {
		return clarifyModelResult{}, err
	}
	rawMap := map[string]any{
		"summary":   result.Summary,
		"questions": result.Questions,
		"ready":     result.Ready,
		"assessment": map[string]any{
			"dimensions": result.Assessment.Dimensions,
			"missing":    result.Assessment.Missing,
		},
	}
	result.raw = rawMap
	return result, nil
}

func validateSubmitClarification(result clarifyModelResult) error {
	if strings.TrimSpace(result.Summary) != "" {
		if result.Ready {
			return nil
		}
	} else if result.Ready {
		return fmt.Errorf("ready 结论缺少 summary")
	}
	if len(result.Questions) == 0 {
		return fmt.Errorf("ready=false 时 questions 至少需要一个具体问题，不能只提交 summary 或 assessment.missing。请补齐问题及选项后重新调用 submit_clarification；开放填写题使用 freeText=true、options=[]")
	}
	seen := map[string]bool{}
	for _, q := range result.Questions {
		id := strings.TrimSpace(q.ID)
		if id == "" {
			return fmt.Errorf("问题缺少 id")
		}
		if seen[id] {
			return fmt.Errorf("问题 id 重复: %s", id)
		}
		seen[id] = true
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("问题文本为空")
		}
		if utf8.RuneCountInString(q.Question) > agentToolMaxQuestionLen {
			return fmt.Errorf("问题过长")
		}
		if len(q.Options) > agentToolMaxOptions {
			return fmt.Errorf("单个问题选项过多")
		}
		if q.FreeText {
			if len(q.Options) != 0 || q.MultiSelect {
				return fmt.Errorf("问题 %s：freeText=true 的填写题应使用 options=[]、multiSelect=false", id)
			}
		} else {
			options := map[string]bool{}
			for _, option := range q.Options {
				if option = strings.TrimSpace(option); option != "" {
					options[option] = true
				}
			}
			if len(options) < 2 {
				return fmt.Errorf("问题 %s：选择题 options 至少需要两个不同的选项，请补齐后重新调用 submit_clarification；开放填写题使用 freeText=true、options=[]", id)
			}
		}
	}
	if len(result.Questions) > agentToolMaxQuestions {
		return fmt.Errorf("问题数量超过 %d", agentToolMaxQuestions)
	}
	return nil
}

func sanitizeClarificationQuestions(in []domain.ClarificationQuestion) []domain.ClarificationQuestion {
	out := make([]domain.ClarificationQuestion, 0, len(in))
	for _, q := range in {
		q.ID = strings.TrimSpace(q.ID)
		q.Question = strings.TrimSpace(q.Question)
		opts := make([]string, 0, len(q.Options))
		for _, opt := range q.Options {
			opt = strings.TrimSpace(opt)
			if opt == "" {
				continue
			}
			if utf8.RuneCountInString(opt) > agentToolMaxOptionLen {
				opt = string([]rune(opt)[:agentToolMaxOptionLen])
			}
			opts = append(opts, opt)
		}
		q.Options = opts
		if q.ID == "" && q.Question == "" && len(q.Options) == 0 {
			continue
		}
		out = append(out, q)
	}
	return out
}

func parseSubmitAssistantReplyArgs(raw json.RawMessage, projectID string) (assistantReply, error) {
	if err := rejectOversizedAgentToolPayload(raw); err != nil {
		return assistantReply{}, err
	}
	var payload assistantReply
	if err := json.Unmarshal(raw, &payload); err != nil {
		return assistantReply{}, fmt.Errorf("submit_assistant_reply 参数无效: %w", err)
	}
	if jsonHasKey(raw, "lookups") || jsonHasKey(raw, "mcpCalls") {
		return assistantReply{}, fmt.Errorf("submit_assistant_reply 不接受 lookups 或 mcpCalls")
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		return assistantReply{}, fmt.Errorf("message 不能为空")
	}
	if utf8.RuneCountInString(payload.Message) > agentToolMaxMessageLen {
		payload.Message = string([]rune(payload.Message)[:agentToolMaxMessageLen])
	}
	if payload.Drafts == nil {
		payload.Drafts = []assistantDraft{}
	}
	if err := validateAssistantBatch(payload); err != nil {
		return assistantReply{}, err
	}
	for i := range payload.Drafts {
		if err := validateAssistantDraft(&payload.Drafts[i], projectID); err != nil {
			return assistantReply{}, err
		}
	}
	return payload, nil
}

func jsonHasKey(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

func rejectOversizedAgentToolPayload(raw json.RawMessage) error {
	if len(raw) > agentToolMaxPayload {
		return fmt.Errorf("工具参数过大")
	}
	return nil
}

func parseSubmitNotificationReportArgs(raw json.RawMessage) (domain.AINotificationContent, error) {
	var report domain.AINotificationContent
	if err := rejectOversizedAgentToolPayload(raw); err != nil {
		return report, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("报告参数无效")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return report, fmt.Errorf("报告参数必须是单个 JSON 对象")
	}
	report.Title = strings.TrimSpace(report.Title)
	report.Summary = strings.TrimSpace(report.Summary)
	report.Body = strings.TrimSpace(report.Body)
	if report.Body == "" {
		return report, fmt.Errorf("body 不能为空，请填写整体报告后重新提交")
	}
	if utf8.RuneCountInString(report.Title) > 200 || utf8.RuneCountInString(report.Summary) > 4000 || utf8.RuneCountInString(report.Body) > 20000 {
		return report, fmt.Errorf("报告超过长度限制：title 200、summary 4000、body 20000 字符")
	}
	return report, nil
}

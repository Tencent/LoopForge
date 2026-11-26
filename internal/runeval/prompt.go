package runeval

import (
	"encoding/json"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

func BuildPrompt(policy domain.RunEvalPolicy, facts domain.RunEvalFacts) (system, user string) {
	policy = domain.EffectiveRunEvalPolicy(policy)
	var b strings.Builder
	b.WriteString("你是 AgentFlow 的 LOOP Run 评测员。根据提供的结构化 facts 和压缩 trace digest，按平台规则给这次 run 打分，并给出可落地的改进建议。\n")
	b.WriteString("只输出一个 JSON 对象，不要 Markdown。字段：\n")
	b.WriteString(`{"dimensions":{`)
	for i, dim := range policy.Dimensions {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + dim.ID + `":0`)
	}
	b.WriteString(`},"findings":[{"severity":"warn","dimension":"control","nodeId":"","ruleId":"","message":"","evidence":""}],"summary":"","recommendations":[]}` + "\n")
	b.WriteString("规则：\n")
	b.WriteString(fmt.Sprintf("- 准入线 %d 分。dimensions 每维填写 0-100 的整数，必须覆盖全部维度。总分由服务端按权重计算。\n", policy.Threshold))
	b.WriteString("- findings 用中文，指向具体 nodeId 和 digest seq / event。severity 只能是 info/warn/error。\n")
	b.WriteString("- summary 用简洁中文，先给本次运行的质量结论，再说明最影响结果的问题或后续动作。不要重复罗列所有分数。\n")
	b.WriteString("- recommendations 可为空。每条包含中文 title（用一句话说清改什么）和 rationale（说明依据及原因）。title 不要用操作代码。\n")
	b.WriteString("- 可回写建议必须给出真实 targetId 和完整修改内容。targetId 和 nodeId 从 facts 取值。缺少可执行修改时，只给 title、rationale 和相关 nodeId。\n")
	b.WriteString("- 调整节点策略：target=workflow、action=update_node、nodeId、set，set 中只放要更新的 maxAttempts/maxReworks/onFailure/retryStrategy。\n")
	b.WriteString("- 追加角色任务说明：target=agent、action=append_task_prompt、text，text 填写实际要追加的完整正文。\n")
	b.WriteString("- 修改角色门禁：target=agent、action=upsert_gate、gate，gate 包含 id/name/mode/required 及适用的 stage/commands/executionScope/onFailure。移除门禁用 action=remove_gate 并给出 gate.id。\n")
	b.WriteString("- 不要整份重写 LOOP 或角色，不要改边或删节点。\n")
	b.WriteString("评分维度：\n")
	for _, dim := range policy.Dimensions {
		b.WriteString(fmt.Sprintf("- %s（%s，权重 %d）：%s\n", dim.ID, dim.Name, dim.Weight, dim.Question))
	}
	raw, _ := json.Marshal(facts)
	return b.String(), "评测这次 LOOP run。facts=" + string(raw)
}

func ParseModelResult(raw string, policy domain.RunEvalPolicy) (domain.RunEvalResult, error) {
	policy = domain.EffectiveRunEvalPolicy(policy)
	body := extractJSONObject(raw)
	if body == "" {
		return domain.RunEvalResult{}, fmt.Errorf("model did not return JSON")
	}
	var parsed struct {
		Summary         string                         `json:"summary"`
		Dimensions      map[string]int                 `json:"dimensions"`
		Findings        []domain.RunEvalFinding        `json:"findings"`
		Recommendations []domain.RunEvalRecommendation `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return domain.RunEvalResult{}, fmt.Errorf("invalid eval JSON: %w", err)
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		return domain.RunEvalResult{}, fmt.Errorf("eval summary is required")
	}
	score, dims, err := domain.ScoreRunEvalDimensions(policy, parsed.Dimensions)
	if err != nil {
		return domain.RunEvalResult{}, err
	}
	recs := make([]domain.RunEvalRecommendation, 0, len(parsed.Recommendations))
	for i, rec := range parsed.Recommendations {
		rec = normalizeRecommendation(rec, i)
		recs = append(recs, rec)
	}
	return domain.RunEvalResult{
		Score:           score,
		Passed:          score >= policy.Threshold,
		Dimensions:      dims,
		Findings:        parsed.Findings,
		Summary:         strings.TrimSpace(parsed.Summary),
		Recommendations: recs,
	}, nil
}

func normalizeRecommendation(rec domain.RunEvalRecommendation, index int) domain.RunEvalRecommendation {
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = fmt.Sprintf("rec-%d", index+1)
	}
	rec.Target = strings.ToLower(strings.TrimSpace(rec.Target))
	rec.Action = strings.TrimSpace(rec.Action)
	if rec.Status == "" {
		rec.Status = domain.RunEvalRecPending
	}
	if rec.Target == "" {
		switch {
		case strings.HasPrefix(domain.NormalizeRunEvalAction("", rec.Action), "agent."):
			rec.Target = "agent"
		case rec.NodeID != "" || rec.Gate != nil:
			rec.Target = "workflow"
		}
	}
	return rec
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```JSON")
		raw = strings.TrimPrefix(raw, "```")
		if i := strings.LastIndex(raw, "```"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return ""
	}
	return raw[start : end+1]
}

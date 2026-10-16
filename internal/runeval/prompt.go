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
	b.WriteString("你是 AgentFlow 的 SOP Run 评测员。根据提供的结构化 facts 和压缩 trace digest，按平台规则给这次 run 打分，并给出可落地的改进建议。\n")
	b.WriteString("只输出一个 JSON 对象，不要 Markdown。字段：\n")
	b.WriteString(`{"score":0,"dimensions":{`)
	for i, dim := range policy.Dimensions {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + dim.ID + `":0`)
	}
	b.WriteString(`},"findings":[{"severity":"warn","dimension":"control","nodeId":"","ruleId":"","message":"","evidence":""}],"summary":"","recommendations":[]}` + "\n")
	b.WriteString("规则：\n")
	b.WriteString(fmt.Sprintf("- 准入线 %d 分。dimensions 每维 0-100，必须覆盖全部维度。score 可省略，服务端会按权重重算。\n", policy.Threshold))
	b.WriteString("- findings 用中文，指向具体 nodeId 和 digest seq / event。severity 只能是 info/warn/error。\n")
	b.WriteString("- summary 必须包含这次 run 的质量结论，以及如果没有可应用补丁时应该怎么改。\n")
	b.WriteString("- recommendations 可为空。可应用时 action 只能是：update_node、upsert_gate、remove_gate、append_instructions、append_task_prompt。\n")
	b.WriteString("- target 为 workflow 或 agent。改节点重试用 update_node + set.maxAttempts/maxReworks/onFailure/retryStrategy。\n")
	b.WriteString("- 门禁只属于角色；改门禁用 target=agent + upsert_gate/remove_gate，并给 gate.id 和 targetId。\n")
	b.WriteString("- 不要整份重写 SOP 或角色，不要改边或删节点。\n")
	b.WriteString("评分维度：\n")
	for _, dim := range policy.Dimensions {
		b.WriteString(fmt.Sprintf("- %s（%s，权重 %d）：%s\n", dim.ID, dim.Name, dim.Weight, dim.Question))
	}
	raw, _ := json.Marshal(facts)
	return b.String(), "评测这次 SOP run。facts=" + string(raw)
}

func ParseModelResult(raw string, policy domain.RunEvalPolicy) (domain.RunEvalResult, error) {
	policy = domain.EffectiveRunEvalPolicy(policy)
	body := extractJSONObject(raw)
	if body == "" {
		return domain.RunEvalResult{}, fmt.Errorf("model did not return JSON")
	}
	var parsed struct {
		Score           int                            `json:"score"`
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

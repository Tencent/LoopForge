package evolution

import (
	"encoding/json"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// DefaultSemanticChecklist defines binary yes/no criteria for Layer 3 AI
// reference comparison (used only when Layer 2 passes and Layer 1 process
// scores are close).
var DefaultSemanticChecklist = []string{
	"结论/交付物准确完整，无遗漏关键步骤",
	"表达简洁清晰，无明显冗余废话或跑偏",
	"遵循原任务约束，未引入错误假设",
}

// BuildSemanticJudgePrompt constructs a low-variance binary-checklist pairwise
// comparison prompt. When swapped is true, New is presented as Option A and Old
// as Option B to neutralize LLM position bias.
func BuildSemanticJudgePrompt(oldOut, newOut string, swapped bool, checklist []string) (string, string) {
	if len(checklist) == 0 {
		checklist = DefaultSemanticChecklist
	}
	system := `你是输出质量评审助手。请仅做客观对比，禁止给 0~100 打分，只输出严格 JSON：
{"winner":"A|B|tie","checklist":[{"criterion":"...","aPass":true,"bPass":true}],"summary":"一句话结论"}
判断标准：若两版实质等价请直接返回 "tie"。`

	optA, optB := oldOut, newOut
	if swapped {
		optA, optB = newOut, oldOut
	}
	var sb strings.Builder
	sb.WriteString("请按以下「是/否」清单对比两版输出（忽略措辞长短偏好，只看业务质量）：\n")
	for i, c := range checklist {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, c))
	}
	sb.WriteString("\n--- 方案 A ---\n")
	sb.WriteString(domain.TrimEvoText(optA, 2400))
	sb.WriteString("\n\n--- 方案 B ---\n")
	sb.WriteString(domain.TrimEvoText(optB, 2400))
	sb.WriteString("\n")
	return system, sb.String()
}

// ParseSemanticJudgeResponse parses one round of Layer 3 pairwise comparison and
// maps A/B back to canonical "old" | "new" | "tie".
func ParseSemanticJudgeResponse(raw string, swapped bool) (winner string, items []domain.EvoSemanticCheckItem, ok bool) {
	body := extractJSONObject(raw)
	if body == "" {
		return "", nil, false
	}
	var parsed struct {
		Winner    string `json:"winner"`
		Checklist []struct {
			Criterion string `json:"criterion"`
			APass     bool   `json:"aPass"`
			BPass     bool   `json:"bPass"`
		} `json:"checklist"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", nil, false
	}
	w := strings.ToUpper(strings.TrimSpace(parsed.Winner))
	switch w {
	case "A":
		if swapped {
			winner = "new"
		} else {
			winner = "old"
		}
	case "B":
		if swapped {
			winner = "old"
		} else {
			winner = "new"
		}
	case "TIE":
		winner = "tie"
	default:
		return "", nil, false
	}
	items = make([]domain.EvoSemanticCheckItem, 0, len(parsed.Checklist))
	for _, c := range parsed.Checklist {
		crit := strings.TrimSpace(c.Criterion)
		if crit == "" {
			continue
		}
		item := domain.EvoSemanticCheckItem{Criterion: crit}
		if swapped {
			item.NewPass = c.APass
			item.OldPass = c.BPass
		} else {
			item.OldPass = c.APass
			item.NewPass = c.BPass
		}
		items = append(items, item)
	}
	return winner, items, true
}

// CombineSemanticJudgeRounds merges round 1 (Old=A, New=B) and swapped round 2
// (New=A, Old=B). A directional winner is accepted ONLY when both rounds agree;
// any disagreement is marked inconsistent (position bias neutralized).
func CombineSemanticJudgeRounds(r1Winner, r2Winner string, r1Items, r2Items []domain.EvoSemanticCheckItem, model string) domain.EvoSemanticComparison {
	cmp := domain.EvoSemanticComparison{
		Model:        strings.TrimSpace(model),
		Round1Winner: r1Winner,
		Round2Winner: r2Winner,
	}
	if len(r1Items) > 0 {
		cmp.Checklist = r1Items
	} else if len(r2Items) > 0 {
		cmp.Checklist = r2Items
	}
	if r1Winner != "" && r1Winner == r2Winner {
		cmp.Consistent = true
		cmp.Winner = r1Winner
		switch r1Winner {
		case "new":
			cmp.Summary = "AI 换序双评一致（2/2）：候选版内容质量更优（仅供参考，不计入主分）"
		case "old":
			cmp.Summary = "AI 换序双评一致（2/2）：旧版内容质量更优（仅供参考，不计入主分）"
		default:
			cmp.Summary = "AI 换序双评一致（2/2）：两版内容质量持平（仅供参考）"
		}
		return cmp
	}
	cmp.Consistent = false
	cmp.Winner = "inconsistent"
	cmp.Summary = fmt.Sprintf("AI 换序双评结论不一致（第1评=%s，换序第2评=%s），已自动忽略以防偏置", r1Winner, r2Winner)
	return cmp
}

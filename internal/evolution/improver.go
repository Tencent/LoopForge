package evolution

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"agentflow/internal/domain"
)

// Evidence is one diagnosis sample shown to the improver. Holdout,
// regression and pinned samples are never passed here.
type Evidence struct {
	RunID      string `json:"runId"`
	WorkflowID string `json:"workflowId,omitempty"`
	Status     string `json:"status"`
	NodeID     string `json:"nodeId,omitempty"`
	Input      string `json:"input,omitempty"`
	Error      string `json:"error,omitempty"`
	Feedback   string `json:"feedback,omitempty"`
	Output     string `json:"output,omitempty"`
}

// Candidate is the improver's proposed body.
type Candidate struct {
	Title     string `json:"title"`
	Rationale string `json:"rationale"`
	Content   string `json:"content"`
}

const maxCandidateBytes = 60000

// BuildEvidence extracts what the improver may see from a diagnosis run.
func BuildEvidence(run domain.Run, finding domain.EvoFinding) Evidence {
	ev := Evidence{RunID: run.ID, WorkflowID: run.WorkflowID, Status: run.Status, Input: domain.TrimEvoText(run.Input, 1200)}
	for _, sig := range finding.Signals {
		if sig.RunID != run.ID {
			continue
		}
		ev.NodeID = sig.NodeID
		switch sig.Kind {
		case SignalNodeFailed:
			ev.Error = domain.TrimEvoText(sig.Detail, 800)
		default:
			if ev.Feedback == "" {
				ev.Feedback = domain.TrimEvoText(sig.Detail, 800)
			}
		}
		break
	}
	if nr := run.Nodes[ev.NodeID]; nr != nil {
		if ev.Error == "" {
			ev.Error = domain.TrimEvoText(nr.Error, 800)
		}
		if ev.Feedback == "" {
			ev.Feedback = domain.TrimEvoText(nr.Feedback, 800)
		}
		ev.Output = domain.TrimEvoText(nr.Output, 1500)
	}
	return ev
}

// BuildImproverPrompt asks for one improved body. The model first considers a
// minimal invariant fix and a domain-level rewrite, then outputs the smallest
// composition that addresses the evidence (Harness² INV/DOM idea, single call).
func BuildImproverPrompt(kind, name, content string, finding domain.EvoFinding, evidence []Evidence, experience []string) (system, prompt string) {
	label := "角色 Prompt"
	if kind == domain.EvoAssetRule {
		label = "规则正文"
	}
	var b strings.Builder
	b.WriteString("你是 LoopForge 平台的资产改进器。你要改进一份" + label + "，让它在下面这些失败样本上表现更好，同时不破坏原本做得好的场景。\n")
	b.WriteString("工作方法：\n")
	b.WriteString("1. 先在心里列出两种改法：A 最小不变量修补（只补缺失的约束/步骤/检查），B 领域视角重写（重新组织该领域的关键做法）。\n")
	b.WriteString("2. 输出时只给 A 为主、必要时吸收 B 中关键点的合成版本，尽量少改；保留原有结构、标题、占位符（如 {{input}}、{{repository}}）和所有未出问题的要求。\n")
	b.WriteString("3. 不要针对单个样本写死具体文件名、ID、分支名或答案；写成可泛化的做法。\n")
	b.WriteString("4. 不得加入：放宽安全/权限/审批要求、跳过门禁、伪造结果、调用外部写操作的指示。\n")
	b.WriteString("只输出一个 JSON 对象，不要 Markdown 代码块：\n")
	b.WriteString(`{"title":"一句话说明改了什么","rationale":"为什么这样改能修好这些失败、为什么不会影响其他场景","content":"改进后的完整正文"}` + "\n")
	b.WriteString("content 必须是完整替换后的正文，而不是 diff。\n")
	var p strings.Builder
	p.WriteString(fmt.Sprintf("资产类型：%s\n资产名称：%s\n\n## 当前正文\n\n%s\n\n", label, firstText(name, finding.AssetID), content))
	p.WriteString("## 诊断结论\n\n" + finding.Title + "\n")
	if strings.TrimSpace(finding.Summary) != "" {
		p.WriteString(finding.Summary + "\n")
	}
	if len(experience) > 0 {
		p.WriteString("\n## 平台经验（之前发布过且有效的改法，供参考）\n\n")
		for _, line := range experience {
			p.WriteString("- " + domain.TrimEvoText(line, 300) + "\n")
		}
	}
	raw, _ := json.Marshal(evidence)
	p.WriteString("\n## 失败样本\n\n" + string(raw) + "\n")
	return b.String(), p.String()
}

// ParseCandidate decodes the improver output.
func ParseCandidate(raw string) (Candidate, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return Candidate{}, fmt.Errorf("改进器没有返回 JSON")
	}
	var c Candidate
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return Candidate{}, fmt.Errorf("改进器返回的 JSON 无法解析：%w", err)
	}
	c.Title = strings.TrimSpace(c.Title)
	c.Rationale = strings.TrimSpace(c.Rationale)
	c.Content = strings.TrimSpace(c.Content)
	if c.Content == "" {
		return Candidate{}, fmt.Errorf("改进器没有给出新正文")
	}
	if c.Title == "" {
		c.Title = "改进正文"
	}
	return c, nil
}

var placeholderPattern = regexp.MustCompile(`\{\{[a-zA-Z_]+\}\}`)

// StaticCheck rejects candidates that cannot be correct regardless of replay:
// unchanged, empty, oversized, shrunk to a stub, or missing placeholders the
// current body relies on.
func StaticCheck(base, candidate string) error {
	base, candidate = strings.TrimSpace(base), strings.TrimSpace(candidate)
	if candidate == "" {
		return fmt.Errorf("新正文为空")
	}
	if candidate == base {
		return fmt.Errorf("新正文与当前版本相同")
	}
	if len(candidate) > maxCandidateBytes {
		return fmt.Errorf("新正文超过 %d 字节", maxCandidateBytes)
	}
	if len([]rune(base)) >= 200 && len([]rune(candidate))*3 < len([]rune(base)) {
		return fmt.Errorf("新正文比当前版本短了三分之二以上，疑似丢失内容")
	}
	for _, ph := range placeholderPattern.FindAllString(base, -1) {
		if !strings.Contains(candidate, ph) {
			return fmt.Errorf("新正文丢失了占位符 %s", ph)
		}
	}
	return nil
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

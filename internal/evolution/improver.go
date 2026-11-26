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
	RunID       string  `json:"runId"`
	WorkflowID  string  `json:"workflowId,omitempty"`
	Status      string  `json:"status"`
	NodeID      string  `json:"nodeId,omitempty"`
	Tokens      int64   `json:"tokens,omitempty"`
	ToolCalls   int     `json:"toolCalls,omitempty"`
	DurationSec float64 `json:"durationSec,omitempty"`
	Input       string  `json:"input,omitempty"`
	Error       string  `json:"error,omitempty"`
	Feedback    string  `json:"feedback,omitempty"`
	Output      string  `json:"output,omitempty"`
}

// Candidate is the improver's proposed body.
type Candidate struct {
	Title     string                  `json:"title"`
	Rationale string                  `json:"rationale"`
	Content   string                  `json:"content"`
	Review    *domain.EvoChangeReview `json:"review,omitempty"`
}

const maxCandidateBytes = 60000

// BuildEvidence extracts what the improver may see from a diagnosis run.
func BuildEvidence(run domain.Run, finding domain.EvoFinding) Evidence {
	ev := Evidence{
		RunID:       run.ID,
		WorkflowID:  run.WorkflowID,
		Status:      run.Status,
		Tokens:      RunTokens(run),
		ToolCalls:   RunToolCalls(run),
		DurationSec: round1(float64(RunDurationMs(run)) / 1000.0),
		Input:       domain.TrimEvoText(run.Input, 1200),
	}
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
	b.WriteString("你是 LoopForge 平台的资产改进器。你要改进一份" + label + "，让它在下面这些失败或被用户反馈的样本上表现更好，同时不破坏原本做得好的场景。\n")
	b.WriteString("工作方法：\n")
	b.WriteString("1. 先在心里列出两种改法：A 最小不变量修补（只补缺失的约束/步骤/检查），B 领域视角重写（重新组织该领域的关键做法）。\n")
	b.WriteString("2. 输出时只给 A 为主、必要时吸收 B 中关键点的合成版本，尽量少改；保留原有结构、标题、占位符（如 {{input}}、{{repository}}）和所有未出问题的要求。\n")
	b.WriteString("3. 不要针对单个样本写死具体文件名、ID、分支名或答案；写成可泛化的做法。\n")
	b.WriteString("4. 不得加入：放宽安全/权限/审批要求、跳过门禁、伪造结果、调用外部写操作的指示。\n")
	b.WriteString("只输出一个 JSON 对象，不要 Markdown 代码块：\n")
	b.WriteString(`{"title":"24字以内说明改了什么","rationale":"修改理由和适用边界","content":"改进后的完整正文","review":{"problem":"24字以内说明用户遇到的问题","reason":"70字以内解释问题原因和修改目的，不承诺已修复","changes":[{"title":"20字以内的改动要点","reason":"60字以内说明为什么改","before":"从当前正文原样摘取的连续片段","after":"从新正文原样摘取的连续片段","sourceRunIds":["关联的失败样本 runId"]}]}}` + "\n")
	b.WriteString("content 必须是完整替换后的正文，而不是 diff。\n")
	b.WriteString("review 供用户审查：changes 最多 3 项；before/after 各不超过 160 字，必须逐字来自对应正文，不使用省略号拼接。纯新增可令 before 为空，纯删除可令 after 为空。sourceRunIds 只能引用本次给出的失败样本；不要编造验证成绩、线上效果或修改了权限等未经验证的结论。\n")
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
	var payload struct {
		Title     string          `json:"title"`
		Rationale string          `json:"rationale"`
		Content   string          `json:"content"`
		Review    json.RawMessage `json:"review"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		// Models often put literal newlines, tabs or unescaped quotes
		// (e.g. 以 "ERROR" 开头) inside the long "content" string; repair
		// them and try once more.
		if retryErr := json.Unmarshal([]byte(repairJSONStrings(body)), &payload); retryErr != nil {
			return Candidate{}, fmt.Errorf("改进器返回的 JSON 无法解析：%w", err)
		}
	}
	c := Candidate{Title: payload.Title, Rationale: payload.Rationale, Content: payload.Content}
	var review domain.EvoChangeReview
	if len(payload.Review) > 0 && string(payload.Review) != "null" && json.Unmarshal(payload.Review, &review) == nil {
		c.Review = &review
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

// CandidateReview drops unsupported excerpts and foreign run references. Old
// improvers may omit review entirely; explanation metadata never blocks a fix.
func CandidateReview(c Candidate, base string, evidence []Evidence) *domain.EvoChangeReview {
	if c.Review == nil {
		return nil
	}
	allowed := make(map[string]bool, len(evidence))
	for _, ev := range evidence {
		allowed[ev.RunID] = ev.RunID != ""
	}
	review := &domain.EvoChangeReview{
		Problem: domain.TrimEvoText(c.Review.Problem, 40),
		Reason:  domain.TrimEvoText(c.Review.Reason, 100),
	}
	for _, change := range c.Review.Changes {
		before, after := strings.TrimSpace(change.Before), strings.TrimSpace(change.After)
		if before == after || len([]rune(before)) > 160 || len([]rune(after)) > 160 ||
			(before != "" && !strings.Contains(base, before)) || (after != "" && !strings.Contains(c.Content, after)) {
			continue
		}
		point := domain.EvoChangePoint{Title: domain.TrimEvoText(change.Title, 30), Reason: domain.TrimEvoText(change.Reason, 80), Before: before, After: after}
		if point.Title == "" {
			continue
		}
		seen := map[string]bool{}
		for _, id := range change.SourceRunIDs {
			if allowed[id] && !seen[id] {
				point.SourceRunIDs = append(point.SourceRunIDs, id)
				seen[id] = true
			}
		}
		review.Changes = append(review.Changes, point)
		if len(review.Changes) == 3 {
			break
		}
	}
	if review.Problem == "" && review.Reason == "" && len(review.Changes) == 0 {
		return nil
	}
	return review
}

// repairJSONStrings escapes control characters and stray quotes that appear
// inside JSON string literals, leaving structure and existing escapes
// untouched. A quote inside a string only terminates it when the following
// text looks like JSON structure.
func repairJSONStrings(body string) string {
	var b strings.Builder
	b.Grow(len(body) + 16)
	inString, escaped := false, false
	for i, r := range body {
		if !inString {
			if r == '"' {
				inString = true
			}
			b.WriteRune(r)
			continue
		}
		switch {
		case escaped:
			escaped = false
			b.WriteRune(r)
		case r == '\\':
			escaped = true
			b.WriteRune(r)
		case r == '"' && closesJSONString(body[i+1:]):
			inString = false
			b.WriteRune(r)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func closesJSONString(rest string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ':':
		return true
	case ',':
		next := strings.TrimLeft(rest[1:], " \t\r\n")
		return next != "" && strings.ContainsRune(`"{[`, rune(next[0]))
	case '}', ']':
		next := strings.TrimLeft(rest[1:], " \t\r\n")
		return next == "" || strings.ContainsRune(`,}]`, rune(next[0]))
	}
	return false
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

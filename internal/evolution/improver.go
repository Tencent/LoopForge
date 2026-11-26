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
	RunID       string            `json:"runId"`
	WorkflowID  string            `json:"workflowId,omitempty"`
	Status      string            `json:"status"`
	NodeID      string            `json:"nodeId,omitempty"`
	Tokens      int64             `json:"tokens,omitempty"`
	ToolCalls   int               `json:"toolCalls,omitempty"`
	DurationSec float64           `json:"durationSec,omitempty"`
	Input       string            `json:"input,omitempty"`
	Error       string            `json:"error,omitempty"`
	Feedback    string            `json:"feedback,omitempty"`
	Output      string            `json:"output,omitempty"`
	ToolTrace   *ToolTraceSummary `json:"toolTrace,omitempty"`
}

// MaxRefineRounds caps automatic reflection-and-refinement retries after an
// initial candidate fails replay validation.
const MaxRefineRounds = 1

// ReplayCritique summarizes one diagnosis replay outcome for reflection so the
// improver can see why the previous draft failed and fix it directly.
// Holdout, regression and pinned sample details must never be passed here.
type ReplayCritique struct {
	SourceRunID  string            `json:"sourceRunId"`
	ReplayRunID  string            `json:"replayRunId,omitempty"`
	SourceStatus string            `json:"sourceStatus,omitempty"`
	ReplayStatus string            `json:"replayStatus,omitempty"`
	Outcome      string            `json:"outcome"`
	Error        string            `json:"error,omitempty"`
	SourceTokens int64             `json:"sourceTokens,omitempty"`
	ReplayTokens int64             `json:"replayTokens,omitempty"`
	SourceTools  int               `json:"sourceTools,omitempty"`
	ReplayTools  int               `json:"replayTools,omitempty"`
	SourceDurSec float64           `json:"sourceDurSec,omitempty"`
	ReplayDurSec float64           `json:"replayDurSec,omitempty"`
	ToolTrace    *ToolTraceSummary `json:"toolTrace,omitempty"`
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

// BuildEvidenceWithTools enriches BuildEvidence with a compact tool-trace
// hotspot summary so the improver sees duplicate calls, tool failures, and
// heavy payloads instead of only high-level counts.
func BuildEvidenceWithTools(run domain.Run, finding domain.EvoFinding, tools []domain.TelemetryEvent) Evidence {
	ev := BuildEvidence(run, finding)
	if len(tools) == 0 && len(run.Telemetry) > 0 {
		tools = run.Telemetry
	}
	ev.ToolTrace = BuildToolTraceSummary(tools, ev.NodeID)
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
	b.WriteString("5. 若样本包含 toolTrace（工具调用热点轨迹），优先针对其中的重复调用（duplicates）、失败调用（failures）和高耗时/大输出调用（heavyCalls）给出具体、可泛化的执行策略约束（例如合并查询、先定位范围再读取、控制输出体积、失败后避免同类盲目重试）。\n")
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
		p.WriteString("\n## 平台经验（历史发布成功的做法与需避免的失败教训，供参考）\n\n")
		for _, line := range experience {
			p.WriteString("- " + domain.TrimEvoText(line, 300) + "\n")
		}
	}
	raw, _ := json.Marshal(evidence)
	p.WriteString("\n## 失败样本\n\n" + string(raw) + "\n")
	return b.String(), p.String()
}

// BuildRefinePrompt asks the improver to reflect on a failed replay round and
// produce a targeted revision. Only diagnosis-group replay details are shown;
// hidden holdout/regression outcomes appear only as aggregate counts.
func BuildRefinePrompt(
	kind, name, baseContent, prevCandidate string,
	finding domain.EvoFinding,
	preferences []string,
	prevVerdict string,
	critiques []ReplayCritique,
	hiddenNote string,
	experience []string,
) (system, prompt string) {
	label := "角色 Prompt"
	if kind == domain.EvoAssetRule {
		label = "规则正文"
	}
	var b strings.Builder
	b.WriteString("你是 LoopForge 平台的资产改进器，当前正在执行「失败回放反思修正」阶段。\n")
	b.WriteString("上一轮你生成的候选" + label + "在真实历史回放中没有通过验证。你需要根据回放判定、诊断样本的指标变化和工具轨迹，找出上一轮改法失效或退化的根本原因，并基于「线上基线正文」生成一版更精准的新候选正文。\n")
	b.WriteString("反思修正要求：\n")
	b.WriteString("1. 禁止重复上一轮已失效的改法，也不要直接原样返回上一轮草稿或基线正文。\n")
	b.WriteString("2. 若回放显示回归（regressed）或报错，优先定位新加规则是否过于激进/缺失必要步骤，并修复该副作用。\n")
	b.WriteString("3. 若回放显示未退化但 P1 效率目标未达标，结合回放后的 toolTrace 找出仍然存在的重复调用、冗余输出或无效步骤，给出更直接可执行的约束。\n")
	b.WriteString("4. 保留基线正文原有结构、标题、占位符（如 {{input}}、{{repository}}）和所有未出问题的要求；不要针对单条样本写死文件名/ID；不得放宽安全/审批/门禁要求。\n")
	b.WriteString("只输出一个 JSON 对象，不要 Markdown 代码块：\n")
	b.WriteString(`{"title":"24字以内说明本轮修正了什么","rationale":"结合上轮失败原因说明本轮修正思路与边界","content":"改进后的完整正文","review":{"problem":"24字以内说明要解决的问题","reason":"70字以内说明针对上轮未通过原因做了哪些调整，不承诺已修复","changes":[{"title":"20字以内的改动要点","reason":"60字以内说明为什么改","before":"从当前线上基线正文原样摘取的连续片段","after":"从新正文原样摘取的连续片段","sourceRunIds":["关联的诊断样本 sourceRunId"]}]}}` + "\n")
	b.WriteString("content 必须是完整替换后的正文，而不是 diff；review.changes 中的 before 必须逐字来自「当前线上基线正文」，after 必须逐字来自新正文。\n")

	var p strings.Builder
	p.WriteString(fmt.Sprintf("资产类型：%s\n资产名称：%s\n\n## 当前线上基线正文\n\n%s\n\n", label, firstText(name, finding.AssetID), baseContent))
	p.WriteString("## 原始诊断目标\n\n" + finding.Title + "\n")
	if strings.TrimSpace(finding.Summary) != "" {
		p.WriteString(finding.Summary + "\n")
	}
	if len(preferences) > 0 {
		p.WriteString("优化优先级：" + SOPPreferencePriorityText(preferences) + "\n")
		p.WriteString(SOPPreferencePromptGuidance(preferences) + "\n")
	}
	p.WriteString("\n## 上一轮未通过的候选正文\n\n" + prevCandidate + "\n\n")
	p.WriteString("## 上一轮回放判定（未通过）\n\n" + prevVerdict + "\n")
	if strings.TrimSpace(hiddenNote) != "" {
		p.WriteString("\n## 隐藏留出/回归集汇总（不展示单条详情，防止过拟合）\n\n" + hiddenNote + "\n")
	}
	if len(experience) > 0 {
		p.WriteString("\n## 平台经验（历史发布成功的做法与需避免的失败教训）\n\n")
		for _, line := range experience {
			p.WriteString("- " + domain.TrimEvoText(line, 300) + "\n")
		}
	}
	raw, _ := json.Marshal(critiques)
	p.WriteString("\n## 诊断样本回放结果与工具轨迹\n\n" + string(raw) + "\n")
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

package domain

import (
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	ClarificationInProgress = "in_progress"
	ClarificationReady      = "ready"
	ClarificationNeedsUser  = "needs_user"
	ClarificationAnalyzing  = "analyzing"
	ClarificationKickoff    = "请先检查这张工作项的描述和相关代码，告诉我还缺哪些关键信息。"
)

// DefaultClarificationPolicy is the fallback policy for projects that never
// customised clarification. Import scoring and the SOP gate both use it.
func DefaultClarificationPolicy() ClarificationPolicy {
	return ClarificationPolicy{Enabled: true, SkillID: "requirement-clarification", Threshold: 80, Dimensions: []ClarificationDimension{
		{ID: "goal", Name: "目标与价值", Question: "要解决什么问题，预期产生什么业务价值？", Weight: 20, MinLength: 30},
		{ID: "user", Name: "用户与场景", Question: "谁会在什么场景下使用？", Weight: 15, MinLength: 20},
		{ID: "scope", Name: "范围与边界", Question: "本次包含什么、不包含什么？", Weight: 20, MinLength: 30},
		{ID: "acceptance", Name: "验收标准", Question: "如何用可测试的条件判断需求完成？", Weight: 25, MinLength: 40},
		{ID: "constraints", Name: "约束与依赖", Question: "有哪些技术约束、依赖、风险或截止时间？", Weight: 20, MinLength: 25},
	}}
}

func (p ClarificationPolicy) IsZero() bool {
	return !p.Enabled && p.Threshold == 0 && p.SkillID == "" && len(p.Dimensions) == 0
}

// EffectiveClarificationPolicy fills missing dimensions from the default
// policy. A completely unset policy is treated as the platform default so
// imported requirements are always scored.
func EffectiveClarificationPolicy(p ClarificationPolicy) ClarificationPolicy {
	if p.IsZero() {
		return DefaultClarificationPolicy()
	}
	if len(p.Dimensions) == 0 {
		p.Dimensions = DefaultClarificationPolicy().Dimensions
	}
	return p
}

func ClarificationPassed(policy ClarificationPolicy, score int) bool {
	policy = EffectiveClarificationPolicy(policy)
	return !policy.Enabled || score >= policy.Threshold
}

// ShouldRefreshImportScore reports whether an existing assessment may be
// replaced by a fresh import-time score. Multi-turn conversations are kept.
func ShouldRefreshImportScore(a ClarificationAssessment) bool {
	if len(a.History) > 0 {
		return false
	}
	switch a.Status {
	case ClarificationReady, ClarificationInProgress, ClarificationAnalyzing:
		return false
	}
	return true
}

// ShouldAutoStartClarification reports whether the server should kick off an
// AI clarification round without a browser. Import scoring (needs_user, no
// questions yet) and a crashed analyzing round both qualify; a round that
// already asked the user waits for answers.
func ShouldAutoStartClarification(issue Issue) bool {
	if !strings.EqualFold(issue.Kind, "requirement") {
		return false
	}
	if !issue.Clarification.Gated {
		return false
	}
	a := issue.Clarification
	if a.Passed || a.Status == ClarificationReady {
		return false
	}
	if len(a.Questions) > 0 {
		return false
	}
	switch a.Status {
	case ClarificationAnalyzing:
		return true
	case ClarificationNeedsUser, "":
		return true
	case ClarificationInProgress:
		if len(a.History) == 0 {
			return true
		}
		return strings.EqualFold(strings.TrimSpace(a.History[len(a.History)-1].Role), "user")
	}
	return false
}

// RequirementSOPBlocked reports whether a requirement is below the project's
// clarification threshold. Gongfeng bugs, disabled policies, and tickets that
// already finished an interactive clarification round are not blocked.
func RequirementSOPBlocked(issue Issue, policy ClarificationPolicy) bool {
	if !strings.EqualFold(issue.Kind, "requirement") {
		return false
	}
	if !issue.Clarification.Gated {
		return false
	}
	if issue.Clarification.Status == ClarificationReady {
		return false
	}
	policy = EffectiveClarificationPolicy(policy)
	return policy.Enabled && !issue.Clarification.Passed
}

// ScoreClarificationDimensions folds per-dimension ratings (0-100) into a
// single weighted score. Unrated dimensions count as zero and are still
// stored, so a partial answer can never inflate the total or come back empty.
func ScoreClarificationDimensions(dimensions []ClarificationDimension, ratings map[string]int) (int, map[string]int) {
	normalized := map[string]int{}
	if len(dimensions) == 0 {
		return 0, normalized
	}
	if ratings == nil {
		ratings = map[string]int{}
	}
	total, weightSum := 0, 0
	for _, dimension := range dimensions {
		weight := dimension.Weight
		if weight <= 0 {
			weight = 1
		}
		weightSum += weight
		rating := ratings[dimension.ID]
		if rating < 0 {
			rating = 0
		}
		if rating > 100 {
			rating = 100
		}
		normalized[dimension.ID] = rating
		total += rating * weight
	}
	if weightSum == 0 {
		return 0, normalized
	}
	return total / weightSum, normalized
}

func TrimClarificationMissing(missing []string) []string {
	out := make([]string, 0, len(missing))
	for _, item := range missing {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AssessClarificationAnswers scores structured answers by length against each
// dimension's minLength, then folds them with the policy weights.
func AssessClarificationAnswers(policy ClarificationPolicy, answers map[string]string) ClarificationAssessment {
	policy = EffectiveClarificationPolicy(policy)
	if answers == nil {
		answers = map[string]string{}
	}
	score, totalWeight := 0, 0
	dimensions := map[string]int{}
	missing := []string{}
	for _, dimension := range policy.Dimensions {
		weight := dimension.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
		minLength := dimension.MinLength
		if minLength <= 0 {
			minLength = 20
		}
		length := len([]rune(strings.TrimSpace(answers[dimension.ID])))
		dimensionScore := 0
		if minLength > 0 {
			dimensionScore = length * 100 / minLength
		}
		if dimensionScore > 100 {
			dimensionScore = 100
		}
		dimensions[dimension.ID] = dimensionScore
		score += dimensionScore * weight
		if dimensionScore < 100 {
			missing = append(missing, dimension.ID)
		}
	}
	if totalWeight > 0 {
		score /= totalWeight
	}
	assessment := ClarificationAssessment{
		Score:      score,
		Passed:     ClarificationPassed(policy, score),
		Answers:    answers,
		Dimensions: dimensions,
		Missing:    missing,
		AssessedAt: time.Now().UTC(),
	}
	if !assessment.Passed {
		assessment.Status = ClarificationNeedsUser
	}
	return assessment
}

// AssessRequirementText scores an unstructured title+description (TAPD import,
// chat-created stories) against the policy dimensions. Matching excerpts are
// length-scored; dimensions with no evidence stay at zero.
func AssessRequirementText(policy ClarificationPolicy, title, description string) ClarificationAssessment {
	policy = EffectiveClarificationPolicy(policy)
	text := NormalizeRequirementText(title, description)
	answers := map[string]string{}
	for _, dimension := range policy.Dimensions {
		answers[dimension.ID] = excerptForDimension(text, dimension)
	}
	return AssessClarificationAnswers(policy, answers)
}

// ScoreRequirementIssue applies import-time scoring unless a conversation is
// already in progress. Client-supplied dimension ratings, when present, are
// folded with the policy weights instead of re-deriving excerpts.
func ScoreRequirementIssue(issue Issue, policy ClarificationPolicy) ClarificationAssessment {
	if !strings.EqualFold(issue.Kind, "requirement") {
		return issue.Clarification
	}
	if !ShouldRefreshImportScore(issue.Clarification) {
		return issue.Clarification
	}
	policy = EffectiveClarificationPolicy(policy)
	if len(issue.Clarification.Dimensions) > 0 {
		score, rated := ScoreClarificationDimensions(policy.Dimensions, issue.Clarification.Dimensions)
		out := issue.Clarification
		out.Score = score
		out.Dimensions = rated
		out.Passed = ClarificationPassed(policy, score)
		out.Missing = TrimClarificationMissing(out.Missing)
		if len(out.Missing) == 0 {
			for _, dimension := range policy.Dimensions {
				if rated[dimension.ID] < 100 {
					out.Missing = append(out.Missing, dimension.ID)
				}
			}
		}
		out.AssessedAt = time.Now().UTC()
		if !out.Passed && out.Status == "" {
			out.Status = ClarificationNeedsUser
		}
		if out.Passed && out.Status == ClarificationNeedsUser {
			out.Status = ""
		}
		out.Gated = true
		return out
	}
	out := AssessRequirementText(policy, issue.Title, issue.Description)
	out.Gated = true
	return out
}

func NormalizeRequirementText(title, description string) string {
	parts := []string{strings.TrimSpace(title), StripRequirementHTML(description)}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

func StripRequirementHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func excerptForDimension(text string, dimension ClarificationDimension) string {
	keys := dimensionKeywords(dimension)
	if text == "" || len(keys) == 0 {
		return ""
	}
	var hits []string
	for _, unit := range splitRequirementUnits(text) {
		if unitContainsKeyword(unit, keys) {
			hits = append(hits, unit)
		}
	}
	return strings.Join(hits, "\n")
}

func splitRequirementUnits(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var units []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		start := 0
		for i, r := range para {
			if r == '。' || r == '！' || r == '？' || r == ';' || r == '；' {
				piece := strings.TrimSpace(para[start:i])
				if piece != "" {
					units = append(units, piece)
				}
				start = i + len(string(r))
			}
		}
		if tail := strings.TrimSpace(para[start:]); tail != "" {
			units = append(units, tail)
		}
	}
	return units
}

var (
	nameSplitter      = regexp.MustCompile(`[与和或、,，/]`)
	questionTokenRe   = regexp.MustCompile(`[\p{Han}]{2,}|[A-Za-z][A-Za-z0-9_-]{1,}`)
	clarificationStop = map[string]bool{
		"什么": true, "如何": true, "哪些": true, "怎么": true, "是否": true, "以及": true,
		"或者": true, "需要": true, "这个": true, "一个": true, "进行": true, "还有": true,
		"还是": true, "可以": true, "应该": true, "为了": true, "及其": true, "相关": true,
		"具体": true, "正确": true, "条件": true, "判断": true, "已经": true, "修复": true,
		"预期": true, "产生": true, "使用": true, "完成": true, "时间": true, "要求": true,
	}
)

func usefulKeyword(raw string) bool {
	n := 0
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.Is(unicode.Han, r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n >= 2
}

func dimensionKeywords(dimension ClarificationDimension) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] || clarificationStop[raw] || !usefulKeyword(raw) {
			return
		}
		seen[raw] = true
		keys = append(keys, raw)
	}
	for _, part := range nameSplitter.Split(dimension.Name, -1) {
		add(part)
	}
	for _, tok := range questionTokenRe.FindAllString(dimension.Question, -1) {
		add(tok)
	}
	add(dimension.ID)
	return keys
}

func unitContainsKeyword(unit string, keys []string) bool {
	lower := strings.ToLower(unit)
	for _, key := range keys {
		if strings.Contains(lower, strings.ToLower(key)) {
			return true
		}
	}
	return false
}

package domain

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Gongfeng MCP often returns http://git.example.com/.../merge_requests/N. Accept
// either scheme and store the canonical https URL.
var githubPRURL = regexp.MustCompile(`https?://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/(\d+)`)

var gongfengMRURL = regexp.MustCompile(`https?://git\.example\.com/([^\s"'<>#]+)/merge_requests/(\d+)`)

// GongfengMRRef is one merge request URL extracted from free text.
type GongfengMRRef struct {
	Project string
	IID     int
	URL     string
}

// ParseGongfengMR extracts the first Gongfeng merge request URL from text.
func ParseGongfengMR(text string) (project string, iid int, url string, ok bool) {
	refs := ParseGongfengMRs(text)
	if len(refs) == 0 {
		return "", 0, "", false
	}
	return refs[0].Project, refs[0].IID, refs[0].URL, true
}

// ParseGongfengMRs extracts unique Gongfeng merge request URLs in appearance order.
func ParseGongfengMRs(text string) []GongfengMRRef {
	for _, match := range githubPRURL.FindAllStringSubmatch(text, -1) {
		text = strings.ReplaceAll(text, match[0], "https://git.example.com/github:"+match[1]+"/merge_requests/"+match[2])
	}
	matches := gongfengMRURL.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]GongfengMRRef, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		project := strings.TrimSuffix(strings.TrimSuffix(match[1], "/"), "/-")
		iid, _ := strconv.Atoi(match[2])
		if project == "" || iid <= 0 {
			continue
		}
		key := strings.ToLower(project) + "#" + strconv.Itoa(iid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, GongfengMRRef{Project: project, IID: iid, URL: GongfengMRURL(project, iid)})
	}
	return out
}

// MRArtifactPriority ranks run artifacts when recovering an issue's own MR.
// repository-patch / diffs score 0 because they quote other issues' MR URLs.
func MRArtifactPriority(name, path, nodeID string) int {
	n := strings.ToLower(strings.TrimSpace(name))
	p := strings.ToLower(strings.TrimSpace(path))
	blob := n + " " + p
	if strings.Contains(blob, "patch") || strings.Contains(blob, "diff") {
		return 0
	}
	switch n {
	case "mr-body":
		return 40
	case "closeout":
		return 30
	case "e2e-report":
		return 20
	}
	if strings.EqualFold(strings.TrimSpace(nodeID), "mr") {
		return 10
	}
	if strings.Contains(n, "mr") || strings.Contains(blob, "closeout") || strings.Contains(blob, "e2e-report") {
		return 10
	}
	return 0
}

func SummarizeDeliveries(items []AnalyticsDelivery) AnalyticsDeliverySummary {
	out := AnalyticsDeliverySummary{Issues: len(items)}
	var durationSum, durationN int64
	var execSum, execN int64
	execValues := make([]int64, 0, len(items))
	for _, item := range items {
		if item.SOPSucceeded {
			out.SOPSucceeded++
			out.CompletedIssues++
		}
		// 按受控标签归类需求，同一需求只计一次（items 已按 Issue 聚合）。
		switch NormalizeMetricCategory(item.Category) {
		case MetricCategoryRequirement:
			out.RequirementCount++
		case MetricCategoryBugfix:
			out.BugFixCount++
		}
		if !item.CostKnown {
			out.CostUnknownIssues++
		}
		if item.ExecutionDurationMs > 0 {
			execSum += item.ExecutionDurationMs
			execN++
			execValues = append(execValues, item.ExecutionDurationMs)
		}
		if item.MRURL != "" {
			out.WithMR++
		}
		if item.MRState == "merged" {
			out.MRMerged++
		}
		if item.MRState == "integrated" {
			out.MRIntegrated++
		}
		if item.Delivered {
			out.Delivered++
		}
		out.TotalTokens += item.Tokens
		out.TotalCostUSD += item.CostUSD
		if item.DurationMs > 0 {
			durationSum += item.DurationMs
			durationN++
		}
	}
	if durationN > 0 {
		out.AverageDurationMs = durationSum / durationN
	}
	if execN > 0 {
		out.AverageExecutionDurationMs = execSum / execN
		out.P50ExecutionDurationMs = analyticsPercentile(execValues, 50)
		out.P95ExecutionDurationMs = analyticsPercentile(execValues, 95)
	}
	return out
}

// analyticsPercentile 取最近邻分位数，输入允许无序（内部排序副本）。
func analyticsPercentile(values []int64, pct int) int64 {
	if len(values) == 0 || pct <= 0 {
		return 0
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if pct >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := (len(sorted) - 1) * pct / 100
	return sorted[idx]
}

// 受控统计标签：用于交付指标归类与前端筛选。
const (
	MetricCategoryRequirement = "requirement"
	MetricCategoryBugfix      = "bugfix"
	MetricCategoryOther       = "other"
)

// NormalizeMetricCategory 将受控标签归一到枚举值，空值归为 other。
func NormalizeMetricCategory(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "requirement", "需求开发", "feature", "需求":
		return MetricCategoryRequirement
	case "bugfix", "bug", "bug 修复", "bug修复", "缺陷修复":
		return MetricCategoryBugfix
	default:
		return MetricCategoryOther
	}
}

// FinalizeAnalyticsDelivery sets default MR state labels and delivered flag.
func FinalizeAnalyticsDelivery(item *AnalyticsDelivery) {
	if item.MRURL == "" && item.MRState == "" {
		item.MRState = "none"
	}
	if item.MRURL != "" && item.MRState == "" {
		item.MRState = "unknown"
	}
	item.Delivered = item.SOPSucceeded && item.MRState == "merged"
}

// NormalizeBranchName strips refs/heads/ and origin/ prefixes for comparison.
func NormalizeBranchName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "refs/heads/")
	name = strings.TrimPrefix(name, "origin/")
	return strings.TrimSpace(name)
}

func SameGitBranch(a, b string) bool {
	left := NormalizeBranchName(a)
	right := NormalizeBranchName(b)
	return left != "" && strings.EqualFold(left, right)
}

// SameGongfengMR reports whether two free-text URLs refer to the same
// Gongfeng merge request (project + IID), ignoring /- vs non-/- URL forms.
func SameGongfengMR(a, b string) bool {
	pa, ia, _, oka := ParseGongfengMR(a)
	pb, ib, _, okb := ParseGongfengMR(b)
	if !oka || !okb {
		left, right := strings.TrimSpace(a), strings.TrimSpace(b)
		return left != "" && strings.EqualFold(left, right)
	}
	return ia == ib && strings.EqualFold(pa, pb)
}

// IsMainlineBranch reports whether target is the repository default branch.
// When defaultBranch is empty, main and master are both treated as mainline.
func IsMainlineBranch(target, defaultBranch string) bool {
	target = NormalizeBranchName(target)
	if target == "" {
		return false
	}
	if def := NormalizeBranchName(defaultBranch); def != "" {
		return SameGitBranch(target, def)
	}
	return SameGitBranch(target, "master") || SameGitBranch(target, "main")
}

// ClassifyMRDeliveryState maps Gongfeng merge state + target onto analytics states.
// "merged" means the change is on the repository default branch.
// A merge into the integrate baseline (or any other non-default branch) is "integrated".
// A Gongfeng merged MR with an empty target is "unknown" so we never claim 已合入.
func ClassifyMRDeliveryState(gongfengState, targetBranch, defaultBranch, integrateBranch string) string {
	state := strings.ToLower(strings.TrimSpace(gongfengState))
	if state != "merged" {
		if state == "" {
			return "unknown"
		}
		return state
	}
	if NormalizeBranchName(targetBranch) == "" {
		return "unknown"
	}
	if IsMainlineBranch(targetBranch, defaultBranch) {
		return "merged"
	}
	_ = integrateBranch
	return "integrated"
}

func AnalyticsMRRank(state string) int {
	switch state {
	case "merged":
		return 3
	case "integrated":
		return 2
	case "opened", "reopened", "closed":
		return 1
	default:
		return 0
	}
}

// BaselinePromoteIssueID marks a mr_delivery_states row as the
// integrate → default-branch MR. It is not a real work item.
const BaselinePromoteIssueID = "__baseline_promote__"

func IsBaselinePromoteIssueID(id string) bool {
	return strings.TrimSpace(id) == BaselinePromoteIssueID
}

// EffectiveAnalyticsMRState upgrades a work MR that only landed on the
// integrate baseline after the issue itself was marked mainline-merged
// (the human-owned baseline → default MR landed).
func EffectiveAnalyticsMRState(classified, issueDeliveryMRState string) string {
	if classified == "integrated" && strings.EqualFold(strings.TrimSpace(issueDeliveryMRState), "merged") {
		return "merged"
	}
	return classified
}

// KeepMainlineDeliveryLabel reports whether a looked-up work-MR state
// must not overwrite an already-promoted mainline label.
func KeepMainlineDeliveryLabel(current, lookedUp string) bool {
	if !strings.EqualFold(strings.TrimSpace(current), "merged") {
		return false
	}
	switch lookedUp {
	case "integrated", "unknown", "":
		return true
	default:
		return false
	}
}

// BranchListed reports whether branch appears in a commit's ref names.
func BranchListed(names []string, branch string) bool {
	if NormalizeBranchName(branch) == "" {
		return false
	}
	for _, name := range names {
		if SameGitBranch(name, branch) {
			return true
		}
	}
	return false
}

// WorkLandedWithPromote reports whether a baseline merge should be treated as
// already on the default branch after this promote MR merged. Delivery labels
// use commit containment instead; this remains for callers that only have times.
func WorkLandedWithPromote(workAt, promoteAt time.Time) bool {
	if workAt.IsZero() || promoteAt.IsZero() {
		return true
	}
	return !workAt.After(promoteAt)
}

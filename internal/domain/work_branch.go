package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultWorkBranchPrefix = "feature/"
	WorkBranchNamingPrefix  = "prefix"
	WorkBranchNamingAI      = "ai"
)

// WorkBranchNaming controls how a repository names newly generated work
// branches. An absent setting uses the legacy prefix. In prefix mode an empty
// Prefix deliberately produces a branch with no prefix.
type WorkBranchNaming struct {
	Mode       string `json:"mode"`
	Prefix     string `json:"prefix,omitempty"`
	ExecutorID string `json:"executorId,omitempty"`
	Model      string `json:"model,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
}

func (n WorkBranchNaming) Normalize() (WorkBranchNaming, error) {
	switch n.Mode {
	case WorkBranchNamingPrefix:
		n.Prefix = strings.TrimSpace(n.Prefix)
		n.ExecutorID, n.Model, n.Prompt = "", "", ""
		if !ValidNewWorkBranchName(n.Prefix + "work-branch") {
			return WorkBranchNaming{}, fmt.Errorf("工作分支前缀不能生成合法的 Git 分支名")
		}
	case WorkBranchNamingAI:
		n.ExecutorID = strings.TrimSpace(n.ExecutorID)
		n.Model = strings.TrimSpace(n.Model)
		n.Prompt = strings.TrimSpace(n.Prompt)
		n.Prefix = ""
		if n.ExecutorID == "" || n.Model == "" || n.Model == "default" || n.Model == "default-model" {
			return WorkBranchNaming{}, fmt.Errorf("请在仓库设置中为 AI 分支命名选择 CLI 和具体模型")
		}
		if strings.HasPrefix(n.Model, "-") {
			return WorkBranchNaming{}, fmt.Errorf("模型 ID 不能以 - 开头")
		}
		if err := ValidateCLIModelID(n.Model); err != nil {
			return WorkBranchNaming{}, err
		}
		if utf8.RuneCountInString(n.Prompt) > 2000 {
			return WorkBranchNaming{}, fmt.Errorf("分支命名要求最多 2000 个字符")
		}
	default:
		return WorkBranchNaming{}, fmt.Errorf("工作分支命名方式必须是 prefix 或 ai")
	}
	return n, nil
}

// ValidGitBranchName accepts literal branch names, excluding revisions and
// values that could be interpreted as command options.
func ValidGitBranchName(branch string) bool {
	if branch == "" || branch == "HEAD" || strings.HasPrefix(branch, "-") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") ||
		strings.Contains(branch, "@{") || strings.ContainsAny(branch, "~^:?*[\\") {
		return false
	}
	for _, r := range branch {
		if r <= ' ' || r == 127 {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// ValidNewWorkBranchName expects a short name suitable for a checkout.
func ValidNewWorkBranchName(branch string) bool {
	return ValidGitBranchName(branch) && branch != "@" && !strings.HasPrefix(branch, "refs/heads/")
}

// DefaultWorkBranch names the git work branch for an issue run. Gongfeng issues
// use fix-issue-{iid}; TAPD stories use story-{tapdId}; platform requirements use
// requirement-{number}. The suffix is a local timestamp that keeps branch names
// readable while separating dispatches started in different minutes.
func DefaultWorkBranch(issue Issue, now time.Time) string {
	suffix := now.In(time.Local).Format("20060102-1504")
	source := strings.ToLower(strings.TrimSpace(issue.Source))
	kind := strings.ToLower(strings.TrimSpace(issue.Kind))

	if source == "tapd" || strings.HasPrefix(strings.TrimSpace(issue.ID), "tapd-") {
		if id := tapdStoryID(issue); id != "" {
			return DefaultWorkBranchPrefix + "story-" + SafeGitRefSegment(id) + "-" + suffix
		}
	}
	if source == "gongfeng" || kind == "issue" {
		if n := gongfengIssueNumber(issue); n > 0 {
			return fmt.Sprintf("%sfix-issue-%d-%s", DefaultWorkBranchPrefix, n, suffix)
		}
	}
	if kind == "requirement" || source == "" || source == "local" {
		if issue.Number > 0 {
			return fmt.Sprintf("%srequirement-%d-%s", DefaultWorkBranchPrefix, issue.Number, suffix)
		}
	}
	if issue.Number > 0 {
		return fmt.Sprintf("%sfix-issue-%d-%s", DefaultWorkBranchPrefix, issue.Number, suffix)
	}
	if issue.ID != "" {
		return DefaultWorkBranchPrefix + SafeGitRefSegment(issue.ID) + "-" + suffix
	}
	return DefaultWorkBranchPrefix + "fix-" + suffix
}

func gongfengIssueNumber(issue Issue) int {
	if issue.ExternalRef != nil && issue.ExternalRef.ExternalIID > 0 {
		return issue.ExternalRef.ExternalIID
	}
	return issue.Number
}

func tapdStoryID(issue Issue) string {
	if issue.ExternalRef != nil {
		if id := strings.TrimSpace(issue.ExternalRef.ExternalID); id != "" {
			return id
		}
	}
	return strings.TrimPrefix(strings.TrimSpace(issue.ID), "tapd-")
}

// SafeGitRefSegment turns an arbitrary value into a single git ref segment:
// lowercase letters, digits, dash and underscore, with every other rune
// replaced by a dash and the segment cut at 64 characters. Leading and
// trailing dashes/dots are trimmed; values that sanitize to nothing fall back
// to "run" so callers always get a usable branch name.
func SafeGitRefSegment(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	result := strings.Trim(b.String(), "-.")
	if result == "" {
		return "run"
	}
	return result
}

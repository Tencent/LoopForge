package domain

import "strings"

const (
	ClarificationInProgress = "in_progress"
	ClarificationReady      = "ready"
	ClarificationNeedsUser  = "needs_user"
	ClarificationAnalyzing  = "analyzing"
	ClarificationKickoff    = "请先检查这张工作项的描述和相关代码，告诉我还缺哪些关键信息。"
)

// ShouldResumeClarification reports whether the server should resume an AI
// clarification round without a browser. Clarification is always started by
// the user; only a round that crashed mid-analysis is picked up again.
func ShouldResumeClarification(issue Issue) bool {
	return strings.TrimSpace(issue.ID) != "" && issue.Clarification.Status == ClarificationAnalyzing
}

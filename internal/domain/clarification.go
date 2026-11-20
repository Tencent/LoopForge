package domain

import (
	"fmt"
	"strings"
)

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

// Clarification before an SOP starts (Project.ClarifyBeforeStart,
// WorkflowDefinition.ClarifyBeforeStart). Empty on an SOP follows the
// project; empty on a project is ClarifyBeforeStartSkip.
const (
	ClarifyBeforeStartRequired = "required"
	ClarifyBeforeStartSkip     = "skip"
)

// ValidateClarifyBeforeStart rejects unknown values instead of silently
// starting SOPs without the clarification someone asked for.
func ValidateClarifyBeforeStart(v string) error {
	switch v {
	case "", ClarifyBeforeStartRequired, ClarifyBeforeStartSkip:
		return nil
	}
	return fmt.Errorf("clarifyBeforeStart %q is not supported (required, skip or empty)", v)
}

// ClarifyRequiredBeforeStart reports whether starting these SOPs of a
// project waits for a clarification conclusion: an SOP's own setting wins,
// the others follow the project, and one SOP requiring it is enough. No
// SOP at all follows the project.
func ClarifyRequiredBeforeStart(project Project, workflows []WorkflowDefinition) bool {
	projectRequired := strings.TrimSpace(project.ClarifyBeforeStart) == ClarifyBeforeStartRequired
	if len(workflows) == 0 {
		return projectRequired
	}
	for _, wf := range workflows {
		switch strings.TrimSpace(wf.ClarifyBeforeStart) {
		case ClarifyBeforeStartRequired:
			return true
		case ClarifyBeforeStartSkip:
		default:
			if projectRequired {
				return true
			}
		}
	}
	return false
}

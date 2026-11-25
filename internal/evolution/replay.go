package evolution

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// Replay budgets per proposal.
const (
	MaxDiagnosisSamples  = 2
	MaxHoldoutSamples    = 2
	MaxRegressionSamples = 2
	MaxPinnedSamples     = 2
)

// RunScore grades a terminal run: 0 failed/cancelled, 1 succeeded after
// rework, 2 succeeded cleanly. Non-terminal runs return -1.
func RunScore(run domain.Run) int {
	switch run.Status {
	case "succeeded":
		for _, nr := range run.Nodes {
			if nr != nil && nr.ReworkCount > 0 {
				return 1
			}
		}
		return 2
	case "failed", "cancelled":
		return 0
	}
	return -1
}

// ReplayWaiting reports whether a replay run is blocked on a human.
func ReplayWaiting(run domain.Run) bool {
	if run.Status == "waiting" {
		return true
	}
	for _, nr := range run.Nodes {
		if nr != nil && (nr.Status == "waiting_approval" || nr.Status == "pending_input") {
			return true
		}
	}
	return false
}

// Outcome compares the replay with its source run.
func Outcome(sourceScore, replayScore int) string {
	switch {
	case replayScore > sourceScore:
		return domain.EvoOutcomeFixed
	case replayScore < sourceScore:
		return domain.EvoOutcomeRegressed
	default:
		return domain.EvoOutcomeSame
	}
}

// FeedbackSample reports whether a cleanly succeeded run carries a user
// feedback or low eval signal in the finding. Its status cannot show a fix,
// but its input is exactly what the improver has to address.
func FeedbackSample(run domain.Run, finding domain.EvoFinding) bool {
	if RunScore(run) != 2 {
		return false
	}
	for _, sig := range finding.Signals {
		if sig.RunID == run.ID && (sig.Kind == SignalUserFeedback || sig.Kind == SignalEvalLow || sig.Kind == SignalSOPDetection || sig.Kind == SignalSOPPreference) {
			return true
		}
	}
	return false
}

// Summarize tallies replays and decides whether the candidate passes:
// every replay finished, at least one more fixed than regressed, at least
// one fix, and no more than one regression.
//
// When every diagnosis/holdout sample already succeeded (a finding raised
// only by user feedback or low eval scores), run status cannot prove a fix.
// The candidate then passes when nothing regressed, and the verdict asks the
// reviewer to compare replay outputs before releasing.
func Summarize(replays []domain.EvoReplay) domain.EvoReplaySummary {
	s := domain.EvoReplaySummary{Total: len(replays)}
	fixable := 0
	for _, r := range replays {
		if (r.Group == domain.EvoGroupDiagnosis || r.Group == domain.EvoGroupHoldout) && r.SourceScore < 2 {
			fixable++
		}
	}
	for _, r := range replays {
		if r.Outcome == "" {
			continue
		}
		s.Finished++
		switch r.Outcome {
		case domain.EvoOutcomeFixed:
			s.Fixed++
		case domain.EvoOutcomeRegressed:
			s.Regressed++
		case domain.EvoOutcomeBlocked:
			s.Blocked++
		case domain.EvoOutcomeChanged:
			s.Changed++
		default:
			s.Unchanged++
		}
	}
	if s.Total == 0 {
		s.Verdict = "没有可回放的样本"
		return s
	}
	if s.Finished < s.Total {
		s.Verdict = fmt.Sprintf("回放进行中 %d/%d", s.Finished, s.Total)
		return s
	}
	judged := s.Total - s.Blocked
	same := s.Unchanged + s.Changed
	switch {
	case judged == 0:
		s.Verdict = "全部回放被人工节点或异常阻塞，无法判定"
	case fixable == 0 && s.Regressed == 0 && s.Changed > 0:
		s.Passed = true
		s.Verdict = fmt.Sprintf("通过（反馈类问题）：%d 个回放按新正文改变了结论，%d 个结论不变，均未退化；请查看回放产出确认是否符合反馈后再发布", s.Changed, s.Unchanged)
	case fixable == 0 && s.Regressed == 0:
		s.Passed = true
		s.Verdict = fmt.Sprintf("通过（反馈类问题）：%d 个回放均未退化，但结论也没有变化；请对比回放产出确认反馈已解决后再发布", judged)
	case fixable == 0:
		s.Verdict = fmt.Sprintf("未通过：反馈类问题的回放出现 %d 个退化", s.Regressed)
	case s.Fixed >= 1 && s.Regressed <= 1 && s.Fixed > s.Regressed:
		s.Passed = true
		s.Verdict = fmt.Sprintf("通过：修好 %d，回归 %d，持平 %d", s.Fixed, s.Regressed, same)
	case s.Fixed == 0:
		s.Verdict = fmt.Sprintf("未通过：没有修好任何失败样本（回归 %d，持平 %d）", s.Regressed, same)
	default:
		s.Verdict = fmt.Sprintf("未通过：修好 %d，回归 %d", s.Fixed, s.Regressed)
	}
	if s.Blocked > 0 {
		s.Verdict += fmt.Sprintf("；%d 个样本被阻塞", s.Blocked)
	}
	return s
}

// SamplePlan is one replay to start.
type SamplePlan struct {
	Group string
	Run   domain.Run
}

// PlanSamples splits failing runs into diagnosis (shown to the improver) and
// holdout (hidden), then adds clean successes as regression samples and
// pinned runs. A run appears at most once.
func PlanSamples(failing, succeeded, pinned []domain.Run) []SamplePlan {
	used := map[string]bool{}
	var out []SamplePlan
	take := func(group string, runs []domain.Run, n int) {
		count := 0
		for _, run := range runs {
			if count >= n {
				return
			}
			if run.ID == "" || used[run.ID] || run.ExecutionContext.IsEvolutionReplay() || strings.TrimSpace(run.WorkflowID) == "" {
				continue
			}
			used[run.ID] = true
			out = append(out, SamplePlan{Group: group, Run: run})
			count++
		}
	}
	take(domain.EvoGroupDiagnosis, failing, MaxDiagnosisSamples)
	take(domain.EvoGroupHoldout, failing, MaxHoldoutSamples)
	take(domain.EvoGroupPinned, pinned, MaxPinnedSamples)
	take(domain.EvoGroupRegression, succeeded, MaxRegressionSamples)
	return out
}

// ReplayRepositories rebuilds a source run's checkout from its base branch on
// an isolated evo/ work branch, without integration merges.
func ReplayRepositories(source domain.Run, replayID string) []domain.RepositoryContext {
	repos := source.RepositoryContexts()
	out := make([]domain.RepositoryContext, 0, len(repos))
	for _, rc := range repos {
		if strings.TrimSpace(rc.RepositoryID) == "" && strings.TrimSpace(rc.CloneURL) == "" && strings.TrimSpace(rc.LocalPath) == "" {
			continue
		}
		rc.WorkspaceDir = ""
		rc.IntegrateBranch = ""
		rc.RequireExistingWorkBranch = false
		// The evo/<replay> name is unique, so the normal "create or resume"
		// checkout starts from the base branch; CreateNewWorkBranch would
		// additionally require a newer runner.
		rc.CreateNewWorkBranch = false
		if rc.ReadOnly {
			rc.WorkBranch = ""
		} else {
			rc.WorkBranch = "evo/" + replayID
		}
		if strings.TrimSpace(rc.BaseBranch) == "" {
			rc.BaseBranch = rc.DefaultBranch
		}
		out = append(out, rc)
	}
	return out
}

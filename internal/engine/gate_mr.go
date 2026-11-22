package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// mrPendingCommand prints the work branch tip on origin and how many commits
// it carries beyond the run's target branch; "- 0" when it was never pushed.
const mrPendingCommand = `refs="$(git ls-remote origin "refs/heads/$AGENTFLOW_WORK_BRANCH")" || exit 2
tip="$(printf '%s\n' "$refs" | cut -f1)"
if [ -z "$tip" ]; then echo "- 0"; exit 0; fi
git cat-file -e "$tip^{commit}" 2>/dev/null || git fetch -q origin "refs/heads/$AGENTFLOW_WORK_BRANCH" || exit 2
printf '%s %s\n' "$tip" "$(git rev-list --count "$tip" --not "refs/remotes/origin/$AGENTFLOW_BASE_BRANCH")"`

// errMRUnverifiable marks MR problems another agent iteration cannot fix: the
// host cannot be queried, so a person has to confirm the MR.
var errMRUnverifiable = errors.New("MR cannot be verified on the hosting platform")

// pushedWork is a repository's work branch tip on origin and how many commits
// it carries beyond the run's target branch.
type pushedWork struct {
	Tip   string
	Ahead int
}

// verifyRunMRs implements mode=mr gates. Every writable repository whose work
// branch on origin carries commits beyond the run's target branch needs an MR
// from that work branch into that target branch that carries the pushed tip:
// an open MR whose head is the tip (or whose host reports no head), or a
// merged MR whose head is the tip. An earlier MR merged before this run's
// pushes does not count.
//
// The lookup runs in the run's execution context: its project and gitToken,
// the credential the run pushed with.
func (e *Engine) verifyRunMRs(run domain.Run, gitToken string, pending func(domain.RepositoryContext) (pushedWork, error)) error {
	var problems []string
	unverifiable := false
	for _, rc := range run.RepositoryContexts() {
		work := writableWorkBranch(rc)
		if work == "" {
			continue
		}
		label := strings.TrimSpace(rc.Name)
		if label == "" {
			label = rc.RepositoryID
		}
		base := repositoryBaseBranch(rc)
		pushed, err := pending(rc)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：无法读取远端工作分支 %s：%v", label, work, err))
			continue
		}
		if pushed.Ahead == 0 {
			continue
		}
		project := strings.TrimSpace(rc.GongfengProjectID)
		if e.FindBranchMRsInto == nil || project == "" {
			problems = append(problems, fmt.Sprintf("%s：托管平台不支持自动核验 MR，请人工确认已创建 %s → %s 的 MR", label, work, base))
			unverifiable = true
			continue
		}
		mrs, err := e.FindBranchMRsInto(domain.BranchMRQuery{ProjectID: run.ExecutionContext.ProjectID, RemoteProject: project, SourceBranch: work, TargetBranch: base, GitToken: gitToken, ResponsibleUserID: run.ExecutionContext.ResponsibleUserID})
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：查询 %s → %s 的 MR 失败：%v", label, work, base, err))
			unverifiable = true
			continue
		}
		if problem := mrCoverageProblem(mrs, pushed.Tip); problem != "" {
			problems = append(problems, fmt.Sprintf("%s：%s → %s %s", label, work, base, problem))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	err := errors.New(strings.Join(problems, "；"))
	if unverifiable {
		return fmt.Errorf("%w：%w", errMRUnverifiable, err)
	}
	return err
}

// mrCoverageProblem returns "" when one of mrs carries tip, else what is wrong.
func mrCoverageProblem(mrs []domain.DeliveryMR, tip string) string {
	var stale, merged []string
	closed := 0
	for _, mr := range mrs {
		head := strings.TrimSpace(mr.HeadSHA)
		switch deliveryMRState(mr) {
		case "opened":
			if head == "" || head == tip {
				return ""
			}
			stale = append(stale, fmt.Sprintf("%s 的源提交是 %s", mr.URL, head))
		case "merged":
			if head == tip {
				return ""
			}
			merged = append(merged, mr.URL)
		default:
			closed++
		}
	}
	switch {
	case len(stale) > 0:
		return fmt.Sprintf("的 MR 还没有包含远端工作分支的 %s：%s", tip, strings.Join(stale, "，"))
	case len(merged) > 0:
		return fmt.Sprintf("只有已合并的 MR %s，不包含本轮推送的 %s，需要创建新的 MR", strings.Join(merged, "，"), tip)
	case closed > 0:
		return "的 MR 已关闭，需要重新打开或创建新的 MR"
	default:
		return "在托管平台上没有 MR/PR，需要创建"
	}
}

func deliveryMRState(mr domain.DeliveryMR) string {
	state := strings.TrimSpace(mr.GongfengState)
	if state == "" {
		state = strings.TrimSpace(mr.State)
	}
	if state == "reopened" {
		return "opened"
	}
	return state
}

func parseMRPending(output string) (pushedWork, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	fields := strings.Fields(last)
	if len(fields) != 2 {
		return pushedWork{}, fmt.Errorf("unexpected output %q", last)
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		return pushedWork{}, fmt.Errorf("unexpected output %q", last)
	}
	return pushedWork{Tip: fields[0], Ahead: n}, nil
}

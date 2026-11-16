package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// mrPendingCommand prints how many commits the work branch on origin carries
// beyond the run's target branch; 0 when the branch was never pushed.
const mrPendingCommand = `refs="$(git ls-remote origin "refs/heads/$AGENTFLOW_WORK_BRANCH")" || exit 2
tip="$(printf '%s\n' "$refs" | cut -f1)"
if [ -z "$tip" ]; then echo 0; exit 0; fi
git cat-file -e "$tip^{commit}" 2>/dev/null || git fetch -q origin "refs/heads/$AGENTFLOW_WORK_BRANCH" || exit 2
git rev-list --count "$tip" --not "refs/remotes/origin/$AGENTFLOW_BASE_BRANCH"`

// errMRUnverifiable marks MR problems another agent iteration cannot fix: the
// host cannot be queried, so a person has to confirm the MR.
var errMRUnverifiable = errors.New("MR cannot be verified on the hosting platform")

// verifyRunMRs implements mode=mr gates. Every writable repository whose work
// branch on origin carries commits beyond the run's target branch must have a
// non-closed MR from that work branch into that target branch on its host.
func (e *Engine) verifyRunMRs(run domain.Run, pending func(domain.RepositoryContext) (int, error)) error {
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
		ahead, err := pending(rc)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：无法读取远端工作分支 %s：%v", label, work, err))
			continue
		}
		if ahead == 0 {
			continue
		}
		project := strings.TrimSpace(rc.GongfengProjectID)
		if e.FindBranchMR == nil || project == "" {
			problems = append(problems, fmt.Sprintf("%s：托管平台不支持自动核验 MR，请人工确认已创建 %s → %s 的 MR", label, work, base))
			unverifiable = true
			continue
		}
		mr, found, err := e.FindBranchMR(project, work)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：查询 %s 的 MR 失败：%v", label, work, err))
			unverifiable = true
			continue
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s：托管平台上没有源分支为 %s 的 MR/PR，需要创建 %s → %s 的 MR", label, work, work, base))
			continue
		}
		state := mr.State
		if state == "" {
			state = mr.GongfengState
		}
		if state == "closed" {
			problems = append(problems, fmt.Sprintf("%s：源分支 %s 的 MR %s 已关闭，需要重新打开或创建 %s → %s 的 MR", label, work, mr.URL, work, base))
			continue
		}
		if !domain.SameGitBranch(mr.TargetBranch, base) {
			problems = append(problems, fmt.Sprintf("%s：MR %s 的目标分支是 %s，本轮目标分支是 %s", label, mr.URL, mr.TargetBranch, base))
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

func parseMRPendingCount(output string) (int, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	n, err := strconv.Atoi(last)
	if err != nil {
		return 0, fmt.Errorf("unexpected output %q", last)
	}
	return n, nil
}

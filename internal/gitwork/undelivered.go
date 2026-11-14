package gitwork

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// UndeliveredChanges lists checkout work that exists only in this
// workspace: uncommitted edits, untracked source files, and commits no remote
// ref contains. A run can succeed without any step committing and pushing
// its changes, and nothing else tells the user that the code never left the
// sandbox. Files the patch handoff excludes (executor secrets, SOP
// deliverable markdown, dependency and cache trees) are ignored here too. An
// empty result means nothing is at risk.
func UndeliveredChanges(workspace string) ([]PatchFileSummary, error) {
	unlock := lockPatchWorkspace(workspace)
	defer unlock()
	base, err := deliveredBase(workspace)
	if err != nil || base == "" {
		return nil, err
	}
	if err := StageUntracked(workspace); err != nil {
		return nil, err
	}
	nameStatus, err := diffAgainstBaseline(workspace, base, "--name-status")
	if err != nil || len(bytes.TrimSpace(nameStatus)) == 0 {
		return nil, err
	}
	numstat, err := diffAgainstBaseline(workspace, base, "--numstat")
	if err != nil {
		return nil, err
	}
	return ParseNameStatusNumstat(nameStatus, numstat), nil
}

// deliveredBase is the newest commit of HEAD's history that a remote ref
// already holds: HEAD itself once it was pushed, otherwise the closest remote
// ancestor. Without any remote ancestor the recorded run baseline is used,
// and an unborn HEAD yields "" (nothing to compare against).
func deliveredBase(workspace string) (string, error) {
	headOut, err := exec.Command("git", "-C", workspace, "rev-parse", "--verify", "-q", "HEAD^{commit}").Output()
	head := strings.TrimSpace(string(headOut))
	if err != nil || head == "" {
		return "", nil
	}
	unpushed, err := exec.Command("git", "-C", workspace, "rev-list", "--count", head, "--not", "--remotes").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-list unpushed: %w", err)
	}
	// Zero commits outside the remotes means some remote ref holds HEAD.
	if strings.TrimSpace(string(unpushed)) == "0" {
		return head, nil
	}
	best, bestCount := "", -1
	refs, err := exec.Command("git", "-C", workspace, "for-each-ref", "--format=%(objectname)", "refs/remotes").Output()
	if err == nil {
		for _, sha := range strings.Fields(string(refs)) {
			if exec.Command("git", "-C", workspace, "merge-base", "--is-ancestor", sha, head).Run() != nil {
				continue
			}
			if n, ok := revListCount(workspace, sha, head); ok && (bestCount < 0 || n < bestCount) {
				best, bestCount = sha, n
			}
		}
	}
	if best != "" {
		return best, nil
	}
	if recorded, err := resolveCommitSHA(workspace, readBaseline(workspace)); err == nil {
		return recorded, nil
	}
	// No remote and no baseline: only uncommitted edits can be measured.
	return head, nil
}

func readBaseline(workspace string) string {
	data, err := os.ReadFile(BaselinePath(workspace))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

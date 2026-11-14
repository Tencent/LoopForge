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
	// The check must not touch the checkout's index: the workspace snapshot
	// taken afterwards should show exactly what the Agent left behind. The
	// untracked files are therefore intent-added into a scratch copy of the
	// index and every diff reads that copy through GIT_INDEX_FILE.
	env, cleanup, err := scratchIndexEnv(workspace)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := stageUntrackedEnv(workspace, env); err != nil {
		return nil, err
	}
	nameStatus, err := diffAgainstBaselineEnv(workspace, env, base, "--name-status")
	if err != nil || len(bytes.TrimSpace(nameStatus)) == 0 {
		return nil, err
	}
	numstat, err := diffAgainstBaselineEnv(workspace, env, base, "--numstat")
	if err != nil {
		return nil, err
	}
	return ParseNameStatusNumstat(nameStatus, numstat), nil
}

// deliveredBase is the newest commit of HEAD's history that a remote ref
// already holds: HEAD itself once it was pushed, otherwise the merge base of
// HEAD and all remote tips. Without any remote ancestor the recorded run baseline is used,
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
	// One merge-base against every remote tip at once instead of one or two
	// git processes per remote ref: the result is the newest commit of HEAD's
	// history that some remote ref contains (a diverged remote branch
	// contributes only its fork point). Exit status 1 means no common history.
	if refs, err := exec.Command("git", "-C", workspace, "for-each-ref", "--format=%(objectname)", "refs/remotes").Output(); err == nil {
		if tips := strings.Fields(string(refs)); len(tips) > 0 {
			args := append([]string{"-C", workspace, "merge-base", head}, tips...)
			if out, err := exec.Command("git", args...).Output(); err == nil {
				if base := strings.TrimSpace(string(out)); base != "" {
					return base, nil
				}
			}
		}
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

// scratchIndexEnv copies the checkout's index to a temporary file and returns
// the GIT_INDEX_FILE environment pointing at it plus a cleanup func. A
// checkout without an index file gets one read from HEAD.
func scratchIndexEnv(workspace string) ([]string, func(), error) {
	out, err := exec.Command("git", "-C", workspace, "rev-parse", "--path-format=absolute", "--git-path", "index").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("git rev-parse index path: %w", err)
	}
	indexPath := strings.TrimSpace(string(out))
	tmp, err := os.CreateTemp("", "loopforge-undelivered-index-*")
	if err != nil {
		return nil, nil, err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	data, readErr := os.ReadFile(indexPath)
	if readErr == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	env := []string{"GIT_INDEX_FILE=" + tmpPath}
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			cleanup()
			return nil, nil, readErr
		}
		// An empty file is not a valid index; let read-tree create it.
		_ = os.Remove(tmpPath)
		cmd := exec.Command("git", "-C", workspace, "read-tree", "HEAD")
		cmd.Env = withGitEnv(env)
		if out, err := cmd.CombinedOutput(); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("git read-tree scratch index: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return env, cleanup, nil
}

package gitwork

import (
	"context"
	"sort"
	"strings"
)

// EnsureIdentity gives a checkout a committer identity when it has none.
func EnsureIdentity(ctx context.Context, workspace string) {
	if n, _ := C(ctx, workspace, "config", "user.name"); strings.TrimSpace(n) == "" {
		_, _ = C(ctx, workspace, "config", "user.name", "AgentFlow")
	}
	if m, _ := C(ctx, workspace, "config", "user.email"); strings.TrimSpace(m) == "" {
		_, _ = C(ctx, workspace, "config", "user.email", "agentflow@local")
	}
}

// MergeLatestIntegration commits pending work on the work branch, then merges
// origin/<integrate> into it with SOP deliverables dropped from the result.
// It returns the files that still conflict; the merge is then left in
// progress so a repair agent can resolve it in the same checkout, and the
// next call completes it.
func MergeLatestIntegration(ctx context.Context, w, work, integrate, label string) ([]string, error) {
	work, integrate = strings.TrimSpace(work), strings.TrimSpace(integrate)
	if branch, _ := C(ctx, w, "branch", "--show-current"); strings.TrimSpace(branch) != work {
		if _, err := C(ctx, w, "checkout", work); err != nil {
			return nil, err
		}
	}
	if c := ResolveExcludedMergeConflicts(ctx, w); len(c) > 0 {
		return c, nil
	}
	if _, err := C(ctx, w, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		_, _ = C(ctx, w, "add", "-A")
		DropSOPDeliverablesFromIndex(ctx, w)
		if err := CommitStaged(ctx, w, labelled("AgentFlow: resolve integration conflicts", label)); err != nil {
			return nil, err
		}
	}
	if s, _ := C(ctx, w, "status", "--porcelain"); strings.TrimSpace(s) != "" {
		_, _ = C(ctx, w, "add", "-A")
		DropSOPDeliverablesFromIndex(ctx, w)
		if err := CommitStaged(ctx, w, labelled("AgentFlow: save scheduled SOP work", label)); err != nil {
			return nil, err
		}
	}
	ref := "+refs/heads/" + integrate + ":refs/remotes/origin/" + integrate
	if _, err := C(ctx, w, "fetch", "origin", ref); err != nil {
		return nil, err
	}
	mergeMsg := labelled("AgentFlow: merge latest "+integrate, label)
	if _, err := C(ctx, w, "merge", "--no-edit", "-m", mergeMsg, "origin/"+integrate); err != nil {
		conflicts, finishErr := FinishMergeAfterExcludedConflicts(ctx, w, mergeMsg)
		if finishErr != nil {
			return nil, finishErr
		}
		if len(conflicts) > 0 {
			return conflicts, nil
		}
		if _, mergeErr := C(ctx, w, "rev-parse", "--verify", "MERGE_HEAD"); mergeErr == nil {
			return UnmergedFiles(ctx, w), nil
		}
		if leftover := UnmergedFiles(ctx, w); len(leftover) > 0 {
			return leftover, nil
		}
		// Only SOP-report conflicts: merge was completed by FinishMergeAfterExcludedConflicts.
	}
	DropSOPDeliverablesFromIndex(ctx, w)
	if err := CommitStaged(ctx, w, labelled("AgentFlow: drop SOP deliverables", label)); err != nil {
		return nil, err
	}
	return nil, nil
}

func labelled(msg, label string) string {
	if label != "" {
		return msg + " (" + label + ")"
	}
	return msg
}

// MergeLatestRemoteBranch merges origin/<branch> into the current HEAD.
// Callers use it to fold the default branch into the integrate baseline or
// a work branch. It never updates the default branch.
func MergeLatestRemoteBranch(ctx context.Context, w, branch string) ([]string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, nil
	}
	ref := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, err := C(ctx, w, "fetch", "origin", ref); err != nil {
		return nil, err
	}
	mergeMsg := "AgentFlow: merge latest " + branch
	if _, err := C(ctx, w, "merge", "--no-edit", "-m", mergeMsg, "origin/"+branch); err != nil {
		conflicts, finishErr := FinishMergeAfterExcludedConflicts(ctx, w, mergeMsg)
		if finishErr != nil {
			return nil, finishErr
		}
		if len(conflicts) > 0 {
			return conflicts, nil
		}
		if leftover := UnmergedFiles(ctx, w); len(leftover) > 0 {
			return leftover, nil
		}
		if _, mergeErr := C(ctx, w, "rev-parse", "--verify", "MERGE_HEAD"); mergeErr == nil {
			return UnmergedFiles(ctx, w), nil
		}
		// Only SOP-report conflicts were auto-resolved.
	}
	return nil, nil
}

// ResolveExcludedMergeConflicts drops unmerged SOP deliverables (reports the
// engine never wants on a baseline) and returns any remaining real conflicts.
func ResolveExcludedMergeConflicts(ctx context.Context, w string) []string {
	for _, path := range UnmergedFiles(ctx, w) {
		if IsSOPDeliverableOrControlFile(path) {
			_, _ = C(ctx, w, "rm", "-f", "--", path)
		}
	}
	return UnmergedFiles(ctx, w)
}

// FinishMergeAfterExcludedConflicts completes an in-progress merge whose only
// conflicts were SOP deliverables, or returns the real conflicts.
func FinishMergeAfterExcludedConflicts(ctx context.Context, w, msg string) ([]string, error) {
	if remaining := ResolveExcludedMergeConflicts(ctx, w); len(remaining) > 0 {
		return remaining, nil
	}
	if _, err := C(ctx, w, "rev-parse", "--verify", "MERGE_HEAD"); err != nil {
		return nil, nil
	}
	DropSOPDeliverablesFromIndex(ctx, w)
	if err := CommitStaged(ctx, w, msg); err != nil {
		return nil, err
	}
	return nil, nil
}

// UnmergedFiles lists the paths git still reports as conflicted.
func UnmergedFiles(ctx context.Context, w string) []string {
	out, _ := C(ctx, w, "diff", "--name-only", "--diff-filter=U")
	var files []string
	for _, x := range strings.Split(out, "\n") {
		if x = strings.TrimSpace(x); x != "" {
			files = append(files, x)
		}
	}
	return files
}

// DropSOPDeliverablesFromIndex unstages SOP report files so they never reach
// a baseline commit.
func DropSOPDeliverablesFromIndex(ctx context.Context, w string) {
	names := make([]string, 0, len(PatchExcludeBasenames))
	for name := range PatchExcludeBasenames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = C(ctx, w, "rm", "-f", "--cached", "--ignore-unmatch", "--", name)
	}
}

// CommitStaged commits the index when it differs from HEAD.
func CommitStaged(ctx context.Context, w, msg string) error {
	if _, err := C(ctx, w, "diff", "--cached", "--quiet"); err == nil {
		return nil
	}
	_, err := C(ctx, w, "commit", "-m", msg, "--quiet")
	return err
}

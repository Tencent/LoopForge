package gitwork

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PushWorkBranch makes the remote work branch the durable state of a
// checkout: whatever changed is committed and pushed, so any machine can
// continue from a fresh clone of that branch. It returns the pushed commit,
// or "" when there was nothing to push or the checkout is not on branch.
// Callers must only pass branches that may receive intermediate commits.
func PushWorkBranch(ctx context.Context, workspace, branch, message string) (string, error) {
	return pushWorkBranch(ctx, workspace, branch, message, false)
}

// PushExistingWorkBranch only advances an existing remote branch. A fresh
// remote SHA and fast-forward check are combined with an explicit lease, so
// deleting or changing the remote between fetch and push cannot recreate it
// or overwrite somebody else's history.
func PushExistingWorkBranch(ctx context.Context, workspace, branch, message string) (string, error) {
	return pushWorkBranch(ctx, workspace, branch, message, true)
}

func pushWorkBranch(ctx context.Context, workspace, branch, message string, requireExisting bool) (string, error) {
	branch = strings.TrimSpace(branch)
	if !onWorkBranch(ctx, workspace, branch) {
		if requireExisting {
			if _, err := fetchExistingWorkBranch(ctx, workspace, branch); err != nil {
				return "", err
			}
			if current, _ := C(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(current) != branch {
				return "", fmt.Errorf("工作区未检出已有工作分支 %s", branch)
			}
		}
		// A merge/rebase in progress is left for its repair node, just as
		// for generated branches. Strict mode still validated the remote.
		return "", nil
	}
	var expectedRemote string
	if requireExisting {
		var err error
		expectedRemote, err = fetchExistingWorkBranch(ctx, workspace, branch)
		if err != nil {
			return "", err
		}
	}
	if err := StageChanges(workspace); err != nil {
		return "", err
	}
	if exec.Command("git", "-C", workspace, "diff", "--cached", "--quiet").Run() != nil {
		args := []string{"commit", "--no-verify", "-q", "-m", message}
		if email, _ := C(ctx, workspace, "config", "user.email"); strings.TrimSpace(email) == "" {
			args = append([]string{"-c", "user.name=AgentFlow", "-c", "user.email=agentflow@local"}, args...)
		}
		if _, err := C(ctx, workspace, args...); err != nil {
			return "", fmt.Errorf("commit work branch: %w", err)
		}
	}
	head, ok := RevParse(ctx, workspace, "HEAD")
	if !ok {
		return "", fmt.Errorf("resolve HEAD")
	}
	trackingRef := "refs/remotes/origin/" + branch
	if remote, ok := RevParse(ctx, workspace, trackingRef); ok && remote == head {
		return "", nil
	}
	pushArgs := []string{"push", "origin", "HEAD:refs/heads/" + branch}
	if requireExisting {
		if _, err := C(ctx, workspace, "merge-base", "--is-ancestor", expectedRemote, head); err != nil {
			return "", fmt.Errorf("远程工作分支 %s 有本机没有的提交，拒绝覆盖", branch)
		}
		// The ancestry check above makes this a fast-forward-only update.
		// The exact lease additionally rejects a deleted or moved head.
		pushArgs = []string{"push", "--force-with-lease=refs/heads/" + branch + ":" + expectedRemote, "origin", "HEAD:refs/heads/" + branch}
	}
	var pushErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		if _, pushErr = C(ctx, workspace, pushArgs...); pushErr == nil {
			break
		}
		message := strings.ToLower(pushErr.Error())
		if IsNonFastForward(pushErr) || (requireExisting && (strings.Contains(message, "stale info") || strings.Contains(message, "remote rejected"))) {
			if requireExisting {
				return "", fmt.Errorf("远程工作分支 %s 已变化或被删除，拒绝覆盖: %w", branch, pushErr)
			}
			return "", fmt.Errorf("远程工作分支 %s 有本机没有的提交，拒绝覆盖（请检查是否有人手动推送过该分支）: %w", branch, pushErr)
		}
	}
	if pushErr != nil {
		return "", fmt.Errorf("push work branch %s: %w", branch, pushErr)
	}
	_, _ = C(ctx, workspace, "update-ref", trackingRef, head)
	return head, nil
}

// PullWorkBranch brings a reused checkout up to the remote work branch.
// Another run may have pushed to the same branch since this checkout was
// made, e.g. a child SOP. Local-only commits (a rejected push) are rebased
// onto the remote. It returns the new HEAD, or "" when nothing moved.
func PullWorkBranch(ctx context.Context, workspace, branch string) (string, error) {
	return pullWorkBranch(ctx, workspace, branch, false)
}

// PullExistingWorkBranch refreshes an explicitly chosen remote branch and
// reports missing/deleted heads instead of allowing a later push to create it.
func PullExistingWorkBranch(ctx context.Context, workspace, branch string) (string, error) {
	return pullWorkBranch(ctx, workspace, branch, true)
}

func pullWorkBranch(ctx context.Context, workspace, branch string, requireExisting bool) (string, error) {
	branch = strings.TrimSpace(branch)
	if !onWorkBranch(ctx, workspace, branch) {
		if requireExisting {
			if _, err := fetchExistingWorkBranch(ctx, workspace, branch); err != nil {
				return "", err
			}
			if current, _ := C(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(current) != branch {
				return "", fmt.Errorf("工作区未检出已有工作分支 %s", branch)
			}
		}
		return "", nil
	}
	trackingRef := "refs/remotes/origin/" + branch
	if _, err := fetchExistingWorkBranch(ctx, workspace, branch); err != nil {
		if !requireExisting && strings.Contains(strings.ToLower(err.Error()), "couldn't find remote ref") {
			return "", nil
		}
		return "", err
	}
	head, ok := RevParse(ctx, workspace, "HEAD")
	if !ok {
		return "", fmt.Errorf("resolve HEAD")
	}
	remote, ok := RevParse(ctx, workspace, trackingRef)
	if !ok || remote == head {
		return "", nil
	}
	if exec.Command("git", "-C", workspace, "merge-base", "--is-ancestor", remote, head).Run() == nil {
		return "", nil
	}
	if exec.Command("git", "-C", workspace, "merge-base", "--is-ancestor", head, remote).Run() == nil {
		if _, err := C(ctx, workspace, "merge", "--ff-only", "-q", trackingRef); err != nil {
			return "", fmt.Errorf("fast-forward work branch %s: %w", branch, err)
		}
		return remote, nil
	}
	args := []string{"rebase", "-q", "--autostash", trackingRef}
	if email, _ := C(ctx, workspace, "config", "user.email"); strings.TrimSpace(email) == "" {
		args = append([]string{"-c", "user.name=AgentFlow", "-c", "user.email=agentflow@local"}, args...)
	}
	if _, err := C(ctx, workspace, args...); err != nil {
		_, _ = C(ctx, workspace, "rebase", "--abort")
		return "", fmt.Errorf("远程工作分支 %s 与本机工作区已分叉且无法自动变基，拒绝覆盖（请检查是否有人手动推送过该分支）: %w", branch, err)
	}
	head, ok = RevParse(ctx, workspace, "HEAD")
	if !ok {
		return "", fmt.Errorf("resolve HEAD")
	}
	return head, nil
}

// fetchExistingWorkBranch fetches only refs/heads, never a same-named tag.
// It validates the current remote even if a stale tracking ref already exists.
func fetchExistingWorkBranch(ctx context.Context, workspace, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", fmt.Errorf("继续已有分支时必须指定工作分支")
	}
	trackingRef := "refs/remotes/origin/" + branch
	if _, err := C(ctx, workspace, "fetch", "--no-tags", "origin", "+refs/heads/"+branch+":"+trackingRef); err != nil {
		return "", fmt.Errorf("无法读取已有远端分支 %s，请确认分支存在且有访问权限: %w", branch, err)
	}
	sha, ok := RevParse(ctx, workspace, trackingRef)
	if !ok {
		return "", fmt.Errorf("找不到已有远端分支 %s", branch)
	}
	return sha, nil
}

// onWorkBranch reports whether workspace is a clean-state checkout of branch
// with an origin remote. A merge/rebase in progress is left for a later node
// to finish; committing it would record conflict markers.
func onWorkBranch(ctx context.Context, workspace, branch string) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return false
	}
	for _, marker := range []string{"MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD"} {
		if _, err := os.Stat(filepath.Join(workspace, ".git", marker)); err == nil {
			return false
		}
	}
	if current, _ := C(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(current) != branch {
		return false
	}
	_, err := C(ctx, workspace, "remote", "get-url", "origin")
	return err == nil
}

// StageChanges stages tracked changes and untracked sources, minus Excluded
// paths: secrets, AgentFlow control files, SOP deliverables and
// dependency/cache trees.
func StageChanges(workspace string) error {
	if err := StageUntracked(workspace); err != nil {
		return err
	}
	// Git 2.55 rejects add -u with pathspecs that match no index entries.
	// An empty checkout (or one containing only excluded files) has nothing
	// eligible to update. Keep exclusions intact instead of ignoring add errors.
	trackedArgs := append([]string{"-C", workspace, "ls-files", "--cached", "-z", "--", "."}, ExcludePathspecs...)
	tracked, err := exec.Command("git", trackedArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("list work branch tracked files: %w: %s", err, strings.TrimSpace(string(tracked)))
	}
	if len(tracked) == 0 {
		return nil
	}
	args := append([]string{"-C", workspace, "add", "-u", "--", "."}, ExcludePathspecs...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("stage work branch changes: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsNonFastForward reports a push rejected because the remote moved on.
func IsNonFastForward(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "[rejected]")
}

package gitwork

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MergeWorkIntoSharedBranch merges workBranch into an existing target branch
// with an explicit merge commit and pushes the result, so shared branches such
// as preview or test can be updated without opening a Gongfeng MR.
//
// The merge runs in a throwaway detached worktree: the run checkout has to
// stay on its work branch (PushWorkBranch refuses to push from anywhere else),
// and a conflicted merge must never leave the run checkout mid-merge. The
// target must already exist remotely: a direct push never creates a shared
// branch. The push itself is fast-forward only and guarded by a lease on the
// fetched head, so a concurrent push is rejected instead of overwritten.
//
// It returns the pushed merge commit (empty when the branch already contains
// the work branch), or the conflicting paths when the merge cannot be done
// automatically. Conflicts are reported, never pushed.
func MergeWorkIntoSharedBranch(ctx context.Context, workspace, workBranch, targetBranch string) (string, []string, error) {
	workBranch, targetBranch = strings.TrimSpace(workBranch), strings.TrimSpace(targetBranch)
	if workBranch == "" || targetBranch == "" {
		return "", nil, fmt.Errorf("直推共享分支需要工作分支和目标分支")
	}
	if workBranch == targetBranch {
		return "", nil, fmt.Errorf("工作分支 %s 就是共享分支本身，不能直推", workBranch)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return "", nil, fmt.Errorf("%s 不是 git 检出", workspace)
	}
	if _, err := C(ctx, workspace, "rev-parse", "--verify", "refs/heads/"+workBranch); err != nil {
		return "", nil, fmt.Errorf("本地没有工作分支 %s", workBranch)
	}
	ref := "+refs/heads/" + targetBranch + ":refs/remotes/origin/" + targetBranch
	if _, err := C(ctx, workspace, "fetch", "origin", ref); err != nil {
		return "", nil, fmt.Errorf("拉取共享分支 %s 失败（目标分支需已存在且当前账号有权限）: %w", targetBranch, err)
	}
	expected, ok := RevParse(ctx, workspace, "refs/remotes/origin/"+targetBranch)
	expected = strings.TrimSpace(expected)
	if !ok || expected == "" {
		return "", nil, fmt.Errorf("共享分支 %s 在远端不存在，直推不会新建共享分支", targetBranch)
	}
	tmp, err := os.MkdirTemp("", "agentflow-shared-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmp)
	if out, err := C(ctx, workspace, "worktree", "add", "--detach", tmp, expected); err != nil {
		return "", nil, fmt.Errorf("建立临时工作树失败: %w: %s", err, strings.TrimSpace(out))
	}
	defer func() {
		_, _ = C(context.Background(), workspace, "worktree", "remove", "--force", tmp)
	}()
	message := fmt.Sprintf("Merge branch '%s' into %s", workBranch, targetBranch)
	if out, err := C(ctx, tmp, "merge", "--no-ff", "--no-edit", "-m", message, workBranch); err != nil {
		conflicts := UnmergedFiles(ctx, tmp)
		_, _ = C(ctx, tmp, "merge", "--abort")
		if len(conflicts) > 0 {
			return "", conflicts, nil
		}
		return "", nil, fmt.Errorf("合并 %s 到 %s 失败: %w: %s", workBranch, targetBranch, err, strings.TrimSpace(out))
	}
	head, ok := RevParse(ctx, tmp, "HEAD")
	head = strings.TrimSpace(head)
	if !ok || head == "" {
		return "", nil, fmt.Errorf("无法解析 %s 合并后的提交", targetBranch)
	}
	if head == expected {
		return "", nil, nil
	}
	if _, err := C(ctx, tmp, "merge-base", "--is-ancestor", expected, head); err != nil {
		return "", nil, fmt.Errorf("合并结果不是 %s 的快进，拒绝覆盖", targetBranch)
	}
	pushArgs := []string{
		"push",
		"--force-with-lease=refs/heads/" + targetBranch + ":" + expected,
		"origin",
		"HEAD:refs/heads/" + targetBranch,
	}
	var pushErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		if _, pushErr = C(ctx, tmp, pushArgs...); pushErr == nil {
			break
		}
		lowered := strings.ToLower(pushErr.Error())
		if IsNonFastForward(pushErr) || strings.Contains(lowered, "stale info") || strings.Contains(lowered, "remote rejected") {
			return "", nil, fmt.Errorf("共享分支 %s 已被别人推送，拒绝覆盖（重新提交即可基于最新分支再合一次）: %w", targetBranch, pushErr)
		}
	}
	if pushErr != nil {
		return "", nil, fmt.Errorf("推送共享分支 %s 失败: %w", targetBranch, pushErr)
	}
	_, _ = C(ctx, workspace, "update-ref", "refs/remotes/origin/"+targetBranch, head)
	return head, nil, nil
}

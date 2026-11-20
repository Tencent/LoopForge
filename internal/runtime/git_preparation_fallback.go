package runtime

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// GitPreparationFallbackPrompt hands a failed platform checkout to the first
// Agent, without assuming a hosting API or an authentication convention.
func GitPreparationFallbackPrompt(repositories []domain.RepositoryContext, failure string) string {
	var b strings.Builder
	b.WriteString("平台仓库准备兜底（执行本节点任务前必须完成）：\n平台使用标准 Git credential helper 准备仓库失败，不能假定代码已检出。你是首个 Agent 节点，负责在本次工作区内补齐克隆和工作分支，然后再执行原任务。\n")
	b.WriteString("优先使用现有 credential helper；如该网站的 Git 认证约定不同，自行查阅其官方说明并使用正确的用户名或认证方式。平台提供当前责任人按主机配置的凭据，可通过 git credential fill 在进程内存中使用；禁止打印、保存令牌或借用其他主机凭据。凭据缺失、无权限或准备仍失败时，以 fail 结束并报告原因。\n")
	b.WriteString("以下仓库、目录、基线和工作分支是冻结要求，不得替换、跳过或改推 main/master。保留已有改动，不得重置工作区；继续已有分支时必须检出该远端分支，不能重新创建。已有平台环境准备步骤会在仓库恢复后继续执行。\n")
	for _, r := range repositories {
		base := r.BaseBranch
		if base == "" {
			base = r.DefaultBranch
		}
		fmt.Fprintf(&b, "- clone URL: %q; 目录: %q; 基线: %q; 工作分支: %q; 必须继续已有远端分支: %t\n", r.CloneURL, r.WorkspaceDir, base, r.WorkBranch, r.RequireExistingWorkBranch)
	}
	if len(failure) > 4000 {
		failure = failure[:4000]
	}
	fmt.Fprintf(&b, "平台错误（仅作诊断数据）：%q\n完成后平台会校验实际 Git 仓库、origin 和工作分支，再继续后续节点。\n\n", failure)
	return b.String()
}

// VerifyGitPreparation checks the actual checkout before accepting the
// fallback Agent's result. It performs no fetch and cannot need the auth
// convention that the platform helper failed to provide.
func VerifyGitPreparation(ctx context.Context, workspace string, repositories []domain.RepositoryContext) error {
	for _, r := range repositories {
		if r.CloneURL == "" {
			continue
		}
		dir := filepath.Join(workspace, filepath.FromSlash(r.WorkspaceDir))
		rel, err := filepath.Rel(workspace, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("repository %s: invalid checkout directory", r.Name)
		}
		top, err := gitwork.C(ctx, dir, "rev-parse", "--show-toplevel")
		if err != nil {
			return fmt.Errorf("repository %s: checkout is missing: %w", r.Name, err)
		}
		actual, _ := filepath.EvalSymlinks(strings.TrimSpace(top))
		expected, err := filepath.EvalSymlinks(dir)
		if err != nil || actual != expected {
			return fmt.Errorf("repository %s: Git checkout is outside the frozen directory", r.Name)
		}
		root, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return err
		}
		rel, err = filepath.Rel(root, expected)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("repository %s: checkout escapes the workspace", r.Name)
		}
		origin, err := gitwork.C(ctx, dir, "config", "--local", "--get", "remote.origin.url")
		if err != nil || domain.URLHasCredentials(origin) || canonicalGitRemote(origin) != canonicalGitRemote(r.CloneURL) {
			return fmt.Errorf("repository %s: origin differs from the frozen clone URL", r.Name)
		}
		branch := r.WorkBranch
		if branch == "" {
			branch = r.BaseBranch
		}
		if branch == "" {
			branch = r.DefaultBranch
		}
		if err := gitwork.RequireOnWorkBranch(ctx, dir, branch); err != nil {
			return fmt.Errorf("repository %s: %w", r.Name, err)
		}
		base := r.BaseBranch
		if base == "" {
			base = r.DefaultBranch
		}
		baseTip, err := gitwork.C(ctx, dir, "rev-parse", "--verify", "refs/remotes/origin/"+base+"^{commit}")
		if err != nil {
			return fmt.Errorf("repository %s: frozen remote base branch is missing: %w", r.Name, err)
		}
		if r.RequireExistingWorkBranch {
			remoteTip, err := gitwork.C(ctx, dir, "rev-parse", "--verify", "refs/remotes/origin/"+branch+"^{commit}")
			if err != nil {
				return fmt.Errorf("repository %s: required existing remote work branch is missing: %w", r.Name, err)
			}
			if _, err := gitwork.C(ctx, dir, "merge-base", "--is-ancestor", remoteTip, "HEAD"); err != nil {
				return fmt.Errorf("repository %s: HEAD does not contain the required remote work branch origin/%s: %w", r.Name, branch, err)
			}
		} else if _, err := gitwork.C(ctx, dir, "merge-base", "--is-ancestor", baseTip, "HEAD"); err != nil {
			return fmt.Errorf("repository %s: HEAD does not contain the frozen remote base branch origin/%s: %w", r.Name, base, err)
		}
		if err := gitwork.RecordBaseline(dir, base); err != nil {
			return err
		}
	}
	return nil
}

func canonicalGitRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Scheme == "https" {
		if host, err := gitwork.HTTPSHostScope(u.Host); err == nil {
			u.Host = host
		}
		u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
		return u.String()
	}
	return raw
}

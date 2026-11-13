package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const gitTokenEnv = gitwork.TokenEnv

type gitIdentityCtxKey struct{}

type gitIdentity struct {
	name  string
	email string
}

func withGitToken(ctx context.Context, token string) context.Context {
	return gitwork.WithToken(ctx, token)
}

func gitTokenFrom(ctx context.Context) string { return gitwork.TokenFrom(ctx) }

type runGitLabCredentialKey struct{}

func withRunGitCredentials(ctx context.Context, credentials runtime.RunCredentials) context.Context {
	ctx = gitwork.WithSSHKeys(withGitToken(ctx, credentials.GitToken), credentials.GitSSHKeys)
	return context.WithValue(ctx, runGitLabCredentialKey{}, credentials.GitLab)
}

func withRepositoryGit(ctx context.Context, repository domain.RepositoryContext) context.Context {
	if repository.Provider == domain.RepositoryProviderSSH {
		return gitwork.WithSSHRemote(ctx, repository.CloneURL)
	}
	if repository.Provider == domain.RepositoryProviderGitLab {
		credential, _ := ctx.Value(runGitLabCredentialKey{}).(gitwork.HTTPSCredential)
		return gitwork.WithHTTPSCredential(ctx, credential)
	}
	return ctx
}

func sshRepositoryURLs(repositories []domain.RepositoryContext) []string {
	var urls []string
	for _, repository := range repositories {
		if repository.Provider == domain.RepositoryProviderSSH {
			urls = append(urls, repository.CloneURL)
		}
	}
	return urls
}

func cloneRemoteForGit(ctx context.Context, remote string) string {
	if gitwork.UsesSSH(ctx) {
		return remote
	}
	if _, scoped := gitwork.HTTPSCredentialFrom(ctx); scoped {
		return remote
	}
	return gitwork.RewriteRemoteForToken(remote, gitTokenFrom(ctx))
}

func withGitIdentity(ctx context.Context, name, email string) context.Context {
	identity := gitIdentity{name: strings.TrimSpace(name), email: strings.TrimSpace(email)}
	if identity.name == "" || identity.email == "" {
		return ctx
	}
	return context.WithValue(ctx, gitIdentityCtxKey{}, identity)
}

func gitIdentityFrom(ctx context.Context) gitIdentity {
	if ctx != nil {
		if identity, ok := ctx.Value(gitIdentityCtxKey{}).(gitIdentity); ok {
			return identity
		}
	}
	return gitIdentity{}
}

func resolveGitIdentity(s *repo.Store, userID int64) gitIdentity {
	if s == nil || userID <= 0 {
		return gitIdentity{}
	}
	user, ok := s.User(userID)
	if !ok {
		return gitIdentity{}
	}
	login := strings.TrimSpace(user.LoginName)
	if login == "" {
		return gitIdentity{}
	}
	// Local platform accounts do not establish corporate or GitHub identity.
	// Use a non-deliverable address rather than inventing an employer email.
	return gitIdentity{name: login, email: login + "@users.noreply.loopforge.invalid"}
}

func applyWorkspaceGitIdentity(ctx context.Context, workspace string) error {
	identity := gitIdentityFrom(ctx)
	if identity.name == "" || identity.email == "" {
		return nil
	}
	for _, item := range []struct {
		key, value string
	}{
		{key: "user.name", value: identity.name},
		{key: "user.email", value: identity.email},
	} {
		if _, err := gitC(ctx, workspace, "config", "--local", item.key, item.value); err != nil {
			return fmt.Errorf("configure git %s: %w", item.key, err)
		}
	}
	return nil
}

func resolveGitToken(s *repo.Store, userID int64) string {
	if s != nil && userID > 0 {
		return s.GitCredential(context.Background(), userID)
	}
	return ""
}

var (
	gitCommandFailure = regexp.MustCompile(`(?i)repository checkout|git (clone|fetch|pull|push|ls-remote)`)
	gitAuthFailure    = regexp.MustCompile(`(?i)could not read Username|Authentication failed|HTTP Basic: Access denied|requested URL returned error: 40[13]|Invalid username or password`)
)

const gitTokenFixHint = "请责任人到「个人配置 → GitHub Git Token」配置：打开 https://github.com/settings/tokens 新建令牌，为目标仓库授予 Contents 和 Pull requests 的读写权限，复制后粘贴并点校验，然后重试本节点"

type gitLabOperationError struct{ cause error }

func (e gitLabOperationError) Error() string { return e.cause.Error() }
func (e gitLabOperationError) Unwrap() error { return e.cause }

// explainGitAuthFailure says what to fix when git rejects the clone or push.
// SOP runs use the responsible user's Git Token, which git's own
// "could not read Username" never mentions. The raw error stays appended.
func explainGitAuthFailure(err error, tokenConfigured bool) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var gitLabErr gitLabOperationError
	if errors.As(err, &gitLabErr) {
		if gitAuthFailure.MatchString(msg) {
			return fmt.Errorf("GitLab PAT 无效、已过期或权限不足，请检查本次执行账号的个人凭证和仓库权限：%w", err)
		}
		return err
	}
	if strings.Contains(msg, "GitLab PAT") || strings.Contains(msg, "Git Token") || !gitCommandFailure.MatchString(msg) || !gitAuthFailure.MatchString(msg) {
		return err
	}
	if !tokenConfigured {
		return fmt.Errorf("运行责任人未配置 Git Token，无法拉取仓库。%s。原始错误：%w", gitTokenFixHint, err)
	}
	return fmt.Errorf("运行责任人的 Git Token 无效、已过期或权限不足，无法拉取仓库。%s；并确认责任人对该仓库有权限。原始错误：%w", gitTokenFixHint, err)
}

const gitAuthIncludeFile = "agentflow.auth"
const gitAskPassFile = "agentflow-askpass"
const gitAskPassSecretFile = "agentflow-askpass-secret"

// applyWorkspaceGitAuth removes credentials persisted by older AgentFlow
// versions. Git authentication is now supplied only to the relevant child
// process, so user tokens never become part of the run workspace.
func applyWorkspaceGitAuth(ctx context.Context, workspace string) error {
	gitDir := filepath.Join(workspace, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	for _, name := range []string{gitAuthIncludeFile, gitAskPassFile, gitAskPassSecretFile} {
		if err := os.Remove(filepath.Join(gitDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	configured, _ := gitC(ctx, workspace, "config", "--local", "--get-all", "include.path")
	_, _ = gitC(ctx, workspace, "config", "--local", "--unset-all", "include.path")
	for _, include := range strings.Split(configured, "\n") {
		include = strings.TrimSpace(include)
		if include == "" || include == gitAuthIncludeFile {
			continue
		}
		if _, err := gitC(ctx, workspace, "config", "--local", "--add", "include.path", include); err != nil {
			return err
		}
	}
	return nil
}

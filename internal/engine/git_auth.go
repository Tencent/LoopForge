package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
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
		if user, ok := s.User(userID); ok {
			return strings.TrimSpace(user.GitToken)
		}
	}
	return ""
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

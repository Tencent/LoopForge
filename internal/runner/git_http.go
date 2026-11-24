package runner

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
	"context"
)

func invocationGitCredentials(inv *domain.RunnerInvocation) runtime.RunCredentials {
	if inv == nil {
		return runtime.RunCredentials{}
	}
	return runtime.RunCredentials{GitHTTPCredentials: inv.GitHTTPCredentials, GitUserName: inv.GitUserName, GitUserEmail: inv.GitUserEmail}
}

func invocationGitHTTPContext(ctx context.Context, inv *domain.RunnerInvocation) context.Context {
	if inv == nil {
		return ctx
	}
	hosts := map[string]gitwork.HTTPCredential{}
	for host, credential := range inv.GitHTTPCredentials {
		hosts[host] = gitwork.HTTPCredential{Username: credential.GitUsername(), Token: credential.Token}
	}
	return gitwork.WithHTTPCredentials(ctx, hosts)
}

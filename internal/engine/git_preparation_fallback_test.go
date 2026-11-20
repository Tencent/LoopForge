package engine

import (
	"agentflow/internal/runtime"
	"context"
	"testing"

	"agentflow/internal/domain"
)

func TestRepositoryFallbackIsOwnedByTheFirstAgent(t *testing.T) {
	for _, tc := range []struct {
		name, previous string
		want           bool
	}{
		{"first Agent", "", true}, {"after approval", "approval", true}, {"after script", "script", false}, {"after Agent", "agent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := domain.WorkflowNode{ID: "first", Type: "agent"}
			wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{n}}
			if tc.previous != "" {
				wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "before", Type: tc.previous})
				wf.Edges = []domain.WorkflowEdge{{Source: "before", Target: "first"}}
			}
			if got := firstRepositoryAgent(wf, n); got != tc.want {
				t.Fatalf("fallback allowed = %v, want %v", got, tc.want)
			}
			n.Type = "script"
			if firstRepositoryAgent(wf, n) {
				t.Fatal("a script cannot receive a fallback prompt")
			}
		})
	}
}

func testRunCredentials(token string) runtime.RunCredentials {
	return runtime.RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{
		"github.com":      {GitHTTPCredential: domain.GitHTTPCredential{Host: "github.com", Username: "x-access-token", Platform: domain.GitPlatformGitHub}, Token: token},
		"git.example.com": {GitHTTPCredential: domain.GitHTTPCredential{Host: "git.example.com", Username: "private", Platform: domain.GitPlatformOther}, Token: token},
	}}
}
func withGitToken(ctx context.Context, token string) context.Context {
	return withRunGitCredentials(ctx, testRunCredentials(token))
}

package runner

import (
	"context"
	"testing"

	"agentflow/internal/domain"
)

func TestInvocationRepositoriesPrefersListAndFallsBackToLegacy(t *testing.T) {
	if got := invocationRepositories(nil); got != nil {
		t.Fatalf("nil invocation = %+v", got)
	}
	if got := invocationRepositories(&domain.RunnerInvocation{}); len(got) != 0 {
		t.Fatalf("empty invocation = %+v", got)
	}
	legacy := &domain.RunnerInvocation{Repository: &domain.RunnerRepository{RepositoryID: "legacy"}}
	if got := invocationRepositories(legacy); len(got) != 1 || got[0].RepositoryID != "legacy" {
		t.Fatalf("legacy fallback = %+v", got)
	}
	multi := &domain.RunnerInvocation{
		Repository:   &domain.RunnerRepository{RepositoryID: "legacy"},
		Repositories: []domain.RunnerRepository{{RepositoryID: "a"}, {RepositoryID: "b"}},
	}
	if got := invocationRepositories(multi); len(got) != 2 || got[0].RepositoryID != "a" || got[1].RepositoryID != "b" {
		t.Fatalf("repositories list = %+v", got)
	}
}

func TestCheckoutInvocationRepositoriesSkipsLegacyWithoutCloneURL(t *testing.T) {
	workspace := t.TempDir()
	inv := &domain.RunnerInvocation{Repository: &domain.RunnerRepository{RepositoryID: "legacy", CloneURL: "  "}}
	if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local"); err != nil {
		t.Fatalf("legacy repository without clone URL should be skipped: %v", err)
	}
}

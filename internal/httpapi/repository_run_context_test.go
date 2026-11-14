package httpapi

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestRepositoryRunContextCopiesRunFields(t *testing.T) {
	got := repositoryRunContext(domain.Repository{
		ID: "repo-1", ProjectID: "project-1", Provider: "github", Name: "LoopForge",
		GongfengProjectID: "123", WebURL: "https://example.com/web", CloneURL: "https://example.com/web.git",
		DefaultBranch: "main", LocalPath: "/srv/repo", GitToken: "secret",
		PipelineProjectID: 7, PipelineDefID: 9, PipelineURLPattern: "https://ci/{id}", IntegrateBranch: "integrate",
	})
	want := domain.RepositoryContext{
		RepositoryID: "repo-1", Provider: "github", Name: "LoopForge",
		GongfengProjectID: "123", WebURL: "https://example.com/web", CloneURL: "https://example.com/web.git",
		BaseBranch: "main", DefaultBranch: "main", LocalPath: "/srv/repo",
		PipelineProjectID: 7, PipelineDefID: 9, PipelineURLPattern: "https://ci/{id}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repositoryRunContext() = %#v, want %#v", got, want)
	}
}

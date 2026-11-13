package engine

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestPromptRepositoryLine(t *testing.T) {
	cases := []struct {
		name string
		repo domain.RepositoryContext
		want string
	}{
		{name: "unbound", want: "未绑定代码仓库"},
		{
			name: "gongfeng",
			repo: domain.RepositoryContext{RepositoryID: "r1", Name: "demo", GongfengProjectID: "123", BaseBranch: "main", WorkBranch: "feat/x"},
			want: "demo (123), 最新远端基线 main, 本次工作分支 feat/x",
		},
		{
			name: "ssh",
			repo: domain.RepositoryContext{RepositoryID: "r2", Name: "infra", Provider: domain.RepositoryProviderSSH, GongfengProjectID: "ignored", BaseBranch: "dev", WorkBranch: "w"},
			want: "infra (Git SSH), 最新远端基线 dev, 本次工作分支 w",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptRepositoryLine(domain.Run{Repository: tc.repo}); got != tc.want {
				t.Fatalf("promptRepositoryLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUpstreamNodeOutputs(t *testing.T) {
	node := domain.WorkflowNode{ID: "target"}
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{
		{Source: "b", Target: "target"},
		{Source: "empty", Target: "target"},
		{Source: "missing", Target: "target"},
		{Source: "a", Target: "other"},
		{Source: "a", Target: "target"},
	}}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{
		"a":     {Output: "out-a"},
		"b":     {Output: "out-b"},
		"empty": {},
	}}
	want := []string{"[b]\nout-b", "[a]\nout-a"}
	if got := upstreamNodeOutputs(wf, node, run); !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreamNodeOutputs() = %#v, want %#v", got, want)
	}
	if got := upstreamNodeOutputs(domain.WorkflowDefinition{}, node, run); got == nil || len(got) != 0 {
		t.Fatalf("no edges should yield empty non-nil slice, got %#v", got)
	}
}

package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestWorkspaceRefreshStopsAfterDevelopmentStarts(t *testing.T) {
	t.Parallel()
	run := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
			{ID: "prepare", Type: "script", RunOnWorkspaceRebuild: true},
			{ID: "implement", Type: "agent"},
		}},
		Nodes: map[string]*domain.NodeRun{"prepare": {Attempt: 2}},
	}
	if !workspaceCanRefreshRepositories(run) {
		t.Fatal("preparation retry cannot refresh an untouched checkout")
	}
	run.Nodes["implement"] = &domain.NodeRun{Attempt: 1, Status: "failed"}
	if workspaceCanRefreshRepositories(run) {
		t.Fatal("development retry would silently change the frozen baseline")
	}
	delete(run.Nodes, "implement")
	run.ExecutionContext.WorkspaceRunID = "parent"
	if workspaceCanRefreshRepositories(run) {
		t.Fatal("child preparation would refresh its parent's checkout")
	}
}

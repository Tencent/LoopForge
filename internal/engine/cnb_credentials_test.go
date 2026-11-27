package engine

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestCloneRemoteForGitNeverEmbedsTheGlobalTokenInACNBRemote(t *testing.T) {
	ctx := context.Background()
	for _, remote := range []string{"https://cnb.cool/org/team/repo.git", "git@cnb.cool:org/repo.git"} {
		if got := cloneRemoteForGit(ctx, remote); got != remote {
			t.Errorf("cloneRemoteForGit(%q) = %q", remote, got)
		}
	}
}

func TestPushConversationWorkspaceNeedsTheRequestersCNBToken(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "cnb-pusher", GitToken: "ghp_FAKEGLOBALPAT"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("cnb-pusher")
	run := domain.Run{ID: "run-cnb-push", Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID},
		Repositories: []domain.RepositoryContext{{RepositoryID: "r", Name: "repo", Provider: domain.RepositoryProviderCNB, CloneURL: "https://cnb.cool/org/repo.git", GongfengProjectID: "cnb:org/repo", WorkBranch: "feature/x"}}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := &Engine{repo: st}
	_, err = e.PushConversationWorkspace(context.Background(), run.ID, user.ID, "cnb-pusher", "cnb@example.invalid", "msg", "")
	if err == nil || !strings.Contains(err.Error(), "CNB 访问令牌") {
		t.Fatalf("the global Git token must not satisfy a CNB repository: %v", err)
	}
}

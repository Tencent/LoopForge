package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestConversationPushRepositoriesKeepsSelectedAndMarksSync(t *testing.T) {
	bound := []domain.RunnerRepository{
		{RepositoryID: "repo-a", Dir: "a"},
		{RepositoryID: "repo-b", Dir: "b"},
		{RepositoryID: "repo-a", Dir: "a-copy"},
	}
	selected := []domain.RepositoryContext{{RepositoryID: "repo-a", WorkspaceDir: "a"}}
	got := conversationPushRepositories(bound, selected, "develop")
	if len(got) != 1 {
		t.Fatalf("kept = %+v, want only repo-a/a", got)
	}
	if got[0].RepositoryID != "repo-a" || got[0].Dir != "a" {
		t.Fatalf("kept wrong repository: %+v", got[0])
	}
	if !got[0].SyncWorkBranch || got[0].SharedPushTarget != "develop" {
		t.Fatalf("selected repository not marked for push: %+v", got[0])
	}
}

func TestConversationPushRepositoriesEmptySelectionDropsAll(t *testing.T) {
	bound := []domain.RunnerRepository{{RepositoryID: "repo-a", Dir: "a"}}
	if got := conversationPushRepositories(bound, nil, ""); len(got) != 0 {
		t.Fatalf("kept = %+v, want none", got)
	}
}

func TestConversationPushHeadsFailureStatus(t *testing.T) {
	_, err := conversationPushHeads(domain.RunnerTask{Status: domain.RunnerTaskFailed, Error: "  remote rejected  "})
	if err == nil || err.Error() != "git push: remote rejected" {
		t.Fatalf("err = %v, want git push: remote rejected", err)
	}
	_, err = conversationPushHeads(domain.RunnerTask{Status: domain.RunnerTaskFailed})
	if err == nil || !strings.Contains(err.Error(), "执行机器推送工作分支失败（failed）") {
		t.Fatalf("err = %v, want fallback message", err)
	}
}

func TestConversationPushHeadsDecodesResult(t *testing.T) {
	heads, err := conversationPushHeads(domain.RunnerTask{Status: domain.RunnerTaskSuccess, Result: "  "})
	if err != nil || heads == nil || len(heads) != 0 {
		t.Fatalf("empty result: heads=%#v err=%v, want empty non-nil slice", heads, err)
	}
	heads, err = conversationPushHeads(domain.RunnerTask{Status: domain.RunnerTaskSuccess, Result: `[{"repositoryId":"repo-a","workBranch":"agentflow/run","head":"abc"}]`})
	if err != nil || len(heads) != 1 || heads[0].RepositoryID != "repo-a" || heads[0].Head != "abc" {
		t.Fatalf("heads=%+v err=%v", heads, err)
	}
	if _, err := conversationPushHeads(domain.RunnerTask{Status: domain.RunnerTaskSuccess, Result: "{"}); err == nil || !strings.Contains(err.Error(), "decode push result") {
		t.Fatalf("err = %v, want decode error", err)
	}
}

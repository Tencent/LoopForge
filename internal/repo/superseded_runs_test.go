package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func issueRun(t *testing.T, s *Store, id, issueID, status string) {
	t.Helper()
	run := domain.Run{
		ID: id, Status: status,
		ExecutionContext: domain.TelemetryContext{IssueID: issueID},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
}

// 需求被重新触发时会新建一个 run，旧 run 停在 failed。失败的 run 为了重试保留
// 工作区，连带把容器也一直留着；重新从头跑之后已经没人会去重试它了。
func TestSupersededIssueRunIDsFindsReplacedRuns(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	issueRun(t, s, "run-old-failed", "issue-1", "failed")
	issueRun(t, s, "run-old-cancelled", "issue-1", "cancelled")
	issueRun(t, s, "run-old-succeeded", "issue-1", "succeeded")
	issueRun(t, s, "run-new", "issue-1", "running")
	issueRun(t, s, "run-other-issue", "issue-2", "failed")

	got := map[string]bool{}
	for _, id := range s.SupersededIssueRunIDs("issue-1", "run-new") {
		got[id] = true
	}

	if !got["run-old-failed"] || !got["run-old-cancelled"] {
		t.Fatalf("settled runs of the same issue must be superseded: %v", got)
	}
	if got["run-new"] {
		t.Fatal("the run that just started must never be superseded")
	}
	if got["run-other-issue"] {
		t.Fatal("another issue's run must not be touched")
	}
	// 成功的 run 由终态清理流程负责，不走这条路径。
	if got["run-old-succeeded"] {
		t.Fatal("succeeded runs are handled by terminal cleanup")
	}
}

// 工作区已经清理过的 run 不需要再回收一次。
func TestSupersededIssueRunIDsSkipsAlreadyCleaned(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	issueRun(t, s, "run-cleaned", "issue-1", "cancelled")
	claimed, err := s.ClaimRunWorkspaceCleanup("run-cleaned", time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("cleanup claim failed: claimed=%v err=%v", claimed, err)
	}

	if ids := s.SupersededIssueRunIDs("issue-1", ""); len(ids) != 0 {
		t.Fatalf("already cleaned run must not be reclaimed again: %v", ids)
	}
}

func TestIdleSandboxRunIDsUsesPerStatusCutoff(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, status := range []string{"failed", "waiting", "running", "succeeded", "cancelled"} {
		issueRun(t, s, "run-"+status, "", status)
	}
	issueRun(t, s, "run-failed-cleaned", "", "failed")
	if err := s.DB.Model(&runRow{}).Where("id = ?", "run-failed-cleaned").Update("workspace_cleaned_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ids := func(failedBefore, waitingBefore time.Time) map[string]bool {
		got := map[string]bool{}
		for _, id := range s.IdleSandboxRunIDs(failedBefore, waitingBefore) {
			got[id] = true
		}
		return got
	}
	if got := ids(now.Add(-time.Hour), now.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("runs updated just now are not idle: %v", got)
	}
	if got := ids(now.Add(time.Hour), now.Add(-time.Hour)); len(got) != 1 || !got["run-failed"] {
		t.Fatalf("only the uncleaned failed run passes the failed cutoff: %v", got)
	}
	if got := ids(now.Add(time.Hour), now.Add(time.Hour)); len(got) != 2 || !got["run-failed"] || !got["run-waiting"] {
		t.Fatalf("running and settled runs are never idle candidates: %v", got)
	}
}

func TestSupersededIssueRunIDsIgnoresEmptyIssue(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	issueRun(t, s, "run-no-issue", "", "failed")

	if ids := s.SupersededIssueRunIDs("", ""); len(ids) != 0 {
		t.Fatalf("an empty issue must not match every run: %v", ids)
	}
}

// 闲置容器被回收后，重试要用同一个镜像重建容器；目录里的 Dockerfile
// 改过之后，只有 run 快照还引用旧镜像。
func TestLiveSandboxDockerfilesReadsFrozenSnapshots(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	put := func(id, content string) {
		run := domain.Run{ID: id, Status: "failed", Nodes: map[string]*domain.NodeRun{}}
		if content != "" {
			run.WorkflowSnapshot.SandboxSnapshot = &domain.RunnerSandboxSpec{Dockerfile: &domain.DockerfileDefinition{ID: "df", Content: content}}
		}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	put("run-live", "FROM old")
	put("run-no-snapshot", "")
	put("run-cleaned", "FROM cleaned")
	if err := s.DB.Model(&runRow{}).Where("id = ?", "run-cleaned").Update("workspace_cleaned_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	got := s.LiveSandboxDockerfiles()
	if len(got) != 1 || got[0].Content != "FROM old" {
		t.Fatalf("live snapshot dockerfiles = %+v", got)
	}
}

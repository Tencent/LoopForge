package tracker

import (
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// These tests pin the four call sites that #841 folded into
// addTAPDStoriesLog: each must keep passing its own project scope, status,
// count and message, not just the shared helper.

const tapdGuardStories = `{"count":1,"data":[{"id":"1001","name":"需求A","status":"planned","owner":"alice","iteration_id":"it1"}]}`

// tapdGuardAuthFailure is an unparsable bug page whose text mentions 401, so
// actionableAuthError rewrites it and the logged message is distinguishable
// from the raw error.
const tapdGuardAuthFailure = "401 Unauthorized: token expired"
const tapdGuardBugs = `{"count":1,"data":[{"id":"2001","title":"缺陷B","status":"new","current_owner":"alice","iteration_id":"it1"}]}`

func tapdGuardStore() *memIssueStore {
	return &memIssueStore{
		issues:       map[string]domain.Issue{},
		repositories: map[string]domain.Repository{},
		projects:     map[string]domain.Project{"dp": {ID: "dp", TAPDWorkspaceID: "10000000", TAPDIterationID: "it1"}},
		users:        []domain.User{{ID: 1, LoginName: "alice"}},
	}
}

func TestPullTAPDStoriesPoolFailureLogsPartialCountPerProject(t *testing.T) {
	// Stories succeed but the bug page is unparsable, so the pool fails after
	// importing one story: the per-project log must keep that partial count.
	gateway := mockTAPDMultiGateway(t, tapdGuardStories, tapdGuardAuthFailure, nil, nil)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})

	client, _, err := svc.trackerClientForProject("tapd", "dp")
	if err != nil {
		t.Fatal(err)
	}
	_, poolErr := svc.pullTAPDPool(client, tapdGuardStore(), tapdPool{ProjectID: "dp", WorkspaceID: "10000000", IterationID: "it1"}, map[string]bool{})
	if poolErr == nil || actionableAuthError("TAPD", poolErr) == poolErr.Error() {
		t.Fatalf("fixture must fail the pool with an auth-rewritten error, got %v", poolErr)
	}

	n, err := svc.pullTAPDStories(tapdGuardStore())
	if err == nil || !strings.Contains(err.Error(), "项目 dp") {
		t.Fatalf("pullTAPDStories() = %d, %v; want project-scoped pool error", n, err)
	}
	if n != 1 {
		t.Fatalf("pullTAPDStories() count = %d, want 1 imported story", n)
	}
	logs := svc.Logs()
	if len(logs) != 1 {
		t.Fatalf("want only the per-project failure log (no platform summary), got %+v", logs)
	}
	got := logs[0]
	if got.Source != "tapd" || got.Action != "sync_stories" || got.Status != "failed" || got.ProjectID != "dp" || got.Count != 1 {
		t.Fatalf("pool failure log = %+v", got)
	}
	if want := actionableAuthError("TAPD", poolErr); got.Message != want {
		t.Fatalf("pool failure message = %q, want %q", got.Message, want)
	}
}

func TestPullTAPDStoriesSuccessWritesPlatformSummary(t *testing.T) {
	gateway := mockTAPDMultiGateway(t, tapdGuardStories, tapdGuardBugs, nil, nil)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})

	n, err := svc.pullTAPDStories(tapdGuardStore())
	if n != 2 || err != nil {
		t.Fatalf("pullTAPDStories() = %d, %v; want 2, nil", n, err)
	}
	logs := svc.Logs()
	if len(logs) != 1 {
		t.Fatalf("want exactly one summary log, got %+v", logs)
	}
	got := logs[0]
	if got.Source != "tapd" || got.Action != "sync_stories" || got.Status != "success" || got.Count != 2 {
		t.Fatalf("summary log = %+v", got)
	}
	if got.ProjectID != "" {
		t.Fatalf("summary log must stay platform-scoped, got ProjectID %q", got.ProjectID)
	}
	if want := fmt.Sprintf("同步 %d 个需求池：拉取 %d 条（需求+缺陷），清理 %d 条", 1, 2, 0); got.Message != want {
		t.Fatalf("summary message = %q, want %q", got.Message, want)
	}
}

func TestPullScheduledTAPDPoolLogsProjectScopedOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		gateway := mockTAPDMultiGateway(t, tapdGuardStories, tapdGuardBugs, nil, nil)
		defer gateway.Close()
		svc := NewService()
		svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})

		n, _, err := svc.PullScheduleJob(tapdGuardStore(), domain.ScheduleJob{Source: "tapd", ProjectID: " dp ", TAPDWorkspaceID: "10000000", TAPDIterationID: "it1"})
		if n != 2 || err != nil {
			t.Fatalf("PullScheduleJob() = %d, %v", n, err)
		}
		logs := svc.Logs()
		if len(logs) != 1 {
			t.Fatalf("want one scheduled log, got %+v", logs)
		}
		got := logs[0]
		if got.Source != "tapd" || got.Action != "sync_stories" || got.Status != "success" || got.ProjectID != "dp" || got.Count != 2 {
			t.Fatalf("scheduled success log = %+v", got)
		}
		if want := tapdPullMessage("it1", 2, 0); got.Message != want {
			t.Fatalf("scheduled success message = %q, want %q", got.Message, want)
		}
	})

	t.Run("failure keeps partial count", func(t *testing.T) {
		gateway := mockTAPDMultiGateway(t, tapdGuardStories, tapdGuardAuthFailure, nil, nil)
		defer gateway.Close()
		svc := NewService()
		svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})

		n, _, err := svc.PullScheduleJob(tapdGuardStore(), domain.ScheduleJob{Source: "tapd", ProjectID: "dp", TAPDWorkspaceID: "10000000", TAPDIterationID: "it1"})
		if err == nil || n != 1 {
			t.Fatalf("PullScheduleJob() = %d, %v; want 1 and an error", n, err)
		}
		logs := svc.Logs()
		if len(logs) != 1 {
			t.Fatalf("want one scheduled log, got %+v", logs)
		}
		got := logs[0]
		if got.Status != "failed" || got.ProjectID != "dp" || got.Count != 1 || got.Message != actionableAuthError("TAPD", err) {
			t.Fatalf("scheduled failure log = %+v (err %v)", got, err)
		}
	})

	t.Run("blank project falls back to platform", func(t *testing.T) {
		gateway := mockTAPDMultiGateway(t, tapdGuardStories, tapdGuardBugs, nil, nil)
		defer gateway.Close()
		svc := NewService()
		svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})

		if _, _, err := svc.PullScheduleJob(tapdGuardStore(), domain.ScheduleJob{Source: "tapd", TAPDWorkspaceID: "10000000", TAPDIterationID: "it1"}); err != nil {
			t.Fatal(err)
		}
		if logs := svc.Logs(); len(logs) != 1 || logs[0].ProjectID != "platform" {
			t.Fatalf("blank-project scheduled log = %+v, want ProjectID platform", logs)
		}
	})
}

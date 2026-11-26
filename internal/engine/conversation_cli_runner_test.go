package engine

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestConversationCloudRunnerRequiresConversationCLISupport(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New(s, runtime.NewManager(t.TempDir()))
	if id, err := e.ConversationCloudRunner("e2e"); err != nil || id != "" {
		t.Fatalf("unconfigured pool = %q, %v; want the built-in host", id, err)
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool=cloud", Projects: []string{"e2e"}})
	if id, err := e.ConversationCloudRunner("other"); err != nil || id != "" {
		t.Fatalf("project outside the pool = %q, %v; want the built-in host", id, err)
	}
	for _, r := range []domain.Runner{
		{ID: "old", Status: domain.RunnerStatusOnline, RunnerVersion: "0.10.0", Labels: []string{"pool=cloud"}},
		{ID: "user-owned", OwnerUserID: 7, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationCLI, Labels: []string{"pool=cloud"}},
		{ID: "offline", Status: domain.RunnerStatusOffline, RunnerVersion: domain.RunnerVersionConversationCLI, Labels: []string{"pool=cloud"}},
	} {
		if err := s.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	if id, err := e.ConversationCloudRunner("e2e"); err == nil || id != "" || !strings.Contains(err.Error(), "升级 Runner") {
		t.Fatalf("no eligible runner = %q, %v; want an upgrade error", id, err)
	}
	if err := s.PutRunner(domain.Runner{ID: "capable", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationCLI, Labels: []string{"pool=cloud"}}); err != nil {
		t.Fatal(err)
	}
	if id, err := e.ConversationCloudRunner("e2e"); err != nil || id != "capable" {
		t.Fatalf("runner = %q, %v; want the online platform runner that supports conversation CLIs", id, err)
	}
}

func TestAcquireConversationCLIRejectsMissingWorkspace(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New(s, runtime.NewManager(t.TempDir()))
	sandbox, release, err := e.AcquireConversationCLI(context.Background(), "missing", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "工作区不存在") || sandbox != nil || release != nil {
		t.Fatalf("missing workspace = %v, %v, %v", sandbox, release != nil, err)
	}
}

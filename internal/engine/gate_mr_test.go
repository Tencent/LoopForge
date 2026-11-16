package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestVerifyRunMRsRequiresAnOpenMRIntoTheFrozenTarget(t *testing.T) {
	t.Parallel()
	run := domain.Run{Repositories: []domain.RepositoryContext{
		{RepositoryID: "svc", Name: "svc", GongfengProjectID: "github:o/svc", WorkspaceDir: "svc", WorkBranch: "feat", BaseBranch: "release/1.2", DefaultBranch: "main"},
		{RepositoryID: "docs", Name: "docs", GongfengProjectID: "github:o/docs", WorkspaceDir: "docs", WorkBranch: "feat", DefaultBranch: "main"},
		{RepositoryID: "ro", Name: "ro", GongfengProjectID: "github:o/ro", WorkspaceDir: "ro", WorkBranch: "feat", ReadOnly: true},
	}}
	ahead := map[string]int{"svc": 2, "docs": 0, "ro": 5}
	pending := func(rc domain.RepositoryContext) (int, error) { return ahead[rc.RepositoryID], nil }
	var looked []string
	found := map[string]domain.DeliveryMR{}
	e := &Engine{FindBranchMR: func(project, branch string) (domain.DeliveryMR, bool, error) {
		looked = append(looked, project+"@"+branch)
		mr, ok := found[project]
		return mr, ok, nil
	}}

	err := e.verifyRunMRs(run, pending)
	if err == nil || errors.Is(err, errMRUnverifiable) || !strings.Contains(err.Error(), "没有源分支为 feat 的 MR/PR") || !strings.Contains(err.Error(), "feat → release/1.2") {
		t.Fatalf("missing MR: %v", err)
	}
	if strings.Join(looked, ",") != "github:o/svc@feat" {
		t.Fatalf("only the repository with pushed commits is looked up, got %v", looked)
	}

	found["github:o/svc"] = domain.DeliveryMR{URL: "https://github.com/o/svc/pull/7", State: "opened", TargetBranch: "main"}
	if err := e.verifyRunMRs(run, pending); err == nil || !strings.Contains(err.Error(), "目标分支是 main，本轮目标分支是 release/1.2") {
		t.Fatalf("wrong target: %v", err)
	}
	found["github:o/svc"] = domain.DeliveryMR{URL: "https://github.com/o/svc/pull/7", State: "closed", TargetBranch: "release/1.2"}
	if err := e.verifyRunMRs(run, pending); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("closed MR: %v", err)
	}
	found["github:o/svc"] = domain.DeliveryMR{URL: "https://git.example/o/svc/merge_requests/7", GongfengState: "opened", TargetBranch: "release/1.2"}
	if err := e.verifyRunMRs(run, pending); err != nil {
		t.Fatalf("open MR into the frozen target must pass: %v", err)
	}
	found["github:o/svc"] = domain.DeliveryMR{State: "merged", TargetBranch: "release/1.2"}
	if err := e.verifyRunMRs(run, pending); err != nil {
		t.Fatalf("merged MR must pass: %v", err)
	}

	lookupDown := &Engine{FindBranchMR: func(string, string) (domain.DeliveryMR, bool, error) {
		return domain.DeliveryMR{}, false, errors.New("401 Bad credentials")
	}}
	if err := lookupDown.verifyRunMRs(run, pending); !errors.Is(err, errMRUnverifiable) || !strings.Contains(err.Error(), "401 Bad credentials") {
		t.Fatalf("a failed lookup goes to a person: %v", err)
	}
	if err := (&Engine{}).verifyRunMRs(run, pending); !errors.Is(err, errMRUnverifiable) {
		t.Fatalf("no MR lookup configured goes to a person: %v", err)
	}
	ssh := domain.Run{Repositories: []domain.RepositoryContext{{RepositoryID: "ssh", Provider: domain.RepositoryProviderSSH, WorkspaceDir: "ssh", WorkBranch: "feat", DefaultBranch: "main"}}}
	if err := e.verifyRunMRs(ssh, func(domain.RepositoryContext) (int, error) { return 1, nil }); !errors.Is(err, errMRUnverifiable) || !strings.Contains(err.Error(), "ssh：托管平台不支持自动核验 MR") {
		t.Fatalf("a host without MR lookup goes to a person: %v", err)
	}
	if err := e.verifyRunMRs(run, func(domain.RepositoryContext) (int, error) { return 0, errors.New("exit status 2") }); err == nil || errors.Is(err, errMRUnverifiable) {
		t.Fatalf("an unreadable remote fails the gate: %v", err)
	}
	if err := e.verifyRunMRs(domain.Run{}, pending); err != nil {
		t.Fatalf("a run without repositories has no MR to verify: %v", err)
	}
}

func TestMRPendingCommandCountsPushedCommitsBeyondTarget(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	git(root, "init", "-q", "--bare", remote)
	workspace := filepath.Join(root, "run")
	checkout := filepath.Join(workspace, "app")
	git(root, "clone", "-q", remote, checkout)
	commit := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		git(checkout, "add", ".")
		git(checkout, "commit", "-qm", name)
	}
	commit("base")
	git(checkout, "push", "-q", "origin", "HEAD:main")
	git(checkout, "checkout", "-qb", "feat")
	target := executionTarget{Scope: domain.ExecutionScopeRepositories, Repository: &domain.RepositoryContext{WorkspaceDir: "app", WorkBranch: "feat", DefaultBranch: "main"}}
	count := func() (int, error) {
		t.Helper()
		cmd := exec.Command("sh", "-c", bindExecutionTarget(mrPendingCommand, target))
		cmd.Dir = workspace
		out, err := cmd.Output()
		if err != nil {
			return 0, err
		}
		return parseMRPendingCount(string(out))
	}
	if n, err := count(); err != nil || n != 0 {
		t.Fatalf("unpushed work branch = %d %v", n, err)
	}
	git(checkout, "push", "-q", "origin", "feat")
	if n, err := count(); err != nil || n != 0 {
		t.Fatalf("work branch at the target = %d %v", n, err)
	}
	commit("one")
	commit("two")
	if n, err := count(); err != nil || n != 0 {
		t.Fatalf("local commits only = %d %v", n, err)
	}
	git(checkout, "push", "-q", "origin", "feat")
	if n, err := count(); err != nil || n != 2 {
		t.Fatalf("pushed commits = %d %v", n, err)
	}
	other := filepath.Join(root, "other")
	git(root, "clone", "-q", "-b", "feat", remote, other)
	if err := os.WriteFile(filepath.Join(other, "three"), []byte("3"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(other, "add", ".")
	git(other, "commit", "-qm", "three")
	git(other, "push", "-q", "origin", "feat")
	if n, err := count(); err != nil || n != 3 {
		t.Fatalf("a remote tip the checkout has not fetched = %d %v", n, err)
	}
	if _, err := parseMRPendingCount("fatal: not a git repository"); err == nil {
		t.Fatal("non-numeric output accepted")
	}
}

func TestMRGateRunsInTheNodeLoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	putGateTestAgent(t, s, "mr-gate-agent", []domain.VerificationGate{{ID: "mr-opened", Name: "MR 已创建", Mode: domain.VerificationModeMR, Required: true, OnFailure: "loop"}})
	wf := domain.WorkflowDefinition{ID: "mr-gate-lab", Name: "mr gate lab", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "mr", Name: "mr", Type: "agent", AgentID: "mr-gate-agent", ExecutorID: "mock", MaxAttempts: 1},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	e.FindBranchMR = func(string, string) (domain.DeliveryMR, bool, error) {
		t.Error("a run without repositories must not look up MRs")
		return domain.DeliveryMR{}, false, nil
	}
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	passed := false
	for _, event := range current.Telemetry {
		if event.Name == "gate.mr.passed" {
			passed = true
		}
	}
	if !passed {
		t.Fatal("the MR gate did not run")
	}
}

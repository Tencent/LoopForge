package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestVerifyRunMRsRequiresAnMRCarryingThePushedTip(t *testing.T) {
	t.Parallel()
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ProjectID: "proj", ResponsibleUserID: 7}, Repositories: []domain.RepositoryContext{
		{RepositoryID: "svc", Name: "svc", GongfengProjectID: "github:o/svc", WorkspaceDir: "svc", WorkBranch: "feat", BaseBranch: "release/1.2", DefaultBranch: "main"},
		{RepositoryID: "docs", Name: "docs", GongfengProjectID: "github:o/docs", WorkspaceDir: "docs", WorkBranch: "feat", DefaultBranch: "main"},
		{RepositoryID: "ro", Name: "ro", GongfengProjectID: "github:o/ro", WorkspaceDir: "ro", WorkBranch: "feat", ReadOnly: true},
	}}
	pushed := map[string]pushedWork{"svc": {Tip: "new", Ahead: 2}, "docs": {Tip: "-", Ahead: 0}, "ro": {Tip: "x", Ahead: 5}}
	pending := func(rc domain.RepositoryContext) (pushedWork, error) { return pushed[rc.RepositoryID], nil }
	var looked []string
	var mrs []domain.DeliveryMR
	e := &Engine{FindBranchMRsInto: func(q domain.BranchMRQuery) ([]domain.DeliveryMR, error) {
		looked = append(looked, q.ProjectID+":"+q.RemoteProject+"@"+q.SourceBranch+"->"+q.TargetBranch+" as "+q.GitToken+"/"+strconv.FormatInt(q.ResponsibleUserID, 10))
		return mrs, nil
	}}
	fails := func(want string) {
		t.Helper()
		err := e.verifyRunMRs(run, "push-token", pending)
		if err == nil || errors.Is(err, errMRUnverifiable) || !strings.Contains(err.Error(), want) {
			t.Fatalf("want a self-correctable failure containing %q, got %v", want, err)
		}
	}
	passes := func() {
		t.Helper()
		if err := e.verifyRunMRs(run, "push-token", pending); err != nil {
			t.Fatal(err)
		}
	}

	fails("svc：feat → release/1.2 在托管平台上没有 MR/PR")
	if strings.Join(looked, ",") != "proj:github:o/svc@feat->release/1.2 as push-token/7" {
		t.Fatalf("only the repository with pushed commits is looked up, into its frozen target, with the run's project and push token: %v", looked)
	}
	mrs = []domain.DeliveryMR{{URL: "pr/3", State: "merged", HeadSHA: "old"}}
	fails("只有已合并的 MR pr/3，不包含本轮推送的 new")
	mrs = []domain.DeliveryMR{{URL: "pr/3", State: "merged"}}
	fails("只有已合并的 MR pr/3")
	mrs = []domain.DeliveryMR{{URL: "pr/3", State: "closed", HeadSHA: "new"}}
	fails("的 MR 已关闭")
	mrs = []domain.DeliveryMR{{URL: "pr/4", State: "opened", HeadSHA: "older"}}
	fails("还没有包含远端工作分支的 new：pr/4 的源提交是 older")

	mrs = []domain.DeliveryMR{{URL: "pr/3", State: "merged", HeadSHA: "old"}, {URL: "pr/4", State: "opened", HeadSHA: "new"}}
	passes()
	mrs = []domain.DeliveryMR{{URL: "!4", GongfengState: "reopened"}}
	passes()
	mrs = []domain.DeliveryMR{{URL: "pr/4", State: "merged", HeadSHA: "new"}}
	passes()

	lookupDown := &Engine{FindBranchMRsInto: func(domain.BranchMRQuery) ([]domain.DeliveryMR, error) {
		return nil, errors.New("401 Bad credentials")
	}}
	if err := lookupDown.verifyRunMRs(run, "", pending); !errors.Is(err, errMRUnverifiable) || !strings.Contains(err.Error(), "401 Bad credentials") {
		t.Fatalf("a failed lookup goes to a person: %v", err)
	}
	if err := (&Engine{}).verifyRunMRs(run, "", pending); !errors.Is(err, errMRUnverifiable) {
		t.Fatalf("no MR lookup configured goes to a person: %v", err)
	}
	ssh := domain.Run{Repositories: []domain.RepositoryContext{{RepositoryID: "ssh", Provider: domain.RepositoryProviderSSH, WorkspaceDir: "ssh", WorkBranch: "feat", DefaultBranch: "main"}}}
	if err := e.verifyRunMRs(ssh, "", func(domain.RepositoryContext) (pushedWork, error) { return pushedWork{Tip: "t", Ahead: 1}, nil }); !errors.Is(err, errMRUnverifiable) || !strings.Contains(err.Error(), "ssh：托管平台不支持自动核验 MR") {
		t.Fatalf("a host without MR lookup goes to a person: %v", err)
	}
	if err := e.verifyRunMRs(run, "", func(domain.RepositoryContext) (pushedWork, error) { return pushedWork{}, errors.New("exit status 2") }); err == nil || errors.Is(err, errMRUnverifiable) {
		t.Fatalf("an unreadable remote fails the gate: %v", err)
	}
	if err := e.verifyRunMRs(domain.Run{}, "", pending); err != nil {
		t.Fatalf("a run without repositories has no MR to verify: %v", err)
	}
}

func TestMRGateRejectsAnEarlierMergedMROnAReusedWorkBranch(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
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
	commit("first delivery")
	git(checkout, "push", "-q", "origin", "feat")
	previous := git(checkout, "rev-parse", "HEAD")
	git(checkout, "push", "-q", "origin", "feat:main")
	git(checkout, "fetch", "-q", "origin")
	commit("this run")
	git(checkout, "push", "-q", "origin", "feat")
	tip := git(checkout, "rev-parse", "HEAD")

	rc := domain.RepositoryContext{RepositoryID: "app", Name: "app", GongfengProjectID: "github:o/app", WorkspaceDir: "app", WorkBranch: "feat", RequireExistingWorkBranch: true, DefaultBranch: "main"}
	pending := func(rc domain.RepositoryContext) (pushedWork, error) {
		cmd := exec.Command("sh", "-c", bindExecutionTarget(mrPendingCommand, executionTarget{Scope: domain.ExecutionScopeRepositories, Repository: &rc}))
		cmd.Dir = workspace
		out, err := cmd.Output()
		if err != nil {
			return pushedWork{}, err
		}
		return parseMRPending(string(out))
	}
	got, err := pending(rc)
	if err != nil || got.Tip != tip || got.Ahead != 1 {
		t.Fatalf("pending = %+v %v, want tip %s with 1 commit beyond main", got, err, tip)
	}
	mrs := []domain.DeliveryMR{{URL: "https://github.com/o/app/pull/1", State: "merged", TargetBranch: "main", HeadSHA: previous}}
	e := &Engine{FindBranchMRsInto: func(domain.BranchMRQuery) ([]domain.DeliveryMR, error) { return mrs, nil }}
	run := domain.Run{Repositories: []domain.RepositoryContext{rc}}
	if err := e.verifyRunMRs(run, "push-token", pending); err == nil || !strings.Contains(err.Error(), "不包含本轮推送的 "+tip) {
		t.Fatalf("the previous merged MR must not cover this run's push: %v", err)
	}
	mrs = append(mrs, domain.DeliveryMR{URL: "https://github.com/o/app/pull/2", State: "opened", TargetBranch: "main", HeadSHA: tip})
	if err := e.verifyRunMRs(run, "push-token", pending); err != nil {
		t.Fatalf("a new MR carrying the tip must pass: %v", err)
	}
}

func TestMRPendingCommandReportsThePushedTip(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	git(root, "init", "-q", "--bare", remote)
	workspace := filepath.Join(root, "run")
	checkout := filepath.Join(workspace, "app")
	git(root, "clone", "-q", remote, checkout)
	commit := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", ".")
		git(dir, "commit", "-qm", name)
	}
	commit(checkout, "base")
	git(checkout, "push", "-q", "origin", "HEAD:main")
	git(checkout, "checkout", "-qb", "feat")
	target := executionTarget{Scope: domain.ExecutionScopeRepositories, Repository: &domain.RepositoryContext{WorkspaceDir: "app", WorkBranch: "feat", DefaultBranch: "main"}}
	pending := func() pushedWork {
		t.Helper()
		cmd := exec.Command("sh", "-c", bindExecutionTarget(mrPendingCommand, target))
		cmd.Dir = workspace
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseMRPending(string(out))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := pending(); got != (pushedWork{Tip: "-", Ahead: 0}) {
		t.Fatalf("unpushed work branch = %+v", got)
	}
	git(checkout, "push", "-q", "origin", "feat")
	if got := pending(); got.Ahead != 0 {
		t.Fatalf("work branch at the target = %+v", got)
	}
	commit(checkout, "one")
	commit(checkout, "two")
	if got := pending(); got.Ahead != 0 {
		t.Fatalf("local commits only = %+v", got)
	}
	git(checkout, "push", "-q", "origin", "feat")
	if got := pending(); got.Ahead != 2 || got.Tip != git(checkout, "rev-parse", "HEAD") {
		t.Fatalf("pushed commits = %+v", got)
	}
	other := filepath.Join(root, "other")
	git(root, "clone", "-q", "-b", "feat", remote, other)
	commit(other, "three")
	git(other, "push", "-q", "origin", "feat")
	if got := pending(); got.Ahead != 3 || got.Tip != git(other, "rev-parse", "HEAD") {
		t.Fatalf("a remote tip the checkout has not fetched = %+v", got)
	}
	if _, err := parseMRPending("fatal: not a git repository"); err == nil {
		t.Fatal("unexpected output accepted")
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
	e.FindBranchMRsInto = func(domain.BranchMRQuery) ([]domain.DeliveryMR, error) {
		t.Error("a run without repositories must not look up MRs")
		return nil, nil
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

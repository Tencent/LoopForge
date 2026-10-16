package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A run bound to a workspace root this instance does not serve must never be
// resumed elsewhere: a fresh clone only contains committed work, so uncommitted
// upstream artifacts (e.g. SOP e2e scripts) would silently vanish from the
// delivered MR (issue: e2e-issue-29.sh lost across two instances).
func TestResumeInterruptedRunsSkipsForeignWorkspaceBind(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("instance-a", "host-a", 10*time.Millisecond, true)
	now := time.Now().UTC()
	r := domain.Run{ID: "run-bound", WorkflowID: "wf", Status: "running", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"test": {NodeID: "test", Status: "running"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(r.ID, "instance-a", 0, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(r.ID)
	// Same host, different root: exactly the two-processes-one-host incident.
	first, err := s.BindRunWorkspace(r.ID, "instance-a", owned.Generation, "host-a", "/ws-other")
	if err != nil || !first {
		t.Fatalf("bind = %v, %v", first, err)
	}
	time.Sleep(30 * time.Millisecond)

	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("foreign-bound run must not be resumed, got %v", resumed)
	}
	got, _ := s.RunState(r.ID)
	if got.OwnerInstanceID != "instance-a" {
		t.Fatalf("owner changed to %q, want instance-a", got.OwnerInstanceID)
	}
	if got.Nodes["test"].Status != "running" {
		t.Fatalf("in-flight node was reset: %+v", got.Nodes["test"])
	}
}

// Human gates must reject foreign-bound runs before acquiring the lease so the
// original owner keeps its workspace and in-flight state.
func TestApproveRejectsForeignWorkspaceBind(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	r := domain.Run{ID: "run-gate", WorkflowID: "wf", Status: "waiting", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(r.ID, "someone-else", 0, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(r.ID)
	if _, err := s.BindRunWorkspace(r.ID, "someone-else", owned.Generation, "host-b", "/ws-b"); err != nil {
		t.Fatal(err)
	}
	heartbeatHost(t, s, "someone-else", "host-b")
	time.Sleep(30 * time.Millisecond) // lease expired so only the bind check can reject

	err := e.Approve(r.ID, "gate", "")
	if err == nil || !strings.Contains(err.Error(), "工作区绑定") {
		t.Fatalf("Approve error = %v, want foreign workspace bind error", err)
	}
	got, _ := s.RunState(r.ID)
	if got.OwnerInstanceID != "someone-else" {
		t.Fatalf("lease was stolen by %q before the bind check", got.OwnerInstanceID)
	}
	if got.Nodes["gate"].Status != "waiting_approval" {
		t.Fatalf("gate node changed: %+v", got.Nodes["gate"])
	}
}

func TestPatchNewFiles(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/client.go b/client.go",
		"index aaa..bbb 100644",
		"--- a/client.go",
		"+++ b/client.go",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/scripts/e2e-issue-29.sh b/scripts/e2e-issue-29.sh",
		"new file mode 100755",
		"index 0000000..1111111",
		"--- /dev/null",
		"+++ b/scripts/e2e-issue-29.sh",
		"@@ -0,0 +1 @@",
		"+#!/usr/bin/env bash",
		"diff --git a/old.txt b/renamed.txt",
		"similarity index 100%",
		"rename from old.txt",
		"rename to renamed.txt",
		"diff --git a/blob.bin b/blob.bin",
		"new file mode 100644",
		"index 0000000..2222222",
		"GIT binary patch",
		"", // trailing newline
	}, "\n")
	got := patchNewFiles(diff)
	want := []string{"scripts/e2e-issue-29.sh", "blob.bin"}
	if len(got) != len(want) {
		t.Fatalf("patchNewFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("patchNewFiles[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if out := patchNewFiles("diff --git a/x.go b/x.go\n+++ b/x.go\n+change\n"); out != nil {
		t.Fatalf("modification-only diff should yield no new files, got %v", out)
	}
}

func gitInit(t *testing.T, workspace string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "master"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "base", "--quiet"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func gitCommitFile(t *testing.T, workspace, path, content string) {
	t.Helper()
	full := filepath.Join(workspace, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", path}, {"commit", "-m", "add " + path, "--quiet"}} {
		if out, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestVerifyDeliveryCompleteness(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	e, _ := newRecoveryEngine(t)
	patch := "diff --git a/server/scripts/test/e2e-issue-29.sh b/server/scripts/test/e2e-issue-29.sh\n" +
		"new file mode 100755\n--- /dev/null\n+++ b/server/scripts/test/e2e-issue-29.sh\n@@ -0,0 +1 @@\n+echo ok\n"
	run := domain.Run{ID: "run-delivery", Artifacts: []domain.Artifact{
		{Name: "repository-patch", NodeID: "test", Content: patch},
	}}

	missing := e.verifyDeliveryCompleteness(run, ws)
	if len(missing) != 1 || !strings.Contains(missing[0], "server/scripts/test/e2e-issue-29.sh (produced by test)") {
		t.Fatalf("missing = %v, want the lost e2e script", missing)
	}

	gitCommitFile(t, ws, "server/scripts/test/e2e-issue-29.sh", "#!/usr/bin/env bash\necho ok\n")
	if missing := e.verifyDeliveryCompleteness(run, ws); missing != nil {
		t.Fatalf("file committed but still reported missing: %v", missing)
	}

	// No repository-patch artifacts: nothing to verify even without a git repo.
	empty := e.verifyDeliveryCompleteness(domain.Run{ID: "run-empty"}, t.TempDir())
	if empty != nil {
		t.Fatalf("empty run should report nothing, got %v", empty)
	}
}

func TestVerifyDeliveryCompletenessPrefersMRPatch(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	e, _ := newRecoveryEngine(t)
	testPatch := "diff --git a/a.sh b/a.sh\nnew file mode 100755\n--- /dev/null\n+++ b/a.sh\n@@ -0,0 +1 @@\n+test\n"
	mrPatch := "diff --git a/a.sh b/a.sh\nnew file mode 100755\n--- /dev/null\n+++ b/a.sh\n@@ -0,0 +1 @@\n+final\n"
	run := domain.Run{ID: "run-two", Artifacts: []domain.Artifact{
		{Name: "repository-patch", NodeID: "test", Content: testPatch},
		{Name: "repository-patch", NodeID: "mr", Content: mrPatch},
	}}
	missing := e.verifyDeliveryCompleteness(run, ws)
	if len(missing) != 1 || !strings.Contains(missing[0], "(produced by mr)") {
		t.Fatalf("missing = %v, want producer labeled mr", missing)
	}
}

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

// installFakeRunnerClone puts a git first on PATH whose `clone` runs
// cloneScript; every other git command goes to the real git.
func installFakeRunnerClone(t *testing.T, cloneScript string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = clone ]; then\n" + cloneScript + "\n  fi\ndone\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Issue #124 on an execution machine: the clone ran with --quiet and a fixed
// 10 minute limit, so a hung transfer held preparation for the full 10 minutes
// and the error said only "signal: killed".
func TestRunnerCloneStopsAStalledTransferAndKeepsItsLastOutput(t *testing.T) {
	t.Setenv(gitwork.CloneStallEnv, "2s")
	const token = "tok-runner-secret"
	installFakeRunnerClone(t, `    echo "Receiving objects:  27% (270/1000), 2.70 MiB | 30.00 KiB/s https://x:`+token+`@github.com/a.git" >&2
    exec sleep 60`)
	repo := &domain.RunnerRepository{Name: "web", CloneURL: "https://git.example.invalid/team/web.git", GitToken: token}
	started := time.Now()
	err := cloneRepository(context.Background(), t.TempDir(), repo)
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("stalled clone held preparation for %s", elapsed)
	}
	if !procwatch.Stopped(err) || !strings.Contains(err.Error(), "克隆仓库 web 已 2s 没有任何输出") || !strings.Contains(err.Error(), gitwork.CloneStallEnv) {
		t.Fatalf("stall not explained: %v", err)
	}
	if !strings.Contains(err.Error(), "Receiving objects:  27%") {
		t.Fatalf("error lacks git's last output: %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked the token: %v", err)
	}
}

func TestRunnerCloneKeepsItsTotalLimitConfigurable(t *testing.T) {
	if got := runnerCloneWatch(&domain.RunnerRepository{CloneURL: "https://github.com/Tencent/LoopForge.git"}); got.Timeout != 10*time.Minute || got.Stall != 5*time.Minute || got.Label != "克隆仓库 LoopForge" {
		t.Fatalf("defaults = %+v", got)
	}
	t.Setenv(gitwork.CloneTimeoutEnv, "500ms")
	installFakeRunnerClone(t, `    while :; do echo "Receiving objects: 1%" >&2; sleep 0.05; done`)
	err := cloneRepository(context.Background(), t.TempDir(), &domain.RunnerRepository{Name: "web", CloneURL: "https://git.example.invalid/team/web.git"})
	if !procwatch.Stopped(err) || !strings.Contains(err.Error(), "超过 500ms 仍未完成") || !strings.Contains(err.Error(), gitwork.CloneTimeoutEnv) {
		t.Fatalf("err = %v", err)
	}
}

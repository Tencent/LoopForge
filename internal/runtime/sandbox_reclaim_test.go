package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 一台机器上可能同时有控制面的 agentflow-run-* 和执行机的 agentflow-rt-*，
// 后者由 runner 自己按工作区清理。归属只能看 /workspace 绑定，不能看名字。
func TestParseDockerSandboxMountsOnlyClaimsOwnRoot(t *testing.T) {
	root := filepath.Clean("/data/app/skillhub-agent/workspaces")
	out := "/agentflow-run-1\t" + root + "/run-1\n" +
		"/agentflow-run-2\t" + root + "/run-2\n" +
		"/agentflow-rt-9-code\t/root/.agentflow-runner/workspace/runs/run-2\n" +
		"/agentflow-run-3\t" + root + "/nested/run-3\n" +
		"/agentflow-run-4\t\n" +
		"malformed line\n"

	found := parseDockerSandboxMounts(out, root)

	want := map[string]string{"agentflow-run-1": "run-1", "agentflow-run-2": "run-2"}
	if len(found) != len(want) {
		t.Fatalf("found = %v, want %v", found, want)
	}
	for container, workspace := range want {
		if found[container] != workspace {
			t.Fatalf("found[%q] = %q, want %q", container, found[container], workspace)
		}
	}
}

// 泄漏的根因就在这里：worker 重启后内存里的 sandbox 句柄没了，容器还在跑。
// 回收必须只依据 run 是否还持有工作区，而不是本进程是否记得这个容器。
func TestStaleSandboxContainersKeepsOnlyLiveRuns(t *testing.T) {
	found := map[string]string{
		"agentflow-run-live":      "run-live",
		"agentflow-run-done":      "run-done",
		"agentflow-persist-run-p": "persist-run-p",
	}

	stale := staleSandboxContainers(found, []string{"run-live", "run-p"})

	if _, ok := stale["agentflow-run-live"]; ok {
		t.Fatal("a run that still owns its workspace must keep its container")
	}
	if _, ok := stale["agentflow-persist-run-p"]; ok {
		t.Fatal("persist- workspace of a live run must be kept")
	}
	if stale["agentflow-run-done"] != "run-done" {
		t.Fatalf("finished run must be reclaimed, got %v", stale)
	}
}

// 已经被删掉的 run 不会出现在 live 列表里，它的容器必须被回收；
// 这类容器状态一直是 running，docker prune 永远碰不到。
func TestStaleSandboxContainersReclaimsDeletedRuns(t *testing.T) {
	found := map[string]string{"agentflow-run-gone": "run-gone"}
	if len(staleSandboxContainers(found, nil)) != 1 {
		t.Fatal("container of a deleted run must be reclaimable")
	}
}

func TestCollectDockerSandboxMountsSkipsVanishedContainer(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	script := `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
case "$last" in
gone) echo "Error: No such object: gone" >&2; exit 1 ;;
keep) printf '/agentflow-run-1\t%s\n' "$AF_ROOT/run-1" ;;
*) echo "boom" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_ROOT", root)
	out, err := collectDockerSandboxMounts(context.Background(), []string{"gone", "keep"})
	if err != nil {
		t.Fatal(err)
	}
	found := parseDockerSandboxMounts(out, root)
	if found["agentflow-run-1"] != "run-1" {
		t.Fatalf("found = %v", found)
	}
	if _, err := collectDockerSandboxMounts(context.Background(), []string{"bad"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("real inspect failure = %v", err)
	}
}

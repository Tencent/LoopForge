//go:build linux || darwin

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/processgroup"
)

func TestTerminateProcessGroupPreservesWaitResult(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("/bin/sh", "-c", `trap 'exit 23' TERM; touch "$1"; while :; do sleep 0.1; done`, "sh", ready)
	cleanup, err := processgroup.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = processgroup.Kill(cmd.Process.Pid)
		cleanup()
	})
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	waitFor(t, 3*time.Second, "process trap", func() bool { _, err := os.Stat(ready); return err == nil })
	terminateProcessGroup(cmd, done)
	<-done
	if exitCode(waitErr) != 23 {
		t.Fatalf("termination stole the command's wait result: %v", waitErr)
	}
}

func TestExecuteProcessCancellationReapsPromptly(t *testing.T) {
	d, _, _ := managedTestDaemon(t, "", nil, nil, nil)
	task := domain.RunnerTask{ID: "cancel-process"}
	ready := filepath.Join(d.taskWorkspace(task), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan execResult, 1)
	go func() {
		done <- d.executeProcess(ctx, task, []string{"/bin/sh", "-c", `trap 'exit 0' TERM; touch ready; while :; do sleep 0.1; done`})
	}()
	waitFor(t, 3*time.Second, "running command", func() bool { _, err := os.Stat(ready); return err == nil })
	cancel()
	select {
	case result := <-done:
		if result.status != domain.RunnerTaskCanceled {
			t.Fatalf("cancellation result: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation blocked on repeated termination or a second Wait")
	}
}
